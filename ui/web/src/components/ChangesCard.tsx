import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Space, Spin, Tag, Tooltip, Typography } from "antd";
import { DownOutlined, FolderOpenOutlined, RightOutlined } from "@ant-design/icons";
import { DiffView } from "./DiffView";
import { gitDiff, type GitDiffResponse } from "../lib/api";
import type { FeedSegment } from "../lib/types";

interface ChangesCardProps {
  /** Tool segments of the finished turn — scanned for write/edit calls. */
  segments: FeedSegment[];
  workspaceDir?: string;
  token?: string | null;
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
 * turn. Scans the turn's tool segments for write_file / edit_file calls,
 * dedupes by path, and lets the user expand an inline working-tree diff
 * (backend GET /git/diff) or jump into the Files panel. Each file shows its
 * added/deleted line counts and the header shows the totals.
 */
export function ChangesCard({ segments, workspaceDir, token, onOpenInFiles }: ChangesCardProps) {
  const files = useMemo(() => collectChangedFiles(segments), [segments]);

  // path -> diff state ("loading" | response). Fetched eagerly so the +/- line
  // stats are visible without expanding; the same response is reused when the
  // user expands the inline diff.
  const [diffState, setDiffState] = useState<Record<string, GitDiffResponse | "loading">>({});

  useEffect(() => {
    setDiffState({});
    if (files.length === 0) return;
    let cancelled = false;
    for (const file of files) {
      setDiffState((prev) => ({ ...prev, [file.path]: "loading" }));
      gitDiff(token ?? null, file.path, workspaceDir)
        .then((resp) => {
          if (!cancelled) setDiffState((prev) => ({ ...prev, [file.path]: resp }));
        })
        .catch(() => {
          if (!cancelled) setDiffState((prev) => ({ ...prev, [file.path]: { repo: true, error: "Failed to load diff" } }));
        });
    }
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [files, workspaceDir, token]);

  const [expanded, setExpanded] = useState<string | null>(null);

  if (files.length === 0) {
    return null;
  }

  const totals = files.reduce<DiffStats>(
    (acc, file) => {
      const state = diffState[file.path];
      if (state && state !== "loading" && !state.error && state.diff) {
        const stats = countUnifiedDiffStats(state.diff);
        acc.added += stats.added;
        acc.deleted += stats.deleted;
      }
      return acc;
    },
    { added: 0, deleted: 0 },
  );
  const statsReady = files.every((file) => {
    const state = diffState[file.path];
    return state !== undefined && state !== "loading";
  });

  const toggle = (path: string) => {
    setExpanded((prev) => (prev === path ? null : path));
  };

  return (
    <div className="changes-card">
      <div className="changes-card-header">
        <Typography.Text strong>Changed files</Typography.Text>
        <Space size={6}>
          {statsReady ? (
            <Tag className="changes-card-total-stats">
              +{totals.added} −{totals.deleted}
            </Tag>
          ) : null}
          <Tag>{files.length}</Tag>
        </Space>
      </div>
      <div className="changes-card-list">
        {files.map((file) => {
          const isOpen = expanded === file.path;
          const state = diffState[file.path];
          const repoUnavailable = state && state !== "loading" && !state.repo;
          const stats = state && state !== "loading" && !state.error && state.diff ? countUnifiedDiffStats(state.diff) : null;
          return (
            <div key={file.path} className="changes-card-file">
              <div className="changes-card-file-row">
                <Button type="text" size="small" className="changes-card-file-toggle" onClick={() => toggle(file.path)}>
                  {isOpen ? <DownOutlined /> : <RightOutlined />}
                  <Typography.Text code className="changes-card-file-path">
                    {file.path}
                  </Typography.Text>
                </Button>
                <Tag color={file.op === "write" ? "blue" : "green"} className="changes-card-file-op">
                  {file.op}
                </Tag>
                {state === "loading" ? (
                  <Spin size="small" />
                ) : stats ? (
                  <Typography.Text className="changes-card-file-stats" type="secondary">
                    <span className="changes-stat-added">+{stats.added}</span>
                    <span className="changes-stat-deleted"> −{stats.deleted}</span>
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
              {isOpen ? (
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
