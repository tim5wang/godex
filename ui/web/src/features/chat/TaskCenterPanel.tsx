import { useCallback, useState, type ReactNode } from "react";
import { Badge, Button, Card, Descriptions, Empty, List, Popconfirm, Progress, Space, Tag, Tooltip, Typography } from "antd";
import {
  CheckOutlined,
  CloseOutlined,
  CompressOutlined,
  DownOutlined,
  EyeOutlined,
  PlayCircleOutlined,
  SafetyCertificateOutlined,
  StopOutlined,
  UpOutlined,
} from "@ant-design/icons";
import type { TaskOutcome } from "./taskCenterOutcome";
import { outcomeIsActive, outcomeNeedsReview, taskOutcomeColor, taskOutcomeLabel } from "./taskCenterOutcome";
import { useTaskCenterText, type TaskCenterText } from "./taskCenter.i18n";
import { AgentGraphDiagram } from "./panels/AgentGraphDiagram";

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
  const t = useTaskCenterText();
  const [dismissed, setDismissed] = useState<Set<string>>(() => new Set());
  const [showDismissed, setShowDismissed] = useState(false);
  const [actionableOnly, setActionableOnly] = useState(false);

  const dismiss = useCallback((id: string) => setDismissed((prev) => new Set(prev).add(id)), []);

  // Actionable = needs the user: review / blocked / failed / pending approval.
  // Pure running items are collapsed behind the toggle so the band focuses on
  // what actually requires attention.
  const isActionable = (o: TaskOutcome) =>
    o.status === "ready_for_review" || o.status === "blocked" || o.status === "failed" || Boolean(o.permission);

  const visibleOutcomes = props.outcomes.filter(
    (o) => o.status !== "idle" && (showDismissed || !dismissed.has(o.id)) && (!actionableOnly || isActionable(o)),
  );
  const active = visibleOutcomes.filter(outcomeIsActive);
  const review = visibleOutcomes.filter(outcomeNeedsReview);
  const unresolved = visibleOutcomes.filter(
    (o) => o.status === "blocked" || o.status === "failed" || o.status === "ready_for_review",
  ).length;

  return (
    <section className={`task-center-band ${props.collapsed ? "task-center-band-collapsed" : ""}`}>
      <Card
        size="small"
        className="task-center-card"
        title={
          <Space size={8} wrap>
            <SafetyCertificateOutlined />
            <span>{t.title}</span>
            {unresolved ? <Badge count={unresolved} size="small" /> : null}
          </Space>
        }
        extra={
          <Space size={4}>
            <Button type="text" size="small" icon={<EyeOutlined />} onClick={() => props.onOpenReviewMergeCenter()}>
              {t.review}
            </Button>
            <Button
              type={actionableOnly ? "primary" : "text"}
              size="small"
              icon={actionableOnly ? <UpOutlined /> : <DownOutlined />}
              onClick={() => setActionableOnly((v) => !v)}
            >
              {actionableOnly ? t.showAll : t.actionableOnly}
            </Button>
            {dismissed.size > 0 ? (
              <Button type="text" size="small" icon={showDismissed ? <UpOutlined /> : <DownOutlined />} onClick={() => setShowDismissed((v) => !v)}>
                {showDismissed ? t.hideDismissed : `${t.showDismissed} (${dismissed.size})`}
              </Button>
            ) : null}
            <Button type="text" size="small" icon={<CompressOutlined />} onClick={() => props.onCollapsedChange(!props.collapsed)}>
              {props.collapsed ? t.expand : t.collapse}
            </Button>
          </Space>
        }
      >
        {props.collapsed ? (
          <CollapsedSummary outcomes={props.outcomes} t={t} />
        ) : (
          <div className="task-center-grid">
            <TaskCenterSection title={t.outcomes} empty={t.noOutcomes}>
              {visibleOutcomes.map((outcome) => (
                <OutcomeRow key={outcome.id} outcome={outcome} t={t} onDismiss={dismiss} actions={<OutcomeActions outcome={outcome} t={t} {...props} />} />
              ))}
            </TaskCenterSection>
            <TaskCenterSection title={t.active} empty={t.idle}>
              {active.map((outcome) => (
                <OutcomeRow key={outcome.id} outcome={outcome} compact t={t} onDismiss={dismiss} actions={<OutcomeActions outcome={outcome} t={t} {...props} />} />
              ))}
            </TaskCenterSection>
            <TaskCenterSection title={t.review} empty={t.nothingReview}>
              {review.map((outcome) => (
                <OutcomeRow key={outcome.id} outcome={outcome} compact t={t} onDismiss={dismiss} actions={<OutcomeActions outcome={outcome} t={t} {...props} />} />
              ))}
            </TaskCenterSection>
          </div>
        )}
      </Card>
    </section>
  );
}

function TaskCenterSection({ title, empty, children, defaultCollapsed = false }: { title: string; empty: string; children: ReactNode[]; defaultCollapsed?: boolean }) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed);
  const items = children.filter(Boolean);
  return (
    <div className="task-center-section">
      <div className="task-center-section-title">
        <Typography.Text strong>{title}</Typography.Text>
        {items.length > 0 ? (
          <Button type="text" size="small" icon={collapsed ? <DownOutlined /> : <UpOutlined />} onClick={() => setCollapsed((v) => !v)}>
            {items.length}
          </Button>
        ) : null}
      </div>
      {collapsed ? null : items.length ? items : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={empty} />}
    </div>
  );
}

function OutcomeRow({ outcome, compact = false, actions, t, onDismiss }: { outcome: TaskOutcome; compact?: boolean; actions?: ReactNode; t: TaskCenterText; onDismiss?: (id: string) => void }) {
  const isTerminal = outcome.status === "merged" || outcome.status === "failed";
  const [expanded, setExpanded] = useState(false);
  const hasDetail = Boolean(outcome.longTask || outcome.worker);
  const toggleExpand = () => {
    if (hasDetail) setExpanded((v) => !v);
  };
  return (
    <div className={`task-outcome-row ${compact ? "task-outcome-row-compact" : ""}`}>
      <div className="task-outcome-main" style={{ cursor: hasDetail ? "pointer" : undefined }} onClick={toggleExpand}>
        <Space size={6} wrap>
          <Tag color={taskOutcomeColor(outcome.status)}>{taskOutcomeLabel(outcome.status, t)}</Tag>
          {outcome.recovered ? <Tag color="green">{t.recovered}</Tag> : null}
          {outcome.longTask ? (
            <Tooltip title={outcome.longTask.longtask_id || outcome.longTask.workflow_id}>
              <Tag>{shortId(outcome.longTask.longtask_id || outcome.longTask.workflow_id)}</Tag>
            </Tooltip>
          ) : null}
          {outcome.worker?.jobId ? (
            <Tooltip title={outcome.worker.jobId}>
              <Tag color="cyan">{shortId(outcome.worker.jobId)}</Tag>
            </Tooltip>
          ) : null}
          {hasDetail ? (
            <Button type="text" size="small" icon={expanded ? <UpOutlined /> : <DownOutlined />} onClick={(e) => { e.stopPropagation(); toggleExpand(); }} />
          ) : null}
        </Space>
        <Tooltip title={outcome.title}>
          <Typography.Text strong ellipsis>
            {outcome.title}
          </Typography.Text>
        </Tooltip>
        {outcome.detail ? (
          <Typography.Text type="secondary" ellipsis={{ tooltip: outcome.detail }}>
            {outcome.recovered && outcome.longTask ? `${t.recovered} — ${outcome.longTask.longtask_id}. ${outcome.detail}` : outcome.detail}
          </Typography.Text>
        ) : null}
        <Space size={8} align="center" style={{ width: "100%" }}>
          <Progress percent={outcomeProgress(outcome)} size="small" status={progressStatus(outcome.status)} showInfo={false} style={{ flex: 1 }} />
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {outcomeProgressLabel(outcome)}
          </Typography.Text>
        </Space>
      </div>
      <div className="task-outcome-actions">
        {actions}
        {isTerminal && onDismiss ? (
          <Tooltip title={t.dismissed}>
            <Button size="small" type="text" icon={<CloseOutlined />} onClick={() => onDismiss(outcome.id)} />
          </Tooltip>
        ) : null}
      </div>
      {expanded && hasDetail ? <OutcomeDetail outcome={outcome} t={t} /> : null}
    </div>
  );
}

function OutcomeDetail({ outcome, t }: { outcome: TaskOutcome; t: TaskCenterText }) {
  const longTask = outcome.longTask;
  const worker = outcome.worker;
  if (!longTask && !worker) {
    return null;
  }
  return (
    <div className="task-outcome-detail" style={{ marginTop: 8, padding: "8px 12px", background: "rgba(0,0,0,0.03)", borderRadius: 6 }}>
      <Space direction="vertical" size={10} style={{ width: "100%" }}>
        {longTask ? <LongTaskDetail longTask={longTask} /> : null}
        {worker ? <WorkerDetail worker={outcome.worker!} /> : null}
      </Space>
    </div>
  );
}

function LongTaskDetail({ longTask }: { longTask: NonNullable<TaskOutcome["longTask"]> }) {
  const { graph, stories, run } = longTask;
  const runningStory = stories.find((story) => story.status === "running");
  const failedStory = stories.find((story) => story.status === "error" || story.status === "failed");
  return (
    <Space direction="vertical" size={8} style={{ width: "100%" }}>
      {graph && graph.nodes.length > 0 ? <AgentGraphDiagram graph={graph} /> : null}
      <Descriptions size="small" column={1} bordered
        items={[
          { key: "status", label: "Status", children: longTask.status },
          { key: "progress", label: "Progress", children: `${longTask.completed}/${longTask.total} completed` },
          { key: "pending", label: "Pending", children: longTask.pending },
          { key: "running", label: "Running", children: longTask.running },
          { key: "failed", label: "Failed", children: longTask.failed },
          ...(run?.blocked_by ? [{ key: "blocked_by", label: "Blocked by", children: run.blocked_by }] : []),
          ...(run && run.iterations > 0 ? [{ key: "iterations", label: "Iterations", children: run.iterations }] : []),
        ]}
      />
      {stories.length > 0 ? (
        <List
          size="small"
          header={<Typography.Text strong>Stories</Typography.Text>}
          dataSource={stories}
          renderItem={(story) => (
            <List.Item>
              <Space direction="vertical" size={2} style={{ width: "100%" }}>
                <Space wrap size={4}>
                  <Tag color={story.passes ? "green" : story.status === "error" ? "red" : story.status === "running" ? "processing" : "default"}>{story.id}</Tag>
                  {story.status ? <Tag>{story.status}</Tag> : null}
                  {story.verdict ? <Tag>{story.verdict}</Tag> : null}
                  {story.commit_hash ? <Tag color="blue">commit {story.commit_hash.slice(0, 8)}</Tag> : null}
                  {story.repair_attempts ? <Tag color="orange">repair x{story.repair_attempts}</Tag> : null}
                </Space>
                {story.title ? <Typography.Text>{story.title}</Typography.Text> : null}
                {story.error ? <Typography.Text type="danger" ellipsis={{ tooltip: story.error }}>{story.error}</Typography.Text> : null}
              </Space>
            </List.Item>
          )}
        />
      ) : null}
    </Space>
  );
}

function WorkerDetail({ worker }: { worker: NonNullable<TaskOutcome["worker"]> }) {
  return (
    <Descriptions size="small" column={1} bordered
      items={[
        { key: "objective", label: "Objective", children: worker.objective || "—" },
        { key: "status", label: "Status", children: worker.status || "—" },
        { key: "message", label: "Last message", children: worker.lastMessage || worker.lastRunnerPhase || worker.phase || "—" },
        { key: "error", label: "Error", children: worker.error || "—" },
        ...(worker.writeScope?.length ? [{ key: "write_scope", label: "Write scope", children: worker.writeScope.join(", ") }] : []),
      ]}
    />
  );
}

function OutcomeActions(props: TaskCenterPanelProps & { outcome: TaskOutcome; t: TaskCenterText }) {
  const { outcome, t } = props;
  const jobId = outcome.worker?.jobId || "";
  const workerStatus = (outcome.worker?.status || "").toLowerCase();
  const canCancelWorker = jobId && ["running", "pending", "resuming"].includes(workerStatus);
  const canResumeWorker = jobId && ["canceled", "cancelled", "interrupted", "error", "failed"].includes(workerStatus);
  const canReviewWorker = jobId && outcome.worker?.writeScope?.length && workerStatus !== "running";
  const canMergeWorker = canReviewWorker && outcome.worker?.mergeStatus !== "merged" && outcome.worker?.mergeStatus !== "no_changes";
  const longTask = outcome.longTask;
  const stories = longTask?.stories ?? [];
  const activeStory = stories.find((story) => story.status === "running") ?? stories.find((story) => story.status === "error");
  const finalizable = stories.find((story) => story.status === "completed" && story.verdict === "pass" && story.validation_status === "pending" && story.node_id);

  return (
    <Space size={4} wrap>
      {canReviewWorker ? (
        <Tooltip title={t.reviewSubagent}>
          <Button
            size="small"
            type="text"
            icon={<EyeOutlined />}
            aria-label={t.reviewSubagent}
            loading={props.reviewingJobId === jobId}
            onClick={() => props.onReviewSubagent(jobId)}
          />
        </Tooltip>
      ) : null}
      {canMergeWorker ? (
        <Popconfirm title={t.mergeConfirm} onConfirm={() => props.onMergeSubagent(jobId)}>
          <Tooltip title={t.mergeSubagent}>
            <Button
              size="small"
              type="text"
              icon={<CheckOutlined />}
              aria-label={t.mergeSubagent}
              loading={props.mergingJobId === jobId}
            />
          </Tooltip>
        </Popconfirm>
      ) : null}
      {canResumeWorker ? (
        <Tooltip title={t.resumeSubagent}>
          <Button
            size="small"
            type="text"
            icon={<PlayCircleOutlined />}
            aria-label={t.resumeSubagent}
            loading={props.resumingJobId === jobId}
            onClick={() => props.onResumeSubagent(jobId)}
          />
        </Tooltip>
      ) : null}
      {canCancelWorker ? (
        <Tooltip title={t.cancelSubagent}>
          <Button
            size="small"
            danger
            type="text"
            icon={<StopOutlined />}
            aria-label={t.cancelSubagent}
            loading={props.cancelingJobId === jobId}
            onClick={() => props.onCancelSubagent(jobId)}
          />
        </Tooltip>
      ) : null}
      {longTask && longTask.status !== "completed" ? (
        <Tooltip title={t.runLongTask}>
          <Button
            size="small"
            type="text"
            icon={<PlayCircleOutlined />}
            aria-label={t.runLongTask}
            loading={props.runningWorkflowId === longTask.workflow_id}
            onClick={() => props.onRunLongTask(longTask.workflow_id)}
          />
        </Tooltip>
      ) : null}
      {finalizable?.node_id && longTask ? (
        <Tooltip title={t.finalizeLongTask}>
          <Button
            size="small"
            type="text"
            icon={<CheckOutlined />}
            aria-label={t.finalizeLongTask}
            loading={props.finalizingLongTask?.workflowId === longTask.workflow_id && props.finalizingLongTask?.nodeId === finalizable.node_id}
            onClick={() => props.onFinalizeLongTask(longTask.workflow_id, finalizable.node_id!)}
          />
        </Tooltip>
      ) : null}
      {activeStory?.node_id && longTask ? (
        <Popconfirm title={t.cancelLongTaskConfirm} onConfirm={() => props.onCancelLongTask(longTask.workflow_id, activeStory.node_id!)}>
          <Tooltip title={t.cancelLongTaskNode}>
            <Button
              size="small"
              danger
              type="text"
              icon={<StopOutlined />}
              aria-label={t.cancelLongTaskNode}
              loading={props.cancelingLongTask?.workflowId === longTask.workflow_id && props.cancelingLongTask?.nodeId === activeStory.node_id}
            />
          </Tooltip>
        </Popconfirm>
      ) : null}
    </Space>
  );
}

function CollapsedSummary({ outcomes, t }: { outcomes: TaskOutcome[]; t: TaskCenterText }) {
  const counts = outcomes.reduce<Record<string, number>>((acc, outcome) => {
    acc[outcome.status] = (acc[outcome.status] ?? 0) + 1;
    return acc;
  }, {});
  return (
    <Space size={8} wrap>
      <Tag color={counts.running ? "processing" : "default"}>{counts.running ?? 0} {t.running}</Tag>
      <Tag color={counts.blocked ? "gold" : "default"}>{counts.blocked ?? 0} {t.blocked}</Tag>
      <Tag color={counts.ready_for_review ? "blue" : "default"}>{counts.ready_for_review ?? 0} {t.readyForReview}</Tag>
      <Tag color={counts.merged ? "green" : "default"}>{counts.merged ?? 0} {t.merged}</Tag>
      <Tag color={counts.failed ? "red" : "default"}>{counts.failed ?? 0} {t.failed}</Tag>
    </Space>
  );
}

function outcomeProgress(outcome: TaskOutcome) {
  if (outcome.longTask && outcome.longTask.total > 0) {
    const passed = (outcome.longTask.stories ?? []).filter((story) => story.passes).length;
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

// outcomeProgressLabel turns the raw progress number into a human-readable
// line: "3/5 done · running: story-B" for longtasks, and the worker's last
// message/phase otherwise.
function outcomeProgressLabel(outcome: TaskOutcome) {
  if (outcome.longTask && outcome.longTask.total > 0) {
    const passed = (outcome.longTask.stories ?? []).filter((story) => story.passes).length;
    const running = (outcome.longTask.stories ?? []).find((story) => story.status === "running");
    const failed = (outcome.longTask.stories ?? []).find((story) => story.status === "error" || story.status === "failed");
    const parts = [`${passed}/${outcome.longTask.total} done`];
    if (running?.title && running.title !== failed?.title) {
      parts.push(`running: ${running.title}`);
    }
    if (failed?.title) {
      parts.push(`failed: ${failed.title}`);
    }
    return parts.join(" · ");
  }
  if (outcome.worker) {
    const last = outcome.worker.lastMessage || outcome.worker.lastRunnerPhase || outcome.worker.phase || outcome.worker.summary || "";
    return last ? String(last).slice(0, 80) : outcome.status;
  }
  return outcome.status;
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
  if (value.length <= 18) return value;
  return `${value.slice(0, 8)}…${value.slice(-6)}`;
}
