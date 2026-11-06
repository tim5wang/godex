import { describe, expect, it } from "vitest";
import {
  acceptancePackage,
  delegationState,
  delegationsNeedingReview,
  filterContexts,
  formatTimestamp,
  isExternalLink,
  isReadOnly,
  joinLines,
  otherSide,
  splitLines,
} from "./workDesktopView";
import type { WorkDelegation, WorkRelation } from "../../lib/apiWorkContext";

function relation(from: string, to: string): WorkRelation {
  return { id: `${from}-${to}`, from_id: from, to_id: to, kind: "related_to", condition: "", created_at: "" };
}

function delegation(overrides: Partial<WorkDelegation>): WorkDelegation {
  return { id: "d1", request_id: "r1", card_id: "", project_id: "", title: "t", pack: "", state: "reserved", created_at: "", ...overrides };
}

describe("work desktop view helpers", () => {
  it("normalizes line lists and round-trips them", () => {
    expect(splitLines(" A\n \nB \r\n")).toEqual(["A", "B"]);
    expect(splitLines("")).toEqual([]);
    expect(splitLines(undefined)).toEqual([]);
    expect(joinLines(["A", "B"])).toBe("A\nB");
    expect(joinLines(undefined)).toBe("");
    expect(splitLines(joinLines(["x", "y"]))).toEqual(["x", "y"]);
  });

  it("only treats http(s) locators as openable, so a path is never fetched or read", () => {
    expect(isExternalLink("https://example.com/runbook")).toBe(true);
    expect(isExternalLink("HTTP://example.com")).toBe(true);
    expect(isExternalLink("/srv/app")).toBe(false);
    expect(isExternalLink("artifact://demo/report")).toBe(false);
    expect(isExternalLink("session://demo/x")).toBe(false);
    expect(isExternalLink("javascript:alert(1)")).toBe(false);
    expect(isExternalLink(undefined)).toBe(false);
  });

  it("treats only archived streams as read-only", () => {
    expect(isReadOnly("archived")).toBe(true);
    expect(isReadOnly("active")).toBe(false);
    expect(isReadOnly("paused")).toBe(false);
    expect(isReadOnly(undefined)).toBe(false);
  });

  it("resolves the far end of a relation from either side", () => {
    expect(otherSide(relation("a", "b"), "a")).toBe("b");
    expect(otherSide(relation("a", "b"), "b")).toBe("a");
  });

  it("searches title, background and goal, and treats a blank query as a match-all", () => {
    const contexts = [
      { title: "Speech service", background: "ASR operations", goal: "traceable releases" },
      { title: "Alignment", background: "Indonesian quality", goal: "reproducible deltas" },
    ];
    expect(filterContexts(contexts, "  ")).toHaveLength(2);
    expect(filterContexts(contexts, "indonesian").map((c) => c.title)).toEqual(["Alignment"]);
    expect(filterContexts(contexts, "traceable").map((c) => c.title)).toEqual(["Speech service"]);
    expect(filterContexts(contexts, "absent")).toEqual([]);
  });

  it("counts only live in-review cards and separates missing cards from unlinked delegations", () => {
    const cards = [
      delegation({ id: "live", card_id: "c1", card: { status: "in_review" } as never }),
      delegation({ id: "running", card_id: "c2", card: { status: "in_progress" } as never }),
      delegation({ id: "gone", card_id: "c3", source_missing: true }),
      delegation({ id: "none" }),
    ];
    expect(delegationsNeedingReview(cards)).toBe(1);
    expect(delegationState(cards[0])).toBe("card");
    expect(delegationState(cards[2])).toBe("missing");
    expect(delegationState(cards[3])).toBe("unlinked");
  });

  it("formats stored UTC timestamps without shifting the zone", () => {
    expect(formatTimestamp("2026-10-30T09:20:00Z")).toBe("2026-10-30 09:20:00 UTC");
    expect(formatTimestamp("")).toBe("");
    expect(formatTimestamp(undefined)).toBe("");
  });
});

describe("acceptance package", () => {
  it("assembles the card's own fields instead of the bare criterion text", () => {
    const pkg = acceptancePackage({
      observed_paths: ["internal/core/flow"],
      touched_paths: ["internal/core/flow", "internal/services/backend"],
      checklist: [
        { text: "能从部署记录定位回滚目标", done: true, evidence: "go test ./internal/core/flow/... ok" },
        { text: "写回滚手册", done: false },
      ],
      research: { open_questions: ["灰度窗口多长"] },
      merge_report: { conflicts: [{ path: "internal/services/backend", other_card: "card-9", other_title: "并发卡" }] },
      executions: [{ summary: "补齐回滚路径并验证" }],
    });
    expect(pkg.summary).toBe("补齐回滚路径并验证");
    expect(pkg.observedPaths).toEqual(["internal/core/flow"]);
    expect(pkg.declaredPaths).toEqual(["internal/services/backend"]);
    expect(pkg.checks).toHaveLength(2);
    expect(pkg.open).toEqual(["灰度窗口多长"]);
    expect(pkg.conflicts).toEqual([{ path: "internal/services/backend", other: "并发卡" }]);
    expect(pkg.hasEvidence).toBe(true);
  });

  it("never presents a claim as proof: a tick without evidence does not count", () => {
    const pkg = acceptancePackage({
      checklist: [{ text: "已测试", done: true }, { text: "未测试", done: false }],
    });
    expect(pkg.checks[0]!.evidence).toBe("");
    // The whole point of the panel: nothing here is checkable, and it says so.
    expect(pkg.hasEvidence).toBe(false);
  });

  it("does not count an UNCHECKED item's evidence, and never duplicates a path", () => {
    const pkg = acceptancePackage({
      // Evidence on an unfinished item is a note, not a verification result.
      checklist: [{ text: "进行中", done: false, evidence: "部分输出" }],
      touched_paths: ["a", "b"],
      observed_paths: ["b"],
    });
    expect(pkg.hasEvidence).toBe(false);
    // Observed wins over declared: showing both would double-count the surface.
    expect(pkg.observedPaths).toEqual(["b"]);
    expect(pkg.declaredPaths).toEqual(["a"]);
  });

  it("keeps the summary empty when no execution ever ran", () => {
    // The frozen brief must not be dressed up as a result.
    const pkg = acceptancePackage({ checklist: [{ text: "criteria", done: false }] });
    expect(pkg.summary).toBe("");
    expect(acceptancePackage(undefined).summary).toBe("");
    expect(acceptancePackage(null).checks).toEqual([]);
  });

  it("takes the LATEST non-empty execution summary as the outcome", () => {
    const pkg = acceptancePackage({ executions: [{ summary: "第一次" }, { summary: "  " }, { summary: "第二次" }] });
    expect(pkg.summary).toBe("第二次");
  });

  it("names a conflict by whichever label the ledger has", () => {
    const pkg = acceptancePackage({
      merge_report: { conflicts: [{ path: "p1", other_card: "card-2" }, { path: "p2", other_path: "p3" }] },
    });
    expect(pkg.conflicts).toEqual([{ path: "p1", other: "card-2" }, { path: "p2", other: "p3" }]);
  });
});
