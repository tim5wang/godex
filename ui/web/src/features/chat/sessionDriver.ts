import type { ComposerSubmission } from "../../components/Composer";

/** The shared chat's control boundary. Rendering does not own an agent loop. */
export interface ChatSessionDriver {
  source: "godex" | "codex";
  capabilities: {
    attachments: boolean;
    queue: boolean;
    steer: boolean;
    interrupt: boolean;
    approvals: boolean;
  };
  submit: (submission: ComposerSubmission) => Promise<void>;
  interrupt: (turnId: string) => Promise<unknown>;
}

export function godexSessionDriver(submit: ChatSessionDriver["submit"], interrupt: ChatSessionDriver["interrupt"]): ChatSessionDriver {
  return { source: "godex", capabilities: { attachments: true, queue: true, steer: true, interrupt: true, approvals: true }, submit, interrupt };
}

export function codexChatRoute(threadId: string, nodeId?: string | null): string {
  return `/chat/codex/${encodeURIComponent(threadId)}?codex_node=${encodeURIComponent(nodeId || "local")}`;
}

export function isCodexChat(channel?: string): boolean { return channel === "codex"; }
export function isCodexChatPath(path: string): boolean { return path.startsWith("/chat/codex/"); }
