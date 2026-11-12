import { describe, expect, it } from "vitest";
import {
  classifyGitChange,
  defaultDiffBase,
  hasStagedChange,
  hasUnstagedChange,
  isDeletedChange,
} from "./gitChanges";
import type { GitStatusFile } from "../../lib/api";

function file(partial: Partial<GitStatusFile>): GitStatusFile {
  return { path: "a.go", index: " ", worktree: " ", ...partial };
}

describe("classifyGitChange", () => {
  it("reads the porcelain columns, not a trimmed string", () => {
    // Regression: trimming porcelain output turned " M" into "M " and made an
    // unstaged edit look staged.
    const unstaged = file({ index: " ", worktree: "M" });
    expect(hasStagedChange(unstaged)).toBe(false);
    expect(hasUnstagedChange(unstaged)).toBe(true);
    expect(classifyGitChange(unstaged)).toBe("modified");
  });

  it("classifies a staged modification", () => {
    const staged = file({ index: "M", worktree: " " });
    expect(hasStagedChange(staged)).toBe(true);
    expect(hasUnstagedChange(staged)).toBe(false);
    expect(classifyGitChange(staged)).toBe("modified");
  });

  it("classifies a staged addition as added", () => {
    expect(classifyGitChange(file({ index: "A", worktree: " " }))).toBe("added");
  });

  it("classifies deletions on either side", () => {
    expect(classifyGitChange(file({ index: "D", worktree: " " }))).toBe("deleted");
    expect(classifyGitChange(file({ index: " ", worktree: "D" }))).toBe("deleted");
    expect(isDeletedChange(file({ index: "D", worktree: " " }))).toBe(true);
  });

  it("classifies renames and copies", () => {
    expect(classifyGitChange(file({ index: "R", worktree: " ", origPath: "b.go" }))).toBe("renamed");
    expect(classifyGitChange(file({ index: "C", worktree: " ", origPath: "b.go" }))).toBe("copied");
  });

  it("classifies untracked files", () => {
    const untracked = file({ index: "?", worktree: "?", untracked: true });
    expect(classifyGitChange(untracked)).toBe("untracked");
    // An untracked file is never "staged".
    expect(hasStagedChange(untracked)).toBe(false);
    expect(hasUnstagedChange(untracked)).toBe(true);
  });

  it("classifies merge conflicts ahead of ordinary edits", () => {
    // "UU" would otherwise read as modified; a conflict needs its own badge.
    expect(classifyGitChange(file({ index: "U", worktree: "U" }))).toBe("conflicted");
    expect(classifyGitChange(file({ index: "A", worktree: "A" }))).toBe("conflicted");
  });
});

describe("defaultDiffBase", () => {
  it("uses the staged baseline for a staged-only change", () => {
    // A staged-only edit produces an EMPTY worktree diff, so defaulting to
    // "worktree" would show an empty pane for a file that did change.
    expect(defaultDiffBase(file({ index: "M", worktree: " " }))).toBe("staged");
  });

  it("uses the HEAD baseline for an unstaged-only change", () => {
    expect(defaultDiffBase(file({ index: " ", worktree: "M" }))).toBe("head");
  });

  it("uses the HEAD baseline when both sides changed", () => {
    expect(defaultDiffBase(file({ index: "M", worktree: "M" }))).toBe("head");
  });

  it("keeps an untracked file on the worktree route", () => {
    // Untracked diffs come from --no-index, which the worktree route triggers.
    expect(defaultDiffBase(file({ index: "?", worktree: "?", untracked: true }))).toBe("worktree");
  });
});
