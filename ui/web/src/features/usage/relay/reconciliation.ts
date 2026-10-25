import type { RelayUsageSummary } from "../../../lib/apiMetering";

export interface RelayReconciliation {
  /** summed is the total request count across the per-tenant rows. */
  summed: number;
  /** total is what the server reported as the grand total. */
  total: number;
  /** reconciles is true when the total equals the sum of its rows. */
  reconciles: boolean;
}

/** reconcileRelayUsage compares the reported grand total with the sum of the
 *  per-tenant rows (design §7, P3 acceptance "metering can be reconciled").
 *
 *  Extracted from the card so the rule is unit-testable and stated once. It is the
 *  whole point of the reconciliation surface: a total that does not equal its rows
 *  means traffic was recorded that no tenant owns — normally the platform
 *  operator's own direct usage, which is exactly the gap the surface exists to make
 *  visible rather than to hide.
 *
 *  An absent total is treated as NOT reconciled: an older or partial response must
 *  not read as "everything adds up". */
export function reconcileRelayUsage(
  rows: RelayUsageSummary[],
  total?: RelayUsageSummary,
): RelayReconciliation {
  const summed = rows.reduce((sum, row) => sum + (row.requests ?? 0), 0);
  const reported = total?.requests ?? 0;
  return {
    summed,
    total: reported,
    reconciles: total !== undefined && reported === summed,
  };
}
