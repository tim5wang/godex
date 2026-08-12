import type { ReactNode } from "react";
import type { PendingPermission, LongTaskView, FeedItem, PackageRoleEntry, TurnRecord, SessionTimelineEntry, TimelinePage, SessionContextInspector, SkillActivation, CompactionRecord } from "../../../lib/types";
import { useMutation } from "@tanstack/react-query";
import { useI18n } from "../../../i18n";
import { Tabs, Button, Badge, Space } from "antd";
import { CompressOutlined } from "@ant-design/icons";
import type { TimelineFilterState } from "../../../lib/timelineUtils";
import { ApprovalList } from "./ApprovalPanels";
import { ContextRecallPanel } from "./ContextPanels";
import { TurnList, AvailableSubagentRoles, SubagentList, LongTaskList } from "./TurnSubagentPanels";
import { TimelineList } from "./TimelinePanels";
import { CompactionHistoryPanel } from "./CompactionHistory";
import { SubagentTimelinePanel } from "./SubagentTimeline";

export function InspectorTabs(props: {
  activeKey: string;
  onActiveKeyChange: (key: string) => void;
  onCollapseInspector: () => void;
  taskCenterPanel: ReactNode;
  pendingPermissions: PendingPermission[];
  longTasks: LongTaskView[];
  longTasksLoading: boolean;
  subagentJobs: FeedItem[];
  packageRoles: PackageRoleEntry[];
  packageRolesLoading: boolean;
  turnRecords: TurnRecord[];
  timelineItems: SessionTimelineEntry[];
  compactions?: CompactionRecord[];
  compactionsLoading?: boolean;
  timelinePage?: TimelinePage;
  timelinePageLoading: boolean;
  timelineFilters: TimelineFilterState;
  currentTurnId: string;
  canPreviousTimelinePage: boolean;
  timelinePageIndex?: number;
  onTimelineFiltersChange: (filters: TimelineFilterState) => void;
  onNextTimelinePage: () => void;
  onPreviousTimelinePage: () => void;
  contextInspector: SessionContextInspector | null;
  contextLoading: boolean;
  sessionId: string;
  activeSkills: SkillActivation[];
  activeSkillsLoading: boolean;
  unloadingSkill: ReturnType<typeof useMutation<SkillActivation, Error, string>>;
  approving: ReturnType<typeof useMutation<unknown, Error, { requestId: string; scope: "once" | "session" }>>;
  denying: ReturnType<typeof useMutation<unknown, Error, { requestId: string }>>;
  reviewingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  cancelingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  resumingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  mergingSubagent: ReturnType<typeof useMutation<unknown, Error, string>>;
  onReviewSubagent: (jobId: string) => void;
  onCancelSubagent: (jobId: string) => void;
  onResumeSubagent: (jobId: string) => void;
  onMergeSubagent: (jobId: string) => void;
  runningLongTask: ReturnType<typeof useMutation<unknown, Error, string>>;
  cancelingLongTask: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  finalizingLongTask: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  onRunLongTask: (workflowId: string) => void;
  onCancelLongTask: (workflowId: string, nodeId: string) => void;
  onFinalizeLongTask: (workflowId: string, nodeId: string) => void;
  retrying: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  resuming: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  onRetryTurn: (turnId: string) => void;
  onResumeTurn: (turnId: string) => void;
  onUnloadSkill: (skillId: string) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="panel-scroll">
      <Tabs
        size="small"
        activeKey={props.activeKey}
        onChange={props.onActiveKeyChange}
        tabBarExtraContent={(
          <Button
            type="text"
            size="small"
            icon={<CompressOutlined />}
            onClick={props.onCollapseInspector}
            data-testid="inspector-collapse"
          >
            {t("panel.collapse") || "Collapse"}
          </Button>
        )}
        items={[
          {
            key: "taskCenter",
            label: t("chat.taskCenter") || "Task Center",
            children: props.taskCenterPanel,
          },
          {
            key: "approvals",
            label: (
              <Badge count={props.pendingPermissions.length} size="small">
                <span>{t("chat.pendingApprovalsTitle")}</span>
              </Badge>
            ),
            children: (
              <ApprovalList
                items={props.pendingPermissions}
                approving={props.approving}
                denying={props.denying}
              />
            ),
          },
          {
            key: "context",
            label: t("chat.contextRecallTitle"),
            children: (
              <ContextRecallPanel
                inspector={props.contextInspector}
                loading={props.contextLoading}
                sessionId={props.sessionId}
                activeSkills={props.activeSkills}
                activeSkillsLoading={props.activeSkillsLoading}
                unloadingSkill={props.unloadingSkill}
                onUnloadSkill={props.onUnloadSkill}
              />
            ),
          },
          {
            key: "turns",
            label: t("chat.turnsTitle"),
            children: (
              <TurnList
                items={props.turnRecords}
                retrying={props.retrying}
                resuming={props.resuming}
                onRetry={props.onRetryTurn}
                onResume={props.onResumeTurn}
              />
            ),
          },
          {
            key: "subagents",
            label: (
              <Badge count={props.subagentJobs.length} size="small">
                <span>Subagents</span>
              </Badge>
            ),
            children: (
              <Space direction="vertical" size={12} style={{ width: "100%" }}>
                <AvailableSubagentRoles items={props.packageRoles} loading={props.packageRolesLoading} />
                <SubagentList
                  items={props.subagentJobs}
                  reviewing={props.reviewingSubagent}
                  canceling={props.cancelingSubagent}
                  resuming={props.resumingSubagent}
                  merging={props.mergingSubagent}
                  onReview={props.onReviewSubagent}
                  onCancel={props.onCancelSubagent}
                  onResume={props.onResumeSubagent}
                  onMerge={props.onMergeSubagent}
                />
              </Space>
            ),
          },
          {
            key: "longtasks",
            label: (
              <Badge count={props.longTasks.filter((item) => item.status !== "completed").length} size="small">
                <span>LongTasks</span>
              </Badge>
            ),
            children: (
              <LongTaskList
                items={props.longTasks}
                loading={props.longTasksLoading}
                running={props.runningLongTask}
                canceling={props.cancelingLongTask}
                finalizing={props.finalizingLongTask}
                onRun={props.onRunLongTask}
                onCancel={props.onCancelLongTask}
                onFinalize={props.onFinalizeLongTask}
              />
            ),
          },
          {
            key: "compactions",
            label: t("chat.compactionHistoryTitle"),
            children: (
              <CompactionHistoryPanel
                records={props.compactions}
                items={props.timelineItems}
                loading={props.compactionsLoading}
              />
            ),
          },
          {
            key: "subagentTimeline",
            label: t("chat.subagentTimelineTitle"),
            children: <SubagentTimelinePanel items={props.timelineItems} />,
          },
          {
            key: "timeline",
            label: t("chat.sessionTimelineTitle"),
            children: (
              <TimelineList
                page={props.timelinePage}
                fallbackItems={props.timelineItems}
                loading={props.timelinePageLoading}
                filters={props.timelineFilters}
                currentTurnId={props.currentTurnId}
                canPrevious={props.canPreviousTimelinePage}
                pageIndex={props.timelinePageIndex}
                onFiltersChange={props.onTimelineFiltersChange}
                onNextPage={props.onNextTimelinePage}
                onPreviousPage={props.onPreviousTimelinePage}
              />
            ),
          },
        ]}
      />
    </div>
  );
}
