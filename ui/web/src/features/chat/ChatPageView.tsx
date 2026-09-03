import { App as AntApp, Avatar, Grid, Space, Tooltip, Button, Divider, Typography, Alert, Select, Badge, Drawer, Spin, Tag } from "antd";
import { useParams, useSearchParams, useLocation, useNavigate, Link } from "react-router-dom";
import { useQueryClient, useQuery, useMutation } from "@tanstack/react-query";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { useNodeContextStore } from "../../store/nodeContext";
import { useChatStore, groupFeedItemsIntoTurns } from "../../store/chat";
import { useState, useRef, useEffect, useCallback, type PointerEvent as ReactPointerEvent, useMemo, type CSSProperties } from "react";
import { useLayoutStore } from "../../store/layout";
import type { SessionTimelineEntry, DurableSubagentReview, DurableSubagentMerge, FeedItem, ListedSession } from "../../lib/types";
import { type ReviewMergeFilter, buildReviewMergeSummary, defaultReviewMergeJobId, shouldAutoLoadReview } from "./reviewMergeCenter";
import { useConversationLayoutStore, type DockTab, DOCK_TABS } from "./layout/layoutStore";
import { getMeta, openSession, getNote, saveNote, getSnapshot, getSessionTimeline, getSessionTimelinePage, getSessionCompactions, listSessionSubagents, listSessionLongTasks, listPackageCommands, listCommands, listPackageRoles, getSessionContextInspector, getActiveSessionSkills, getModels, listSessions, approveSessionPermission, denySessionPermission, deleteSession, renameSession, APIError, cancelSessionTurn, cancelQueuedTurn, steerQueuedTurn, retrySessionTurn, resumeSessionTurn, setSessionModel, unloadSessionSkill, forkSession, reviewSessionSubagent, cancelSessionSubagent, resumeSessionSubagent, mergeSessionSubagent, runSessionLongTask, cancelSessionLongTask, finalizeSessionLongTaskStory, executeCommand, uploadAttachments, submitMessage, listSkillsCatalog, listAgentTemplates } from "../../lib/api";
import type { SkillCatalogEntry } from "../../lib/types";
import type { TerminalExecutionConfig } from "../../lib/terminalClient";
import { streamEvents } from "../../lib/sse";
import { isLongTaskRefluxMessage, LongTaskRefluxBubble } from "./LongTaskRefluxBubble";
import { readPersistedRefluxDismissed, writePersistedRefluxDismissed } from "./refluxDismissPersistence";
import { buildTaskOutcomes } from "./taskCenterOutcome";
import { locatorMatchesRoute, buildChatRouteForSession } from "../../lib/chatRoutes";
import { writeClipboardText } from "../../lib/clipboard";
import { type ComposerSubmission, Composer, type ComposerHandle } from "../../components/Composer";
import { VoiceBar } from "../../components/VoiceBar";
import { TaskCenterPanel } from "./TaskCenterPanel";
import { SessionsRail } from "./layout/SessionsRail";
import { VerticalRightOutlined, VerticalLeftOutlined, StopOutlined, CloseOutlined, PlusOutlined, ReloadOutlined, LogoutOutlined, BellOutlined, EditOutlined, EnterOutlined } from "@ant-design/icons";
import { DOCK_TAB_META } from "./layout/DockRail";
import { MessageFeedV2 } from "../../components/MessageFeedV2";
import { FilesPanel } from "../files/FilesPanel";
import { TerminalPanel } from "../terminal/TerminalPanel";
import { PreviewPanel } from "../preview/PreviewPanel";
import { ReviewMergeCenterPanel } from "./ReviewMergeCenterPanel";
import { type TimelineFilterState, defaultTimelineFilters, appendTimelineEvent, mergeChronologicalFeedItems, pendingSendToFeedItem, mergeSubagentItems, subagentJobToFeedItem, collectSubagentJobs, buildContextStatusSummary, shortTurnId } from "../../lib/timelineUtils";
import { compactWorkspaceName, noteContextMetadata, NoteContextBanner } from "./panels/NoteContextBanner";
import { InspectorTabs } from "./panels/InspectorTabs";
import { ApprovalBanner } from "./panels/ApprovalPanels";
import { ContextStatusInline } from "./panels/ContextPanels";
import { SubagentReviewPanel } from "./panels/TurnSubagentPanels";

function makeSessionKey() {
  return crypto.randomUUID();
}

const reasoningEffortOptions = [
  { value: "none", label: "None" },
  { value: "minimal", label: "Minimal" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X High" },
];

import type { ChatPageController } from "./ChatPage";

export function ChatPageView({ controller }: { controller: ChatPageController }) {
  const {
    message,
    routeChannel,
    routeSessionKey,
    searchParams,
    location,
    navigate,
    queryClient,
    screens,
    t,
    token,
    remoteNodeID,
    remoteNodeName,
    clearRemoteNode,
    defaultSessionKey,
    setDefaultSessionKey,
    sessionId,
    historyItems,
    overlayItems,
    pendingSends,
    addPendingSend,
    removePendingSend,
    status,
    running,
    currentTurnId,
    streamConnected,
    setSession,
    syncSnapshot,
    setRunningTurn,
    handleEvent,
    toggleTool,
    setStreamConnected,
    reset,
    sessionsOpen,
    setSessionsOpen,
    inspectorOpen,
    setInspectorOpen,
    inspectorCollapsed,
    openInspector,
    closeInspector,
    inspectorActiveKey,
    setInspectorActiveKey,
    uploadProgress,
    setUploadProgress,
    uploading,
    setUploading,
    queuedComposerFiles,
    setQueuedComposerFiles,
    timelineItems,
    setTimelineItems,
    subagentReview,
    setSubagentReview,
    subagentMergeResult,
    setSubagentMergeResult,
    subagentReviewOpen,
    setSubagentReviewOpen,
    reviewMergeOpen,
    setReviewMergeOpen,
    reviewMergeFilter,
    setReviewMergeFilter,
    reviewMergeSelectedJobId,
    setReviewMergeSelectedJobId,
    reviewSubagentTargetRef,
    reviewMergeAutoLoadJobRef,
    channelFilter,
    setChannelFilter,
    notifyArmed,
    setNotifyArmed,
    pendingModelProfileID,
    setPendingModelProfileID,
    pendingReasoningEffort,
    setPendingReasoningEffort,
    timelineFilters,
    setTimelineFilters,
    timelineCursor,
    setTimelineCursor,
    timelineCursorStack,
    setTimelineCursorStack,
    openTaskCenterPanel,
    refluxDismissed,
    setRefluxDismissed,
    scrollerRef,
    composerRef,
    stickToBottom,
    setStickToBottom,
    stickToBottomRef,
    handleFeedScroll,
    v2LeftCollapsed,
    v2RightCollapsed,
    v2ActiveDockTab,
    v2LeftWidth,
    v2RightWidth,
    v2ToggleLeft,
    v2ToggleRight,
    v2CloseRight,
    v2SetActiveDockTab,
    v2SetLeftWidth,
    v2SetRightWidth,
    v2SessionSearch,
    setV2SessionSearch,
    filesFocusPath,
    setFilesFocusPath,
    mountedDockTabs,
    setMountedDockTabs,
    toggleNotifyMe,
    terminalTabs,
    setTerminalTabs,
    activeTerminal,
    setActiveTerminal,
    addTerminalTab,
    closeTerminalTab,
    beginV2LeftResize,
    beginV2RightResize,
    metaQuery,
    authRequired,
    routeUserId,
    noteContextId,
    workspaceDirParam,
    modeParam,
    templateParam,
    skillsParam,
    sessionKey,
    sessionLocator,
    openQuery,
    skillsCatalogQuery,
    templatesQuery,
    activeTemplate,
    sessionWorkspaceDir,
    terminalExecution,
    noteContextQuery,
    saveMessageToNoteMutation,
    snapshotQuery,
    switchingSession,
    timelineQuery,
    compactionsQuery,
    currentTimelineTurnId,
    effectiveTimelineTurnId,
    timelinePageQuery,
    subagentsQuery,
    longTasksQuery,
    packageCommandsQuery,
    builtinCommandsQuery,
    packageRolesQuery,
    contextInspectorQuery,
    activeSkillsQuery,
    modelsQuery,
    sessionsQuery,
    items,
    v2Items,
    v2ItemsWithPending,
    refluxBubbles,
    subagentJobs,
    reviewMergeSummary,
    pendingPermissions,
    turnRecords,
    queuedTurns,
    taskOutcomes,
    contextInspector,
    contextStatus,
    sortedSessions,
    channels,
    filteredSessions,
    currentSession,
    compactHeader,
    sessionTitle,
    modelName,
    selectedProfileID,
    selectedProfile,
    sessionReasoningEffort,
    selectedReasoningEffort,
    modelGroupOptions,
    activeModelLabel,
    modelScopeLabel,
    modelScopeColor,
    workspaceDir,
    headerWorkspace,
    headerSubtitle,
    copySessionInfo,
    approvePermissionMutation,
    denyPermissionMutation,
    deleteSessionMutation,
    renameSessionMutation,
    cancelTurnMutation,
    cancelQueuedMutation,
    steerQueuedMutation,
    retryTurnMutation,
    resumeTurnMutation,
    modelMutation,
    acpAgentID,
    acpModelOptions,
    acpModelsLoading,
    acpModelMutation,
    selectedACPAgentModel,
    unloadSkillMutation,
    forkMutation,
    refreshSubagentViews,
    refreshLongTaskViews,
    reviewSubagentMutation,
    cancelSubagentMutation,
    resumeSubagentMutation,
    mergeSubagentMutation,
    runLongTaskMutation,
    cancelLongTaskMutation,
    finalizeLongTaskMutation,
    reviewSubagentInDrawer,
    reviewSubagentInCenter,
    openReviewMergeCenter,
    onSend,
    createSession,
    clearNoteContext,
    authError,
    updateTimelineFilters,
    goToNextTimelinePage,
    goToPreviousTimelinePage,
  } = controller;

  const inspectorPanel = (
    <InspectorTabs
      activeKey={inspectorActiveKey}
      onActiveKeyChange={setInspectorActiveKey}
      onCollapseInspector={v2CloseRight}
      taskCenterPanel={(
        <TaskCenterPanel
          outcomes={taskOutcomes}
          collapsed={false}
          onCollapsedChange={() => {
            closeInspector();
            setInspectorOpen(false);
          }}
          reviewingJobId={reviewSubagentMutation.isPending ? reviewSubagentMutation.variables : undefined}
          mergingJobId={mergeSubagentMutation.isPending ? mergeSubagentMutation.variables : undefined}
          resumingJobId={resumeSubagentMutation.isPending ? resumeSubagentMutation.variables : undefined}
          cancelingJobId={cancelSubagentMutation.isPending ? cancelSubagentMutation.variables : undefined}
          runningWorkflowId={runLongTaskMutation.isPending ? runLongTaskMutation.variables : undefined}
          cancelingLongTask={cancelLongTaskMutation.isPending ? cancelLongTaskMutation.variables : undefined}
          finalizingLongTask={finalizeLongTaskMutation.isPending ? finalizeLongTaskMutation.variables : undefined}
          onReviewSubagent={reviewSubagentInDrawer}
          onMergeSubagent={(jobId) => mergeSubagentMutation.mutate(jobId)}
          onResumeSubagent={(jobId) => resumeSubagentMutation.mutate(jobId)}
          onCancelSubagent={(jobId) => cancelSubagentMutation.mutate(jobId)}
          onRunLongTask={(workflowId) => runLongTaskMutation.mutate(workflowId)}
          onCancelLongTask={(workflowId, nodeId) => cancelLongTaskMutation.mutate({ workflowId, nodeId })}
          onFinalizeLongTask={(workflowId, nodeId) => finalizeLongTaskMutation.mutate({ workflowId, nodeId })}
          onOpenReviewMergeCenter={openReviewMergeCenter}
        />
      )}
      pendingPermissions={pendingPermissions}
      longTasks={longTasksQuery.data ?? []}
      longTasksLoading={longTasksQuery.isLoading || longTasksQuery.isFetching}
      subagentJobs={subagentJobs}
      packageRoles={packageRolesQuery.data ?? []}
      packageRolesLoading={packageRolesQuery.isLoading}
      turnRecords={turnRecords}
      timelineItems={timelineItems}
      compactions={compactionsQuery.data}
      compactionsLoading={compactionsQuery.isLoading || compactionsQuery.isFetching}
      timelinePage={timelinePageQuery.data}
      timelinePageLoading={timelinePageQuery.isLoading || timelinePageQuery.isFetching}
      timelineFilters={timelineFilters}
      currentTurnId={currentTimelineTurnId}
      canPreviousTimelinePage={timelineCursorStack.length > 0}
      timelinePageIndex={timelineCursorStack.length}
      onTimelineFiltersChange={updateTimelineFilters}
      onNextTimelinePage={goToNextTimelinePage}
      onPreviousTimelinePage={goToPreviousTimelinePage}
      contextInspector={contextInspector}
      contextLoading={contextInspectorQuery.isLoading}
      sessionId={openQuery.data?.session_id ?? ""}
      token={token}
      activeSkills={activeSkillsQuery.data ?? []}
      activeSkillsLoading={activeSkillsQuery.isLoading}
      unloadingSkill={unloadSkillMutation}
      approving={approvePermissionMutation}
      denying={denyPermissionMutation}
      reviewingSubagent={reviewSubagentMutation}
      cancelingSubagent={cancelSubagentMutation}
      resumingSubagent={resumeSubagentMutation}
      mergingSubagent={mergeSubagentMutation}
      onReviewSubagent={reviewSubagentInDrawer}
      onCancelSubagent={(jobId) => cancelSubagentMutation.mutate(jobId)}
      onResumeSubagent={(jobId) => resumeSubagentMutation.mutate(jobId)}
      onMergeSubagent={(jobId) => mergeSubagentMutation.mutate(jobId)}
      runningLongTask={runLongTaskMutation}
      cancelingLongTask={cancelLongTaskMutation}
      finalizingLongTask={finalizeLongTaskMutation}
      onRunLongTask={(workflowId) => runLongTaskMutation.mutate(workflowId)}
      onCancelLongTask={(workflowId, nodeId) => cancelLongTaskMutation.mutate({ workflowId, nodeId })}
      onFinalizeLongTask={(workflowId, nodeId) => finalizeLongTaskMutation.mutate({ workflowId, nodeId })}
      retrying={retryTurnMutation}
      resuming={resumeTurnMutation}
      onRetryTurn={(turnId) => {
        if (openQuery.data?.session_id) {
          retryTurnMutation.mutate({ sessionId: openQuery.data.session_id, turnId });
        }
      }}
      onResumeTurn={(turnId) => {
        if (openQuery.data?.session_id) {
          resumeTurnMutation.mutate({ sessionId: openQuery.data.session_id, turnId });
        }
      }}
      onUnloadSkill={(skillId) => unloadSkillMutation.mutate(skillId)}
    />
  );

  return (
    <>
        <div className="chat-v2-layout" style={{ "--chat-v2-left-width": v2LeftCollapsed ? "0px" : `${v2LeftWidth}px`, "--chat-v2-right-width": v2RightCollapsed ? "48px" : `${v2RightWidth}px` } as CSSProperties}>
          {/* Left rail */}
          <div className="chat-v2-left" data-collapsed={v2LeftCollapsed ? "true" : "false"}>
            {!v2LeftCollapsed ? (
              <div className="chat-v2-left-resize" onPointerDown={beginV2LeftResize} role="separator" aria-label="Resize left panel" />
            ) : null}
            <SessionsRail
              collapsed={v2LeftCollapsed}
              sessions={filteredSessions}
              loading={sessionsQuery.isLoading}
              error={sessionsQuery.isError}
              onRetry={() => void queryClient.invalidateQueries({ queryKey: ["sessions", token, remoteNodeID] })}
              activeSessionId={openQuery.data?.session_id ?? ""}
              searchQuery={v2SessionSearch ?? ""}
              deletingSessionId={deleteSessionMutation.variables?.session_id ?? ""}
              onSearchChange={setV2SessionSearch}
              skillsCatalog={skillsCatalogQuery.data ?? []}
              skillsLoading={skillsCatalogQuery.isLoading}
              templates={templatesQuery.data ?? []}
              templatesLoading={templatesQuery.isLoading}
              onCreate={(workspaceDir, template, skills) => createSession(false, workspaceDir, template, skills)}
              onPrefetch={(session) => {
                // Warm the target session before the click so a cold-session
                // load (open POST reads session files from disk, multi-second)
                // happens on hover instead of after navigation. Fire-and-forget:
                // a failed prefetch is harmless — the real click path retries.
                const locator = session.locator;
                const meta = locator.metadata ?? {};
                const query: Record<string, string> = {};
                if (locator.user_id) query.user_id = locator.user_id;
                if (meta.project_dir) query.workspace_dir = meta.project_dir;
                if (meta.mode) query.mode = meta.mode;
                if (meta.template) query.template = meta.template;
                if (meta.requested_skills) query.skills = meta.requested_skills;
                const warm = async () => {
                  const opened = await queryClient.fetchQuery({
                    queryKey: ["session-open", token, locator.channel || "web", locator.key, locator.user_id],
                    queryFn: () => openSession(token || null, { channel: locator.channel || "web", key: locator.key, ...(locator.user_id ? { user_id: locator.user_id } : {}), ...(Object.keys(query).length > 0 ? { metadata: query } : {}) }),
                    staleTime: 30 * 1000,
                  });
                  if (opened?.session_id) {
                    await queryClient.prefetchQuery({
                      queryKey: ["snapshot", token, opened.session_id],
                      queryFn: () => getSnapshot(token || null, opened.session_id),
                      staleTime: 10 * 1000,
                    });
                  }
                };
                void warm().catch(() => {});
              }}
              onSelect={(session) => {
                navigate(buildChatRouteForSession(session));
              }}
              onDelete={(session) => deleteSessionMutation.mutate(session)}
              renamingSessionId={renameSessionMutation.variables?.sessionId ?? ""}
              onRename={(session, title) => renameSessionMutation.mutate({ sessionId: session.session_id, title })}
              onToggleCollapsed={() => v2ToggleLeft()}
            />
          </div>
          {/* Center: topbar + feed + composer */}
          <div className="chat-v2-center-wrap">
            <div className="chat-v2-topbar">
              <Space size={4} className="chat-v2-topbar-session">
                <Tooltip title={v2LeftCollapsed ? "Expand sidebar" : "Collapse sidebar"}>
                  <Button type="text" size="small" icon={v2LeftCollapsed ? <VerticalRightOutlined /> : <VerticalLeftOutlined />} aria-label="Toggle sidebar" onClick={() => v2ToggleLeft()} />
                </Tooltip>
                <Divider type="vertical" />
                <Tooltip title={t("chat.copySessionInfo")}>
                  <Typography.Text className="chat-v2-topbar-title" strong ellipsis={{ tooltip: sessionTitle }} onClick={() => void copySessionInfo()} style={{ cursor: "pointer", maxWidth: 200 }}>
                    {sessionTitle}
                  </Typography.Text>
                </Tooltip>
                {activeTemplate ? (
                  <Tooltip title={activeTemplate.description || activeTemplate.id}>
                    <Tag style={{ marginInlineStart: 4 }}>
                      {activeTemplate.avatar?.trim() ? (
                        /^https?:\/\//.test(activeTemplate.avatar.trim()) ? (
                          <Avatar src={activeTemplate.avatar.trim()} size={16} style={{ marginInlineEnd: 4, verticalAlign: "middle" }} />
                        ) : (
                          <span style={{ marginInlineEnd: 4 }}>{activeTemplate.avatar.trim()}</span>
                        )
                      ) : null}
                      {activeTemplate.name || activeTemplate.id}
                    </Tag>
                  </Tooltip>
                ) : null}
                {activeTemplate?.engine && activeTemplate.engine !== "godex" ? (
                  <Tooltip title={t("chat.engineExternalTooltip")}>
                    <Tag color="gold" style={{ marginInlineStart: 4 }}>
                      {activeTemplate.engine}
                    </Tag>
                  </Tooltip>
                ) : null}
              </Space>
              <Space size={4} className="chat-v2-topbar-actions">
                {remoteNodeID ? (
                  <span className="chat-v2-topbar-remote">
                    <span className="chat-v2-topbar-remote-label">{remoteNodeName || remoteNodeID}</span>
                    <Button type="text" size="small" icon={<LogoutOutlined />} onClick={clearRemoteNode} aria-label={t("nodes.remoteExit")} title={t("nodes.remoteExit")} />
                  </span>
                ) : null}
                <div className="chat-v2-topbar-tabs">
                  {DOCK_TABS.map((tab: DockTab) => {
                    const meta = DOCK_TAB_META[tab];
                    const active = tab === v2ActiveDockTab && !v2RightCollapsed;
                    const badge = tab === "tasks" ? (pendingPermissions.length || 0) : 0;
                    return (
                      <Tooltip key={tab} title={meta.label}>
                        <Button
                          type="text"
                          size="small"
                          icon={meta.icon}
                          aria-label={meta.label}
                          className={`chat-v2-topbar-tab${active ? " chat-v2-topbar-tab-active" : ""}`}
                          onClick={() => v2SetActiveDockTab(tab)}
                        >
                          {badge > 0 ? <span className="chat-v2-topbar-badge">{badge}</span> : null}
                        </Button>
                      </Tooltip>
                    );
                  })}
                </div>
                <Tooltip title={v2RightCollapsed ? "Expand panel" : "Collapse panel"}>
                  <Button type="text" size="small" icon={v2RightCollapsed ? <VerticalLeftOutlined /> : <VerticalRightOutlined />} aria-label="Toggle right panel" onClick={() => v2ToggleRight()} />
                </Tooltip>
              </Space>
            </div>
            {authRequired && !token ? (
              <div style={{ padding: 16 }}>
                <Alert
                  type="warning"
                  showIcon
                  message={
                    <>
                      {t("chat.authRequiredPrefix")} <Link to="/settings">{t("app.nav.settings")}</Link> {t("chat.authRequiredSuffix")}
                    </>
                  }
                />
              </div>
            ) : authError ? (
              <div style={{ padding: 16 }}>
                <Alert type="error" showIcon message={authError} />
              </div>
            ) : (
              <div className="chat-v2-center-body">
                <div className="chat-feed-v2-scrollport">
                  <div className="chat-feed chat-feed-v2-scroll" ref={scrollerRef} onScroll={handleFeedScroll} style={{ minHeight: 0 }}>
                    <div className="chat-feed-inner chat-feed-v2-inner">
                      <MessageFeedV2
                        items={v2ItemsWithPending}
                        botName={activeTemplate?.name}
                        botAvatar={activeTemplate?.avatar}
                        botColor={activeTemplate?.color}
                        onToggleTool={toggleTool}
                        onSaveToNote={(item) => saveMessageToNoteMutation.mutate(item)}
                        savingToNote={saveMessageToNoteMutation.isPending}
                        hasNoteContext={!!noteContextQuery.data}
                        workspaceDir={sessionWorkspaceDir}
                        token={token}
                        voiceEnabled={metaQuery.data?.voice_enabled ?? false}
                        onForkTurn={(item) =>
                          forkMutation.mutate({
                            // Historical turns carry a synthetic turnId (msg-N) that the
                            // backend can't resolve; use the tracked message index instead.
                            ...(item.forkMessageIndex !== undefined ? { messageIndex: item.forkMessageIndex } : item.turnId ? { turnId: item.turnId } : {}),
                          })
                        }
                        onEditMessage={(item) => {
                          composerRef.current?.setText(item.body);
                        }}
                        onOpenInFiles={(path) => {
                          setFilesFocusPath(path);
                          v2SetActiveDockTab("files");
                        }}
                        onSubmitCard={(value) => void onSend({ text: value, files: [] })}
                      />
                    </div>
                    {!stickToBottom ? (
                      <button
                        type="button"
                        className="chat-feed-jump-latest"
                        onClick={() => {
                          const scroller = scrollerRef.current;
                          if (scroller) {
                            scroller.scrollTop = scroller.scrollHeight;
                          }
                          stickToBottomRef.current = true;
                          setStickToBottom(true);
                        }}
                      >
                        Jump to latest
                      </button>
                    ) : null}
                  </div>
                  {switchingSession ? (
                    <div className="chat-feed-switching-overlay" role="status" aria-live="polite">
                      <Spin size="small" />
                      <span>{t("chat.switchingSession")}</span>
                    </div>
                  ) : null}
                </div>
                <NoteContextBanner
                  note={noteContextQuery.data}
                  loading={noteContextQuery.isLoading}
                  error={noteContextQuery.error}
                  onClear={clearNoteContext}
                />
                <ApprovalBanner
                  items={pendingPermissions}
                  approving={approvePermissionMutation}
                  denying={denyPermissionMutation}
                />
                <div style={{ borderTop: "1px solid var(--godex-border)", padding: "6px 12px" }}>
                  <Space style={{ width: "100%", justifyContent: "space-between" }} size={4} wrap>
                    <Space size={4}>
                      {acpAgentID ? (
                        <Select
                          size="small"
                          value={selectedACPAgentModel || undefined}
                          style={{ minWidth: 120, maxWidth: 200 }}
                          loading={acpModelsLoading || acpModelMutation.isPending}
                          disabled={acpModelMutation.isPending}
                          placeholder={t("chat.acpModelPlaceholder")}
                          onChange={(value) => acpModelMutation.mutate({ model: value || "" })}
                          options={acpModelOptions}
                          showSearch
                          optionFilterProp="label"
                        />
                      ) : modelsQuery.data?.profiles.length ? (
                        <Select
                          size="small"
                          value={selectedProfile?.id}
                          style={{ minWidth: 100, maxWidth: 140 }}
                          loading={modelsQuery.isLoading || modelMutation.isPending}
                          disabled={modelMutation.isPending}
                          onChange={(value) => modelMutation.mutate({ profileId: value, reasoningEffort: sessionReasoningEffort || undefined })}
                          options={modelGroupOptions}
                        />
                      ) : null}
                      {!acpAgentID && modelsQuery.data?.profiles.length && selectedProfile?.id ? (
                        <Select
                          allowClear
                          size="small"
                          placeholder="Think"
                          value={selectedReasoningEffort || undefined}
                          style={{ minWidth: 72, maxWidth: 96 }}
                          loading={modelsQuery.isLoading || modelMutation.isPending}
                          disabled={modelMutation.isPending}
                          onChange={(value) => modelMutation.mutate({ profileId: selectedProfile!.id, reasoningEffort: value || "" })}
                          options={reasoningEffortOptions}
                        />
                      ) : null}
                      <Tooltip title={streamConnected ? t("chat.streamConnected") : t("chat.streamReconnecting")}>
                        <Badge status={streamConnected ? "success" : "processing"} />
                      </Tooltip>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {modelMutation.isPending
                          ? t("chat.modelSwitching")
                          : uploading
                            ? `${t("chat.uploadingAttachments")} ${uploadProgress ?? 0}%`
                            : queuedTurns.length
                              ? `${status} · ${queuedTurns.length} queued`
                              : status}
                      </Typography.Text>
                    </Space>
                    <Space size={4}>
                      <ContextStatusInline summary={contextStatus} inspector={contextInspector} />
                      <VoiceBar token={token} sessionId={openQuery.data?.session_id ?? null} enabled={metaQuery.data?.voice_enabled ?? false} disabled={!openQuery.data?.session_id || modelMutation.isPending} onResult={(text) => composerRef.current?.appendText(text)} />
                      {running ? (
                        <Tooltip title={notifyArmed ? t("chat.notifyArmed") : t("chat.notifyMeAfter")}>
                          <Button
                            size="small"
                            type={notifyArmed ? "primary" : "default"}
                            icon={<BellOutlined />}
                            aria-label={t("chat.notifyMeAfter")}
                            onClick={() => void toggleNotifyMe()}
                          />
                        </Tooltip>
                      ) : null}
                      {running && currentTurnId ? (
                        <Tooltip title={t("chat.cancelTurn")}>
                          <Button
                            danger
                            size="small"
                            icon={<StopOutlined />}
                            aria-label={t("chat.cancelTurn")}
                            loading={cancelTurnMutation.isPending}
                            onClick={() => cancelTurnMutation.mutate({ sessionId, turnId: currentTurnId })}
                          />
                        </Tooltip>
                      ) : null}
                    </Space>
                  </Space>
                </div>
                {queuedTurns.length > 0 ? (
                  <div className="chat-queued-strip">
                    {queuedTurns.map((queued) => (
                      <div className="chat-queued-item" key={queued.id}>
                        <Typography.Text ellipsis style={{ flex: 1, minWidth: 0 }}>
                          {queued.summary || t("chat.queuedMessage")}
                        </Typography.Text>
                        <Tooltip title={t("chat.editQueued")}>
                          <Button
                            type="text"
                            size="small"
                            icon={<EditOutlined />}
                            aria-label={t("chat.editQueued")}
                            disabled={cancelQueuedMutation.isPending || steerQueuedMutation.isPending}
                            onClick={() => cancelQueuedMutation.mutate({ sessionId, queueId: queued.id, edit: true })}
                          />
                        </Tooltip>
                        <Tooltip title={t("chat.steerQueued")}>
                          <Button
                            type="text"
                            size="small"
                            icon={<EnterOutlined />}
                            aria-label={t("chat.steerQueued")}
                            disabled={cancelQueuedMutation.isPending || steerQueuedMutation.isPending}
                            onClick={() => steerQueuedMutation.mutate({ sessionId, queueId: queued.id })}
                          />
                        </Tooltip>
                      </div>
                    ))}
                  </div>
                ) : null}
                <Composer
                  ref={composerRef}
                  disabled={!openQuery.data?.session_id || modelMutation.isPending}
                  uploading={uploading}
                  uploadProgress={uploadProgress}
                  packageCommands={packageCommandsQuery.data ?? []}
                  builtinCommands={builtinCommandsQuery.data ?? []}
                  queuedFiles={queuedComposerFiles}
                  onQueuedFilesConsumed={() => setQueuedComposerFiles([])}
                  draftScope={openQuery.data?.session_id ? `session:${openQuery.data.session_id}` : ""}
                  onSubmit={onSend}
                />
              </div>
            )}
          </div>
          {/* Right dock: content pane only (tabs are in topbar) */}
          <div className="chat-v2-right" data-collapsed={v2RightCollapsed ? "true" : "false"} data-active-tab={v2ActiveDockTab}>
            <div className="chat-v2-right-resize" onPointerDown={beginV2RightResize} role="separator" aria-label="Resize right panel" />
            <div className="chat-v2-dock-pane">
              <div className="chat-v2-dock-pane-body" data-active-tab={v2ActiveDockTab}>
                {mountedDockTabs.has("files") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "files" ? "true" : "false"}>
                    <FilesPanel mode="dock" cwd={sessionWorkspaceDir} fillContainer focusPath={filesFocusPath} onAttachFile={(file) => setQueuedComposerFiles((current) => [...current, file])} />
                  </div>
                ) : null}
                {mountedDockTabs.has("terminal") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "terminal" ? "true" : "false"}>
                    <div className="chat-v2-terminal-tabs" data-testid="chat-v2-terminal-tabs">
                      {terminalTabs.map((id) => (
                        <button
                          key={id}
                          type="button"
                          className={`chat-v2-terminal-tab${activeTerminal === id ? " chat-v2-terminal-tab-active" : ""}`}
                          onClick={() => setActiveTerminal(id)}
                        >
                          <span className="chat-v2-terminal-tab-label">Terminal {id + 1}</span>
                          {terminalTabs.length > 1 ? (
                            <CloseOutlined
                              className="chat-v2-terminal-tab-close"
                              aria-label={`Close terminal ${id + 1}`}
                              onClick={(e) => {
                                e.stopPropagation();
                                closeTerminalTab(id);
                              }}
                            />
                          ) : null}
                        </button>
                      ))}
                      <button type="button" className="chat-v2-terminal-tab-add" aria-label="New terminal" onClick={addTerminalTab}>
                        <PlusOutlined />
                      </button>
                    </div>
                    <div className="chat-v2-terminal-panes">
                      {terminalTabs.map((id) => (
                        <div
                          key={id}
                          className="chat-v2-terminal-pane"
                          data-active={activeTerminal === id ? "true" : "false"}
                        >
                          <TerminalPanel workspaceDir={sessionWorkspaceDir} execution={terminalExecution} />
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}
                {mountedDockTabs.has("tasks") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "tasks" ? "true" : "false"}>
                    <TaskCenterPanel
                      outcomes={taskOutcomes}
                      collapsed={false}
                      onCollapsedChange={() => v2SetActiveDockTab(v2ActiveDockTab)}
                      reviewingJobId={reviewSubagentMutation.isPending ? reviewSubagentMutation.variables : undefined}
                      mergingJobId={mergeSubagentMutation.isPending ? mergeSubagentMutation.variables : undefined}
                      resumingJobId={resumeSubagentMutation.isPending ? resumeSubagentMutation.variables : undefined}
                      cancelingJobId={cancelSubagentMutation.isPending ? cancelSubagentMutation.variables : undefined}
                      runningWorkflowId={runLongTaskMutation.isPending ? runLongTaskMutation.variables : undefined}
                      cancelingLongTask={cancelLongTaskMutation.isPending ? cancelLongTaskMutation.variables : undefined}
                      finalizingLongTask={finalizeLongTaskMutation.isPending ? finalizeLongTaskMutation.variables : undefined}
                      onReviewSubagent={reviewSubagentInDrawer}
                      onMergeSubagent={(jobId) => mergeSubagentMutation.mutate(jobId)}
                      onResumeSubagent={(jobId) => resumeSubagentMutation.mutate(jobId)}
                      onCancelSubagent={(jobId) => cancelSubagentMutation.mutate(jobId)}
                      onRunLongTask={(workflowId) => runLongTaskMutation.mutate(workflowId)}
                      onCancelLongTask={(workflowId, nodeId) => cancelLongTaskMutation.mutate({ workflowId, nodeId })}
                      onFinalizeLongTask={(workflowId, nodeId) => finalizeLongTaskMutation.mutate({ workflowId, nodeId })}
                      onOpenReviewMergeCenter={openReviewMergeCenter}
                    />
                  </div>
                ) : null}
                {mountedDockTabs.has("preview") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "preview" ? "true" : "false"}>
                    <PreviewPanel workspaceDir={sessionWorkspaceDir} token={token} />
                  </div>
                ) : null}
                {mountedDockTabs.has("status") ? (
                  <div className="chat-v2-dock-tab-pane" data-active={v2ActiveDockTab === "status" ? "true" : "false"}>
                    {inspectorPanel}
                  </div>
                ) : null}
              </div>
            </div>
          </div>
        </div>
      <Drawer
        title={`Subagent review${subagentReview?.job_id ? ` · ${shortTurnId(subagentReview.job_id)}` : ""}`}
        placement="right"
        width={720}
        open={subagentReviewOpen}
        onClose={() => setSubagentReviewOpen(false)}
      >
        <SubagentReviewPanel review={subagentReview} loading={reviewSubagentMutation.isPending} />
      </Drawer>
      <ReviewMergeCenterPanel
        open={reviewMergeOpen}
        summary={reviewMergeSummary}
        filter={reviewMergeFilter}
        selectedJobId={reviewMergeSelectedJobId}
        outcomes={taskOutcomes}
        review={subagentReview}
        mergeResult={subagentMergeResult}
        reviewingJobId={reviewSubagentMutation.isPending ? reviewSubagentMutation.variables : undefined}
        mergingJobId={mergeSubagentMutation.isPending ? mergeSubagentMutation.variables : undefined}
        resumingJobId={resumeSubagentMutation.isPending ? resumeSubagentMutation.variables : undefined}
        cancelingJobId={cancelSubagentMutation.isPending ? cancelSubagentMutation.variables : undefined}
        onClose={() => setReviewMergeOpen(false)}
        onFilterChange={setReviewMergeFilter}
        onSelectJob={setReviewMergeSelectedJobId}
        onReview={reviewSubagentInCenter}
        onMerge={(jobId) => {
          setReviewMergeSelectedJobId(jobId);
          mergeSubagentMutation.mutate(jobId);
        }}
        onResume={(jobId) => resumeSubagentMutation.mutate(jobId)}
        onCancel={(jobId) => cancelSubagentMutation.mutate(jobId)}
      />
      {refluxBubbles.length > 0 ? (
        <>
          {refluxBubbles.map((b, i) => (
            <LongTaskRefluxBubble
              key={b.id}
              longtaskId={b.longtaskId}
              status={b.status}
              content={b.content}
              stackIndex={i}
              onDismiss={() => {
                if (refluxDismissed.has(b.dismissKey)) return;
                const next = new Set(refluxDismissed);
                next.add(b.dismissKey);
                setRefluxDismissed(next);
                writePersistedRefluxDismissed(next);
              }}
            />
          ))}
        </>
      ) : null}
    </>
  );
}
