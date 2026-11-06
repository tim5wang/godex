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

// ---------------------------------------------------------------------------
// Acceptance package
//
// The delegated card already carries everything needed to decide "do I accept
// this?" — the executor writes its summary, the checklist records a per-item
// evidence string, and gate 3 records the paths the run actually reported. The
// desktop nevertheless rendered only the bare criterion text, which is why
// accepting meant opening the execution transcript. This assembles the card's
// existing fields into the one thing a person needs, and keeps the honest
// distinction the ledger makes: DECLARED paths (gate 1, unverified) are never
// presented as OBSERVED ones (gate 3, reported by the run).
// ---------------------------------------------------------------------------

/** The subset of a taskboard card the acceptance package reads. Structural on
 *  purpose so this stays unit-testable without fabricating a whole card. */
export interface AcceptanceCard {
  description?: string;
  checklist?: { text: string; done: boolean; evidence?: string }[];
  observed_paths?: string[];
  touched_paths?: string[];
  research?: { open_questions?: string[] };
  merge_report?: { conflicts?: { path: string; other_path?: string; other_card?: string; other_title?: string }[] };
  executions?: { summary?: string }[];
}

export interface AcceptanceCheck {
  text: string;
  done: boolean;
  evidence: string;
}

export interface AcceptanceConflict {
  path: string;
  /** The other card involved, named by whichever label the ledger has. */
  other: string;
}

export interface AcceptancePackage {
  /** What was done: the latest execution summary, falling back to the brief. */
  summary: string;
  /** Paths the run REPORTED touching (gate 3). */
  observedPaths: string[];
  /** Paths only DECLARED (gate 1) — a claim, so shown apart from observed ones. */
  declaredPaths: string[];
  /** How it was verified: the acceptance criteria with their recorded evidence. */
  checks: AcceptanceCheck[];
  /** What is still open, from the card's own research asset. */
  open: string[];
  /** Cross-card conflicts detected when the card entered review. */
  conflicts: AcceptanceConflict[];
  /** Whether any completed check recorded an evidence string. A ticked box with
   *  no evidence is a CLAIM, not proof, so it deliberately does not count. */
  hasEvidence: boolean;
}

/** acceptancePackage assembles the structured self-check for one delegated card. */
export function acceptancePackage(card: AcceptanceCard | undefined | null): AcceptancePackage {
  const empty: AcceptancePackage = {
    summary: "", observedPaths: [], declaredPaths: [], checks: [], open: [], conflicts: [], hasEvidence: false,
  };
  if (!card) return empty;

  // Observed wins over declared when a path appears in both: the run's own report
  // is the stronger fact, and showing it twice would double-count the surface.
  const observedPaths = (card.observed_paths ?? []).filter(Boolean);
  const observed = new Set(observedPaths);
  const declaredPaths = (card.touched_paths ?? []).filter((path) => path && !observed.has(path));

  const checks = (card.checklist ?? []).map((item) => ({
    text: item.text,
    done: item.done,
    evidence: (item.evidence ?? "").trim(),
  }));

  // No executions means no run to speak for: leaving the summary empty is more
  // honest than rendering the brief as if it were a result.
  let summary = "";
  for (const execution of card.executions ?? []) {
    const text = (execution.summary ?? "").trim();
    if (text) summary = text;
  }

  return {
    summary,
    observedPaths,
    declaredPaths,
    checks,
    open: (card.research?.open_questions ?? []).filter(Boolean),
    conflicts: (card.merge_report?.conflicts ?? []).map((conflict) => ({
      path: conflict.path,
      other: conflict.other_title || conflict.other_card || conflict.other_path || "",
    })),
    hasEvidence: checks.some((check) => check.done && check.evidence),
  };
}
