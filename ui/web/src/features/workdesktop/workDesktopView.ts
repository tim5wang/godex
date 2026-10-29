import type { WorkDelegation, WorkRelation } from "../../lib/apiWorkContext";

// Pure helpers for the Work Desktop page. Kept separate from the components so the
// rules the design calls out (read-only when archived, http(s)-only external links,
// line-list normalization, dependency direction) are unit-testable without a DOM.

/** splitLines normalizes a textarea into the array shape the API expects: trimmed,
 *  non-empty, order preserved. An empty textarea becomes an empty array, not [""]. */
export function splitLines(value: string | undefined): string[] {
  return (value ?? "")
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

/** joinLines is the inverse, used to prefill a form from a stored list. */
export function joinLines(value: string[] | undefined): string {
  return (value ?? []).join("\n");
}

/** Only http(s) locators may be opened by the browser. A directory path, an
 *  artifact:// or a session:// reference is display/copy only: the desktop must not
 *  turn a stored reference into a fetch or a local directory read (design §3.3). */
export function isExternalLink(locator: string | undefined): boolean {
  return /^https?:\/\//i.test((locator ?? "").trim());
}

/** An archived stream is read-only until explicitly restored (design §3.1). */
export function isReadOnly(status: string | undefined): boolean {
  return status === "archived";
}

/** otherSide returns the id at the far end of a relation, so the sidebar can name the
 *  other stream without assuming the current one is the source. */
export function otherSide(relation: WorkRelation, contextId: string): string {
  return relation.from_id === contextId ? relation.to_id : relation.from_id;
}

/** Text matching over the fields a person would actually type. A whitespace-only query
 *  matches everything rather than nothing. */
export function filterContexts<T extends { title: string; background: string; goal: string }>(contexts: T[], search: string): T[] {
  const needle = search.trim().toLocaleLowerCase();
  if (!needle) return contexts;
  return contexts.filter((item) => `${item.title} ${item.background} ${item.goal}`.toLocaleLowerCase().includes(needle));
}

/** delegationsNeedingReview counts cards sitting in review, for the header hint. It
 *  reads the live card, so a delegation whose card is gone is never counted. */
export function delegationsNeedingReview(delegations: WorkDelegation[]): number {
  return delegations.filter((delegation) => delegation.card?.status === "in_review").length;
}

/** A delegation row must distinguish "card present", "card gone" and "no card yet".
 *  A missing card is never rendered as progress; it is an explicit source-missing state. */
export function delegationState(delegation: WorkDelegation): "card" | "missing" | "unlinked" {
  if (delegation.card) return "card";
  if (delegation.card_id || delegation.source_missing) return "missing";
  return "unlinked";
}

/** formatTimestamp renders a stored RFC3339 UTC value for display without lying about
 *  the zone: the value is UTC, so it is labelled as such and never re-localized. */
export function formatTimestamp(value: string | undefined): string {
  const trimmed = (value ?? "").trim();
  if (!trimmed) return "";
  return trimmed.replace("T", " ").replace(/Z$/, " UTC");
}
