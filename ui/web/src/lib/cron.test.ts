import { describe, expect, it } from "vitest";
import {
  cronBreakdown,
  cronDescribe,
  cronNextRuns,
  cronValidate,
} from "./cron";

describe("cronValidate", () => {
  it("accepts valid 5-field standard expressions", () => {
    expect(cronValidate("0 3 * * *")).toEqual({ valid: true });
    expect(cronValidate("*/15 * * * *")).toEqual({ valid: true });
    expect(cronValidate("0 9 * * 1-5")).toEqual({ valid: true });
  });

  it("rejects empty string", () => {
    expect(cronValidate("").valid).toBe(false);
    expect(cronValidate("   ").valid).toBe(false);
  });

  it("rejects non-5-field expressions", () => {
    expect(cronValidate("0 3").valid).toBe(false);
    expect(cronValidate("0 3 * *").valid).toBe(false);
    expect(cronValidate("* * * * * *").valid).toBe(false);
  });

  it("rejects out-of-range fields", () => {
    expect(cronValidate("99 * * * *").valid).toBe(false);
  });

  it("accepts @macros", () => {
    expect(cronValidate("@daily").valid).toBe(true);
    expect(cronValidate("@weekly").valid).toBe(true);
  });
});

describe("cronDescribe", () => {
  it("describes expressions in zh", () => {
    expect(cronDescribe("0 9 * * 1-5", "zh")).toContain("星期");
  });

  it("describes expressions in en", () => {
    expect(cronDescribe("0 9 * * 1-5", "en")).toContain("Monday");
  });

  it("returns empty for empty input", () => {
    expect(cronDescribe("", "en")).toBe("");
  });
});

describe("cronBreakdown", () => {
  it("splits fields of a 5-field expression", () => {
    const b = cronBreakdown("5 4 * * 1-5");
    expect(b).toEqual({
      minute: "5",
      hour: "4",
      dayOfMonth: "*",
      month: "*",
      dayOfWeek: "1-5",
    });
  });

  it("returns null for non-5-field or @macros", () => {
    expect(cronBreakdown("@daily")).toBeNull();
    expect(cronBreakdown("0 3")).toBeNull();
    expect(cronBreakdown("")).toBeNull();
  });
});

describe("cronNextRuns", () => {
  it("computes next runs", () => {
    const runs = cronNextRuns("0 3 * * *", 2);
    expect(runs.length).toBeGreaterThanOrEqual(1);
  });

  it("returns [] for invalid input", () => {
    expect(cronNextRuns("bad expr", 2)).toEqual([]);
    expect(cronNextRuns("", 2)).toEqual([]);
  });
});
