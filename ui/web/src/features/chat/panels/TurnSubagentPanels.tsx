import { useState } from "react";
import type { TurnRecord, FeedItem, LongTaskView, DurableSubagentReview, PackageRoleEntry, AgentGraphNode } from "../../../lib/types";
import { useMutation } from "@tanstack/react-query";
import { useI18n } from "../../../i18n";
import { Empty, List, Tooltip, Button, Space, Typography, Tag, Card, Progress, Popconfirm, Alert, Descriptions, Collapse, Drawer } from "antd";
import { PlayCircleOutlined, RedoOutlined, CheckOutlined, StopOutlined, EyeOutlined, ApartmentOutlined } from "@ant-design/icons";
import { SubagentCard } from "../../../components/SubagentCard";
import { turnStatusColor, shortTurnId, formatTimelineTime, formatTurnError, previewText, formatCompactNumber } from "../../../lib/timelineUtils";
import { AgentGraphDiagram } from "./AgentGraphDiagram";

export function TurnList({
  items,
  retrying,
  resuming,
  onRetry,
  onResume,
}: {
  items: TurnRecord[];
  retrying: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  resuming: ReturnType<typeof useMutation<unknown, Error, { sessionId: string; turnId: string }>>;
  onRetry: (turnId: string) => void;
  onResume: (turnId: string) => void;
}) {
  const { t } = useI18n();
  if (items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.noTurns")} />;
  }
  return (
    <List
      size="small"
      dataSource={items.slice().reverse()}
      renderItem={(turn) => (
        <List.Item
          actions={[
            turn.can_resume ? (
              <Tooltip key="resume" title={t("chat.resumeTurn")}>
                <Button
                  size="small"
                  icon={<PlayCircleOutlined />}
                  loading={resuming.isPending && resuming.variables?.turnId === turn.id}
                  onClick={() => onResume(turn.id)}
                />
              </Tooltip>
            ) : null,
            turn.can_retry ? (
              <Tooltip key="retry" title={t("chat.retryTurn")}>
                <Button
                  size="small"
                  icon={<RedoOutlined />}
                  loading={retrying.isPending && retrying.variables?.turnId === turn.id}
                  onClick={() => onRetry(turn.id)}
                />
              </Tooltip>
            ) : null,
          ].filter(Boolean)}
        >
          <List.Item.Meta
            title={
              <Space wrap>
                <Typography.Text strong>{turn.summary || turn.id}</Typography.Text>
                <Tag color={turnStatusColor(turn.status)}>{turn.status || "unknown"}</Tag>
                {turn.retry_of ? <Tag>{t("chat.retryOf", { id: shortTurnId(turn.retry_of) })}</Tag> : null}
              </Space>
            }
            description={
              <Space direction="vertical" size={2}>
                <Typography.Text type="secondary">
                  {turn.source || "web"} · {formatTimelineTime(turn.updated_at)}
                </Typography.Text>
                {turn.error ? <Typography.Text type="danger">{formatTurnError(turn.error)}</Typography.Text> : null}
              </Space>
            }
          />
        </List.Item>
      )}
    />
  );
}

export function SubagentList({
  items,
  reviewing,
  canceling,
  resuming,
  merging,
  onReview,
  onCancel,
  onResume,
  onMerge,
}: {
  items: FeedItem[];
  reviewing: ReturnType<typeof useMutation<unknown, Error, string>>;
  canceling: ReturnType<typeof useMutation<unknown, Error, string>>;
  resuming: ReturnType<typeof useMutation<unknown, Error, string>>;
  merging: ReturnType<typeof useMutation<unknown, Error, string>>;
  onReview: (jobId: string) => void;
  onCancel: (jobId: string) => void;
  onResume: (jobId: string) => void;
  onMerge: (jobId: string) => void;
}) {
  if (items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No subagent jobs" />;
  }
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <SubagentOverview items={items} />
      {items.map((item) => (
        <div key={item.id} className="subagent-inspector-item">
          <SubagentQuickMeta item={item} />
          <SubagentCard
            item={item}
            defaultExpanded
            actions={
              <SubagentActions
                item={item}
                reviewing={reviewing.isPending && reviewing.variables === item.jobId}
                canceling={canceling.isPending && canceling.variables === item.jobId}
                resuming={resuming.isPending && resuming.variables === item.jobId}
                merging={merging.isPending && merging.variables === item.jobId}
                onReview={onReview}
                onCancel={onCancel}
                onResume={onResume}
                onMerge={onMerge}
              />
            }
          />
        </div>
      ))}
    </Space>
  );
}

export function LongTaskList({
  items,
  loading,
  running,
  canceling,
  finalizing,
  onRun,
  onCancel,
  onFinalize,
}: {
  items: LongTaskView[];
  loading: boolean;
  running: ReturnType<typeof useMutation<unknown, Error, string>>;
  canceling: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  finalizing: ReturnType<typeof useMutation<unknown, Error, { workflowId: string; nodeId: string }>>;
  onRun: (workflowId: string) => void;
  onCancel: (workflowId: string, nodeId: string) => void;
  onFinalize: (workflowId: string, nodeId: string) => void;
}) {
  const [selectedNode, setSelectedNode] = useState<AgentGraphNode | null>(null);
  if (!loading && items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No LongTasks" />;
  }
  return (
    <>
    <List
      loading={loading}
      dataSource={items}
      rowKey={(item) => item.workflow_id}
      renderItem={(item) => {
        const done = item.stories.filter((story) => story.passes).length;
        const percent = item.total > 0 ? Math.round((done / item.total) * 100) : 0;
        const activeStory = item.stories.find((story) => story.status === "running") ?? item.stories.find((story) => story.status === "error");
        const finalizable = item.stories.find(
          (story) => story.status === "completed" && story.verdict === "pass" && story.validation_status === "pending" && story.node_id,
        );
        return (
          <List.Item>
            <Card size="small" style={{ width: "100%" }}>
              <Space direction="vertical" size={8} style={{ width: "100%" }}>
                <Space wrap align="center">
                  <Typography.Text strong ellipsis style={{ maxWidth: 260 }}>
                    {item.project || item.longtask_id || item.workflow_id}
                  </Typography.Text>
                  <Tag color={item.status === "completed" ? "green" : item.failed ? "red" : item.running ? "processing" : "default"}>
                    {item.status}
                  </Tag>
                  {item.run?.status ? <Tag>{item.run.status}</Tag> : null}
                </Space>
                {item.description ? (
                  <Typography.Paragraph type="secondary" ellipsis={{ rows: 2 }} style={{ marginBottom: 0 }}>
                    {item.description}
                  </Typography.Paragraph>
                ) : null}
                <Progress percent={percent} size="small" status={item.failed ? "exception" : item.status === "completed" ? "success" : "active"} />
                <Space wrap size={[6, 6]}>
                  <Tag>{done}/{item.total} passed</Tag>
                  <Tag>{item.running} running</Tag>
                  <Tag>{item.failed} failed</Tag>
                  {item.run?.blocked_by ? <Tag color="red">blocked: {item.run.blocked_by}</Tag> : null}
                </Space>
                <Space direction="vertical" size={4} style={{ width: "100%" }}>
                  {item.stories.map((story) => (
                    <div key={story.id} className="longtask-story-row">
                      <Space wrap size={[6, 4]}>
                        <Tag color={story.passes ? "green" : story.status === "error" ? "red" : story.status === "running" ? "processing" : "default"}>
                          {story.id}
                        </Tag>
                        <Typography.Text ellipsis style={{ maxWidth: 220 }}>
                          {story.title || story.node_id || story.id}
                        </Typography.Text>
                        {story.validation_status ? <Tag>validation {story.validation_status}</Tag> : null}
                        {story.merge_status ? <Tag>merge {story.merge_status}</Tag> : null}
                        {story.commit_status ? <Tag>commit {story.commit_status}</Tag> : null}
                        {story.repair_attempts ? <Tag color="orange">repair x{story.repair_attempts}</Tag> : null}
                      </Space>
                    </div>
                  ))}
                </Space>
                {item.graph && item.graph.nodes.length > 0 ? (
                  <Collapse
                    size="small"
                    items={[
                      {
                        key: "graph",
                        label: (
                          <Space size={6}>
                            <ApartmentOutlined />
                            <span>Graph</span>
                            <Tag>{item.graph.nodes.length} nodes</Tag>
                            <Tag>{item.graph.edges.length} edges</Tag>
                            {item.graph.failed > 0 ? <Tag color="red">{item.graph.failed} failed</Tag> : null}
                          </Space>
                        ),
                        children: <AgentGraphDiagram graph={item.graph} onSelectNode={setSelectedNode} />,
                      },
                    ]}
                  />
                ) : null}
                <Space wrap>
                  <Button
                    size="small"
                    icon={<PlayCircleOutlined />}
                    loading={running.isPending && running.variables === item.workflow_id}
                    onClick={() => onRun(item.workflow_id)}
                  >
                    Run
                  </Button>
                  {finalizable?.node_id ? (
                    <Button
                      size="small"
                      icon={<CheckOutlined />}
                      loading={
                        finalizing.isPending &&
                        finalizing.variables?.workflowId === item.workflow_id &&
                        finalizing.variables?.nodeId === finalizable.node_id
                      }
                      onClick={() => onFinalize(item.workflow_id, finalizable.node_id!)}
                    >
                      Finalize
                    </Button>
                  ) : null}
                  {activeStory?.node_id ? (
                    <Popconfirm title="Cancel this LongTask node?" onConfirm={() => onCancel(item.workflow_id, activeStory.node_id!)}>
                      <Button
                        size="small"
                        danger
                        icon={<StopOutlined />}
                        loading={
                          canceling.isPending &&
                          canceling.variables?.workflowId === item.workflow_id &&
                          canceling.variables?.nodeId === activeStory.node_id
                        }
                      >
                        Cancel
                      </Button>
                    </Popconfirm>
                  ) : null}
                </Space>
              </Space>
            </Card>
          </List.Item>
        );
      }}
    />
    <Drawer
      title={selectedNode ? selectedNode.title || selectedNode.id : "Node"}
      open={!!selectedNode}
      onClose={() => setSelectedNode(null)}
      width={380}
    >
      {selectedNode ? <AgentGraphNodeDetail node={selectedNode} /> : null}
    </Drawer>
    </>
  );
}

function AgentGraphNodeDetail({ node }: { node: AgentGraphNode }) {
  const items = [
    { key: "id", label: "Node", children: node.id },
    { key: "type", label: "Type", children: node.node_type || "—" },
    { key: "status", label: "Status", children: node.status },
    { key: "verdict", label: "Verdict", children: node.verdict || "—" },
    { key: "agent_type", label: "Agent", children: node.agent_type || "—" },
    { key: "attempt", label: "Attempt", children: node.attempt ?? 1 },
    { key: "job_id", label: "Job", children: node.job_id || "—" },
    { key: "handoff_ref", label: "Handoff", children: node.handoff_ref || "—" },
    { key: "error", label: "Error", children: node.error || "—" },
  ];
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <Descriptions size="small" column={1} bordered items={items} />
      {node.write_scope?.length ? (
        <div>
          <Typography.Text strong>Write scope</Typography.Text>
          <div style={{ marginTop: 6 }}>
            <Space wrap size={4}>
              {node.write_scope.map((path) => (
                <Tag key={path} style={{ fontFamily: "monospace" }}>
                  {path}
                </Tag>
              ))}
            </Space>
          </div>
        </div>
      ) : null}
    </Space>
  );
}

function SubagentOverview({ items }: { items: FeedItem[] }) {
  const counts = items.reduce<Record<string, number>>((acc, item) => {
    const status = (item.status || "updated").toLowerCase();
    acc[status] = (acc[status] ?? 0) + 1;
    return acc;
  }, {});
  const active = (counts.running ?? 0) + (counts.pending ?? 0) + (counts.pending_approval ?? 0);
  const failed = (counts.error ?? 0) + (counts.failed ?? 0) + (counts.interrupted ?? 0);
  const completed = counts.completed ?? 0;
  return (
    <Card size="small">
      <Space wrap>
        <Tag color={active ? "processing" : "default"}>{active} active</Tag>
        <Tag color={failed ? "red" : "default"}>{failed} needs attention</Tag>
        <Tag color={completed ? "green" : "default"}>{completed} completed</Tag>
        <Typography.Text type="secondary">{items.length} total subagents</Typography.Text>
      </Space>
    </Card>
  );
}

function SubagentQuickMeta({ item }: { item: FeedItem }) {
  const role = item.roleName || item.roleId || item.agentType || "subagent";
  const lastActivity = item.lastRecoveryHint || item.lastRunnerPhase || item.phase || item.lastToolName || item.lastMessage || item.summary || item.status || "";
  return (
    <div className="subagent-quick-meta">
      <Space direction="vertical" size={4} style={{ width: "100%" }}>
        <Space wrap>
          <Tooltip title={item.displayTitle || item.title}>
            <Typography.Text strong ellipsis style={{ maxWidth: 260 }}>
              {item.displayTitle || item.title}
            </Typography.Text>
          </Tooltip>
          {item.sequence ? <Tag>#{item.sequence}</Tag> : null}
          <Tag color="purple">{role}</Tag>
          {item.parentTurnId ? (
            <Tooltip title={item.parentTurnId}>
              <Tag color="blue">parent {shortTurnId(item.parentTurnId)}</Tag>
            </Tooltip>
          ) : null}
          {item.modelRequestCount ? <Tag>{item.modelRequestCount} calls</Tag> : null}
          {item.toolCallCount ? <Tag>{item.toolCallCount} tools</Tag> : null}
          {item.contextBudget ? <Tag color="cyan">budget {formatCompactNumber(item.contextBudget)}</Tag> : null}
        </Space>
        <Typography.Text type="secondary" ellipsis={{ tooltip: item.objective || lastActivity }}>
          {item.objective ? `Objective: ${item.objective}` : "Objective not recorded"}
        </Typography.Text>
        <Typography.Text type="secondary" ellipsis={{ tooltip: lastActivity }}>
          Last activity: {lastActivity || "none"}
        </Typography.Text>
      </Space>
    </div>
  );
}

function SubagentActions({
  item,
  reviewing,
  canceling,
  resuming,
  merging,
  onReview,
  onCancel,
  onResume,
  onMerge,
}: {
  item: FeedItem;
  reviewing: boolean;
  canceling: boolean;
  resuming: boolean;
  merging: boolean;
  onReview: (jobId: string) => void;
  onCancel: (jobId: string) => void;
  onResume: (jobId: string) => void;
  onMerge: (jobId: string) => void;
}) {
  const jobId = item.jobId || "";
  const status = (item.status || "").toLowerCase();
  const canCancel = jobId && status === "running";
  const canResume = jobId && ["canceled", "interrupted", "error", "failed"].includes(status);
  const canReview = jobId && item.writeScope?.length && status !== "running";
  const canMerge = canReview && item.mergeStatus !== "merged";
  return (
    <Space size={4}>
      {canReview ? (
        <Tooltip title="Review subagent changes">
          <Button
            size="small"
            type="text"
            aria-label="Review subagent changes"
            icon={<EyeOutlined />}
            loading={reviewing}
            onClick={() => onReview(jobId)}
          />
        </Tooltip>
      ) : null}
      {canCancel ? (
        <Tooltip title="Cancel subagent">
          <Button
            size="small"
            danger
            type="text"
            aria-label="Cancel subagent"
            icon={<StopOutlined />}
            loading={canceling}
            onClick={() => onCancel(jobId)}
          />
        </Tooltip>
      ) : null}
      {canResume ? (
        <Tooltip title="Resume subagent">
          <Button
            size="small"
            type="text"
            aria-label="Resume subagent"
            icon={<PlayCircleOutlined />}
            loading={resuming}
            onClick={() => onResume(jobId)}
          />
        </Tooltip>
      ) : null}
      {canMerge ? (
        <Popconfirm title="Merge subagent changes?" onConfirm={() => onMerge(jobId)}>
          <Tooltip title="Merge subagent changes">
            <Button size="small" type="text" aria-label="Merge subagent changes" icon={<CheckOutlined />} loading={merging} />
          </Tooltip>
        </Popconfirm>
      ) : null}
    </Space>
  );
}

export function SubagentReviewPanel({ review, loading }: { review: DurableSubagentReview | null; loading: boolean }) {
  if (loading && !review) {
    return <Alert type="info" showIcon message="Loading subagent review..." />;
  }
  if (!review) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No review loaded" />;
  }
  return (
    <Space direction="vertical" size={14} style={{ width: "100%" }}>
      <Descriptions
        bordered
        size="small"
        column={1}
        items={[
          { key: "job", label: "Job", children: <Typography.Text copyable>{review.job_id}</Typography.Text> },
          { key: "worktree", label: "Worktree", children: review.worktree_dir ? <Typography.Text copyable>{review.worktree_dir}</Typography.Text> : "-" },
          { key: "scope", label: "Write scope", children: review.write_scope?.length ? review.write_scope.join(", ") : "-" },
          { key: "diff", label: "Diff", children: review.diff_truncated ? <Tag color="gold">truncated</Tag> : <Tag>complete</Tag> },
        ]}
      />
      {review.conflicts?.length ? <Alert type="warning" showIcon message="Merge conflicts" description={review.conflicts.join("\n")} /> : null}
      <Card size="small" title="Changed files">
        {review.changes.length === 0 ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No changes" />
        ) : (
          <List
            size="small"
            dataSource={review.changes}
            renderItem={(change) => (
              <List.Item>
                <Space wrap>
                  <Tag color={fileChangeColor(change.status)}>{change.status}</Tag>
                  <Typography.Text code>{change.path}</Typography.Text>
                  {change.binary ? <Tag>binary</Tag> : null}
                  {typeof change.bytes === "number" ? <Typography.Text type="secondary">{change.bytes} bytes</Typography.Text> : null}
                </Space>
              </List.Item>
            )}
          />
        )}
      </Card>
      <Card size="small" title="Diff">
        {review.diff?.trim() ? (
          <Typography.Paragraph>
            <pre className="subagent-review-diff">{review.diff}</pre>
          </Typography.Paragraph>
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No diff" />
        )}
      </Card>
    </Space>
  );
}

export function AvailableSubagentRoles({ items, loading }: { items: PackageRoleEntry[]; loading: boolean }) {
  return (
    <Card size="small" title="Available roles" loading={loading}>
      {items.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No package roles" />
      ) : (
        <List
          size="small"
          dataSource={items}
          renderItem={(role) => {
            const title = `${role.package_name}/${role.id}`;
            return (
              <List.Item>
                <Space direction="vertical" size={4} style={{ width: "100%" }}>
                  <Space wrap>
                    <Tooltip title={title}>
                      <Typography.Text strong ellipsis style={{ maxWidth: 220 }}>
                        {role.name || role.id}
                      </Typography.Text>
                    </Tooltip>
                    <Tag color={role.write_enabled ? "gold" : "default"}>{role.write_enabled ? "write" : "read-only"}</Tag>
                    {role.model_hint ? <Tag>{role.model_hint}</Tag> : null}
                  </Space>
                  <Typography.Text type="secondary" ellipsis={{ tooltip: role.description || title }}>
                    {role.description || title}
                  </Typography.Text>
                  <Space wrap size={4}>
                    <Tooltip title={title}>
                      <Tag>{role.package_name}</Tag>
                    </Tooltip>
                    {role.capabilities?.slice(0, 3).map((capability) => (
                      <Tooltip key={capability} title={capability}>
                        <Tag color="blue">{previewText(capability, 28)}</Tag>
                      </Tooltip>
                    ))}
                    {(role.capabilities?.length ?? 0) > 3 ? <Tag>+{(role.capabilities?.length ?? 0) - 3}</Tag> : null}
                  </Space>
                  {role.tools?.length ? (
                    <Typography.Text type="secondary" ellipsis={{ tooltip: role.tools.join(", ") }}>
                      tools: {role.tools.slice(0, 6).join(", ")}
                      {role.tools.length > 6 ? `, +${role.tools.length - 6}` : ""}
                    </Typography.Text>
                  ) : null}
                </Space>
              </List.Item>
            );
          }}
        />
      )}
    </Card>
  );
}

function fileChangeColor(status: string) {
  switch (status) {
    case "added":
      return "green";
    case "deleted":
      return "red";
    case "modified":
      return "blue";
    default:
      return "default";
  }
}
