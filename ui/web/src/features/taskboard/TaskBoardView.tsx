import { useState } from "react";
import { Button, Checkbox, Drawer, Empty, Input, InputNumber, List, Modal, Popconfirm, Select, Space, Tag, Tooltip, Typography } from "antd";
import { CompassOutlined, DeleteOutlined, EditOutlined, FolderOutlined, PlayCircleOutlined, PlusOutlined, ProfileOutlined, ReloadOutlined, ScheduleOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { buildChatRoute } from "../../lib/chatRoutes";
import { CronExprInput } from "../../components/CronExprInput";
import type { TaskboardCard, TaskboardExecution, TaskboardProject, TaskboardStatus, TaskboardUrgency } from "../../lib/types";
import type { TaskBoardController } from "./TaskBoardPage";
import { TaskBoardDialogs } from "./TaskBoardDialogs";
import { COLUMNS, EXECUTION_STATUS_COLORS, STAGE_COLORS, URGENCY_COLORS, urgencyRank } from "./taskboardViewModel";

export function TaskBoardView({ controller }: { controller: TaskBoardController }) {
  const {
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
  } = controller;


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

      <TaskBoardDialogs controller={controller} />
    </div>
  );
}
