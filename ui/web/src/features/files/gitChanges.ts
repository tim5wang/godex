import { useEffect, useRef, useState } from "react";
import { gitStatus, type GitDiffBase, type GitStatusFile, type GitStatusResponse } from "../../lib/api";

/**
 * Read-only Git state for the Files surfaces.
 *
 * Everything here is derived from `git status --porcelain=v1` through the
 * existing read-only /git/status route. Nothing in this module (or its
 * callers) stages, checks out, resets or otherwise writes to the repository —
 * the Files UI is a viewer of the working tree, not a Git client.
 */

export type GitChangeKind = "added" | "modified" | "deleted" | "renamed" | "copied" | "conflicted" | "untracked";

/** Index/worktree tags that mean a merge conflict rather than an ordinary edit. */
const CONFLICT_TAGS = new Set(["DD", "AU", "UD", "UA", "DU", "AA", "UU"]);

/** Classify one porcelain entry into the badge the UI shows. */
export function classifyGitChange(file: GitStatusFile): GitChangeKind {
  if (file.untracked || (file.index === "?" && file.worktree === "?")) {
    return "untracked";
  }
  const pair = `${file.index}${file.worktree}`;
  if (CONFLICT_TAGS.has(pair)) {
    return "conflicted";
  }
  if (file.index === "R" || file.worktree === "R") return "renamed";
  if (file.index === "C" || file.worktree === "C") return "copied";
  if (file.index === "D" || file.worktree === "D") return "deleted";
  if (file.index === "A") return "added";
  return "modified";
}

/** True when some part of the change is already in the index. */
export function hasStagedChange(file: GitStatusFile): boolean {
  return file.untracked ? false : file.index !== " " && file.index !== "?";
}

/** True when the working tree has changes git has not staged (or new files it has never seen). */
export function hasUnstagedChange(file: GitStatusFile): boolean {
  if (file.untracked) return true;
  return file.worktree !== " " && file.worktree !== "?";
}

/**
 * The baseline that actually shows this file's change.
 *
 * A staged-only edit is invisible in the worktree baseline and an untracked
 * file is invisible everywhere except --no-index, so each kind gets the
 * baseline that yields a non-empty diff by default. The user can still switch,
 * and the pane always states which baseline is being shown.
 */
export function defaultDiffBase(file: GitStatusFile): GitDiffBase {
  if (file.untracked) return "worktree";
  const staged = hasStagedChange(file);
  const unstaged = hasUnstagedChange(file);
  if (staged && !unstaged) return "staged";
  // Both sides changed, or unstaged only: compare with HEAD so the whole
  // change since the last commit is visible in one diff.
  return "head";
}

/** Paths in `files` that no longer exist on disk, so the editor must not try to read them. */
export function isDeletedChange(file: GitStatusFile): boolean {
  return classifyGitChange(file) === "deleted";
}

export interface GitStatusState {
  loading: boolean;
  /** Response from /git/status, or undefined before the first load resolves. */
  data?: GitStatusResponse;
  /** Set when the request itself failed (network/auth), not for non-git dirs. */
  error?: string;
}

/**
 * Load git status for a workspace root.
 *
 * `root` is the same value the file routes use, so a non-git directory, an SSH
 * workspace or a relay node all degrade to `repo: false` instead of erroring.
 * Re-fetches whenever `refreshKey` changes, which is what the refresh button,
 * a save and a successful write all bump.
 */
export function useGitStatus(options: {
  token: string | null;
  root: string;
  relayNode?: string;
  refreshKey?: number;
  enabled?: boolean;
}): GitStatusState {
  const { token, root, relayNode, refreshKey = 0, enabled = true } = options;
  const [state, setState] = useState<GitStatusState>({ loading: enabled });

  // Guard against a slow response for a previous root landing after the user
  // switched workspaces: only the newest request may write state.
  const requestIdRef = useRef(0);

  useEffect(() => {
    if (!enabled) {
      setState({ loading: false });
      return;
    }
    const requestId = ++requestIdRef.current;
    const controller = new AbortController();
    setState((prev) => ({ ...prev, loading: true, error: undefined }));

    gitStatus(token, root, controller.signal, relayNode)
      .then((data) => {
        if (requestId !== requestIdRef.current || controller.signal.aborted) return;
        setState({ loading: false, data });
      })
      .catch((err: Error) => {
        if (requestId !== requestIdRef.current || controller.signal.aborted) return;
        if (err?.name === "AbortError") return;
        setState({ loading: false, error: err?.message || "Failed to load git status" });
      });

    return () => controller.abort();
  }, [token, root, relayNode, refreshKey, enabled]);

  return state;
}
