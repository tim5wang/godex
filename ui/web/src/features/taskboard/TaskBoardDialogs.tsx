import { useState } from "react";
import { Button, Checkbox, Drawer, Empty, Input, InputNumber, List, Modal, Popconfirm, Select, Space, Tag, Tooltip, Typography } from "antd";
import { CompassOutlined, DeleteOutlined, EditOutlined, FolderOutlined, PlayCircleOutlined, PlusOutlined, ProfileOutlined, ReloadOutlined, ScheduleOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { buildChatRoute } from "../../lib/chatRoutes";
import { CronExprInput } from "../../components/CronExprInput";
import type { TaskboardCard, TaskboardExecution, TaskboardProject, TaskboardStatus, TaskboardUrgency } from "../../lib/types";
import type { TaskBoardController } from "./TaskBoardPage";
import { COLUMNS, EXECUTION_STATUS_COLORS, STAGE_COLORS, URGENCY_COLORS, urgencyRank } from "./taskboardViewModel";

export function TaskBoardDialogs({ controller }: { controller: TaskBoardController }) {
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
    <>
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
        title={t("taskboard.reconcileDetail")}
        open={reconcileOpen}
        zIndex={1300}
        footer={null}
        onCancel={() => setReconcileOpen(false)}
        width={720}
      >
        {reconcileReport && (
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            <Space wrap>
              <Tag>{t("taskboard.reconcileDetailScanned", { count: reconcileReport.scanned })}</Tag>
              <Tag color="blue">{t("taskboard.reconcileDetailObserved", { count: reconcileReport.observed })}</Tag>
              <Tag color="red">{t("taskboard.reconcileDetailFinalized", { count: reconcileReport.finalized })}</Tag>
              <Tag color="orange">{t("taskboard.reconcileDetailStalled", { count: reconcileReport.stalled })}</Tag>
            </Space>
            {(reconcileReport.results?.length || 0) > 0 && (
              <List
                size="small"
                header={<Typography.Text strong>{t("taskboard.reconcileDetailExecutions")}</Typography.Text>}
                dataSource={reconcileReport.results || []}
                renderItem={(item) => (
                  <List.Item>
                    <Space direction="vertical" size={0} style={{ width: "100%" }}>
                      <Space wrap style={{ width: "100%" }}>
                        <Typography.Text strong>{item.card_title}</Typography.Text>
                        <Tag color={item.action === "finalized" ? "red" : item.action === "stalled" ? "orange" : "default"}>
                          {item.action}
                        </Tag>
                        {item.stage && <Tag>{item.stage}</Tag>}
                        {item.error_type && <Tag color="error">{item.error_type}</Tag>}
                        {item.stall && <Tag color="orange">{item.stall_reason || "stalled"}</Tag>}
                      </Space>
                      {item.last_error && (
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {item.last_error}
                        </Typography.Text>
                      )}
                    </Space>
                  </List.Item>
                )}
              />
            )}
            {(reconcileReport.signals?.length || 0) > 0 && (
              <List
                size="small"
                header={<Typography.Text strong>{t("taskboard.reconcileDetailSignals")}</Typography.Text>}
                dataSource={reconcileReport.signals || []}
                renderItem={(item) => (
                  <List.Item>
                    <Space wrap style={{ width: "100%" }}>
                      <Typography.Text strong>{item.card_title}</Typography.Text>
                      <Tag color="warning">{item.field}</Tag>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {item.problem}
                      </Typography.Text>
                    </Space>
                  </List.Item>
                )}
              />
            )}
            {(reconcileReport.results?.length || 0) === 0 && (reconcileReport.signals?.length || 0) === 0 && (
              <Empty description={t("taskboard.reconcileDetailEmpty")} />
            )}
          </Space>
        )}
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
    </>
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
