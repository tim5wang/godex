import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Alert, App as AntApp, Button, Card, Empty, Form, Input, Popconfirm, Select, Segmented, Space, Switch, Tag, Typography } from "antd";
import { CheckOutlined, DatabaseOutlined, DeleteOutlined, EditOutlined, FileTextOutlined, MessageOutlined, PlusOutlined, ReloadOutlined, SaveOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
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

type ContentView = "edit" | "preview";

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
  const [showSummary, setShowSummary] = useState(false);
  const [editingMeta, setEditingMeta] = useState(false);
  const [contentView, setContentView] = useState<ContentView>("edit");
  const [form] = Form.useForm<NoteFormValues>();
  const titleValue = Form.useWatch("title", form) as string | undefined;
  const summaryValue = Form.useWatch("summary", form) as string | undefined;
  const tagsValue = Form.useWatch("tags", form) as string[] | undefined;
  const contentValue = Form.useWatch("content", form) as string | undefined;

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
      form.setFieldsValue({ id: "", title: "", summary: "", tags: [], content: "" });
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
      setEditingMeta(false);
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
    setEditingMeta(true);
    setContentView("edit");
    form.setFieldsValue({
      id: "",
      title: t("notes.untitled"),
      summary: "",
      tags: [],
      content: "",
    });
  };

  const finishEditingMeta = async () => {
    try {
      await form.validateFields(["title", "summary", "tags"]);
      setEditingMeta(false);
    } catch {
      // validation errors shown inline; stay in edit mode
    }
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

  const updatedTime = selected ? formatNoteTime(selected.updated_at) : "";

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
          <Space direction="vertical" size={10} style={{ width: "100%" }}>
            <Input.Search value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("notes.searchPlaceholder")} allowClear style={{ width: "100%" }} />
            <div className="notes-toolbar-row">
              <Select
                value={selectedTag}
                options={tagOptions}
                onChange={(value) => setSelectedTag(value)}
                placeholder={t("notes.filterTag")}
                allowClear
                style={{ flex: 1, minWidth: 0 }}
              />
              <div className="notes-summary-toggle">
                <Typography.Text type="secondary" className="notes-summary-toggle-label">{t("notes.showSummary")}</Typography.Text>
                <Switch size="small" checked={showSummary} onChange={setShowSummary} aria-label={t("notes.showSummary")} />
              </div>
            </div>
            {notesQuery.isLoading ? <Typography.Text type="secondary">{t("app.loading")}</Typography.Text> : null}
            {notes.length === 0 && !notesQuery.isLoading ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("notes.empty")}>
                <Button type="primary" icon={<PlusOutlined />} aria-label={t("notes.newNote")} onClick={createNew}>
                  {t("notes.newNote")}
                </Button>
              </Empty>
            ) : (
              <div className="notes-list">
                {notes.map((note) => (
                  <button
                    key={note.id}
                    type="button"
                    className={`notes-list-item${note.id === selected?.id ? " notes-list-item-active" : ""}`}
                    onClick={() => {
                      setSelectedID(note.id);
                      setEditingMeta(false);
                    }}
                  >
                    <div className="notes-list-item-title">
                      <FileTextOutlined className="notes-list-item-icon" />
                      <Typography.Text strong ellipsis={{ tooltip: note.title }} style={{ flex: 1, minWidth: 0 }}>
                        {note.title}
                      </Typography.Text>
                      <Typography.Text type="secondary" className="notes-list-item-time">
                        {formatNoteTime(note.updated_at)}
                      </Typography.Text>
                    </div>
                    {(note.tags ?? []).length > 0 ? (
                      <div className="notes-list-item-tags">
                        {(note.tags ?? []).slice(0, 4).map((tag) => (
                          <Tag key={tag}>{tag}</Tag>
                        ))}
                      </div>
                    ) : null}
                    {showSummary && note.summary ? (
                      <Typography.Text type="secondary" className="notes-list-item-summary" ellipsis={{ tooltip: note.summary }}>
                        {note.summary}
                      </Typography.Text>
                    ) : null}
                  </button>
                ))}
              </div>
            )}
          </Space>
        </Card>

        <Card
          title={selected ? titleValue || selected.title || t("notes.untitled") : t("notes.newNote")}
          extra={
            <Space wrap>
              <Button type="primary" icon={<SaveOutlined />} htmlType="submit" form="note-form" aria-label={t("notes.save")} loading={saveMutation.isPending}>
                {t("notes.save")}
              </Button>
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
          <Form form={form} id="note-form" layout="vertical" onFinish={(values) => saveMutation.mutate({ ...values, title: values.title || selected?.title || t("notes.untitled") })}>
            <Form.Item name="id" hidden><Input /></Form.Item>

            {editingMeta ? (
              <div className="notes-meta">
                <div className="notes-meta-fields">
                  <Form.Item name="title" label={t("notes.title")} rules={[{ required: true }]}>
                    <Input />
                  </Form.Item>
                  <Form.Item name="summary" label={t("notes.summary")}>
                    <Input.TextArea rows={2} />
                  </Form.Item>
                  <Form.Item name="tags" label={t("notes.tags")}>
                    <Select mode="tags" open={false} tokenSeparators={[","]} placeholder={t("notes.tagsPlaceholder")} />
                  </Form.Item>
                </div>
                <Button type="text" icon={<CheckOutlined />} aria-label={t("notes.doneEditMeta")} onClick={() => void finishEditingMeta()} />
              </div>
            ) : (
              <div className="notes-meta">
                <div className="notes-meta-preview">
                  <Space direction="vertical" size={6} style={{ flex: 1, minWidth: 0 }}>
                    {summaryValue ? (
                      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
                        {summaryValue}
                      </Typography.Paragraph>
                    ) : (
                      <Typography.Text type="secondary">{t("notes.noSummary")}</Typography.Text>
                    )}
                    <Space size={6} wrap>
                      {(tagsValue ?? []).map((tag) => (
                        <Tag key={tag}>{tag}</Tag>
                      ))}
                      {updatedTime ? (
                        <Typography.Text type="secondary" className="notes-meta-time">
                          {t("notes.updatedAt", { time: updatedTime })}
                        </Typography.Text>
                      ) : null}
                    </Space>
                  </Space>
                  <Button type="text" icon={<EditOutlined />} aria-label={t("notes.editMeta")} onClick={() => setEditingMeta(true)} />
                </div>
              </div>
            )}

            <div className="notes-content-toolbar">
              <Segmented
                size="small"
                value={contentView}
                onChange={(value) => setContentView(value as ContentView)}
                options={[
                  { label: t("notes.edit"), value: "edit" },
                  { label: t("notes.preview"), value: "preview" },
                ]}
              />
            </div>
            <Form.Item name="content" rules={[{ required: true }]} hidden={contentView !== "edit"} style={{ marginBottom: 0 }}>
              <Input.TextArea autoSize={{ minRows: 18, maxRows: 44 }} placeholder={t("notes.contentPlaceholder")} />
            </Form.Item>
            {contentView === "preview" ? (
              <div className="notes-preview">
                <MarkdownContent content={contentValue || ""} forceMarkdown />
              </div>
            ) : null}
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

function formatNoteTime(value?: string): string {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return dayjs(date).format("MM-DD HH:mm");
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
