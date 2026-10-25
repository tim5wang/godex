import { describe, expect, it } from "vitest";
import { reconcileRelayUsage } from "./reconciliation";
import type { RelayUsageSummary } from "../../../lib/apiMetering";

function row(tenantID: string, requests: number): RelayUsageSummary {
  return { tenant_id: tenantID, requests, request_bytes: 0, response_bytes: 0 };
}

describe("reconcileRelayUsage", () => {
  it("accepts a total equal to the sum of its rows", () => {
    const result = reconcileRelayUsage([row("t1", 2), row("t2", 3)], row("", 5));
    expect(result).toEqual({ summed: 5, total: 5, reconciles: true });
  });

  // The case the surface exists for: traffic recorded that no tenant owns (the
  // operator's own direct usage) makes the total exceed the rows.
  it("flags a total that exceeds the rows", () => {
    const result = reconcileRelayUsage([row("t1", 2)], row("", 4));
    expect(result.reconciles).toBe(false);
    expect(result.total).toBe(4);
    expect(result.summed).toBe(2);
  });

  // An unscoped bucket is still a row, so it must be included in the sum: the
  // mismatch signal is about rows that are MISSING, not about rows with no tenant.
  it("counts the unscoped bucket as a row", () => {
    const result = reconcileRelayUsage([row("t1", 2), row("", 3)], row("", 5));
    expect(result.reconciles).toBe(true);
    expect(result.summed).toBe(5);
  });

  it("treats no rows and no total as reconciled at zero", () => {
    expect(reconcileRelayUsage([], row("", 0))).toEqual({ summed: 0, total: 0, reconciles: true });
  });

  // A missing total must fail the check rather than read as "adds up": a partial
  // response is not evidence of correctness.
  it("does not report reconciliation when the total is absent", () => {
    const result = reconcileRelayUsage([row("t1", 2)], undefined);
    expect(result.reconciles).toBe(false);
    expect(result.summed).toBe(2);
  });

  // Nil counters can arrive from a window with no traffic; they must not poison
  // the arithmetic into NaN.
  it("treats missing counters as zero", () => {
    const partial = [{ tenant_id: "t1" } as RelayUsageSummary];
    const result = reconcileRelayUsage(partial, row("", 0));
    expect(result.summed).toBe(0);
    expect(Number.isNaN(result.summed)).toBe(false);
    expect(result.reconciles).toBe(true);
  });
});
