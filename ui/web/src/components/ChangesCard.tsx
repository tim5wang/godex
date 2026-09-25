import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Space, Spin, Tag, Tooltip, Typography } from "antd";
import { DownOutlined, FolderOpenOutlined, RightOutlined } from "@ant-design/icons";
import { DiffView } from "./DiffView";
import { gitDiff, gitDiffStats, type GitDiffResponse, type GitDiffStatsResponse } from "../lib/api";
import type { FeedSegment } from "../lib/types";

interface ChangesCardProps {
  /** Tool segments of the finished turn — scanned for write/edit calls. */
  segments: FeedSegment[];
  workspaceDir?: string;
  token?: string | null;
  /** Only the newest non-archived turn may show a live working-tree diff. */
  isLatestTurn?: boolean;
  /** Called when the user asks to open a changed file in the Files panel. */
  onOpenInFiles?: (path: string) => void;
}

interface ChangedFile {
  path: string;
  op: "write" | "edit";
}

interface DiffStats {
  added: number;
  deleted: number;
}

const WRITE_TOOL = "write_file";
const EDIT_TOOL = "edit_file";

/**
 * "Changed files" summary card rendered at the tail of a finished assistant
 * turn. Historical turns only show tool-recorded paths: their old workspace
 * diff cannot be reconstructed from the current working tree. The latest
 * turn loads all line counts in one request and fetches a full diff only when
 * the user expands a file.
 */
export function ChangesCard({ segments, workspaceDir, token, isLatestTurn = false, onOpenInFiles }: ChangesCardProps) {
  const files = useMemo(() => collectChangedFiles(segments), [segments]);
  const pathsKey = JSON.stringify(files.map((file) => file.path));
  const requestPaths = useMemo(() => JSON.parse(pathsKey) as string[], [pathsKey]);
  const [statsState, setStatsState] = useState<{ key: string; loading: boolean; response?: GitDiffStatsResponse }>({
    key: "",
    loading: false,
  });
  const [diffState, setDiffState] = useState<Record<string, GitDiffResponse | "loading">>({});
  const [expanded, setExpanded] = useState<string | null>(null);

  useEffect(() => {
    setDiffState({});
    setExpanded(null);
  }, [pathsKey, workspaceDir, token, isLatestTurn]);

  useEffect(() => {
    setStatsState({ key: pathsKey, loading: false });
    if (!isLatestTurn || requestPaths.length === 0) return;

    const controller = new AbortController();
    setStatsState({ key: pathsKey, loading: true });
    gitDiffStats(token ?? null, requestPaths, workspaceDir, controller.signal)
      .then((response) => {
        if (!controller.signal.aborted) setStatsState({ key: pathsKey, loading: false, response });
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setStatsState({ key: pathsKey, loading: false, response: { repo: true, error: "Failed to load diff stats" } });
        }
      });
    return () => controller.abort();
  }, [isLatestTurn, pathsKey, requestPaths, token, workspaceDir]);

  useEffect(() => {
    if (!isLatestTurn || !expanded) return;
    const controller = new AbortController();
    const path = expanded;
    setDiffState((prev) => ({ ...prev, [path]: "loading" }));
    gitDiff(token ?? null, path, workspaceDir, controller.signal)
      .then((response) => {
        if (!controller.signal.aborted) setDiffState((prev) => ({ ...prev, [path]: response }));
      })
      .catch(() => {
        if (!controller.signal.aborted) {
          setDiffState((prev) => ({ ...prev, [path]: { repo: true, error: "Failed to load diff" } }));
        }
      });
    return () => controller.abort();
  }, [expanded, isLatestTurn, token, workspaceDir]);

  if (files.length === 0) {
    return null;
  }

  const stats = statsState.key === pathsKey ? statsState.response : undefined;
  const statsByPath = new Map((stats?.files ?? []).map((item) => [item.path, item]));
  const totals = files.reduce<DiffStats>(
    (acc, file) => {
      const item = statsByPath.get(file.path);
      if (item) {
        acc.added += item.added;
        acc.deleted += item.deleted;
      }
      return acc;
    },
    { added: 0, deleted: 0 },
  );
  const statsReady = isLatestTurn && statsState.key === pathsKey && !statsState.loading && Boolean(stats);

  const toggle = (path: string) => {
    setExpanded((prev) => (prev === path ? null : path));
  };

  return (
    <div className="changes-card">
      <div className="changes-card-header">
        <Typography.Text strong>Changed files</Typography.Text>
        <Space size={6}>
          {isLatestTurn && statsState.key === pathsKey && statsState.loading ? <Spin size="small" /> : null}
          {statsReady && stats?.repo ? (
            <Tag className="changes-card-total-stats">
              +{totals.added} −{totals.deleted}
            </Tag>
          ) : null}
          <Tag>{files.length}</Tag>
        </Space>
      </div>
      {isLatestTurn && stats?.error ? <Alert type="error" showIcon message={stats.error} /> : null}
      {!isLatestTurn ? (
        <Typography.Text type="secondary" className="changes-card-historical-note">
          Historical turn: showing recorded file operations only.
        </Typography.Text>
      ) : null}
      <div className="changes-card-list">
        {files.map((file) => {
          const isOpen = expanded === file.path;
          const state = diffState[file.path];
          const repoUnavailable = state && state !== "loading" && !state.repo;
          const fileStats = statsByPath.get(file.path);
          return (
            <div key={file.path} className="changes-card-file">
              <div className="changes-card-file-row">
                {isLatestTurn ? (
                  <Button type="text" size="small" className="changes-card-file-toggle" onClick={() => toggle(file.path)}>
                    {isOpen ? <DownOutlined /> : <RightOutlined />}
                    <Typography.Text code className="changes-card-file-path">
                      {file.path}
                    </Typography.Text>
                  </Button>
                ) : (
                  <Typography.Text code className="changes-card-file-path">
                    {file.path}
                  </Typography.Text>
                )}
                <Tag color={file.op === "write" ? "blue" : "green"} className="changes-card-file-op">
                  {file.op}
                </Tag>
                {isLatestTurn && state === "loading" ? (
                  <Spin size="small" />
                ) : isLatestTurn && fileStats ? (
                  <Typography.Text className="changes-card-file-stats" type="secondary">
                    <span className="changes-stat-added">+{fileStats.added}</span>
                    <span className="changes-stat-deleted"> −{fileStats.deleted}</span>
                  </Typography.Text>
                ) : null}
                {onOpenInFiles ? (
                  <Tooltip title="Open in Files">
                    <Button
                      type="text"
                      size="small"
                      icon={<FolderOpenOutlined />}
                      aria-label={`Open ${file.path} in Files`}
                      onClick={() => onOpenInFiles(file.path)}
                    />
                  </Tooltip>
                ) : null}
              </div>
              {isLatestTurn && isOpen ? (
                <div className="changes-card-file-diff">
                  {state === "loading" ? (
                    <Spin size="small" />
                  ) : state === undefined ? null : state.error ? (
                    <Alert type="error" showIcon message={state.error} />
                  ) : repoUnavailable ? (
                    <Alert type="info" showIcon message="Not a git repository — diff unavailable" />
                  ) : (
                    <DiffView diff={state.diff ?? ""} />
                  )}
                </div>
              ) : null}
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Collect write/edit tool calls from turn segments, deduped by path. */
export function collectChangedFiles(segments: FeedSegment[]): ChangedFile[] {
  const seen = new Map<string, ChangedFile>();
  for (const segment of segments) {
    if (segment.type !== "tool" || !segment.item) continue;
    const item = segment.item;
    let op: ChangedFile["op"] | null = null;
    if (item.title === WRITE_TOOL) op = "write";
    else if (item.title === EDIT_TOOL) op = "edit";
    if (!op) continue;
    const path = typeof item.input?.path === "string" ? item.input.path.trim() : "";
    if (!path) continue;
    seen.set(path, { path, op });
  }
  return [...seen.values()];
}

/**
 * Count added/deleted lines in a unified diff (`git diff --no-color`),
 * ignoring meta lines (diff --git / index / --- / +++ / @@ hunks).
 */
export function countUnifiedDiffStats(diff: string): DiffStats {
  let added = 0;
  let deleted = 0;
  for (const line of diff.split("\n")) {
    if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff --git") || line.startsWith("index ") || line.startsWith("@@") || line.startsWith("\\")) {
      continue;
    }
    if (line.startsWith("+")) {
      added++;
    } else if (line.startsWith("-")) {
      deleted++;
    }
  }
  return { added, deleted };
}
