import { App as AntApp } from "antd";
import type { QueryClient } from "@tanstack/react-query";
import type { ComposerSubmission } from "../../components/Composer";
import { executeCommand, submitMessage, uploadAttachments } from "../../lib/api";
import type { PendingSend } from "../../store/chat";

type MessageApi = ReturnType<typeof AntApp.useApp>["message"];

type ChatSubmissionDependencies = {
  activeSessionId?: string;
  token: string;
  sender: string;
  metadata?: Record<string, string>;
  addPendingSend: (send: PendingSend) => void;
  removePendingSend: (id: string) => void;
  setRunningTurn: (turnId: string) => void;
  setUploading: (uploading: boolean) => void;
  setUploadProgress: (progress: number | null) => void;
  message: MessageApi;
  t: (key: string) => string;
  queryClient: QueryClient;
};

export function createChatSubmissionHandler({
  activeSessionId,
  token,
  sender,
  metadata,
  addPendingSend,
  removePendingSend,
  setRunningTurn,
  setUploading,
  setUploadProgress,
  message,
  t,
  queryClient,
}: ChatSubmissionDependencies) {
  return async ({ text, files }: ComposerSubmission) => {
    if (!activeSessionId || (!text && files.length === 0)) {
      return;
    }
    if (text.startsWith("/") && files.length === 0) {
      await runSlashCommand(text);
    } else {
      if (!(await submitUserMessage(text, files))) {
        return;
      }
    }
    await invalidateSessionQueries(queryClient, token, activeSessionId);
  };

  async function runSlashCommand(text: string) {
    // Surface slow commands such as /compact while they execute.
    const commandName = text.trim().split(/\s+/)[0].slice(1) || "command";
    const pendingId = `cmd:${Date.now()}:${Math.random().toString(36).slice(2)}`;
    addPendingSend({ id: pendingId, kind: "command", commandName });
    try {
      const result = await executeCommand(token || null, activeSessionId!, text, metadata);
      if (result.dispatched_turn_id) {
        setRunningTurn(result.dispatched_turn_id);
      }
    } finally {
      removePendingSend(pendingId);
    }
  }

  async function submitUserMessage(text: string, files: File[]) {
    try {
      const attachments = files.length > 0 ? await uploadFiles(files) : [];
      const pendingId = `user:${Date.now()}:${Math.random().toString(36).slice(2)}`;
      addPendingSend({ id: pendingId, kind: "user", text, attachments, sender });
      try {
        const result = await submitMessage(
          token || null,
          activeSessionId!,
          { source: "web", sender, text, content: text, attachments, metadata },
          {},
        );
        if (result.turn_id) {
          setRunningTurn(result.turn_id);
        }
      } catch (error) {
        // Keep the optimistic item across transient disconnects; the next
        // snapshot either confirms it or leaves it available for retry.
        if (error instanceof TypeError) {
          message.warning(t("chat.submitNetworkError"));
          return false;
        }
        removePendingSend(pendingId);
        message.error(error instanceof Error ? error.message : String(error));
        throw error;
      }
    } finally {
      setUploading(false);
      setUploadProgress(null);
    }
    return true;
  }

  async function uploadFiles(files: File[]) {
    setUploading(true);
    setUploadProgress(0);
    return uploadAttachments(token || null, activeSessionId!, files, setUploadProgress);
  }
}

async function invalidateSessionQueries(queryClient: QueryClient, token: string, sessionId: string) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ["snapshot", token, sessionId] }),
    queryClient.invalidateQueries({ queryKey: ["timeline", token, sessionId] }),
    queryClient.invalidateQueries({ queryKey: ["timeline-page", token, sessionId] }),
    queryClient.invalidateQueries({ queryKey: ["subagents", token, sessionId] }),
    queryClient.invalidateQueries({ queryKey: ["context-inspector", token, sessionId] }),
    queryClient.invalidateQueries({ queryKey: ["skills-active", token, sessionId] }),
  ]);
}
