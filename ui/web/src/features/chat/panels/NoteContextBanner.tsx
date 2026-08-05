import type { Note } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { Alert, Button, Space, Typography } from "antd";
import { FileTextOutlined } from "@ant-design/icons";

export function compactWorkspaceName(path: string) {
  const normalized = path.trim();
  if (!normalized) {
    return "";
  }
  const parts = normalized.split(/[\\/]/).filter(Boolean);
  return parts.at(-1) || normalized;
}

export function noteContextMetadata(note?: Note, noteId?: string): Record<string, string> | undefined {
  const id = note?.id || noteId?.trim() || "";
  if (!id) {
    return undefined;
  }
  const metadata: Record<string, string> = {
    note_id: id,
    app_object_type: "note",
    app_object_id: id,
  };
  if (note?.title) {
    metadata.app_object_title = note.title;
  }
  return metadata;
}

export function NoteContextBanner({
  note,
  loading,
  error,
  onClear,
}: {
  note?: Note;
  loading: boolean;
  error: unknown;
  onClear: () => void;
}) {
  const { t } = useI18n();
  if (!loading && !error && !note) {
    return null;
  }
  return (
    <div className="chat-note-context">
      {error ? (
        <Alert type="warning" showIcon message={t("chat.noteContextError")} action={<Button size="small" onClick={onClear}>{t("chat.noteContextClear")}</Button>} />
      ) : (
        <Alert
          type="info"
          showIcon
          message={loading ? t("chat.noteContextLoading") : t("chat.noteContextTitle")}
          description={note ? (
            <Space size={6} wrap>
              <FileTextOutlined />
              <Typography.Text strong>{note.title}</Typography.Text>
              <Typography.Text type="secondary">{note.id}</Typography.Text>
            </Space>
          ) : null}
          action={<Button size="small" onClick={onClear}>{t("chat.noteContextClear")}</Button>}
        />
      )}
    </div>
  );
}
