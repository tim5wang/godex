import { describe, expect, it } from "vitest";
import {
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
