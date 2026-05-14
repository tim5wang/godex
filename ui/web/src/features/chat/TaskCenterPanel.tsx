import type { ReactNode } from "react";
import { Badge, Button, Card, Empty, Popconfirm, Progress, Space, Tag, Tooltip, Typography } from "antd";
import {
  CheckOutlined,
  CompressOutlined,
  EyeOutlined,
  PlayCircleOutlined,
  SafetyCertificateOutlined,
  StopOutlined,
} from "@ant-design/icons";
import type { TaskOutcome } from "./taskCenterOutcome";
import { outcomeIsActive, outcomeNeedsReview, taskOutcomeColor, taskOutcomeLabel } from "./taskCenterOutcome";

interface TaskCenterPanelProps {
  outcomes: TaskOutcome[];
  collapsed: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  reviewingJobId?: string;
  mergingJobId?: string;
  resumingJobId?: string;
  cancelingJobId?: string;
  runningWorkflowId?: string;
  cancelingLongTask?: { workflowId?: string; nodeId?: string };
  finalizingLongTask?: { workflowId?: string; nodeId?: string };
  onReviewSubagent: (jobId: string) => void;
  onMergeSubagent: (jobId: string) => void;
  onResumeSubagent: (jobId: string) => void;
  onCancelSubagent: (jobId: string) => void;
  onRunLongTask: (workflowId: string) => void;
  onCancelLongTask: (workflowId: string, nodeId: string) => void;
  onFinalizeLongTask: (workflowId: string, nodeId: string) => void;
  onOpenReviewMergeCenter: (jobId?: string) => void;
}

export function TaskCenterPanel(props: TaskCenterPanelProps) {
  const active = props.outcomes.filter(outcomeIsActive);
  const review = props.outcomes.filter(outcomeNeedsReview);
  const visibleOutcomes = props.outcomes.filter((outcome) => outcome.status !== "idle");
  const unresolved = props.outcomes.filter((outcome) => outcome.status === "blocked" || outcome.status === "failed" || outcome.status === "ready_for_review").length;

  return (
    <section className={`task-center-band ${props.collapsed ? "task-center-band-collapsed" : ""}`}>
      <Card
        size="small"
        className="task-center-card"
        title={
          <Space size={8} wrap>
            <SafetyCertificateOutlined />
            <span>Task Center</span>
            {unresolved ? <Badge count={unresolved} size="small" /> : null}
          </Space>
        }
        extra={
          <Space size={4}>
            <Button type="text" size="small" icon={<EyeOutlined />} onClick={() => props.onOpenReviewMergeCenter()}>
              Review
            </Button>
            <Button
              type="text"
              size="small"
              icon={<CompressOutlined />}
              onClick={() => props.onCollapsedChange(!props.collapsed)}
            >
              {props.collapsed ? "Expand" : "Collapse"}
            </Button>
          </Space>
        }
      >
        {props.collapsed ? (
          <CollapsedSummary outcomes={props.outcomes} />
        ) : (
          <div className="task-center-grid">
            <TaskCenterSection title="Outcomes" empty="No task outcomes yet">
              {visibleOutcomes.map((outcome) => (
                <OutcomeRow key={outcome.id} outcome={outcome} actions={<OutcomeActions outcome={outcome} {...props} />} />
              ))}
            </TaskCenterSection>
            <TaskCenterSection title="Active" empty="Idle">
              {active.map((outcome) => (
                <OutcomeRow key={outcome.id} outcome={outcome} compact actions={<OutcomeActions outcome={outcome} {...props} />} />
              ))}
            </TaskCenterSection>
            <TaskCenterSection title="Review" empty="Nothing waiting for review">
              {review.map((outcome) => (
                <OutcomeRow key={outcome.id} outcome={outcome} compact actions={<OutcomeActions outcome={outcome} {...props} />} />
              ))}
            </TaskCenterSection>
          </div>
        )}
      </Card>
    </section>
  );
}

function TaskCenterSection({ title, empty, children }: { title: string; empty: string; children: ReactNode[] }) {
  const items = children.filter(Boolean);
  return (
    <div className="task-center-section">
      <div className="task-center-section-title">
        <Typography.Text strong>{title}</Typography.Text>
      </div>
      {items.length ? items : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={empty} />}
    </div>
  );
}

function OutcomeRow({ outcome, compact = false, actions }: { outcome: TaskOutcome; compact?: boolean; actions?: ReactNode }) {
  return (
    <div className={`task-outcome-row ${compact ? "task-outcome-row-compact" : ""}`}>
      <div className="task-outcome-main">
        <Space size={6} wrap>
          <Tag color={taskOutcomeColor(outcome.status)}>{taskOutcomeLabel(outcome.status)}</Tag>
          {outcome.recovered ? <Tag color="green">recovered</Tag> : null}
          {outcome.longTask ? <Tag>{shortId(outcome.longTask.longtask_id || outcome.longTask.workflow_id)}</Tag> : null}
          {outcome.worker?.jobId ? <Tag color="cyan">{shortId(outcome.worker.jobId)}</Tag> : null}
        </Space>
        <Tooltip title={outcome.title}>
          <Typography.Text strong ellipsis>
            {outcome.title}
          </Typography.Text>
        </Tooltip>
        {outcome.detail ? (
          <Typography.Text type="secondary" ellipsis={{ tooltip: outcome.detail }}>
            {outcome.recovered && outcome.longTask ? `Recovered from failed longtask ${outcome.longTask.longtask_id}. ${outcome.detail}` : outcome.detail}
          </Typography.Text>
        ) : null}
        <Progress percent={outcomeProgress(outcome)} size="small" status={progressStatus(outcome.status)} showInfo={false} />
      </div>
      {actions ? <div className="task-outcome-actions">{actions}</div> : null}
    </div>
  );
}

function OutcomeActions(props: TaskCenterPanelProps & { outcome: TaskOutcome }) {
  const { outcome } = props;
  const jobId = outcome.worker?.jobId || "";
  const workerStatus = (outcome.worker?.status || "").toLowerCase();
  const canCancelWorker = jobId && ["running", "pending", "resuming"].includes(workerStatus);
  const canResumeWorker = jobId && ["canceled", "cancelled", "interrupted", "error", "failed"].includes(workerStatus);
  const canReviewWorker = jobId && outcome.worker?.writeScope?.length && workerStatus !== "running";
  const canMergeWorker = canReviewWorker && outcome.worker?.mergeStatus !== "merged" && outcome.worker?.mergeStatus !== "no_changes";
  const longTask = outcome.longTask;
  const activeStory = longTask?.stories.find((story) => story.status === "running") ?? longTask?.stories.find((story) => story.status === "error");
  const finalizable = longTask?.stories.find((story) => story.status === "completed" && story.verdict === "pass" && story.validation_status === "pending" && story.node_id);

  return (
    <Space size={4} wrap>
      {canReviewWorker ? (
        <Tooltip title="Review subagent changes">
          <Button
            size="small"
            type="text"
            icon={<EyeOutlined />}
            aria-label="Review subagent changes"
            loading={props.reviewingJobId === jobId}
            onClick={() => props.onReviewSubagent(jobId)}
          />
        </Tooltip>
      ) : null}
      {canMergeWorker ? (
        <Popconfirm title="Merge subagent changes?" onConfirm={() => props.onMergeSubagent(jobId)}>
          <Tooltip title="Merge subagent changes">
            <Button
              size="small"
              type="text"
              icon={<CheckOutlined />}
              aria-label="Merge subagent changes"
              loading={props.mergingJobId === jobId}
            />
          </Tooltip>
        </Popconfirm>
      ) : null}
      {canResumeWorker ? (
        <Tooltip title="Resume subagent">
          <Button
            size="small"
            type="text"
            icon={<PlayCircleOutlined />}
            aria-label="Resume subagent"
            loading={props.resumingJobId === jobId}
            onClick={() => props.onResumeSubagent(jobId)}
          />
        </Tooltip>
      ) : null}
      {canCancelWorker ? (
        <Tooltip title="Cancel subagent">
          <Button
            size="small"
            danger
            type="text"
            icon={<StopOutlined />}
            aria-label="Cancel subagent"
            loading={props.cancelingJobId === jobId}
            onClick={() => props.onCancelSubagent(jobId)}
          />
        </Tooltip>
      ) : null}
      {longTask && longTask.status !== "completed" ? (
        <Tooltip title="Run LongTask">
          <Button
            size="small"
            type="text"
            icon={<PlayCircleOutlined />}
            aria-label="Run LongTask"
            loading={props.runningWorkflowId === longTask.workflow_id}
            onClick={() => props.onRunLongTask(longTask.workflow_id)}
          />
        </Tooltip>
      ) : null}
      {finalizable?.node_id && longTask ? (
        <Tooltip title="Finalize LongTask story">
          <Button
            size="small"
            type="text"
            icon={<CheckOutlined />}
            aria-label="Finalize LongTask story"
            loading={props.finalizingLongTask?.workflowId === longTask.workflow_id && props.finalizingLongTask?.nodeId === finalizable.node_id}
            onClick={() => props.onFinalizeLongTask(longTask.workflow_id, finalizable.node_id!)}
          />
        </Tooltip>
      ) : null}
      {activeStory?.node_id && longTask ? (
        <Popconfirm title="Cancel this LongTask node?" onConfirm={() => props.onCancelLongTask(longTask.workflow_id, activeStory.node_id!)}>
          <Tooltip title="Cancel LongTask node">
            <Button
              size="small"
              danger
              type="text"
              icon={<StopOutlined />}
              aria-label="Cancel LongTask node"
              loading={props.cancelingLongTask?.workflowId === longTask.workflow_id && props.cancelingLongTask?.nodeId === activeStory.node_id}
            />
          </Tooltip>
        </Popconfirm>
      ) : null}
    </Space>
  );
}

function CollapsedSummary({ outcomes }: { outcomes: TaskOutcome[] }) {
  const counts = outcomes.reduce<Record<string, number>>((acc, outcome) => {
    acc[outcome.status] = (acc[outcome.status] ?? 0) + 1;
    return acc;
  }, {});
  return (
    <Space size={8} wrap>
      <Tag color={counts.running ? "processing" : "default"}>{counts.running ?? 0} running</Tag>
      <Tag color={counts.blocked ? "gold" : "default"}>{counts.blocked ?? 0} blocked</Tag>
      <Tag color={counts.ready_for_review ? "blue" : "default"}>{counts.ready_for_review ?? 0} review</Tag>
      <Tag color={counts.merged ? "green" : "default"}>{counts.merged ?? 0} merged</Tag>
      <Tag color={counts.failed ? "red" : "default"}>{counts.failed ?? 0} failed</Tag>
    </Space>
  );
}

function outcomeProgress(outcome: TaskOutcome) {
  if (outcome.longTask && outcome.longTask.total > 0) {
    const passed = outcome.longTask.stories.filter((story) => story.passes).length;
    if (outcome.status === "merged") {
      return 100;
    }
    return Math.max(8, Math.round((passed / outcome.longTask.total) * 100));
  }
  switch (outcome.status) {
    case "merged":
      return 100;
    case "ready_for_review":
      return 92;
    case "failed":
      return 100;
    case "blocked":
      return 64;
    case "running":
      return 48;
    default:
      return 0;
  }
}

function progressStatus(status: TaskOutcome["status"]) {
  switch (status) {
    case "merged":
      return "success";
    case "failed":
      return "exception";
    case "running":
      return "active";
    default:
      return "normal";
  }
}

function shortId(value: string) {
  return value.length <= 18 ? value : `${value.slice(0, 15)}...`;
}
