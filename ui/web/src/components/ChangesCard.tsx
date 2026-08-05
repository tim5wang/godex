import { useMemo, useState } from "react";
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

const WRITE_TOOL = "write_file";
const EDIT_TOOL = "edit_file";

/**
 * "Changed files" summary card rendered at the tail of a finished assistant
 * turn. Scans the turn's tool segments for write_file / edit_file calls,
 * dedupes by path, and lets the user expand an inline working-tree diff
 * (backend GET /git/diff) or jump into the Files panel.
 */
export function ChangesCard({ segments, workspaceDir, token, onOpenInFiles }: ChangesCardProps) {
  const files = useMemo(() => collectChangedFiles(segments), [segments]);

  // path -> diff state ("loading" | response). Kept local so expanding is
  // cheap and the card stays self-contained.
  const [diffState, setDiffState] = useState<Record<string, GitDiffResponse | "loading">>({});
  const [expanded, setExpanded] = useState<string | null>(null);

  if (files.length === 0) {
    return null;
  }

  const toggle = (path: string) => {
    const next = expanded === path ? null : path;
    setExpanded(next);
    if (next && !diffState[next]) {
      setDiffState((prev) => ({ ...prev, [next]: "loading" }));
      gitDiff(token ?? null, next, workspaceDir)
        .then((resp) => setDiffState((prev) => ({ ...prev, [next]: resp })))
        .catch(() => setDiffState((prev) => ({ ...prev, [next]: { repo: true, error: "Failed to load diff" } })));
    }
  };

  return (
    <div className="changes-card">
      <div className="changes-card-header">
        <Typography.Text strong>Changed files</Typography.Text>
        <Tag>{files.length}</Tag>
      </div>
      <div className="changes-card-list">
        {files.map((file) => {
          const isOpen = expanded === file.path;
          const state = diffState[file.path];
          const repoUnavailable = state && state !== "loading" && !state.repo;
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
