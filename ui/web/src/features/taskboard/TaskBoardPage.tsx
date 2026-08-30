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
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  CompassOutlined,
  DeleteOutlined,
  EditOutlined,
  FolderOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ProfileOutlined,
  ReloadOutlined,
  ScheduleOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { buildChatRoute } from "../../lib/chatRoutes";
import { showError } from "../../lib/notifications";
import { useSettingsStore } from "../../store/settings";
import { CronExprInput } from "../../components/CronExprInput";
import {
  createCronJob,
  createTaskboardCard,
  createTaskboardProject,
  deleteCronJob,
  deleteTaskboardCard,
  deleteTaskboardProject,
  executeTaskboardCard,
  getTaskboardCard,
  listAgentTemplates,
  listCronJobs,
  listSessions,
  listTaskboardCards,
  listTaskboardProjects,
  observeTaskboardExecution,
  patchTaskboardCard,
  reconcileTaskboard,
  recoverTaskboardExecution,
  retryTaskboardExecution,
  runCronJob,
  updateCronJob,
  updateTaskboardProject,
} from "../../lib/api";
import type { CronJob, TaskboardCard, TaskboardCardPatchInput, TaskboardExecution, TaskboardExecutionObservation, TaskboardProject, TaskboardResearch, TaskboardStatus, TaskboardUrgency } from "../../lib/types";

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

/**
 * buildResearch turns the comma-separated research form fields into the
 * structured 方案A 上下文传递 asset. Returns undefined when every field is
 * empty so a card submitted without research does not carry an empty block.
 */
function buildResearch(input: {
  facts: string;
  locations: string;
  excluded: string;
  open: string;
}): TaskboardResearch | undefined {
  const splitLines = (s: string) =>
    s
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean);
  const facts = splitLines(input.facts);
  const locations = splitLines(input.locations);
  const excluded = splitLines(input.excluded);
  const open = splitLines(input.open);
  if (!facts.length && !locations.length && !excluded.length && !open.length) {
    return undefined;
  }
  return {
    facts,
    locations,
    excluded_paths: excluded,
    open_questions: open,
  };
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
  const [createProjectID, setCreateProjectID] = useState<string | undefined>(undefined);
  const [createWorkDir, setCreateWorkDir] = useState<string | undefined>(undefined);
  const [createTitle, setCreateTitle] = useState("");
  const [createDescription, setCreateDescription] = useState("");
  const [createPrompt, setCreatePrompt] = useState("");
  const [createUrgency, setCreateUrgency] = useState<TaskboardUrgency>("normal");
  const [createChecklist, setCreateChecklist] = useState("");
  const [createTouchedPaths, setCreateTouchedPaths] = useState("");
  const [createTemplateID, setCreateTemplateID] = useState<string | undefined>(undefined);
  const [createResearchFacts, setCreateResearchFacts] = useState("");
  const [createResearchLocations, setCreateResearchLocations] = useState("");
  const [createResearchExcluded, setCreateResearchExcluded] = useState("");
  const [createResearchOpen, setCreateResearchOpen] = useState("");
  const [editOpen, setEditOpen] = useState(false);
  const [editId, setEditId] = useState("");
  const [editVersion, setEditVersion] = useState(0);
  const [editTitle, setEditTitle] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [editPrompt, setEditPrompt] = useState("");
  const [editUrgency, setEditUrgency] = useState<TaskboardUrgency>("normal");
  const [editChecklist, setEditChecklist] = useState("");
  const [editTouchedPaths, setEditTouchedPaths] = useState("");
  const [editTemplateID, setEditTemplateID] = useState<string | undefined>(undefined);
  const [editResearchFacts, setEditResearchFacts] = useState("");
  const [editResearchLocations, setEditResearchLocations] = useState("");
  const [editResearchExcluded, setEditResearchExcluded] = useState("");
  const [editResearchOpen, setEditResearchOpen] = useState("");
  const [commentText, setCommentText] = useState("");

  // ---- Project management (需求池 1): bind multiple work dirs to a project ----
  const [projectManageOpen, setProjectManageOpen] = useState(false);
  const [projectID, setProjectID] = useState<string | undefined>(undefined);
  const [projectName, setProjectName] = useState("");
  const [projectWorkDirs, setProjectWorkDirs] = useState("");

  // ---- PJM automation (M5 P3): a scheduled cron job sweeps the board ----
  const [pjmAutoOpen, setPjmAutoOpen] = useState(false);
  const [pjmEnabled, setPjmEnabled] = useState(false);
  const [pjmEverySeconds, setPjmEverySeconds] = useState<number>(3600);
  const [pjmCronExpr, setPjmCronExpr] = useState("0 3 * * *");
  const [pjmJobId, setPjmJobId] = useState<string | null>(null);

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["taskboard"] });
  };
  const fail = (error: unknown, key: string) => showError(message, error, t(key));

  const projectsQuery = useQuery({
    queryKey: ["taskboard", "projects", token],
    queryFn: async () => listTaskboardProjects(token || null),
  });

  const projects = projectsQuery.data?.projects ?? [];
  const workDirsFor = (projectID?: string) => {
    const project = projects.find((p) => p.id === projectID);
    const dirs = project?.work_dirs ?? [];
    if (dirs.length) return dirs;
    return project?.root_dir ? [project.root_dir] : [];
  };
  const openProjectManage = () => {
    setProjectID(undefined);
    setProjectName("");
    setProjectWorkDirs("");
    setProjectManageOpen(true);
  };
  const editProject = (project: TaskboardProject) => {
    setProjectID(project.id);
    setProjectName(project.name);
    setProjectWorkDirs((project.work_dirs ?? []).join("\n"));
    setProjectManageOpen(true);
  };

  // ---- Project management mutations (bind multiple work dirs) ----
  const createProjectMutation = useMutation({
    mutationFn: async () =>
      createTaskboardProject(token || null, {
        name: projectName,
        work_dirs: projectWorkDirs
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean),
      }),
    onSuccess: () => {
      message.success(t("taskboard.projectCreated"));
      void projectsQuery.refetch();
      setProjectManageOpen(false);
      setProjectID(undefined);
      setProjectName("");
      setProjectWorkDirs("");
    },
    onError: (error) => fail(error, "taskboard.projectSaveFailed"),
  });

  const updateProjectMutation = useMutation({
    mutationFn: async () =>
      updateTaskboardProject(token || null, projectID || "", {
        name: projectName,
        work_dirs: projectWorkDirs
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean),
      }),
    onSuccess: () => {
      message.success(t("taskboard.projectUpdated"));
      void projectsQuery.refetch();
      setProjectManageOpen(false);
      setProjectID(undefined);
      setProjectName("");
      setProjectWorkDirs("");
    },
    onError: (error) => fail(error, "taskboard.projectSaveFailed"),
  });

  const deleteProjectMutation = useMutation({
    mutationFn: async (id: string) => deleteTaskboardProject(token || null, id),
    onSuccess: () => {
      message.success(t("taskboard.projectDeleted"));
      void projectsQuery.refetch();
      setProjectManageOpen(false);
    },
    onError: (error) => fail(error, "taskboard.projectDeleteFailed"),
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
  const templatesQuery = useQuery({
    queryKey: ["agent-templates", token],
    queryFn: () => listAgentTemplates(token || null),
  });

  // The PJM sweep job is identified by a fixed name; at most one exists.
  const PJM_JOB_NAME = "taskboard-pjm-sweep";
  const pjmJobQuery = useQuery({
    queryKey: ["cron", "pjm", token],
    queryFn: async () => {
      const jobs = await listCronJobs(token || null);
      return jobs.find((job) => job.name === PJM_JOB_NAME) ?? null;
    },
  });
  const pjmJob = pjmJobQuery.data;

  const savePjmJob = useMutation({
    mutationFn: async () => {
      const body = {
        name: PJM_JOB_NAME,
        message: t("taskboard.pjmAutoMessage"),
        timezone: "Asia/Shanghai",
        schedule: {
          type: pjmCronExpr.trim() && pjmCronExpr.trim() !== "every" ? "cron" : "every",
          every_seconds: pjmCronExpr.trim() === "every" || !pjmCronExpr.trim() ? pjmEverySeconds : undefined,
          cron_expr: pjmCronExpr.trim() && pjmCronExpr.trim() !== "every" ? pjmCronExpr.trim() : undefined,
        },
        session_mode: "shared",
        enabled: pjmEnabled,
      };
      if (pjmJob) {
        const updated = await updateCronJob(token || null, pjmJob.id, body);
        setPjmJobId(updated.id);
        return;
      }
      const created = await createCronJob(token || null, body);
      setPjmJobId(created.id);
    },
    onSuccess: () => {
      message.success(t("taskboard.pjmAutoSaved"));
      void pjmJobQuery.refetch();
    },
    onError: (error) => fail(error, "taskboard.pjmAutoSaveFailed"),
  });

  const runPjmJob = useMutation({
    mutationFn: (jobId: string) => runCronJob(token || null, jobId),
    onSuccess: () => message.success(t("taskboard.pjmAutoRunDone")),
    onError: (error) => fail(error, "taskboard.pjmAutoRunFailed"),
  });

  const deletePjmJob = useMutation({
    mutationFn: (jobId: string) => deleteCronJob(token || null, jobId),
    onSuccess: () => {
      message.success(t("taskboard.pjmAutoDeleted"));
      setPjmJobId(null);
      setPjmEnabled(false);
      void pjmJobQuery.refetch();
    },
    onError: (error) => fail(error, "taskboard.pjmAutoSaveFailed"),
  });

  const createMutation = useMutation({
    mutationFn: async () =>
      createTaskboardCard(token || null, {
        project_id: createProjectID || projectFilter || undefined,
        work_dir: createWorkDir,
        title: createTitle,
        description: createDescription || undefined,
        prompt: createPrompt || undefined,
        urgency: createUrgency,
        template_id: createTemplateID,
        touched_paths: createTouchedPaths
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean),
        research: buildResearch({
          facts: createResearchFacts,
          locations: createResearchLocations,
          excluded: createResearchExcluded,
          open: createResearchOpen,
        }),
        checklist: createChecklist
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean),
      }),
    onSuccess: () => {
      message.success(t("taskboard.created"));
      setCreateOpen(false);
      setCreateProjectID(undefined);
      setCreateWorkDir(undefined);
      setCreateTitle("");
      setCreateDescription("");
      setCreatePrompt("");
      setCreateChecklist("");
      setCreateTouchedPaths("");
      setCreateResearchFacts("");
      setCreateResearchLocations("");
      setCreateResearchExcluded("");
      setCreateResearchOpen("");
      setCreateTemplateID(undefined);
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

  const recoverMutation = useMutation({
    mutationFn: async ({ cardId, executionId, text }: { cardId: string; executionId: string; text: string }) =>
      recoverTaskboardExecution(token || null, cardId, executionId, text),
    onSuccess: () => {
      message.success(t("taskboard.recoverySent"));
      detailQuery.refetch();
    },
    onError: (error) => fail(error, "taskboard.recoveryFailed"),
  });

  const retryMutation = useMutation({
    mutationFn: async ({ cardId, executionId }: { cardId: string; executionId: string }) =>
      retryTaskboardExecution(token || null, cardId, executionId),
    onSuccess: () => {
      message.success(t("taskboard.retrySubmitted"));
      detailQuery.refetch();
    },
    onError: (error) => fail(error, "taskboard.retryFailed"),
  });

  const reconcileMutation = useMutation({
    mutationFn: async () => reconcileTaskboard(token || null),
    onSuccess: (report) => {
      message.success(t("taskboard.reconcileDone", { scanned: report.reconcile_report.scanned, finalized: report.reconcile_report.finalized }));
      invalidate();
      if (detailId) detailQuery.refetch();
    },
    onError: (error) => fail(error, "taskboard.reconcileFailed"),
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

  const openEdit = (card: TaskboardCard) => {
    setEditId(card.id);
    setEditVersion(card.version);
    setEditTitle(card.title);
    setEditDescription(card.description ?? "");
    setEditPrompt(card.prompt ?? "");
    setEditUrgency(card.urgency);
    setEditTouchedPaths((card.touched_paths ?? []).join("\n"));
    setEditChecklist((card.checklist ?? []).map((item) => item.text).join("\n"));
    setEditResearchFacts((card.research?.facts ?? []).join("\n"));
    setEditResearchLocations((card.research?.locations ?? []).join("\n"));
    setEditResearchExcluded((card.research?.excluded_paths ?? []).join("\n"));
    setEditResearchOpen((card.research?.open_questions ?? []).join("\n"));
    setEditTemplateID(card.template_id);
    setEditOpen(true);
  };

  const submitEdit = () => {
    if (!editTitle.trim()) {
      message.warning(t("taskboard.titleRequired"));
      return;
    }
    patchMutation.mutate({
      cardId: editId,
      body: {
        action: "update",
        version: editVersion,
        title: editTitle.trim(),
        description: editDescription.trim() || undefined,
        prompt: editPrompt.trim() || undefined,
        urgency: editUrgency,
        template_id: editTemplateID,
        touched_paths: editTouchedPaths
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean),
        checklist: editChecklist
          .split("\n")
          .map((line) => line.trim())
          .filter(Boolean),
        research: buildResearch({
          facts: editResearchFacts,
          locations: editResearchLocations,
          excluded: editResearchExcluded,
          open: editResearchOpen,
        }),
      },
    });
    setEditOpen(false);
  };

  const submitComment = () => {
    if (!commentText.trim()) {
      return;
    }
    patchMutation.mutate(
      {
        cardId: detailId || "",
        body: { action: "comment", version: detail?.version ?? 0, text: commentText.trim() },
      },
      { onSuccess: () => setCommentText("") },
    );
  };

  const jumpToHost = async (execution: TaskboardExecution) => {
    // Prefer the execution's OWN isolated session: the run's messages, tool
    // calls and timeline live there. The host session may be an empty/new
    // chat and carries none of it — job session first, host as fallback.
    const targetIDs = [execution.job_session_id, execution.host?.session_id].filter(
      (id): id is string => !!id,
    );
    try {
      const sessions = await listSessions(token || null);
      for (const id of targetIDs) {
        const target = sessions.find((session) => session.session_id === id);
        if (target) {
          navigate(buildChatRoute(target.locator));
          setDetailId(null);
          return;
        }
      }
    } catch {
      // fall through to the coarse route below
    }
    const host = execution.host;
    if (!host) return;
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
          icon={<FolderOutlined />}
          onClick={() => openProjectManage()}
        >
          {t("taskboard.manageProjects")}
        </Button>
        <Button icon={<CompassOutlined />} onClick={() => navigate(buildChatRoute({ channel: "pjm", key: "pjm", metadata: { template: "pjm" } }))}>
          {t("taskboard.pjmChat")}
        </Button>
        <Button icon={<ScheduleOutlined />} onClick={() => {
          if (pjmJob) {
            setPjmJobId(pjmJob.id);
            setPjmEnabled(pjmJob.enabled);
            const sched = pjmJob.schedule;
            if (sched?.type === "cron" && sched.cron_expr) {
              setPjmCronExpr(sched.cron_expr);
            } else {
              setPjmCronExpr("every");
              setPjmEverySeconds(sched?.every_seconds || 3600);
            }
          }
          setPjmAutoOpen(true);
        }}>
          {t("taskboard.pjmAuto")}
        </Button>
        <Button
          icon={<ProfileOutlined />}
          loading={reconcileMutation.isPending}
          onClick={() => reconcileMutation.mutate()}
        >
          {t("taskboard.reconcile")}
        </Button>
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
              {detail.template_id && <Tag color="geekblue">{detail.template_id}</Tag>}
            </Space>
            <Space size={6} wrap>
              {actionButtons(detail)}
              {detail.status !== "done" && (
                <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(detail)}>
                  {t("taskboard.edit")}
                </Button>
              )}
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
            {(detail.touched_paths ?? []).length > 0 && (
              <div>
                <Typography.Text strong>{t("taskboard.touchedPaths")}</Typography.Text>
                <div style={{ display: "flex", flexWrap: "wrap", gap: 4, marginTop: 6 }}>
                  {detail.touched_paths!.map((item) => (
                    <Tag key={item} color="cyan">{item}</Tag>
                  ))}
                </div>
              </div>
            )}
            {detail.research && (
              <div>
                <Typography.Text strong>{t("taskboard.research")}</Typography.Text>
                <div style={{ display: "flex", flexDirection: "column", gap: 8, marginTop: 6 }}>
                  {(detail.research.facts ?? []).length > 0 && (
                    <div>
                      <Typography.Text type="success" style={{ fontSize: 12 }}>{t("taskboard.researchFacts")}</Typography.Text>
                      <ul style={{ margin: "4px 0 0", paddingLeft: 18 }}>
                        {detail.research.facts!.map((item, i) => <li key={i}>{item}</li>)}
                      </ul>
                    </div>
                  )}
                  {(detail.research.locations ?? []).length > 0 && (
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>{t("taskboard.researchLocations")}</Typography.Text>
                      <div style={{ display: "flex", flexWrap: "wrap", gap: 4, marginTop: 4 }}>
                        {detail.research.locations!.map((item, i) => <Tag key={i} color="blue">{item}</Tag>)}
                      </div>
                    </div>
                  )}
                  {(detail.research.excluded_paths ?? []).length > 0 && (
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>{t("taskboard.researchExcluded")}</Typography.Text>
                      <div style={{ display: "flex", flexWrap: "wrap", gap: 4, marginTop: 4 }}>
                        {detail.research.excluded_paths!.map((item, i) => <Tag key={i} color="red">{item}</Tag>)}
                      </div>
                    </div>
                  )}
                  {(detail.research.open_questions ?? []).length > 0 && (
                    <div>
                      <Typography.Text type="warning" style={{ fontSize: 12 }}>{t("taskboard.researchOpen")}</Typography.Text>
                      <ul style={{ margin: "4px 0 0", paddingLeft: 18 }}>
                        {detail.research.open_questions!.map((item, i) => <li key={i}>{item}</li>)}
                      </ul>
                    </div>
                  )}
                </div>
              </div>
            )}
            {(detail.merge_report?.conflicts?.length ?? 0) > 0 && detail.merge_report && (
              <div>
                <Typography.Text strong type="danger">{t("taskboard.mergeConflict")}</Typography.Text>
                <div style={{ display: "flex", flexDirection: "column", gap: 6, marginTop: 6 }}>
                  {detail.merge_report.conflicts!.map((conflict, index) => (
                    <div key={index} style={{ background: "rgba(255,0,0,0.06)", borderRadius: 6, padding: "4px 8px" }}>
                      <Typography.Text type="danger" style={{ fontSize: 12 }}>
                        {conflict.path} ↔ {conflict.other_path}
                      </Typography.Text>
                      <Typography.Text type="secondary" style={{ fontSize: 12, display: "block" }}>
                        {t("taskboard.conflictsWith")}: {conflict.other_card} ({conflict.other_title})
                      </Typography.Text>
                    </div>
                  ))}
                </div>
              </div>
            )}
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
            <div>
              <Typography.Text strong>{t("taskboard.comments")}</Typography.Text>
              {(detail.comments ?? []).length > 0 && (
                <div style={{ display: "flex", flexDirection: "column", gap: 6, marginTop: 6 }}>
                  {detail.comments!.map((comment, index) => (
                    <div key={index} style={{ background: "rgba(128,128,128,0.06)", borderRadius: 6, padding: "4px 8px" }}>
                      <Typography.Text strong style={{ fontSize: 12 }}>{comment.author}</Typography.Text>
                      <Typography.Text style={{ fontSize: 13 }}> {comment.text}</Typography.Text>
                    </div>
                  ))}
                </div>
              )}
              <div style={{ display: "flex", gap: 6, marginTop: 8 }}>
                <Input
                  placeholder={t("taskboard.commentPlaceholder")}
                  value={commentText}
                  onChange={(event) => setCommentText(event.target.value)}
                  onPressEnter={submitComment}
                />
                <Button type="primary" loading={patchMutation.isPending} onClick={submitComment}>
                  {t("taskboard.commentSubmit")}
                </Button>
              </div>
            </div>
            <div>
              <Typography.Text strong>{t("taskboard.executions")}</Typography.Text>
              {(detail.executions ?? []).length === 0 ? (
                <div style={{ marginTop: 6 }}>
                  <Typography.Text type="secondary">{t("taskboard.noExecutions")}</Typography.Text>
                </div>
              ) : (
                <div style={{ display: "flex", flexDirection: "column-reverse", gap: 6, marginTop: 6 }}>
                  {detail.executions!.map((execution) => (
                    <ExecutionRow
                      key={execution.id}
                      execution={execution}
                      card={detail}
                      onJump={() => jumpToHost(execution)}
                      onInvalidate={() => detailQuery.refetch()}
                      onRecover={(text) => recoverMutation.mutate({ cardId: detail.id, executionId: execution.id, text })}
                      onRetry={() => retryMutation.mutate({ cardId: detail.id, executionId: execution.id })}
                    />
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
          <Select
            allowClear
            placeholder={t("taskboard.projectPlaceholder")}
            value={createProjectID}
            onChange={setCreateProjectID}
            options={(projectsQuery.data?.projects ?? []).map((project) => ({
              value: project.id,
              label: project.name,
            }))}
          />
          {workDirsFor(createProjectID).length > 0 && (
            <Select
              allowClear
              placeholder={t("taskboard.workDirPlaceholder")}
              value={createWorkDir}
              onChange={setCreateWorkDir}
              options={workDirsFor(createProjectID).map((dir) => ({ value: dir, label: dir }))}
            />
          )}
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
          <Select
            allowClear
            placeholder={t("taskboard.templatePlaceholder")}
            value={createTemplateID}
            onChange={setCreateTemplateID}
            loading={templatesQuery.isLoading}
            options={(templatesQuery.data ?? [])
              .filter((tpl) => tpl.id?.trim())
              .map((tpl) => ({ value: tpl.id, label: tpl.avatar ? `${tpl.avatar} ${tpl.name || tpl.id}` : tpl.name || tpl.id }))}
          />
          <Input.TextArea
            rows={3}
            placeholder={t("taskboard.checklistHint")}
            value={createChecklist}
            onChange={(event) => setCreateChecklist(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.touchedPathsHint")}
            value={createTouchedPaths}
            onChange={(event) => setCreateTouchedPaths(event.target.value)}
          />
          <Input.TextArea
            rows={3}
            placeholder={t("taskboard.researchFactsHint")}
            value={createResearchFacts}
            onChange={(event) => setCreateResearchFacts(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.researchLocationsHint")}
            value={createResearchLocations}
            onChange={(event) => setCreateResearchLocations(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.researchExcludedHint")}
            value={createResearchExcluded}
            onChange={(event) => setCreateResearchExcluded(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.researchOpenHint")}
            value={createResearchOpen}
            onChange={(event) => setCreateResearchOpen(event.target.value)}
          />
        </div>
      </Modal>

      <Modal
        title={t("taskboard.editTitle")}
        open={editOpen}
        zIndex={1300}
        onCancel={() => setEditOpen(false)}
        onOk={submitEdit}
        confirmLoading={patchMutation.isPending}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
          <Input placeholder={t("taskboard.title")} value={editTitle} onChange={(event) => setEditTitle(event.target.value)} />
          <Input.TextArea rows={2} placeholder={t("taskboard.description")} value={editDescription} onChange={(event) => setEditDescription(event.target.value)} />
          <Input.TextArea rows={3} placeholder={t("taskboard.prompt")} value={editPrompt} onChange={(event) => setEditPrompt(event.target.value)} />
          <Select
            value={editUrgency}
            onChange={(value) => setEditUrgency(value)}
            style={{ width: 160 }}
            options={[
              { value: "urgent", label: t("taskboard.urgency.urgent") },
              { value: "normal", label: t("taskboard.urgency.normal") },
              { value: "low", label: t("taskboard.urgency.low") },
            ]}
          />
          <Select
            allowClear
            placeholder={t("taskboard.templatePlaceholder")}
            value={editTemplateID}
            onChange={setEditTemplateID}
            loading={templatesQuery.isLoading}
            options={(templatesQuery.data ?? [])
              .filter((tpl) => tpl.id?.trim())
              .map((tpl) => ({ value: tpl.id, label: tpl.avatar ? `${tpl.avatar} ${tpl.name || tpl.id}` : tpl.name || tpl.id }))}
          />
          <Input.TextArea
            rows={3}
            placeholder={t("taskboard.checklistHint")}
            value={editChecklist}
            onChange={(event) => setEditChecklist(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.touchedPathsHint")}
            value={editTouchedPaths}
            onChange={(event) => setEditTouchedPaths(event.target.value)}
          />
          <Input.TextArea
            rows={3}
            placeholder={t("taskboard.researchFactsHint")}
            value={editResearchFacts}
            onChange={(event) => setEditResearchFacts(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.researchLocationsHint")}
            value={editResearchLocations}
            onChange={(event) => setEditResearchLocations(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.researchExcludedHint")}
            value={editResearchExcluded}
            onChange={(event) => setEditResearchExcluded(event.target.value)}
          />
          <Input.TextArea
            rows={2}
            placeholder={t("taskboard.researchOpenHint")}
            value={editResearchOpen}
            onChange={(event) => setEditResearchOpen(event.target.value)}
          />
        </div>
      </Modal>

      <Modal
        title={t("taskboard.pjmAuto")}
        open={pjmAutoOpen}
        zIndex={1300}
        onCancel={() => setPjmAutoOpen(false)}
        footer={
          <Space>
            {pjmJob && (
              <Popconfirm title={t("taskboard.pjmAutoDeleteConfirm")} onConfirm={() => deletePjmJob.mutate(pjmJob.id)}>
                <Button danger loading={deletePjmJob.isPending}>
                  {t("taskboard.pjmAutoDelete")}
                </Button>
              </Popconfirm>
            )}
            {pjmJob && (
              <Button loading={runPjmJob.isPending} onClick={() => runPjmJob.mutate(pjmJob.id)}>
                {t("taskboard.pjmAutoRunNow")}
              </Button>
            )}
            <Button type="primary" loading={savePjmJob.isPending} onClick={() => savePjmJob.mutate()}>
              {t("taskboard.pjmAutoSave")}
            </Button>
          </Space>
        }
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
          <Typography.Text type="secondary">{t("taskboard.pjmAutoHint")}</Typography.Text>
          <Space>
            <Checkbox
              checked={pjmEnabled}
              onChange={(e) => setPjmEnabled(e.target.checked)}
            >
              {t("taskboard.pjmAutoEnabled")}
            </Checkbox>
          </Space>
          <div style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
            <Typography.Text>{t("taskboard.pjmAutoSchedule")}</Typography.Text>
            <Select
              style={{ width: 130 }}
              value={pjmCronExpr.trim() === "every" || !pjmCronExpr.trim() ? "every" : "cron"}
              onChange={(v) => setPjmCronExpr(v === "every" ? "every" : pjmCronExpr)}
              options={[
                { value: "every", label: t("taskboard.pjmAutoEvery") },
                { value: "cron", label: t("taskboard.pjmAutoCron") },
              ]}
            />
            {pjmCronExpr.trim() === "every" || !pjmCronExpr.trim() ? (
              <InputNumber
                min={60}
                style={{ width: 120 }}
                value={pjmEverySeconds}
                onChange={(v) => setPjmEverySeconds(v ?? 3600)}
              />
            ) : (
              <CronExprInput
                value={pjmCronExpr}
                onChange={(v) => setPjmCronExpr(v)}
                placeholder="0 3 * * *"
                timezone="Asia/Shanghai"
              />
            )}
          </div>
          {pjmJob && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t("taskboard.pjmAutoLast")}: {pjmJob.last_run_at ? new Date(pjmJob.last_run_at).toLocaleString() : t("taskboard.pjmAutoNever")}
              {pjmJob.last_status ? ` · ${pjmJob.last_status}` : ""}
            </Typography.Text>
          )}
        </div>
      </Modal>

      <Modal
        title={t("taskboard.manageProjects")}
        open={projectManageOpen}
        zIndex={1300}
        onCancel={() => setProjectManageOpen(false)}
        onOk={() => {
          if (!projectName.trim()) {
            message.warning(t("taskboard.projectNameRequired"));
            return;
          }
          if (projectID) {
            updateProjectMutation.mutate();
          } else {
            createProjectMutation.mutate();
          }
        }}
        confirmLoading={createProjectMutation.isPending || updateProjectMutation.isPending}
        footer={
          <Space>
            {projectID && (
              <Popconfirm
                title={t("taskboard.projectDeleteConfirm")}
                onConfirm={() => deleteProjectMutation.mutate(projectID)}
              >
                <Button danger loading={deleteProjectMutation.isPending}>
                  {t("taskboard.projectDelete")}
                </Button>
              </Popconfirm>
            )}
            <Button onClick={() => setProjectManageOpen(false)}>{t("app.cancel")}</Button>
            <Button type="primary" loading={createProjectMutation.isPending || updateProjectMutation.isPending} onClick={() => {
              if (!projectName.trim()) {
                message.warning(t("taskboard.projectNameRequired"));
                return;
              }
              if (projectID) {
                updateProjectMutation.mutate();
              } else {
                createProjectMutation.mutate();
              }
            }}>
              {projectID ? t("taskboard.projectUpdate") : t("taskboard.projectCreate")}
            </Button>
          </Space>
        }
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
          <Select
            allowClear
            placeholder={t("taskboard.projectSelectPlaceholder")}
            value={projectID}
            onChange={(value) => {
              setProjectID(value || undefined);
              if (value) editProject(projects.find((p) => p.id === value) as TaskboardProject);
              else {
                setProjectName("");
                setProjectWorkDirs("");
              }
            }}
            options={projects.map((project) => ({
              value: project.id,
              label: project.name,
            }))}
          />
          <Input placeholder={t("taskboard.projectNamePlaceholder")} value={projectName} onChange={(event) => setProjectName(event.target.value)} />
          <Input.TextArea
            rows={4}
            placeholder={t("taskboard.projectWorkDirsHint")}
            value={projectWorkDirs}
            onChange={(event) => setProjectWorkDirs(event.target.value)}
          />
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t("taskboard.projectWorkDirsHelp")}
          </Typography.Text>
        </div>
      </Modal>
    </div>
  );
}

// ExecutionRow renders one execution record with its observability snapshot
// (stage / error type / last error / last tool) and, when running, controls to
// nudge the run back — append a recovery message or retry the last failed turn.
function ExecutionRow({
  execution,
  card,
  onJump,
  onInvalidate,
  onRecover,
  onRetry,
}: {
  execution: TaskboardExecution;
  card: TaskboardCard;
  onJump: () => void;
  onInvalidate: () => void;
  onRecover: (text: string) => void;
  onRetry: () => void;
}) {
  const { t } = useI18n();
  const [recoveryText, setRecoveryText] = useState("");
  const running = execution.status === "running";
  const error = !!execution.error_type || !!execution.last_error || execution.status === "failed";

  const submitRecover = () => {
    const text = recoveryText.trim();
    if (!text) return;
    onRecover(text);
    setRecoveryText("");
  };

  return (
    <div style={{ background: "rgba(128,128,128,0.06)", borderRadius: 6, padding: "4px 8px" }}>
      <Space size={6} wrap>
        <Tag color={EXECUTION_STATUS_COLORS[execution.status] || "default"}>{execution.status}</Tag>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>{execution.session_id}</Typography.Text>
        {execution.stage && <Tag color={STAGE_COLORS[execution.stage] || "default"}>{execution.stage}</Tag>}
        {execution.error_type && <Tag color="error">{execution.error_type}</Tag>}
        {execution.host && (
          <Button size="small" type="link" onClick={onJump}>
            {t("taskboard.viewProgress")}
          </Button>
        )}
      </Space>
      {(execution.last_tool || error) && (
        <Space size={6} wrap style={{ marginTop: 4 }}>
          {execution.last_tool && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t("taskboard.lastTool")}: {execution.last_tool}
            </Typography.Text>
          )}
          {execution.last_error && (
            <Typography.Text type="danger" style={{ fontSize: 12 }}>
              {execution.last_error}
            </Typography.Text>
          )}
        </Space>
      )}
      {execution.summary && (
        <Typography.Paragraph style={{ fontSize: 12, marginBottom: 0, marginTop: 4 }} ellipsis={{ rows: 4, expandable: true }}>
          {execution.summary}
        </Typography.Paragraph>
      )}
      {running && (
        <div style={{ display: "flex", gap: 6, marginTop: 4, alignItems: "center" }}>
          <Input
            size="small"
            placeholder={t("taskboard.recoverPlaceholder")}
            value={recoveryText}
            onChange={(event) => setRecoveryText(event.target.value)}
            onPressEnter={submitRecover}
            style={{ flex: 1 }}
          />
          <Button size="small" type="primary" ghost disabled={!recoveryText.trim()} onClick={submitRecover}>
            {t("taskboard.recover")}
          </Button>
          <Button size="small" onClick={onRetry}>
            {t("taskboard.retry")}
          </Button>
        </div>
      )}
    </div>
  );
}

const STAGE_COLORS: Record<string, string> = {
  thinking: "purple",
  tool_call: "geekblue",
  final_response: "green",
  waiting_approval: "warning",
  error: "error",
  interrupted: "default",
  idle: "default",
};
