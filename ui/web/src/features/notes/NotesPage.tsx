import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Alert, App as AntApp, Button, Card, Empty, Form, Input, Popconfirm, Select, Space, Tabs, Tag, Typography } from "antd";
import { DatabaseOutlined, DeleteOutlined, FileTextOutlined, MessageOutlined, PlusOutlined, ReloadOutlined, SaveOutlined } from "@ant-design/icons";
import { MarkdownContent } from "../../components/MarkdownContent";
import { useI18n } from "../../i18n";
import { deleteNote, getMeta, getNoteRelatedMemories, listNotes, saveNote } from "../../lib/api";
import { buildChatRoute } from "../../lib/chatRoutes";
import { showError } from "../../lib/notifications";
import type { MemoryRecord, Note } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";

type NoteFormValues = {
  id?: string;
  title: string;
  summary?: string;
  tags?: string[] | string;
  content: string;
};

export function NotesPage() {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const defaultSessionKey = useSettingsStore((state) => state.defaultSessionKey);
  const setDefaultSessionKey = useSettingsStore((state) => state.setDefaultSessionKey);
  const [query, setQuery] = useState("");
  const [selectedTag, setSelectedTag] = useState<string | undefined>();
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [form] = Form.useForm<NoteFormValues>();

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachNotes = !authRequired || !!token;
  const notesQuery = useQuery({
    queryKey: ["notes", token, query, selectedTag],
    enabled: canReachNotes,
    queryFn: () => listNotes(token || null, { query, tag: selectedTag }),
  });
  const allNotesQuery = useQuery({
    queryKey: ["notes", token, "all-tags"],
    enabled: canReachNotes,
    queryFn: () => listNotes(token || null),
  });
  const notes = notesQuery.data ?? [];
  const selected = useMemo(() => notes.find((item) => item.id === selectedID) ?? notes[0] ?? null, [notes, selectedID]);
  const tagOptions = useMemo(() => {
    const tags = new Set<string>();
    for (const note of allNotesQuery.data ?? []) {
      for (const tag of note.tags ?? []) {
        if (tag.trim()) {
          tags.add(tag.trim());
        }
      }
    }
    return Array.from(tags).sort((left, right) => left.localeCompare(right)).map((tag) => ({ label: tag, value: tag }));
  }, [allNotesQuery.data]);

  useEffect(() => {
    if (selected && selected.id !== selectedID) {
      setSelectedID(selected.id);
    }
  }, [selected, selectedID]);

  useEffect(() => {
    if (!selected) {
      form.setFieldsValue({ id: "", title: "", summary: "", tags: "", content: "" });
      return;
    }
    form.setFieldsValue({
      id: selected.id,
      title: selected.title,
      summary: selected.summary ?? "",
      tags: selected.tags ?? [],
      content: selected.content,
    });
  }, [form, selected]);

  const saveMutation = useMutation({
    mutationFn: (values: NoteFormValues) =>
      saveNote(token || null, {
        id: values.id,
        title: values.title,
        summary: values.summary,
        tags: splitTags(values.tags),
        content: values.content,
      }),
    onSuccess: async (note) => {
      setSelectedID(note.id);
      void message.success(`Saved ${note.title}.`);
      await queryClient.invalidateQueries({ queryKey: ["notes", token] });
    },
    onError: (error) => showError(message, error, "Failed to save note."),
  });
  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteNote(token || null, id),
    onSuccess: async (note) => {
      if (selectedID === note.id) {
        setSelectedID(null);
      }
      void message.success(`Deleted ${note.title}.`);
      await queryClient.invalidateQueries({ queryKey: ["notes", token] });
    },
    onError: (error) => showError(message, error, "Failed to delete note."),
  });

  const relatedQuery = useQuery({
    queryKey: ["notes", token, selected?.id, "related-memories"],
    enabled: canReachNotes && !!selected,
    queryFn: () => getNoteRelatedMemories(token || null, selected!.id),
  });

  const createNew = () => {
    setSelectedID(null);
    form.setFieldsValue({
      id: "",
      title: "Untitled note",
      summary: "",
      tags: "",
      content: "# Untitled note\n\n",
    });
  };

  const askAboutNote = (note: Note) => {
    const sessionKey = defaultSessionKey || crypto.randomUUID();
    if (!defaultSessionKey) {
      setDefaultSessionKey(sessionKey);
    }
    navigate(`${buildChatRoute({ channel: "web", key: sessionKey })}?note_id=${encodeURIComponent(note.id)}`);
  };

  if (!canReachNotes) {
    return (
      <main className="page-shell">
        <Alert type="warning" showIcon message={t("notes.authRequired")} />
      </main>
    );
  }

  return (
    <main className="page-shell notes-page">
      <div className="notes-layout">
        <Card
          title={t("notes.listTitle")}
          extra={
            <Button icon={<ReloadOutlined />} aria-label={t("notes.refresh")} onClick={() => void queryClient.invalidateQueries({ queryKey: ["notes", token] })}>
              {t("notes.refresh")}
            </Button>
          }
        >
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            <Input.Search value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("notes.searchPlaceholder")} allowClear />
            <Select
              value={selectedTag}
              options={tagOptions}
              onChange={(value) => setSelectedTag(value)}
              placeholder={t("notes.filterTag")}
              allowClear
              style={{ width: "100%" }}
            />
            {notesQuery.isLoading ? <Typography.Text type="secondary">{t("app.loading")}</Typography.Text> : null}
            {notes.length === 0 && !notesQuery.isLoading ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("notes.empty")}>
                <Button type="primary" icon={<PlusOutlined />} aria-label={t("notes.newNote")} onClick={createNew}>
                  {t("notes.newNote")}
                </Button>
              </Empty>
            ) : (
              <Space direction="vertical" size={8} style={{ width: "100%" }}>
                {notes.map((note) => (
                  <button
                    key={note.id}
                    type="button"
                    className={`notes-list-item${note.id === selected?.id ? " notes-list-item-active" : ""}`}
                    onClick={() => setSelectedID(note.id)}
                  >
                    <Space direction="vertical" size={4} style={{ width: "100%" }}>
                      <Space size={6} wrap>
                        <FileTextOutlined />
                        <Typography.Text strong ellipsis={{ tooltip: note.title }}>
                          {note.title}
                        </Typography.Text>
                      </Space>
                      {note.summary ? <Typography.Text type="secondary">{note.summary}</Typography.Text> : null}
                      <Space size={4} wrap>
                        {(note.tags ?? []).slice(0, 4).map((tag) => <Tag key={tag}>{tag}</Tag>)}
                      </Space>
                    </Space>
                  </button>
                ))}
              </Space>
            )}
          </Space>
        </Card>
        <Card
          title={selected ? selected.title : t("notes.newNote")}
          extra={
            <Space>
              <Button icon={<PlusOutlined />} aria-label={t("notes.newNote")} onClick={createNew}>
                {t("notes.newNote")}
              </Button>
              {selected ? (
                <Button icon={<MessageOutlined />} aria-label={t("notes.askInChat")} onClick={() => askAboutNote(selected)}>
                  {t("notes.askInChat")}
                </Button>
              ) : null}
              {selected ? (
                <Popconfirm title={t("notes.deleteConfirm")} onConfirm={() => deleteMutation.mutate(selected.id)}>
                  <Button danger icon={<DeleteOutlined />} aria-label={t("notes.delete")} loading={deleteMutation.isPending}>
                    {t("notes.delete")}
                  </Button>
                </Popconfirm>
              ) : null}
            </Space>
          }
        >
          <Form form={form} layout="vertical" onFinish={(values) => saveMutation.mutate(values)}>
            <Form.Item name="id" hidden><Input /></Form.Item>
            <Form.Item name="title" label={t("notes.title")} rules={[{ required: true }]}>
              <Input />
            </Form.Item>
            <Form.Item name="summary" label={t("notes.summary")}>
              <Input />
            </Form.Item>
            <Form.Item name="tags" label={t("notes.tags")}>
              <Select mode="tags" open={false} tokenSeparators={[","]} placeholder={t("notes.tagsPlaceholder")} />
            </Form.Item>
            <Tabs
              items={[
                {
                  key: "edit",
                  label: t("notes.edit"),
                  children: (
                    <Form.Item name="content" label={t("notes.content")} rules={[{ required: true }]}>
                      <Input.TextArea rows={16} />
                    </Form.Item>
                  ),
                },
                {
                  key: "preview",
                  label: t("notes.preview"),
                  children: (
                    <div className="notes-preview">
                      <MarkdownContent content={Form.useWatch("content", form) || ""} forceMarkdown />
                    </div>
                  ),
                },
              ]}
            />
            <Button type="primary" htmlType="submit" icon={<SaveOutlined />} aria-label={t("notes.save")} loading={saveMutation.isPending}>
              {t("notes.save")}
            </Button>
          </Form>
          {selected && relatedQuery.data && relatedQuery.data.length > 0 ? (
            <Card size="small" title={<><DatabaseOutlined /> Related memories</>} style={{ marginTop: 16 }}>
              <Space direction="vertical" size={4} style={{ width: "100%" }}>
                {relatedQuery.data.slice(0, 6).map((mem) => (
                  <Typography.Text key={mem.id} ellipsis style={{ maxWidth: "100%" }}>
                    <Tag>{mem.type}</Tag> {mem.title}
                  </Typography.Text>
                ))}
              </Space>
            </Card>
          ) : null}
        </Card>
      </div>
    </main>
  );
}

function splitTags(value?: string | string[]): string[] {
  if (Array.isArray(value)) {
    return value.flatMap((item) => splitTags(item));
  }
  return (value ?? "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}
