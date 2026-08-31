import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { App as AntApp, Input, Modal } from "antd";
import { useI18n } from "../../i18n";
import { buildChatRoute } from "../../lib/chatRoutes";
import { showError } from "../../lib/notifications";
import { useSettingsStore } from "../../store/settings";
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
import type { CronJob, TaskboardCard, TaskboardCardPatchInput, TaskboardExecution, TaskboardExecutionObservation, TaskboardProject, TaskboardReconcileReport, TaskboardResearch, TaskboardStatus, TaskboardUrgency } from "../../lib/types";

import { TaskBoardView } from "./TaskBoardView";
import { COLUMNS } from "./taskboardViewModel";
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

export function useTaskBoardController() {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const [projectFilter, setProjectFilter] = useState<string>("");
  const [urgencyFilter, setUrgencyFilter] = useState<TaskboardUrgency | "">("");
  const [search, setSearch] = useState("");
  const [detailId, setDetailId] = useState<string | null>(null);
  const [reconcileOpen, setReconcileOpen] = useState(false);
  const [reconcileReport, setReconcileReport] = useState<TaskboardReconcileReport | null>(null);
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
      message.success(t("taskboard.reconcileDone", { scanned: report.reconcile_report.scanned, finalized: report.reconcile_report.finalized, stalled: report.reconcile_report.stalled }));
      setReconcileReport(report.reconcile_report);
      setReconcileOpen(true);
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

  return {
    t,
    message,
    navigate,
    projectFilter,
    setProjectFilter,
    urgencyFilter,
    setUrgencyFilter,
    search,
    setSearch,
    detailId,
    setDetailId,
    reconcileOpen,
    setReconcileOpen,
    reconcileReport,
    setReconcileReport,
    createOpen,
    setCreateOpen,
    createProjectID,
    setCreateProjectID,
    createWorkDir,
    setCreateWorkDir,
    createTitle,
    setCreateTitle,
    createDescription,
    setCreateDescription,
    createPrompt,
    setCreatePrompt,
    createUrgency,
    setCreateUrgency,
    createChecklist,
    setCreateChecklist,
    createTouchedPaths,
    setCreateTouchedPaths,
    createTemplateID,
    setCreateTemplateID,
    createResearchFacts,
    setCreateResearchFacts,
    createResearchLocations,
    setCreateResearchLocations,
    createResearchExcluded,
    setCreateResearchExcluded,
    createResearchOpen,
    setCreateResearchOpen,
    editOpen,
    setEditOpen,
    editId,
    editVersion,
    editTitle,
    setEditTitle,
    editDescription,
    setEditDescription,
    editPrompt,
    setEditPrompt,
    editUrgency,
    setEditUrgency,
    editChecklist,
    setEditChecklist,
    editTouchedPaths,
    setEditTouchedPaths,
    editTemplateID,
    setEditTemplateID,
    editResearchFacts,
    setEditResearchFacts,
    editResearchLocations,
    setEditResearchLocations,
    editResearchExcluded,
    setEditResearchExcluded,
    editResearchOpen,
    setEditResearchOpen,
    commentText,
    setCommentText,
    projectManageOpen,
    setProjectManageOpen,
    projectID,
    setProjectID,
    projectName,
    setProjectName,
    projectWorkDirs,
    setProjectWorkDirs,
    pjmAutoOpen,
    setPjmAutoOpen,
    pjmEnabled,
    setPjmEnabled,
    pjmEverySeconds,
    setPjmEverySeconds,
    pjmCronExpr,
    setPjmCronExpr,
    pjmJobId,
    setPjmJobId,
    invalidate,
    projectsQuery,
    projects,
    workDirsFor,
    openProjectManage,
    editProject,
    createProjectMutation,
    updateProjectMutation,
    deleteProjectMutation,
    cardsQuery,
    detailQuery,
    templatesQuery,
    pjmJobQuery,
    pjmJob,
    savePjmJob,
    runPjmJob,
    deletePjmJob,
    createMutation,
    patchMutation,
    executeMutation,
    recoverMutation,
    retryMutation,
    reconcileMutation,
    deleteMutation,
    grouped,
    detail,
    advance,
    acceptCard,
    rejectCard,
    openEdit,
    submitEdit,
    submitComment,
    jumpToHost,
    toggleChecklist,
  };
}

export type TaskBoardController = ReturnType<typeof useTaskBoardController>;

export function TaskBoardPage() {
  return <TaskBoardView controller={useTaskBoardController()} />;
}
