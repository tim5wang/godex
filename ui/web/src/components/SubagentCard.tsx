import { useState, type ReactNode } from "react";
import { Alert, Button, Progress, Space, Tag, Tooltip, Typography } from "antd";
import { CaretDownOutlined, CaretRightOutlined, RobotOutlined, ToolOutlined, WarningOutlined } from "@ant-design/icons";
import { MarkdownContent } from "./MarkdownContent";
import type { FeedItem, SubagentProgressItem } from "../lib/types";

interface SubagentCardProps {
  item: FeedItem;
  onToggle?: () => void;
  defaultExpanded?: boolean;
  actions?: ReactNode;
}

export function SubagentCard({ item, onToggle, defaultExpanded = false, actions }: SubagentCardProps) {
  const [localExpanded, setLocalExpanded] = useState(defaultExpanded);
  const expanded = onToggle ? item.expanded ?? defaultExpanded : localExpanded;
  const latest = latestSubagentProgress(item);
  const activity = item.lastRecoveryHint || latest?.recoveryHint || item.lastRunnerPhase || latest?.phase || item.lastToolName || latest?.toolName || "";
  const detail = latest?.error || item.error || latest?.message || latest?.result || item.body || item.summary || item.objective || "Subagent job updated.";
  const status = item.status || latest?.status || "updated";
  const progress = progressForSubagentStatus(status, item.progress?.length ?? 0);
  const maxTurnsReached = isMaxTurnsError(detail);
  const iterationLabel = subagentIterationLabel(item, latest);
  const toggleDetails = () => {
    if (onToggle) {
      onToggle();
      return;
    }
    setLocalExpanded((value) => !value);
  };

  return (
    <div className={`subagent-card subagent-card-${subagentStatusClass(status)}`}>
      <div className="subagent-card-header">
        <Space size={10} className="subagent-card-title" wrap>
          <RobotOutlined />
          <Tooltip title={item.title}>
            <Typography.Text strong ellipsis style={{ maxWidth: 260 }}>
              {item.title}
            </Typography.Text>
          </Tooltip>
          <Tag color={subagentStatusColor(status)}>{status}</Tag>
          {item.sequence ? <Tag>#{item.sequence}</Tag> : null}
          {item.lastRunnerPhase || item.phase ? <Tag>{item.lastRunnerPhase || item.phase}</Tag> : null}
        </Space>
        <Space size={4}>
          {actions}
          <Tooltip title={expanded ? "Hide details" : "Show details"}>
            <Button
              className="subagent-card-toggle"
              icon={expanded ? <CaretDownOutlined /> : <CaretRightOutlined />}
              aria-label={expanded ? "Hide subagent details" : "Show subagent details"}
              onClick={toggleDetails}
              size="small"
              type="text"
            />
          </Tooltip>
        </Space>
      </div>

      <Progress percent={progress.percent} size="small" status={progress.status} showInfo={false} />

      <Typography.Paragraph className="subagent-card-summary" ellipsis={{ rows: 2, expandable: true, symbol: "more", tooltip: detail }}>
        {latest?.error || item.error ? <WarningOutlined /> : latest?.toolName ? <ToolOutlined /> : null}
        {latest?.error || item.error || latest?.toolName ? " " : null}
        {detail}
      </Typography.Paragraph>

      {activity ? (
        <Typography.Text type="secondary" className="subagent-card-activity">
          Current: {activity}
        </Typography.Text>
      ) : null}

      <div className="subagent-card-meta">
        {item.jobId ? <MetaTag value={item.jobId} /> : null}
        {item.objective ? <MetaTag label="objective" value={item.objective} color="cyan" /> : null}
        {item.identityId ? <MetaTag label="identity" value={item.identityId} /> : null}
        {item.parentTurnId ? <MetaTag label="turn" value={item.parentTurnId} /> : null}
        {item.agentType ? <MetaTag value={item.agentType} /> : null}
        {item.roleId ? <MetaTag label="role" value={item.roleId} color="purple" /> : null}
        {item.roleName ? <MetaTag value={item.roleName} color="purple" /> : null}
        {item.packageName ? <MetaTag value={item.packageName} /> : null}
        {item.budgetHint ? <MetaTag label="budget" value={item.budgetHint} /> : null}
        {item.maxTurns ? <MetaTag label="max turns" value={String(item.maxTurns)} color={maxTurnsReached ? "red" : undefined} /> : null}
        {iterationLabel ? <MetaTag label="iter" value={iterationLabel} color={maxTurnsReached ? "red" : undefined} /> : null}
        {item.modelRequestCount ? <MetaTag label="calls" value={String(item.modelRequestCount)} /> : null}
        {item.toolCallCount ? <MetaTag label="tools" value={String(item.toolCallCount)} /> : null}
        {item.modelHint ? <MetaTag label="model" value={item.modelHint} /> : null}
        {item.lastToolName ? <MetaTag value={item.lastToolName} icon={<ToolOutlined />} /> : null}
        {item.isolation ? <MetaTag label="isolation" value={item.isolation} color="geekblue" /> : null}
        {item.workspaceOrigin ? <MetaTag label="origin" value={item.workspaceOrigin} /> : null}
        {item.cleanupState ? <MetaTag label="cleanup" value={item.cleanupState} color={item.cleanupState === "cleaned" ? "green" : undefined} /> : null}
        {item.mergeStatus ? <MetaTag label="merge" value={item.mergeStatus} /> : null}
        {item.writeScope?.length ? <MetaTag label="scope" value={item.writeScope.join(", ")} /> : null}
      </div>

      {expanded ? (
        <div className="subagent-card-details">
          {maxTurnsReached ? (
            <Alert
              type="error"
              showIcon
              message="Subagent reached its turn budget"
              description={detail}
              style={{ marginBottom: 12 }}
            />
          ) : null}
          {item.progress?.length ? (
            <div className="subagent-progress-list">
              {item.progress.slice(-8).map((entry, index) => (
                <div className="subagent-progress-row" key={`${entry.timestamp || index}:${entry.phase || ""}:${entry.toolName || ""}`}>
                  <div className="subagent-progress-dot" />
                  <div className="subagent-progress-body">
                    <Space size={6} wrap>
                      <Typography.Text strong>{entry.phase || entry.status || "update"}</Typography.Text>
                      {entry.toolName ? <Tag icon={<ToolOutlined />}>{entry.toolName}</Tag> : null}
                      {entry.iteration ? <Tag>{entry.maxTurns ? `${entry.iteration}/${entry.maxTurns}` : `turn ${entry.iteration}`}</Tag> : null}
                      {entry.model ? <Tag>{entry.model}</Tag> : null}
                      {entry.status ? <Tag color={subagentStatusColor(entry.status)}>{entry.status}</Tag> : null}
                      {entry.timestamp ? <Typography.Text type="secondary">{formatSubagentTime(entry.timestamp)}</Typography.Text> : null}
                    </Space>
                    {entry.error ? (
                      <Typography.Text type="danger">{entry.error}</Typography.Text>
                    ) : entry.message || entry.result ? (
                      <MarkdownContent content={entry.message || entry.result || ""} />
                    ) : null}
                    {entry.recoveryHint ? <Typography.Text type="secondary">{entry.recoveryHint}</Typography.Text> : null}
                  </div>
                </div>
              ))}
            </div>
          ) : null}
          {item.worktreeDir ? (
            <div className="subagent-worktree">
              <Typography.Text type="secondary">Worktree</Typography.Text>
              <Typography.Text code>{item.worktreeDir}</Typography.Text>
            </div>
          ) : null}
          {item.gitBranch ? (
            <div className="subagent-worktree">
              <Typography.Text type="secondary">Git branch</Typography.Text>
              <Typography.Text code>{item.gitBranch}</Typography.Text>
            </div>
          ) : null}
          {item.toolNames?.length ? (
            <div className="subagent-worktree">
              <Typography.Text type="secondary">Allowed tools</Typography.Text>
              <Space size={4} wrap>
                {item.toolNames.map((tool) => (
                  <MetaTag key={tool} value={tool} icon={<ToolOutlined />} />
                ))}
              </Space>
            </div>
          ) : null}
          {item.capabilitySummary?.length ? (
            <div className="subagent-worktree">
              <Typography.Text type="secondary">Capabilities</Typography.Text>
              <Space size={4} wrap>
                {item.capabilitySummary.map((capability) => (
                  <MetaTag key={capability} value={capability} />
                ))}
              </Space>
            </div>
          ) : null}
          {item.prompt ? (
            <div className="subagent-worktree">
              <Typography.Text type="secondary">Prompt</Typography.Text>
              <Typography.Text>{item.prompt}</Typography.Text>
            </div>
          ) : null}
          {item.lastRecoveryHint ? (
            <div className="subagent-worktree">
              <Typography.Text type="secondary">Recovery hint</Typography.Text>
              <Typography.Text>{item.lastRecoveryHint}</Typography.Text>
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function MetaTag({ label, value, color, icon }: { label?: string; value: string; color?: string; icon?: ReactNode }) {
  const text = label ? `${label}: ${value}` : value;
  return (
    <Tooltip title={text}>
      <Tag color={color} icon={icon}>
        {truncateTag(text)}
      </Tag>
    </Tooltip>
  );
}

function truncateTag(value: string) {
  return value.length <= 48 ? value : `${value.slice(0, 45)}...`;
}

function latestSubagentProgress(item: FeedItem): SubagentProgressItem | undefined {
  return item.progress?.at(-1);
}

function isMaxTurnsError(value: string) {
  return value.toLowerCase().includes("reached max turns");
}

function subagentIterationLabel(item: FeedItem, latest?: SubagentProgressItem) {
  const iteration = item.lastIteration || latest?.iteration;
  const maxTurns = item.maxTurns || latest?.maxTurns;
  if (iteration && maxTurns) {
    return `${iteration}/${maxTurns}`;
  }
  if (iteration) {
    return `turn ${iteration}`;
  }
  if (maxTurns) {
    return `max ${maxTurns}`;
  }
  return "";
}

function progressForSubagentStatus(status: string, count: number): { percent: number; status?: "active" | "exception" | "success" | "normal" } {
  const normalized = status.toLowerCase();
  if (normalized === "completed") {
    return { percent: 100, status: "success" };
  }
  if (normalized === "error" || normalized === "failed") {
    return { percent: 100, status: "exception" };
  }
  if (normalized === "canceled" || normalized === "interrupted") {
    return { percent: 100, status: "normal" };
  }
  return { percent: Math.min(92, Math.max(18, 18 + count * 8)), status: "active" };
}

export function subagentStatusColor(status: string) {
  switch (status.toLowerCase()) {
    case "completed":
      return "green";
    case "running":
      return "processing";
    case "error":
    case "failed":
      return "red";
    case "canceled":
    case "interrupted":
      return "default";
    case "pending_approval":
      return "gold";
    default:
      return "blue";
  }
}

function subagentStatusClass(status: string) {
  return status.toLowerCase().replace(/[^a-z0-9_-]+/g, "-") || "updated";
}

function formatSubagentTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
