import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { App as AntApp, Button, Input, Modal, Select, Space, Typography } from "antd";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { listSessions } from "../../lib/api";
import { linkWorkSession } from "../../lib/apiWorkContext";

/**
 * LinkSessionPicker attaches an EXISTING conversation to the stream.
 *
 * The stream's own "new topic conversation" flow always creates a fresh session;
 * this is the other half — pulling in conversations that already happened, so a
 * stream can be assembled from real history instead of only from work started
 * here. It is a picker and not a free-text session id box because the user
 * recognizes a conversation by its title, never by its opaque session id.
 *
 * Linking only records a reference: the original conversation is not moved,
 * renamed or deleted.
 */
export function LinkSessionPicker(props: {
  contextId: string;
  /** Session ids already linked to this stream; they are not offered again. */
  linkedIds: string[];
  disabled?: boolean;
  /** Called after a successful link so the caller can refresh its detail query. */
  onLinked: () => void;
}) {
  const { contextId, linkedIds, disabled, onLinked } = props;
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);

  const [open, setOpen] = useState(false);
  const [sessionId, setSessionId] = useState("");
  const [topic, setTopic] = useState("");

  // Fetched only while the picker is open: the session list is not needed to render
  // the desktop, and it is the heaviest list on this page.
  const sessionsQuery = useQuery({
    queryKey: ["sessions", token ?? ""],
    queryFn: () => listSessions(token || null),
    enabled: open,
  });
  const options = (sessionsQuery.data ?? [])
    .filter((item) => !linkedIds.includes(item.session_id))
    .map((item) => ({ value: item.session_id, label: item.title?.trim() || item.session_id }));

  const linkMutation = useMutation({
    mutationFn: () => linkWorkSession(token || null, contextId, { topic: topic.trim(), session_id: sessionId }),
    onSuccess: () => {
      setOpen(false);
      setSessionId("");
      setTopic("");
      onLinked();
      void message.success(tr("sessionLinked"));
    },
    onError: (error) => void message.error(error instanceof Error ? error.message : tr("saveFailed")),
  });

  const pick = (value: string) => {
    setSessionId(value);
    // Prefill the topic from the conversation's own title so the common case is
    // one click; a conversation without a title still requires the user to name it
    // because the backend rejects an empty topic.
    const chosen = (sessionsQuery.data ?? []).find((item) => item.session_id === value);
    if (chosen) setTopic((current) => current || chosen.title?.trim() || "");
  };

  return (
    <>
      <Button size="small" disabled={disabled} onClick={() => setOpen(true)}>{tr("linkExisting")}</Button>
      <Modal
        title={tr("linkExisting")}
        open={open}
        onCancel={() => setOpen(false)}
        onOk={() => linkMutation.mutate()}
        okText={tr("linkExisting")}
        confirmLoading={linkMutation.isPending}
        okButtonProps={{ disabled: !sessionId || !topic.trim() }}
        destroyOnHidden
      >
        <Space direction="vertical" size={10} style={{ width: "100%" }}>
          <Typography.Text type="secondary">{tr("linkExistingHint")}</Typography.Text>
          <Select
            style={{ width: "100%" }}
            aria-label={tr("linkPickSession")}
            placeholder={tr("linkPickSession")}
            loading={sessionsQuery.isLoading}
            value={sessionId || undefined}
            onChange={pick}
            showSearch
            optionFilterProp="label"
            notFoundContent={tr("linkNoAvailable")}
            options={options}
          />
          <Input
            aria-label={tr("topicName")}
            placeholder={tr("topicName")}
            maxLength={160}
            value={topic}
            onChange={(event) => setTopic(event.target.value)}
          />
        </Space>
      </Modal>
    </>
  );
}
