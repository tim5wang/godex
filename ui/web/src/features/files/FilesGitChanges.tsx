import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Spin, Tag, Tooltip, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { DiffView } from "../../components/DiffView";
import { gitDiff, type GitDiffBase, type GitDiffFileStats, type GitDiffResponse, type GitStatusFile } from "../../lib/api";
import { classifyGitChange, hasStagedChange, hasUnstagedChange, useGitStatus, type GitChangeKind } from "./gitChanges";

/**
 * Git change surfaces for the Files page and dock.
 *
 * Both surfaces render these same components, so a change looks and behaves
 * identically in either place. Every baseline is stated in words next to the
 * diff: the one thing this UI must never do is let "unsaved edits vs disk"
 * pass for a Git diff.
 */

const KIND_COLOR: Record<GitChangeKind, string> = {
  added: "green",
  modified: "gold",
  deleted: "red",
  renamed: "blue",
  copied: "cyan",
  conflicted: "magenta",
  untracked: "default",
};

/** Single-letter porcelain badge, the convention developers already read. */
const KIND_LETTER: Record<GitChangeKind, string> = {
  added: "A",
  modified: "M",
  deleted: "D",
  renamed: "R",
  copied: "C",
  conflicted: "!",
  untracked: "U",
};

export type FilesGitChangesProps = {
  token: string | null;
  cwd: string;
  relayNode?: string;
  refreshKey: number;
  selectedPath?: string;
  /** Open a changed file in the editor. Deleted files have nothing to open. */
  onSelectPath: (path: string) => void;
  /** Bump the caller's refresh key. */
  onRefresh: () => void;
  t: (key: string, vars?: Record<string, string | number>) => string;
};

/**
 * The list of changed files. Renders one of four honest states: git
 * unavailable, no changes, failed request, or the list itself.
 */
export function FilesGitChanges(props: FilesGitChangesProps) {
  const { t, selectedPath, refreshKey } = props;
  const status = useGitStatus({
    token: props.token,
    root: props.cwd,
    relayNode: props.relayNode,
    refreshKey,
  });

  if (status.loading && !status.data) {
    return (
      <div style={{ display: "grid", placeItems: "center", padding: 24 }} data-testid="files-git-loading">
        <Spin size="small" />
      </div>
    );
  }

  if (status.error) {
    return (
      <div style={{ padding: 8 }} data-testid="files-git-error">
        <Alert
          type="warning"
          showIcon
          message={t("files.gitStatusFailed") || "Could not read Git status"}
          description={status.error}
          action={
            <Button size="small" icon={<ReloadOutlined />} onClick={props.onRefresh}>
              {t("files.refresh") || "Refresh"}
            </Button>
          }
        />
      </div>
    );
  }

  // Non-git workspace (or an SSH/relay workspace that cannot answer): say so
  // instead of showing an empty "no changes" list, which would be a lie.
  if (!status.data?.repo) {
    return (
      <div style={{ padding: 12 }} data-testid="files-git-not-a-repo">
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t("files.gitNotRepo") || "Not a Git repository — showing files only."}
        </Typography.Text>
      </div>
    );
  }

  if (status.data.error) {
    return (
      <div style={{ padding: 8 }} data-testid="files-git-error">
        <Alert type="warning" showIcon message={status.data.error} />
      </div>
    );
  }

  const files = status.data.files ?? [];

  if (files.length === 0) {
    return (
      <div style={{ padding: 12 }} data-testid="files-git-no-changes">
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t("files.gitNoChanges") || "No changes in the working tree."}
        </Typography.Text>
      </div>
    );
  }

  return (
    <div data-testid="files-git-changes" style={{ display: "flex", flexDirection: "column", gap: 1 }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 6,
          padding: "4px 8px",
          borderBottom: "1px solid var(--border)",
          fontSize: 11,
          color: "var(--godex-muted)",
        }}
      >
        <span>{t("files.gitChangedCount", { count: files.length }) || `${files.length} changed`}</span>
        {status.data.branch ? (
          <Tag style={{ marginLeft: "auto", fontFamily: "ui-monospace, monospace" }}>{status.data.branch}</Tag>
        ) : null}
      </div>
      {files.map((file) => (
        <GitChangeRow
          key={file.path}
          file={file}
          active={file.path === selectedPath}
          onSelect={() => props.onSelectPath(file.path)}
          t={t}
        />
      ))}
    </div>
  );
}

function GitChangeRow(props: { file: GitStatusFile; active: boolean; onSelect: () => void; t: FilesGitChangesProps["t"] }) {
  const { file } = props;
  const kind = classifyGitChange(file);
  // Deleted files have no content to open, but their diff is still readable.
  const selectable = kind !== "deleted";
  const staged = hasStagedChange(file);
  const unstaged = hasUnstagedChange(file);

  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`${file.path} (${kind})`}
      data-testid="files-git-change-row"
      data-path={file.path}
      data-kind={kind}
      onClick={() => props.onSelect()}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          props.onSelect();
        }
      }}
      style={{
        display: "flex",
        alignItems: "center",
        gap: 6,
        padding: "3px 8px",
        cursor: "pointer",
        fontSize: 12,
        background: props.active ? "var(--godex-accent-muted)" : "transparent",
      }}
    >
      <Tag color={KIND_COLOR[kind]} style={{ marginInlineEnd: 0, minWidth: 22, textAlign: "center" }}>
        {KIND_LETTER[kind]}
      </Tag>
      <Tooltip title={file.origPath ? `${file.origPath} → ${file.path}` : file.path}>
        <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", direction: "rtl", textAlign: "left" }}>
          {file.path}
        </span>
      </Tooltip>
      {/* Which side of the change exists. Kept explicit so "staged" is never implied. */}
      {staged ? <Tag color="blue" style={{ marginInlineEnd: 0 }}>{props.t("files.gitStaged") || "staged"}</Tag> : null}
      {unstaged ? <Tag style={{ marginInlineEnd: 0 }}>{props.t("files.gitUnstaged") || "unstaged"}</Tag> : null}
      {!selectable ? <span style={{ fontSize: 10, color: "var(--godex-muted)" }}>·</span> : null}
    </div>
  );
}

const BASE_OPTIONS: { value: GitDiffBase; key: string; fallback: string }[] = [
  { value: "head", key: "files.gitBaseHead", fallback: "HEAD → working tree" },
  { value: "worktree", key: "files.gitBaseWorktree", fallback: "index → working tree" },
  { value: "staged", key: "files.gitBaseStaged", fallback: "HEAD → index" },
];

export type GitDiffPaneProps = {
  token: string | null;
  path: string;
  cwd: string;
  relayNode?: string;
  base: GitDiffBase;
  onBaseChange: (base: GitDiffBase) => void;
  refreshKey: number;
  /** Line counts from /git/diff-stats, when already loaded. */
  stats?: GitDiffFileStats;
  t: (key: string, vars?: Record<string, string | number>) => string;
};

/**
 * The diff for one path against a stated baseline.
 *
 * This is the ONLY place the Files UI shows a Git diff, and it always names its
 * baseline. Unsaved editor content is a separate concern: the editor's own
 * "Diff" view compares against the on-disk file and is labelled as such.
 */
export function GitDiffPane(props: GitDiffPaneProps) {
  const { token, path, cwd, relayNode, base, refreshKey, t } = props;
  const [state, setState] = useState<{ loading: boolean; response?: GitDiffResponse; error?: string }>({
    loading: true,
  });

  useEffect(() => {
    if (!path) return;
    let cancelled = false;
    const controller = new AbortController();
    setState({ loading: true });
    gitDiff(token, path, cwd, controller.signal, base, relayNode)
      .then((response) => {
        if (cancelled) return;
        setState({ loading: false, response });
      })
      .catch((err: Error) => {
        if (cancelled || err?.name === "AbortError") return;
        setState({ loading: false, error: err?.message || "Failed to load diff" });
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
    // relayNode is passed to gitDiff so the diff is fetched from the same node
    // as the file reads; a change of relay target therefore refetches here.
  }, [token, path, cwd, base, refreshKey, relayNode]);
  const baselineLabel = useMemo(() => {
    const option = BASE_OPTIONS.find((entry) => entry.value === base);
    return option ? t(option.key) || option.fallback : base;
  }, [base, t]);

  if (state.loading) {
    return (
      <div style={{ display: "grid", placeItems: "center", padding: 24 }} data-testid="files-git-diff-loading">
        <Spin size="small" />
      </div>
    );
  }

  if (state.error) {
    return (
      <Alert
        type="error"
        showIcon
        style={{ margin: 8 }}
        message={t("files.gitDiffFailed") || "Could not load the diff"}
        description={state.error}
      />
    );
  }

  if (state.response && !state.response.repo) {
    return (
      <Alert
        type="info"
        showIcon
        style={{ margin: 8 }}
        message={t("files.gitNotRepo") || "Not a Git repository — showing files only."}
      />
    );
  }

  const response = state.response;
  const notice = response?.error
    ? `${t("files.gitDiffError") || "Git reported a problem"}: ${response.error}`
    : response?.truncated
      ? t("files.gitDiffTruncated") || "Diff is large and was truncated."
      : undefined;

  return (
    <div data-testid="files-git-diff" style={{ display: "flex", flexDirection: "column", minHeight: 0, height: "100%" }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 6,
          padding: "4px 8px",
          borderBottom: "1px solid var(--border)",
          flexWrap: "wrap",
        }}
      >
        <Typography.Text type="secondary" style={{ fontSize: 11 }}>
          {t("files.gitBaseline") || "Baseline"}
        </Typography.Text>
        <select
          aria-label={t("files.gitBaseline") || "Baseline"}
          data-testid="files-git-diff-base"
          value={base}
          onChange={(event) => props.onBaseChange(event.target.value as GitDiffBase)}
          style={{
            fontSize: 11,
            background: "transparent",
            color: "inherit",
            border: "1px solid var(--border)",
            borderRadius: 4,
            padding: "1px 4px",
          }}
        >
          {BASE_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {t(option.key) || option.fallback}
            </option>
          ))}
        </select>
        {response?.untracked ? <Tag>{t("files.gitUntrackedFile") || "untracked file"}</Tag> : null}
        {props.stats ? (
          <span style={{ marginLeft: "auto", fontSize: 11, fontFamily: "ui-monospace, monospace" }}>
            <span style={{ color: "#15803d" }}>+{props.stats.added}</span>{" "}
            <span style={{ color: "#b91c1c" }}>−{props.stats.deleted}</span>
          </span>
        ) : null}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
        <DiffView
          diff={response?.diff ?? ""}
          showLineNumbers
          baselineLabel={baselineLabel}
          notice={notice}
          maxLines={2000}
        />
      </div>
    </div>
  );
}
