import type { WorkCheckpoint, WorkDelegation, WorkEvent, WorkRelation, WorkSessionLink } from "../../lib/apiWorkContext";

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

// ---------------------------------------------------------------------------
// Rail status
//
// A rail card used to show only the stream's own state (active/paused/archived) plus a
// bare revision, so all the work happening in a stream's CONVERSATIONS was invisible
// unless you opened it. These helpers derive what a card can honestly say about that
// work from server facts already fetched — no new model, no guessing. The rule
// throughout: a claim is only made when the data proves it.
// ---------------------------------------------------------------------------

/** The server's own turn outcome, read off the recorded event title ("<title> · <status>"). */
export type RailTurnStatus = "completed" | "error" | "canceled" | "interrupted" | "unknown";

export type RailStatusKind = "running" | "finished" | "error" | "idle";

export interface RailStatus {
  kind: RailStatusKind;
  /** The linked conversation the status is about, when there is one. */
  sessionId: string;
  /** When it last moved (RFC3339 as stored), for the tooltip. */
  at: string;
}

/**
 * turnStatusFromTitle splits the status back out of a recorded turn event's title.
 *
 * The turn event's title is "<conversation> · <status>"; the status suffix is the only
 * place the outcome is recorded, and an unrecognised suffix must degrade to "unknown"
 * rather than being reported as a success — a card that shows a tick for a turn that
 * actually failed is worse than one that shows nothing.
 */
export function turnStatusFromTitle(title: string | undefined): RailTurnStatus {
  const text = (title ?? "").trim();
  const at = text.lastIndexOf(" · ");
  if (at < 0) return "unknown";
  const suffix = text.slice(at + 3).trim();
  switch (suffix) {
    case "completed":
      return "completed";
    case "error":
      return "error";
    case "canceled":
      return "canceled";
    case "interrupted":
      return "interrupted";
    default:
      return "unknown";
  }
}

/**
 * railStatus decides one word for a stream's conversation work, in priority order:
 * running > error > finished > idle.
 *
 * Running wins because a stream that is working right now is the one fact the user must
 * not miss. Error outranks a clean finish because it needs the same slot and is the more
 * urgent of the two. "Finished" requires BOTH a completed turn newer than the seen
 * watermark AND a linked conversation — without the watermark check a stream would claim
 * "finished" forever after its first turn, and without a link the event would be
 * attributable to no conversation. An archived stream reports nothing: it is read-only,
 * so "running" cannot be true of it.
 */
export function railStatus(input: {
  status: string | undefined;
  sessions: WorkSessionLink[];
  lastTurn: WorkEvent | undefined;
  seenAt: string | undefined;
  runningSessionIds: ReadonlySet<string>;
}): RailStatus {
  const { status, sessions, lastTurn, seenAt, runningSessionIds } = input;
  const links = sessions ?? [];
  if (isReadOnly(status) || links.length === 0) {
    return { kind: "idle", sessionId: "", at: "" };
  }
  const running = links.find((link) => runningSessionIds.has(link.session_id));
  if (running) {
    return { kind: "running", sessionId: running.session_id, at: "" };
  }
  if (!lastTurn) {
    return { kind: "idle", sessionId: "", at: "" };
  }
  // The event is owned by the stream, but the card must name the conversation it is
  // about, so it is matched by session id; an event for an unlinked session is ignored
  // rather than mis-attributed to a link that happens to exist.
  const owner = lastTurn.source.startsWith("session://") ? lastTurn.source.slice("session://".length) : "";
  const link = links.find((item) => item.session_id === owner);
  if (!link) {
    return { kind: "idle", sessionId: "", at: "" };
  }
  const outcome = turnStatusFromTitle(lastTurn.title);
  if (outcome === "error") {
    return { kind: "error", sessionId: link.session_id, at: lastTurn.created_at };
  }
  if (outcome !== "completed") {
    return { kind: "idle", sessionId: "", at: "" };
  }
  // ISO-8601 UTC strings compare correctly as text, which is exactly how the server
  // compares them; an empty watermark means "everything is new" (never marked seen).
  if ((seenAt ?? "") >= lastTurn.created_at) {
    return { kind: "idle", sessionId: "", at: lastTurn.created_at };
  }
  return { kind: "finished", sessionId: link.session_id, at: lastTurn.created_at };
}

// ---------------------------------------------------------------------------
// Rail card aggregates
//
// The card's second line used to be `checkpoint.summary`, i.e. free prose. The card clamps
// it to two lines, so what actually reached the eye was an arbitrary PREFIX of a long
// paragraph ("下一步建议…") — a dozen characters that answered no question. Counts answer
// the question the list exists to answer — which stream is waiting on me, and for what —
// at a fixed width that cannot degrade with the length of someone's prose.
// ---------------------------------------------------------------------------

/** What a card counts. Two DISJOINT populations, which is the whole point: see below. */
export type RailAggregateKey = "attention" | "next";

export interface RailAggregate {
  key: RailAggregateKey;
  count: number;
}

/**
 * railAggregates turns a stream's outstanding work into counts for its rail card.
 *
 * Two chips only, and they are deliberately DISJOINT populations:
 *
 *   attention — what needs the user, from the cross-stream inbox response.
 *   next      — the checkpoint's forward-looking next actions.
 *
 * The checkpoint's blockers and open questions are NOT separate chips even though they are
 * easy counts to add. The inbox ALREADY includes them (kinds `checkpoint_blocker` and
 * `checkpoint_question`), so rendering "待处理 3 · 阻塞 1 · 待确认 1" would count the same
 * entry twice and leave the reader adding the row up wrong — the exact unreadability this
 * line replaced prose to avoid. The disjointness is what makes the row trustworthy.
 *
 * Zero counts are DROPPED rather than rendered as 0: a row of zeroes would make every calm
 * stream look like it has a dashboard, and the card only has room for what is real.
 *
 * `attentionCount` is passed in because it comes from the inbox rather than the checkpoint:
 * the inbox counts ACTIONABLE items and must stay deterministic, so this function never
 * derives one from the other.
 */
export function railAggregates(input: {
  checkpoint: Pick<WorkCheckpoint, "next_actions"> | undefined;
  attentionCount?: number;
}): RailAggregate[] {
  const candidates: RailAggregate[] = [
    { key: "attention", count: input.attentionCount ?? 0 },
    { key: "next", count: input.checkpoint?.next_actions?.length ?? 0 },
  ];
  return candidates.filter((item) => item.count > 0);
}

/** The conversation a stream should open when it is selected in the rail. */
export function mostRecentlyUsedConversation(
  sessions: WorkSessionLink[],
  activityBySessionId: Record<string, string>,
): WorkSessionLink | undefined {
  if (!sessions?.length) return undefined;
  return [...sessions].sort((left, right) => {
    const leftAt = activityBySessionId[left.session_id] ?? "";
    const rightAt = activityBySessionId[right.session_id] ?? "";
    if (leftAt !== rightAt) return rightAt.localeCompare(leftAt);
    // Deterministic tiebreak so the choice cannot flicker between renders.
    return left.id.localeCompare(right.id);
  })[0];
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
