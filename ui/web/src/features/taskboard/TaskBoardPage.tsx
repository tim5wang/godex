import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import {
  App as AntApp,
  Button,
  Checkbox,
  Drawer,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  DeleteOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { buildChatRoute } from "../../lib/chatRoutes";
import { showError } from "../../lib/notifications";
import { useSettingsStore } from "../../store/settings";
import {
  createTaskboardCard,
  deleteTaskboardCard,
  executeTaskboardCard,
  getTaskboardCard,
  listSessions,
  listTaskboardCards,
  listTaskboardProjects,
  patchTaskboardCard,
} from "../../lib/api";
import type { TaskboardCard, TaskboardCardPatchInput, TaskboardExecution, TaskboardStatus, TaskboardUrgency } from "../../lib/types";

const COLUMNS: { status: TaskboardStatus; labelKey: string; dot: string }[] = [
  { status: "backlog", labelKey: "taskboard.col.backlog", dot: "#8c8c8c" },
  { status: "todo", labelKey: "taskboard.col.todo", dot: "#1677ff" },
  { status: "in_progress", labelKey: "taskboard.col.inProgress", dot: "#fa8c16" },
  { status: "in_review", labelKey: "taskboard.col.inReview", dot: "#722ed1" },
  { status: "done", labelKey: "taskboard.col.done", dot: "#52c41a" },
];

const URGENCY_COLORS: Record<TaskboardUrgency, string> = {
  urgent: "#ff4d4f",
  normal: "#1677ff",
  low: "#8c8c8c",
};

const EXECUTION_STATUS_COLORS: Record<string, string> = {
  running: "processing",
  completed: "success",
  failed: "error",
  cancelled: "default",
};

function urgencyRank(u: TaskboardUrgency): number {
  return u === "urgent" ? 0 : u === "low" ? 2 : 1;
}

export function TaskBoardPage() {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const [projectFilter, setProjectFilter] = useState<string>("");
  const [urgencyFilter, setUrgencyFilter] = useState<TaskboardUrgency | "">("");
  const [search, setSearch] = useState("");
  const [detailId, setDetailId] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [createTitle, setCreateTitle] = useState("");
  const [createDescription, setCreateDescription] = useState("");
  const [createPrompt, setCreatePrompt] = useState("");
  const [createUrgency, setCreateUrgency] = useState<TaskboardUrgency>("normal");
  const [createChecklist, setCreateChecklist] = useState("");

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["taskboard"] });
  };
  const fail = (error: unknown, key: string) => showError(message, error, t(key));

  const projectsQuery = useQuery({
    queryKey: ["taskboard", "projects", token],
    queryFn: async () => listTaskboardProjects(token || null),
  });
  const cardsQuery = useQuery({
    queryKey: ["taskboard", "cards", token, projectFilter, urgencyFilter],
    queryFn: async () =>
      listTaskboardCards(token || null, {
        project: projectFilter || undefined,
        urgency: urgencyFilter || undefined,
      }),
    refetchInterval: 15000,
  });
  const detailQuery = useQuery({
    queryKey: ["taskboard", "card", token, detailId],
    queryFn: async () => getTaskboardCard(token || null, detailId || ""),
    enabled: !!detailId,
  });

  const createMutation = useMutation({
    mutationFn: async () =>
      createTaskboardCard(token || null, {
        project_id: projectFilter || undefined,
        title: createTitle,
        description: createDescription || undefined,
        prompt: createPrompt || undefined,
        urgency: createUrgency,
        checklist: createChecklist
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean),
      }),
    onSuccess: () => {
      message.success(t("taskboard.created"));
      setCreateOpen(false);
      setCreateTitle("");
      setCreateDescription("");
      setCreatePrompt("");
      setCreateChecklist("");
      invalidate();
    },
    onError: (error) => fail(error, "taskboard.createFailed"),
  });

  const patchMutation = useMutation({
    mutationFn: async (input: { cardId: string; body: TaskboardCardPatchInput }) =>
      patchTaskboardCard(token || null, input.cardId, input.body),
    onSuccess: (_data, variables) => {
      if (variables.body.action !== "checklist") {
        message.success(t("taskboard.updated"));
      }
      invalidate();
    },
    onError: (error) => fail(error, "taskboard.updateFailed"),
  });

  const executeMutation = useMutation({
    mutationFn: async (cardId: string) => executeTaskboardCard(token || null, cardId),
    onSuccess: () => {
      message.success(t("taskboard.executionStarted"));
      invalidate();
    },
    onError: (error) => fail(error, "taskboard.executeFailed"),
  });

  const deleteMutation = useMutation({
    mutationFn: async (cardId: string) => deleteTaskboardCard(token || null, cardId),
    onSuccess: () => {
      message.success(t("taskboard.deleted"));
      setDetailId(null);
      invalidate();
    },
    onError: (error) => fail(error, "taskboard.deleteFailed"),
  });

  const grouped = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    const byStatus = new Map<TaskboardStatus, TaskboardCard[]>();
    for (const column of COLUMNS) byStatus.set(column.status, []);
    for (const card of cardsQuery.data?.cards ?? []) {
      if (keyword && !card.title.toLowerCase().includes(keyword) && !card.id.toLowerCase().includes(keyword)) {
        continue;
      }
      byStatus.get(card.status)?.push(card);
    }
    return byStatus;
  }, [cardsQuery.data, search]);

  const detail = detailQuery.data?.card;

  const advance = (card: TaskboardCard, to: TaskboardStatus) => {
    patchMutation.mutate({ cardId: card.id, body: { action: "move", version: card.version, to } });
  };

  const acceptCard = (card: TaskboardCard) => {
    const doneCount = (card.checklist ?? []).filter((item) => item.done).length;
    const total = (card.checklist ?? []).length;
    if (doneCount < total) {
      Modal.confirm({
        zIndex: 1300,
        title: t("taskboard.acceptForceTitle"),
        content: t("taskboard.acceptForceContent", { done: doneCount, total }),
        okText: t("taskboard.acceptForceOk"),
        onOk: () => patchMutation.mutate({ cardId: card.id, body: { action: "complete", version: card.version, force: true } }),
      });
      return;
    }
    patchMutation.mutate({ cardId: card.id, body: { action: "complete", version: card.version, force: false } });
  };

  const rejectCard = (card: TaskboardCard) => {
    let reason = "";
    Modal.confirm({
      zIndex: 1300,
      title: t("taskboard.rejectTitle"),
      content: (
        <Input
          placeholder={t("taskboard.rejectReasonPlaceholder")}
          onChange={(event) => {
            reason = event.target.value;
          }}
        />
      ),
      onOk: () => {
        if (!reason.trim()) {
          message.warning(t("taskboard.rejectReasonRequired"));
          return Promise.reject();
        }
        patchMutation.mutate({
          cardId: card.id,
          body: { action: "reject", version: card.version, reason: reason.trim() },
        });
      },
    });
  };

  const jumpToHost = async (execution: TaskboardExecution) => {
    const host = execution.host;
    if (!host) return;
    // Session identity is hashed from the FULL locator (channel + key +
    // user_id + metadata like workspace_dir), so a coarse channel/key route
    // usually hashes to a different session — ChatPage then falls back to
    // creating a new chat. Resolve the host session's complete locator from
    // the session list instead; fall back to the stored triple if absent.
    try {
      const sessions = await listSessions(token || null);
      const target = sessions.find((session) => session.session_id === host.session_id);
      if (target) {
        navigate(buildChatRoute(target.locator));
        setDetailId(null);
        return;
      }
    } catch {
      // fall through to the coarse route below
    }
    navigate(
      buildChatRoute({
        channel: host.channel || "web",
        key: host.key || "",
        user_id: host.user_id,
        // project_dir participates in the session identity hash — without it
        // OpenSession rebuilds a different id and ChatPage creates a new chat.
        metadata: host.project_dir ? { project_dir: host.project_dir } : undefined,
      }),
    );
    setDetailId(null);
  };

  const toggleChecklist = (card: TaskboardCard, index: number, done: boolean) => {
    patchMutation.mutate({
      cardId: card.id,
      body: { action: "checklist", version: card.version, check_action: done ? "check" : "uncheck", index },
    });
  };

  const actionButtons = (card: TaskboardCard) => {
    const buttons: React.ReactNode[] = [];
    if (card.status === "backlog") {
      buttons.push(
        <Button key="plan" size="small" onClick={() => advance(card, "todo")}>
          {t("taskboard.action.plan")}
        </Button>,
      );
    }
    if (card.status === "todo") {
      buttons.push(
        <Button key="claim" size="small" type="primary" ghost onClick={() => advance(card, "in_progress")}>
          {t("taskboard.action.claim")}
        </Button>,
      );
    }
    if (card.status === "in_progress") {
      buttons.push(
        <Button key="review" size="small" onClick={() => advance(card, "in_review")}>
          {t("taskboard.action.submitReview")}
        </Button>,
      );
    }
    if (card.status === "in_review") {
      buttons.push(
        <Button key="accept" size="small" type="primary" onClick={() => acceptCard(card)}>
          {t("taskboard.action.accept")}
        </Button>,
      );
      buttons.push(
        <Button key="reject" size="small" danger onClick={() => rejectCard(card)}>
          {t("taskboard.action.reject")}
        </Button>,
      );
    }
    const running = (card.executions ?? []).some((execution) => execution.status === "running");
    if (card.status !== "done" && !running) {
      buttons.push(
        <Tooltip key="exec" title={t("taskboard.action.execute")}>
          <Button
            size="small"
            icon={<PlayCircleOutlined />}
            onClick={() => executeMutation.mutate(card.id)}
          />
        </Tooltip>,
      );
    }
    return buttons;
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12, padding: 16, height: "100%", overflow: "hidden" }}>
      <Space wrap>
        <Select
          allowClear
          placeholder={t("taskboard.allProjects")}
          style={{ minWidth: 180 }}
          value={projectFilter || undefined}
          onChange={(value) => setProjectFilter(value || "")}
          options={(projectsQuery.data?.projects ?? []).map((project) => ({
            value: project.id,
            label: project.name,
          }))}
        />
        <Select
          allowClear
          placeholder={t("taskboard.allUrgencies")}
          style={{ minWidth: 130 }}
          value={urgencyFilter || undefined}
          onChange={(value) => setUrgencyFilter((value || "") as TaskboardUrgency | "")}
          options={[
            { value: "urgent", label: t("taskboard.urgency.urgent") },
            { value: "normal", label: t("taskboard.urgency.normal") },
            { value: "low", label: t("taskboard.urgency.low") },
          ]}
        />
        <Input
          allowClear
          placeholder={t("taskboard.searchPlaceholder")}
          style={{ width: 220 }}
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
        <Button icon={<ReloadOutlined />} onClick={() => invalidate()} />
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setCreateOpen(true)}
        >
          {t("taskboard.newCard")}
        </Button>
      </Space>

      <div style={{ display: "flex", gap: 12, flex: 1, overflow: "auto", alignItems: "stretch" }}>
        {COLUMNS.map((column) => {
          const cards = grouped.get(column.status) ?? [];
          return (
            <div
              key={column.status}
              style={{
                flex: "1 1 0",
                minWidth: 220,
                background: "rgba(128,128,128,0.06)",
                borderRadius: 8,
                padding: 8,
                display: "flex",
                flexDirection: "column",
                gap: 8,
              }}
            >
              <Space size={6}>
                <span style={{ width: 8, height: 8, borderRadius: 4, background: column.dot, display: "inline-block" }} />
                <Typography.Text strong>{t(column.labelKey)}</Typography.Text>
                <Typography.Text type="secondary">{cards.length}</Typography.Text>
              </Space>
              {cards.map((card) => {
                const doneCount = (card.checklist ?? []).filter((item) => item.done).length;
                const total = (card.checklist ?? []).length;
                const running = (card.executions ?? []).some((execution) => execution.status === "running");
                return (
                  <div
                    key={card.id}
                    onClick={() => setDetailId(card.id)}
                    style={{
                      background: "var(--ant-color-bg-container, #fff)",
                      borderRadius: 6,
                      borderLeft: `3px solid ${URGENCY_COLORS[card.urgency]}`,
                      padding: "6px 8px",
                      cursor: "pointer",
                      display: "flex",
                      flexDirection: "column",
                      gap: 4,
                    }}
                  >
                    <Typography.Text style={{ fontSize: 13 }} ellipsis>
                      {card.title}
                    </Typography.Text>
                    <Space size={4} wrap>
                      {card.urgency === "urgent" && <Tag color="red">{t("taskboard.urgency.urgent")}</Tag>}
                      {card.urgency === "low" && <Tag>{t("taskboard.urgency.low")}</Tag>}
                      {card.blocked && <Tag color="orange">{t("taskboard.blocked")}</Tag>}
                      {running && <Tag color="processing">{t("taskboard.running")}</Tag>}
                      {total > 0 && (
                        <Typography.Text type={doneCount < total && card.status === "in_review" ? "danger" : "secondary"} style={{ fontSize: 12 }}>
                          ☑ {doneCount}/{total}
                        </Typography.Text>
                      )}
                    </Space>
                  </div>
                );
              })}
              {cards.length === 0 && (
                <Typography.Text type="secondary" style={{ fontSize: 12, textAlign: "center", padding: 8 }}>
                  {t("taskboard.empty")}
                </Typography.Text>
              )}
            </div>
          );
        })}
      </div>

      <Drawer
        title={detail?.title || detailId}
        open={!!detailId}
        zIndex={1300}
        onClose={() => setDetailId(null)}
        width={520}
      >
        {detail ? (
          <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
            <Space size={6} wrap>
              <Tag>{detail.id}</Tag>
              <Tag color={URGENCY_COLORS[detail.urgency]}>{t(`taskboard.urgency.${detail.urgency}`)}</Tag>
              <Tag>{t(`taskboard.col.${detail.status === "in_progress" ? "inProgress" : detail.status === "in_review" ? "inReview" : detail.status}`)}</Tag>
              {detail.holder && <Tag color="processing">{detail.holder}</Tag>}
            </Space>
            <Space size={6} wrap>
              {actionButtons(detail)}
              {detail.status !== "done" && (
                <Popconfirm
                  zIndex={1300}
                  title={t("taskboard.deleteConfirm")}
                  onConfirm={() => deleteMutation.mutate(detail.id)}
                >
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              )}
            </Space>
            {detail.description && <Typography.Paragraph>{detail.description}</Typography.Paragraph>}
            {detail.prompt && (
              <div>
                <Typography.Text type="secondary">{t("taskboard.prompt")}</Typography.Text>
                <pre style={{ whiteSpace: "pre-wrap", background: "rgba(128,128,128,0.08)", padding: 8, borderRadius: 6, fontSize: 12 }}>{detail.prompt}</pre>
              </div>
            )}
            {(detail.checklist ?? []).length > 0 && (
              <div>
                <Typography.Text strong>{t("taskboard.checklist")}</Typography.Text>
                <div style={{ display: "flex", flexDirection: "column", gap: 4, marginTop: 6 }}>
                  {detail.checklist!.map((item, index) => (
                    <Checkbox
                      key={index}
                      checked={item.done}
                      onChange={(event) => toggleChecklist(detail, index, event.target.checked)}
                    >
                      <Typography.Text delete={item.done} type={item.done ? "secondary" : undefined}>
                        {item.text}
                      </Typography.Text>
                      {item.evidence && (
                        <Typography.Text type="secondary" style={{ fontSize: 12, marginLeft: 6 }}>
                          ({item.evidence})
                        </Typography.Text>
                      )}
                    </Checkbox>
                  ))}
                </div>
              </div>
            )}
            {(detail.comments ?? []).length > 0 && (
              <div>
                <Typography.Text strong>{t("taskboard.comments")}</Typography.Text>
                <div style={{ display: "flex", flexDirection: "column", gap: 6, marginTop: 6 }}>
                  {detail.comments!.map((comment, index) => (
                    <div key={index} style={{ background: "rgba(128,128,128,0.06)", borderRadius: 6, padding: "4px 8px" }}>
                      <Typography.Text strong style={{ fontSize: 12 }}>{comment.author}</Typography.Text>
                      <Typography.Text style={{ fontSize: 13 }}> {comment.text}</Typography.Text>
                    </div>
                  ))}
                </div>
              </div>
            )}
            <div>
              <Typography.Text strong>{t("taskboard.executions")}</Typography.Text>
              {(detail.executions ?? []).length === 0 ? (
                <div style={{ marginTop: 6 }}>
                  <Typography.Text type="secondary">{t("taskboard.noExecutions")}</Typography.Text>
                </div>
              ) : (
                <div style={{ display: "flex", flexDirection: "column-reverse", gap: 6, marginTop: 6 }}>
                  {detail.executions!.map((execution) => (
                    <div key={execution.id} style={{ background: "rgba(128,128,128,0.06)", borderRadius: 6, padding: "4px 8px" }}>
                      <Space size={6}>
                        <Tag color={EXECUTION_STATUS_COLORS[execution.status] || "default"}>{execution.status}</Tag>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>{execution.session_id}</Typography.Text>
                        {execution.host && (
                          <Button size="small" type="link" onClick={() => jumpToHost(execution)}>
                            {t("taskboard.viewProgress")}
                          </Button>
                        )}
                      </Space>
                      {execution.summary && (
                        <Typography.Paragraph style={{ fontSize: 12, marginBottom: 0, marginTop: 4 }} ellipsis={{ rows: 4, expandable: true }}>
                          {execution.summary}
                        </Typography.Paragraph>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        ) : (
          <Empty description={t("app.loading")} />
        )}
      </Drawer>

      <Modal
        title={t("taskboard.newCard")}
        open={createOpen}
        zIndex={1300}
        onCancel={() => setCreateOpen(false)}
        onOk={() => {
          if (!createTitle.trim()) {
            message.warning(t("taskboard.titleRequired"));
            return;
          }
          createMutation.mutate();
        }}
        confirmLoading={createMutation.isPending}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
          <Input placeholder={t("taskboard.title")} value={createTitle} onChange={(event) => setCreateTitle(event.target.value)} />
          <Input.TextArea rows={2} placeholder={t("taskboard.description")} value={createDescription} onChange={(event) => setCreateDescription(event.target.value)} />
          <Input.TextArea rows={3} placeholder={t("taskboard.prompt")} value={createPrompt} onChange={(event) => setCreatePrompt(event.target.value)} />
          <Select
            value={createUrgency}
            onChange={(value) => setCreateUrgency(value)}
            style={{ width: 160 }}
            options={[
              { value: "urgent", label: t("taskboard.urgency.urgent") },
              { value: "normal", label: t("taskboard.urgency.normal") },
              { value: "low", label: t("taskboard.urgency.low") },
            ]}
          />
          <Input.TextArea
            rows={3}
            placeholder={t("taskboard.checklistHint")}
            value={createChecklist}
            onChange={(event) => setCreateChecklist(event.target.value)}
          />
        </div>
      </Modal>
    </div>
  );
}
