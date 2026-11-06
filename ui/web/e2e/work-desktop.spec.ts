import { test, expect, type Page, type Route } from "@playwright/test";
import { mockBackend } from "./fixtures";

// The Work Desktop is a production page backed by /v1/work-contexts. These tests drive a
// stateful in-memory mock of the documented contract so the UI's real request flow is
// exercised (create/patch/checkpoint/resources/relations/sessions/facts/delegations) —
// not a client-side demo store. The store starts EMPTY by default: a fresh install must
// show the empty state, and every list below is produced by the API, never seeded by the
// page itself. Seeds exist only as test fixtures for tests that need an existing stream.

type Ctx = {
  id: string; title: string; background: string; goal: string; status: string; version: number;
  checkpoint: { summary: string; completed: string[]; in_progress: string[]; next_actions: string[]; blockers: string[]; open_questions: string[]; evidence_refs: string[]; revision: number; created_at: string };
  created_at: string; updated_at: string;
};
type Res = { id: string; title: string; kind: string; locator: string; node_id: string; purpose: string; context_ids: string[]; created_at: string };
type Rel = { id: string; from_id: string; to_id: string; kind: string; condition: string; created_at: string };
type Fact = { id: string; context_id: string; title: string; content: string; kind: string; status: string; scope: string; subject: string; predicate: string; source: string; source_excerpt: string; source_revision: string; observed_at: string; verified_at: string; provenance: string; supersedes_id: string; version: number; created_at: string; memory_candidate_id: string };
type Link = { id: string; session_id: string; topic: string; created_at: string };
type Card = { id: string; project_id: string; title: string; status: string; urgency: string; version: number; checklist: { text: string; done: boolean }[]; created_at: string };
type Event = { id: string; context_id: string; kind: string; title: string; source: string; created_at: string };
type Delegation = { id: string; request_id: string; card_id: string; project_id: string; title: string; pack: string; state: string; created_at: string };
type Todo = { id: string; context_id: string; text: string; status: string; owner: string; due_at: string; source: string; position: number; version: number; created_at: string; updated_at: string };
type Queued = { id: string; mode: string; status: string; source: string; sender: string; summary: string; created_at: string; updated_at: string };
// The owning stream travels alongside the API shape so the mock can scope a delegation to
// one context, mirroring the real store's context_id column.
type StoredDelegation = Delegation & { ctx: string };
const delegationCtx = (delegation: Delegation) => (delegation as StoredDelegation).ctx ?? "";

type Store = {
  contexts: Ctx[]; resources: Res[]; relations: Rel[]; facts: Fact[]; sessions: Record<string, Link[]>;
  delegations: StoredDelegation[]; cards: Record<string, Card>; events: Record<string, Event[]>;
  todos: Todo[]; seen: Record<string, string>;
  queuedTurns: Queued[];
  // Models a freshly restarted server: a persisted session is NOT resident, so session
  // reads answer 404 until something opens it (POST /sessions hydrates it). Defaults to
  // false so only the restart-scenario test is affected.
  coldStart: boolean;
  resident: string[];
  checkpointHistory: Record<string, unknown[]>;
  writes: string[];
  failNextPatch: boolean;
};

const NOW = "2026-10-30T09:00:00Z";

// A feed long enough to overflow the dock. The dock's scrolling was never covered
// before because the snapshot mock always returned no messages, so "can you scroll
// the conversation" could not fail.
const CHAT_MESSAGES = Array.from({ length: 40 }, (_, index) => ({
  id: `m-${index + 1}`,
  role: index % 2 === 0 ? "user" : "assistant",
  content: [{ type: "text", text: `消息 ${index + 1}` }],
  created_at: NOW,
}));

function emptyContext(id: string, title: string, goal: string): Ctx {
  return {
    id, title, background: `${title} background`, goal, status: "active", version: 1,
    checkpoint: { summary: "", completed: [], in_progress: [], next_actions: [], blockers: [], open_questions: [], evidence_refs: [], revision: 1, created_at: NOW },
    created_at: NOW, updated_at: NOW,
  };
}

function baseContext(overrides: Partial<Ctx> & { id: string; title: string }): Ctx {
  return { ...emptyContext(overrides.id, overrides.title, overrides.goal ?? "goal"), ...overrides } as Ctx;
}

function newStore(seed: Partial<Store> = {}): Store {
  return { contexts: [], resources: [], relations: [], facts: [], sessions: {}, delegations: [], cards: {}, events: {}, todos: [], seen: {}, queuedTurns: [], coldStart: false, resident: [], checkpointHistory: {}, writes: [], failNextPatch: false, ...seed };
}

function checkpoint(body: Record<string, unknown>, revision: number) {
  return {
    summary: String(body.summary ?? ""),
    completed: (body.completed as string[]) ?? [],
    in_progress: (body.in_progress as string[]) ?? [],
    next_actions: (body.next_actions as string[]) ?? [],
    blockers: (body.blockers as string[]) ?? [],
    open_questions: (body.open_questions as string[]) ?? [],
    evidence_refs: (body.evidence_refs as string[]) ?? [],
    revision, created_at: NOW,
  };
}

// handleWorkContext implements the documented API over the in-memory store. Anything it
// does not own falls back to the shared mockBackend handler.
function handleWorkContext(store: Store) {
  return async (route: Route): Promise<void> => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api/, "");
    const method = request.method();
    const json = (body: unknown, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    const body = (): Record<string, unknown> => (request.postDataJSON() as Record<string, unknown>) ?? {};
    const detail = (context: Ctx) => ({
      context,
      resources: store.resources.filter((resource) => resource.context_ids.includes(context.id)),
      sessions: store.sessions[context.id] ?? [],
      // Live card read: the delegation carries its current card, and a missing card is
      // flagged instead of being shown as progress.
      delegations: store.delegations
        .filter((delegation) => delegationCtx(delegation) === context.id)
        .map((delegation) => ({ ...delegation, card: store.cards[delegation.card_id] ?? null, source_missing: !store.cards[delegation.card_id] })),
      facts: store.facts.filter((fact) => fact.context_id === context.id),
      relations: store.relations.filter((relation) => relation.from_id === context.id || relation.to_id === context.id),
      events: store.events[context.id] ?? [],
      todos: store.todos.filter((todo) => todo.context_id === context.id),
    });

    if (method !== "GET") store.writes.push(`${method} ${path}`);

    // Sessions list is used to resolve a stored session_id into a FULL locator before
    // navigating (a partial locator would re-hash into a different session), and by the
    // link-existing picker. wc-session-9 is deliberately NOT linked to ctx-1 so the
    // picker has something to offer.
    if (path === "/sessions" && method === "GET") {
      return json([
        { session_id: "wc-session-1", locator: { channel: "web", key: "wc-topic-1", metadata: { project_dir: "/srv/app" } }, title: "Topic", created_at: NOW, updated_at: NOW, last_activity_at: NOW },
        { session_id: "wc-session-9", locator: { channel: "web", key: "wc-topic-9", metadata: { project_dir: "/srv/app" } }, title: "历史排查对话", created_at: NOW, updated_at: NOW, last_activity_at: NOW },
      ]);
    }
    // Opening a session. The real server derives the session id FROM the locator, so
    // reopening an existing conversation returns its own id (not a new one) — that is
    // what lets a stored session_id be resolved back into a full locator and opened.
    if (path === "/sessions" && method === "POST") {
      const locator = (body().locator ?? {}) as { channel?: string; key?: string; metadata?: Record<string, string> };
      const key = String(locator.key ?? "");
      const sessionId = key === "wc-topic-1" ? "wc-session-1" : key === "wc-topic-9" ? "wc-session-9" : key;
      if (!store.resident.includes(sessionId)) store.resident.push(sessionId);
      return json({ session_id: sessionId, locator: { channel: locator.channel ?? "web", key, ...(locator.metadata ? { metadata: locator.metadata } : {}) } });
    }
    // The embedded chat dock reads the linked session directly (snapshot plus the
    // three sources behind the status line), so the status line is asserted for
    // real rather than stubbed away.
    if (path === "/models" && method === "GET") {
      return json({
        default_profile_id: "p1", session_profile_id: "p1",
        profiles: [{ id: "p1", name: "Default", provider: "openai", model: "gpt-5.5", base_url: "", max_tokens: 0, timeout_seconds: 0, supports_streaming: true, supports_vision: false }],
      });
    }
    // The dock's snapshot. Served with a long feed so the scroll path is real.
    const snapMatch = path.match(/^\/sessions\/([^/]+)$/);
    if (snapMatch && method === "GET") {
      const id = decodeURIComponent(snapMatch[1]!);
      // A cold (restarted) server answers session reads with 404 until the session has
      // been opened. Reproducing this is the whole point of coldStart: the bug was a
      // read-before-open that only worked once something else had hydrated the session.
      if (store.coldStart && !store.resident.includes(id)) {
        return json({ error: "session not found" }, 404);
      }
      // queued_turns is served from the store (the server owns the queue), so the
      // dock's queue strip is exercised through the real snapshot field rather than
      // a stubbed-away list.
      return json({ session_id: id, locator: { channel: "web", key: id }, messages: CHAT_MESSAGES, display_messages: CHAT_MESSAGES, running: false, queued_turns: store.queuedTurns, updated_at: NOW });
    }
    // "Edit" (cancel + return the text) and "steer" on a queued turn.
    const queuedAction = path.match(/^\/sessions\/([^/]+)\/queued\/([^/]+)\/(cancel|steer)$/);
    if (queuedAction && method === "POST") {
      const queueId = decodeURIComponent(queuedAction[2]!);
      const action = queuedAction[3]!;
      const queued = store.queuedTurns.find((item) => item.id === queueId);
      store.queuedTurns = store.queuedTurns.filter((item) => item.id !== queueId);
      return json({ session_id: decodeURIComponent(queuedAction[1]!), turn_id: queueId, status: action === "cancel" ? "cancelled" : "steered", updated_at: NOW, text: action === "cancel" ? queued?.summary : undefined });
    }
    const sesSub = path.match(/^\/sessions\/([^/]+)(\/.*)?$/);
    if (sesSub && method === "GET" && sesSub[2]) {
      const sub = sesSub[2];
      if (sub === "/context-inspector") {
        return json({
          context: {
            message_count: 2, token_estimate: 1200, total_token_estimate: 1200, compress_threshold: 10000, suggest_compact: false,
            token_breakdown: { system: 400, history: 800, total: 1200 },
            prefix_cache: { stable_system_tokens: 400, stable_tool_schema_tokens: 0, stable_memory_index_tokens: 0, dynamic_runtime_tokens: 0, dynamic_section_tokens: {} },
          },
          transcript_ref_count: 0, memory_preview: {},
        });
      }
      if (sub === "/context-usage") {
        return json({
          cache_usage: { calls: 1, input_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, hit_rate_percent: 0 },
          cumulative_tokens: 1200, cumulative_input_tokens: 1000, cumulative_output_tokens: 200,
        });
      }
      if (sub === "/timeline") return json([]);
      if (sub === "/timeline/page") return json({ items: [], has_more: false, total: 0 });
      // The desktop now renders INSIDE the chat page, so the full chat page's own
      // session collection endpoints run here too (compactions, subagents, long
      // tasks, active skills). They are not what these tests are about, but they
      // must answer with the COLLECTION shape: the shared fallback returns `{}`,
      // and the chat page calls `.map` on it, which crashes the whole page.
      return json([]);
    }
    if (path === "/agent-templates" && method === "GET") {
      return json([{ id: "tpl-1", name: "Reviewer" }]);
    }
    if (path === "/v1/taskboard/projects" && method === "GET") {
      return json({ projects: [{ id: "prod-1", name: "Platform", work_dirs: ["/srv/app"] }] });
    }

    if (path === "/v1/work-context-resources" && method === "GET") return json(store.resources);
    // Cross-stream attention inbox: decisions (cards awaiting acceptance) plus
    // overdue/soon plan items, each pointing at the tab that resolves it.
    if (path === "/v1/work-attention" && method === "GET") {
      const items: Record<string, string>[] = [];
      for (const delegation of store.delegations) {
        const card = store.cards[delegation.card_id];
        if (!card || card.status !== "in_review") continue;
        items.push({
          kind: "delegation_review", priority: "high", context_id: delegationCtx(delegation),
          context_title: store.contexts.find((item) => item.id === delegationCtx(delegation))?.title ?? "",
          title: card.title, detail: "等待验收", tab: "tasks",
        });
      }
      for (const todo of store.todos) {
        if (!todo.due_at || todo.status === "done") continue;
        const due = Date.parse(todo.due_at);
        if (Number.isNaN(due) || due > Date.now() + 24 * 3600 * 1000) continue;
        items.push({
          kind: due <= Date.now() ? "todo_overdue" : "todo_soon",
          priority: due <= Date.now() ? "normal" : "low", context_id: todo.context_id,
          context_title: store.contexts.find((item) => item.id === todo.context_id)?.title ?? "",
          title: todo.text, detail: due <= Date.now() ? "已过期" : "即将到期", due_at: todo.due_at, tab: "plan",
        });
      }
      const unread: Record<string, number> = {};
      for (const context of store.contexts) {
        const seen = store.seen[context.id] ?? "";
        unread[context.id] = (store.events[context.id] ?? []).filter((event) => event.created_at > seen).length;
      }
      return json({ items, unread, counts: {}, generated_at: NOW });
    }
    if (path === "/v1/work-contexts" && method === "GET") return json(store.contexts);
    if (path === "/v1/work-contexts" && method === "POST") {
      const input = body();
      const created = emptyContext(`ctx-${store.contexts.length + 1}`, String(input.title ?? ""), String(input.goal ?? ""));
      created.background = String(input.background ?? "");
      store.contexts.push(created);
      store.events[created.id] = [];
      return json(created, 201);
    }

    const ctxMatch = path.match(/^\/v1\/work-contexts\/([^/]+)(\/.*)?$/);
    const ctx = ctxMatch ? store.contexts.find((item) => item.id === decodeURIComponent(ctxMatch[1]!)) : undefined;
    if (ctxMatch && !ctx) return json({ error: "work context: not found" }, 404);

    if (ctx && ctxMatch) {
      const sub = ctxMatch[2] ?? "";
      if (!sub && method === "GET") return json(detail(ctx));
      if (!sub && method === "PATCH") {
        const input = body();
        if (store.failNextPatch) { store.failNextPatch = false; return json({ error: "work context: version conflict; reload before saving" }, 409); }
        if (Number(input.if_version) !== ctx.version) return json({ error: "work context: version conflict; reload before saving" }, 409);
        if (typeof input.title === "string") ctx.title = input.title;
        if (typeof input.background === "string") ctx.background = input.background;
        if (typeof input.goal === "string") ctx.goal = input.goal;
        if (typeof input.status === "string") ctx.status = input.status;
        ctx.version += 1;
        store.events[ctx.id] = [{ id: `e-${Date.now()}`, context_id: ctx.id, kind: "status", title: `status → ${ctx.status}`, source: "desktop", created_at: NOW }, ...(store.events[ctx.id] ?? [])];
        return json(ctx);
      }
      if (sub === "/checkpoints" && method === "GET") return json(store.checkpointHistory[ctx.id] ?? []);
      if (sub === "/checkpoints" && method === "POST") {
        const input = body();
        if (Number(input.if_version) !== ctx.version) return json({ error: "work context: version conflict; reload before saving" }, 409);
        const next = checkpoint((input.checkpoint as Record<string, unknown>) ?? {}, ctx.checkpoint.revision + 1);
        ctx.checkpoint = next;
        ctx.version += 1;
        ctx.updated_at = NOW;
        (store.checkpointHistory[ctx.id] ??= []).push(next);
        store.events[ctx.id] = [{ id: `e-${Date.now()}`, context_id: ctx.id, kind: "checkpoint", title: `checkpoint r${next.revision}`, source: "desktop", created_at: NOW }, ...(store.events[ctx.id] ?? [])];
        return json(ctx);
      }
      if (sub === "/brief" && method === "GET") {
        return json({ context: ctx, events: store.events[ctx.id] ?? [], facts: store.facts.filter((fact) => fact.context_id === ctx.id), delegations: store.delegations, production_verified: false });
      }
      // Plan items: first-class rows edited in place, plus the focus view (plan +
      // what changed since the stream was last seen) and the explicit mark-seen.
      if (sub === "/todos" && method === "GET") return json(store.todos.filter((todo) => todo.context_id === ctx.id));
      if (sub === "/todos" && method === "POST") {
        const input = body();
        const todo: Todo = { id: `todo-${store.todos.length + 1}`, context_id: ctx.id, text: String(input.text ?? ""), status: String(input.status ?? "todo"), owner: String(input.owner ?? ""), due_at: String(input.due_at ?? ""), source: "desktop", position: store.todos.length + 1, version: 1, created_at: NOW, updated_at: NOW };
        store.todos.push(todo);
        store.events[ctx.id] = [{ id: `e-${Date.now()}`, context_id: ctx.id, kind: "todo_created", title: `新增计划：${todo.text}`, source: "desktop", created_at: NOW }, ...(store.events[ctx.id] ?? [])];
        return json(todo, 201);
      }
      const todoMatch = sub.match(/^\/todos\/([^/]+)$/);
      if (todoMatch && method === "PATCH") {
        const todo = store.todos.find((item) => item.id === decodeURIComponent(todoMatch[1]!));
        if (!todo) return json({ error: "work context: not found" }, 404);
        const input = body();
        if (Number(input.if_version) !== todo.version) return json({ error: "work context: version conflict; reload before saving" }, 409);
        if (typeof input.text === "string") todo.text = input.text;
        if (typeof input.status === "string") todo.status = input.status;
        if (typeof input.owner === "string") todo.owner = input.owner;
        if (typeof input.due_at === "string") todo.due_at = input.due_at;
        todo.version += 1;
        todo.updated_at = NOW;
        store.events[ctx.id] = [{ id: `e-${Date.now()}`, context_id: ctx.id, kind: "todo_updated", title: `更新计划：${todo.text}`, source: "desktop", created_at: NOW }, ...(store.events[ctx.id] ?? [])];
        return json(todo);
      }
      if (todoMatch && method === "DELETE") {
        store.todos = store.todos.filter((item) => item.id !== decodeURIComponent(todoMatch[1]!));
        return json({ deleted: true });
      }
      if (sub === "/focus" && method === "GET") {
        return json({ last_seen_at: store.seen[ctx.id] ?? "", changes: store.events[ctx.id] ?? [], todos: store.todos.filter((todo) => todo.context_id === ctx.id) });
      }
      if (sub === "/seen" && method === "POST") {
        const previous = store.seen[ctx.id] ?? "";
        store.seen[ctx.id] = NOW;
        return json({ previous_last_seen_at: previous });
      }
      if (sub === "/resources" && method === "POST") {
        const input = body();
        const existing = store.resources.find((item) => item.locator === input.locator && item.title === input.title);
        if (existing) { if (!existing.context_ids.includes(ctx.id)) existing.context_ids.push(ctx.id); return json(existing, 201); }
        const resource: Res = { id: `res-${store.resources.length + 1}`, title: String(input.title ?? ""), kind: String(input.kind ?? ""), locator: String(input.locator ?? ""), node_id: String(input.node_id ?? ""), purpose: String(input.purpose ?? ""), context_ids: [ctx.id], created_at: NOW };
        store.resources.push(resource);
        return json(resource, 201);
      }
      const resMatch = sub.match(/^\/resources\/([^/]+)$/);
      if (resMatch && method === "DELETE") {
        const id = decodeURIComponent(resMatch[1]!);
        const resource = store.resources.find((item) => item.id === id);
        if (resource) resource.context_ids = resource.context_ids.filter((cid) => cid !== ctx.id);
        if (resource && !resource.context_ids.length) store.resources = store.resources.filter((item) => item.id !== id);
        return json({ deleted: true });
      }
      if (sub === "/relations" && method === "POST") {
        const input = body();
        const to = String(input.to_id ?? "");
        if (to === ctx.id) return json({ error: "work context: invalid input" }, 400);
        const relation: Rel = { from_id: ctx.id, to_id: to, kind: String(input.kind ?? "related_to"), condition: String(input.condition ?? ""), created_at: NOW, id: `rel-${store.relations.length + 1}` };
        store.relations.push(relation);
        return json(relation, 201);
      }
      const relMatch = sub.match(/^\/relations\/([^/]+)$/);
      if (relMatch && method === "DELETE") {
        const id = decodeURIComponent(relMatch[1]!);
        store.relations = store.relations.filter((item) => item.id !== id);
        return json({ deleted: true });
      }
      if (sub === "/facts" && method === "POST") {
        const input = body();
        const fact: Fact = { id: `fact-${store.facts.length + 1}`, context_id: ctx.id, title: String(input.title ?? ""), content: String(input.content ?? ""), kind: String(input.kind ?? "fact"), status: "candidate", scope: String(input.scope ?? ""), subject: String(input.subject ?? ""), predicate: String(input.predicate ?? ""), source: String(input.source ?? ""), source_excerpt: String(input.source_excerpt ?? ""), source_revision: "", observed_at: String(input.observed_at ?? ""), verified_at: "", provenance: String(input.provenance ?? ""), supersedes_id: String(input.supersedes_id ?? ""), version: 1, created_at: NOW, memory_candidate_id: "" };
        store.facts.push(fact);
        return json(fact, 201);
      }
      const factMatch = sub.match(/^\/facts\/([^/]+)$/);
      if (factMatch && method === "PATCH") {
        const id = decodeURIComponent(factMatch[1]!);
        const fact = store.facts.find((item) => item.id === id);
        if (!fact) return json({ error: "work context: not found" }, 404);
        const input = body();
        if (Number(input.if_version) !== fact.version) return json({ error: "work context: version conflict; reload before saving" }, 409);
        fact.status = String(input.status ?? fact.status);
        if (fact.status === "confirmed") fact.verified_at = NOW;
        fact.version += 1;
        return json(fact);
      }
      const memMatch = sub.match(/^\/facts\/([^/]+)\/memory-candidate$/);
      if (memMatch && method === "POST") {
        const fact = store.facts.find((item) => item.id === decodeURIComponent(memMatch[1]!));
        if (!fact) return json({ error: "work context: not found" }, 404);
        if (!fact.source || !fact.observed_at) return json({ error: "work context: invalid input" }, 400);
        fact.memory_candidate_id = `cand-${fact.id}`;
        return json(fact);
      }
      if (sub === "/recall" && method === "GET") {
        const scope = url.searchParams.get("scope") ?? "current";
        const q = (url.searchParams.get("q") ?? "").trim().toLowerCase();
        const related = new Set([ctx.id]);
        if (scope === "related") for (const relation of store.relations) {
          if (relation.from_id === ctx.id) related.add(relation.to_id);
          if (relation.to_id === ctx.id) related.add(relation.from_id);
        }
        return json(store.facts.filter((fact) => (scope === "all" || related.has(fact.context_id)) && (!q || `${fact.title} ${fact.content} ${fact.scope}`.toLowerCase().includes(q))));
      }
      if (sub === "/sessions" && method === "POST") {
        const input = body();
        const link: Link = { id: `link-${(store.sessions[ctx.id] ?? []).length + 1}`, session_id: String(input.session_id || `wc-session-${(store.sessions[ctx.id] ?? []).length + 1}`), topic: String(input.topic ?? ""), created_at: NOW };
        (store.sessions[ctx.id] ??= []).push(link);
        return json(link, 201);
      }
      const sesMatch = sub.match(/^\/sessions\/([^/]+)$/);
      if (sesMatch && method === "DELETE") {
        const id = decodeURIComponent(sesMatch[1]!);
        store.sessions[ctx.id] = (store.sessions[ctx.id] ?? []).filter((link) => link.session_id !== id);
        return json({ deleted: true });
      }
      if (sub === "/delegations" && method === "POST") {
        const input = body();
        const requestId = String(input.request_id ?? "");
        // request_id is the idempotency key: a retried submit reuses the same card.
        const existing = store.delegations.find((item) => item.request_id === requestId && delegationCtx(item) === ctx.id);
        if (existing) return json({ delegation: existing, card: store.cards[existing.card_id] }, 200);
        const card: Card = { id: `card-${store.delegations.length + 1}`, project_id: String(input.project_id ?? ""), title: String(input.title ?? ""), status: "in_review", urgency: "normal", version: 1, checklist: ((input.acceptance as string[]) ?? []).map((text) => ({ text, done: false })), created_at: NOW };
        store.cards[card.id] = card;
        const delegation: StoredDelegation = { id: `del-${store.delegations.length + 1}`, request_id: requestId, card_id: card.id, project_id: card.project_id, title: card.title, pack: `r${ctx.checkpoint.revision}`, state: "reserved", created_at: NOW, ctx: ctx.id };
        store.delegations.push(delegation);
        return json({ delegation, card }, 201);
      }
    }

    const executeMatch = path.match(/^\/v1\/taskboard\/cards\/([^/]+)\/execute$/);
    if (executeMatch && method === "POST") {
      const card = store.cards[decodeURIComponent(executeMatch[1]!)];
      if (!card) return json({ error: "card not found" }, 404);
      card.status = "in_progress";
      return json({ execution_id: "exec-1", session_id: "wc-session-1" });
    }
    const cardMatch = path.match(/^\/v1\/taskboard\/cards\/([^/]+)$/);
    if (cardMatch && method === "PATCH") {
      const card = store.cards[decodeURIComponent(cardMatch[1]!)];
      if (!card) return json({ error: "card not found" }, 404);
      const input = body();
      if (input.action === "complete") card.status = "done";
      if (input.action === "reject") card.status = "todo";
      card.version += 1;
      return json({ card });
    }

    return route.fallback();
  };
}

// antd renders two-character CJK button labels with a space between them ("确 认"),
// and icon buttons prefix the icon name ("comment 打开对话"), so exact name matching is
// brittle. rx() tolerates both the inserted space and a leading icon label.
function rx(label: string): RegExp {
  return new RegExp(label.split("").map((ch) => ch.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("\\s*"));
}

async function install(page: Page, seed?: Partial<Store>): Promise<Store> {
  await mockBackend(page);
  const store = newStore(seed);
  // Registered after mockBackend so it takes precedence; unknown paths fall through.
  await page.route("**/api/**", handleWorkContext(store));
  await page.addInitScript(() => {
    localStorage.setItem("godex:web:locale", "zh");
    localStorage.setItem("godex.web.layout.v1", JSON.stringify({ taskCenterDrawerOpen: false }));
  });
  return store;
}

function seededStore(): Partial<Store> {
  const primary = baseContext({ id: "ctx-1", title: "语音服务 · 长期维护", goal: "可追溯的发布与回滚", background: "共享语音能力维护" });
  primary.checkpoint = { summary: "已完成离线评测", completed: ["补齐评测脚本"], in_progress: [], next_actions: ["固定权重对比参数"], blockers: [], open_questions: ["参数是否主因"], evidence_refs: [], revision: 3, created_at: NOW };
  primary.version = 4;
  const related = baseContext({ id: "ctx-2", title: "模型效果 · 对齐与更新", goal: "可复现的效果差异", background: "印尼语效果对齐" });
  const resource: Res = { id: "res-1", title: "共享语音服务", kind: "service", locator: "service://speech", node_id: "", purpose: "服务标识", context_ids: ["ctx-1", "ctx-2"], created_at: NOW };
  const fact: Fact = { id: "fact-1", context_id: "ctx-1", title: "退化可能来自解码参数", content: "尚未完成单变量对比", kind: "trace", status: "candidate", scope: "speech / short-utterances", subject: "", predicate: "", source: "artifact://eval/r12", source_excerpt: "9.1% → 10.2%", source_revision: "r12", observed_at: NOW, verified_at: "", provenance: "human", supersedes_id: "", version: 1, created_at: NOW, memory_candidate_id: "" };
  const relatedFact: Fact = { ...fact, id: "fact-2", context_id: "ctx-2", title: "r12 整体改善但短句回归", status: "confirmed", verified_at: NOW };
  const link: Link = { id: "link-1", session_id: "wc-session-1", topic: "短句回归排查", created_at: NOW };
  const card: Card = { id: "card-1", project_id: "prod-1", title: "补充发布回滚 Runbook", status: "in_review", urgency: "normal", version: 1, checklist: [{ text: "能从部署记录定位回滚目标", done: false }], created_at: NOW };
  const delegation: StoredDelegation = { id: "del-1", request_id: "req-1", card_id: "card-1", project_id: "prod-1", title: "补充发布回滚 Runbook", pack: "r3", state: "reserved", created_at: NOW, ctx: "ctx-1" };
  return {
    contexts: [primary, related], resources: [resource], facts: [fact, relatedFact],
    relations: [{ id: "rel-1", from_id: "ctx-1", to_id: "ctx-2", kind: "related_to", condition: "共享服务仓库与评测资产", created_at: NOW }],
    sessions: { "ctx-1": [link] }, cards: { "card-1": card }, delegations: [delegation],
    events: { "ctx-1": [{ id: "ev-1", context_id: "ctx-1", kind: "checkpoint", title: "检查点 r3 已保存", source: "desktop", created_at: NOW }] },
  };
}

// ---------------------------------------------------------------------------
// The desktop is now a VIEW of the chat page's left rail, not its own route: the
// left rail switches between the conversation directory and the desktop list, the
// centre column stays the normal chat, and the desktop's boards live in the right
// dock. These helpers drive that fused entrance, so every test below walks the
// path a user actually takes instead of a retired URL.
// ---------------------------------------------------------------------------

/** The desktop's left-rail list. */
const rail = (page: Page) => page.locator('[data-testid="chat-v2-workdesk"]');
/** The desktop's boards inside the right dock. */
const boards = (page: Page) => page.locator('[data-testid="chat-v2-workdesk-panel"]');
/** The board switcher inside the dock. */
const sections = (page: Page) => page.locator('[data-testid="chat-v2-workdesk-sections"]');
const viewSwitch = (page: Page) => page.locator('[data-testid="chat-v2-left-view"]');

/**
 * Get to the desktop view the way a user does: open the chat, expand the rail if
 * it starts collapsed (it does on narrow viewports), then switch the rail to the
 * desktop. Deliberately does NOT require the right dock: several behaviours are in
 * the rail alone, and forcing the dock open on a phone would cover the screen.
 */
async function openDesktopRail(page: Page, path = "/chat") {
  await page.goto(path);
  // Three legitimate first states, and isVisible() does not auto-wait, so wait for
  // ONE of them before deciding:
  //  - the rail with its view switch (a configured install, widened rail),
  //  - the first-run provider guide (the desktop is exempt from that gate), or
  //  - the topbar's sidebar toggle (narrow viewport: the rail starts collapsed, and
  //    a collapsed rail is display:none — so its own expand button is unreachable
  //    and the topbar toggle is the control that actually exists).
  const guideEntry = page.locator('[data-testid="first-run-open-desktop"]');
  const sidebarToggle = page.getByRole("button", { name: "Toggle sidebar" });
  await page.waitForSelector('[data-testid="chat-v2-left-view"], [data-testid="first-run-open-desktop"], button[aria-label="Toggle sidebar"]', { timeout: 20_000 });
  // A fresh install has no provider, so go in through the guide's own entry point —
  // the same path a user following a background nudge would take.
  if (await guideEntry.isVisible()) {
    await guideEntry.click();
    await expect(rail(page)).toBeVisible();
    return;
  }
  if (!(await viewSwitch(page).isVisible())) {
    await sidebarToggle.first().click();
  }
  await expect(viewSwitch(page)).toBeVisible();
  // Locale-tolerant: the two options are 对话/桌面 in zh and Directory/Desktop in en.
  await page.locator(".ant-segmented-item", { hasText: /桌面|Desktop/ }).click();
  await expect(rail(page)).toBeVisible();
}

/**
 * Enter the desktop view AND make sure the boards are on screen. Switching opens
 * the dock on a wide viewport; on a narrow one the dock is intentionally left
 * alone, so this opens it explicitly.
 */
async function openDesktop(page: Page) {
  await openDesktopRail(page);
  // Do NOT probe with isVisible() and then click: the dock tab's click TOGGLES, so
  // clicking an already-open tab collapses it. That race (the guide or the view
  // switch can already have opened it) would leave the panel hidden forever.
  // Only click when the tab is not active, and let the assertion below auto-wait.
  const dockTab = page.locator('.chat-v2-topbar-tab[aria-label="Work desktop"]');
  const isActive = await dockTab.evaluate((el) => el.classList.contains("chat-v2-topbar-tab-active"));
  if (!isActive) {
    await dockTab.click();
  }
  await expect(boards(page)).toBeVisible();
}

/** Enter the desktop view and open one board by its switch label. */
async function openSection(page: Page, label: string) {
  await openDesktop(page);
  await sections(page).getByText(label, { exact: true }).click();
}

/** Select a desktop by its title in the left rail. */
async function selectDesktop(page: Page, title: string) {
  await rail(page).getByText(title, { exact: true }).first().click();
}

test("offers both rail views, and switching between them does not disturb the conversation", async ({ page }) => {
  // The fusion's core promise: the desktop is a second view of the SAME page, so
  // the centre column must stay the ordinary chat. If the switch replaced the page
  // (as the retired desktop route did), the feed below would disappear.
  await install(page, seededStore());
  await page.goto("/chat/web/wc-topic-1");
  const centreFeed = page.locator(".chat-v2-center-wrap .chat-feed").first();
  await expect(centreFeed).toBeVisible();
  const url = page.url();

  // From a conversation, peek at the desktops and come back.
  await openDesktopRail(page, page.url());
  await expect(centreFeed).toBeVisible();
  expect(page.url()).toBe(url);

  await viewSwitch(page).getByText("对话", { exact: true }).click();
  await expect(page.locator('[data-testid="chat-v2-sessions"]')).toBeVisible();
  await expect(centreFeed).toBeVisible();
  // Merely switching views must not create or delete anything.
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("switching to the desktop view reveals its boards without a second click", async ({ page }) => {
  // The desktop's content lives in the right dock now, so the view switch must bring
  // it up: otherwise "switch to my desktops" would show a desktop list with no
  // desktop content. Asserted WITHOUT the helper's fallback click, so it fails if the
  // two ever stop moving together.
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  await install(page, seededStore());
  await page.goto("/chat");
  const railViewSwitch = viewSwitch(page);
  await expect(railViewSwitch).toBeVisible();
  await expect(boards(page)).toBeHidden();

  await railViewSwitch.getByText("桌面", { exact: true }).click();
  await expect(rail(page)).toBeVisible();
  await expect(boards(page)).toBeVisible();
  await expect(sections(page)).toBeVisible();
});

test("remembers the left view across a reload", async ({ page }) => {
  // The user asked for the view to be remembered. It matters beyond convenience:
  // the work desktop is where background nudges land, so a reload must not silently
  // drop you back into the conversation directory. Asserted through a real reload
  // (not by calling the store), so it fails if the persistence wiring is ever dropped
  // again — the helpers existed for a while with nothing calling them.
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  await install(page, seededStore());
  await openDesktopRail(page);
  await expect(page.locator(".chat-v2-left")).toHaveAttribute("data-left-view", "desktop");

  await page.reload();
  await expect(page.locator(".chat-v2-left")).toHaveAttribute("data-left-view", "desktop");
  await expect(rail(page)).toBeVisible();
  // And the directory really is not what came back.
  await expect(page.locator('[data-testid="chat-v2-sessions"]')).toHaveCount(0);
});

test("an empty store is a real first-run state, and creating a desktop writes through the API", async ({ page }) => {
  const store = await install(page);
  await openDesktopRail(page);

  // The rail must not invent a demo stream when the store is empty.
  await expect(rail(page).getByText("还没有工作主线", { exact: true })).toBeVisible();
  // The storage boundary stays visible: this is a personal store and does not
  // verify production state, which matters most on a blank slate.
  await expect(rail(page).getByText(/个人 SQLite 存储/, { exact: false })).toBeVisible();

  await rail(page).getByRole("button", { name: rx("新建桌面") }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: rx("创建桌面") }).click();
  await expect(dialog.getByText("请输入非空内容").first()).toBeVisible();
  await dialog.getByLabel("主线名称", { exact: true }).fill("新服务维护主线");
  await dialog.getByLabel("背景", { exact: true }).fill("长期维护服务和模型更新");
  await dialog.getByLabel("目标", { exact: true }).fill("能快速找回工作背景");
  await dialog.getByRole("button", { name: rx("创建桌面") }).click();

  await expect(rail(page).getByText("新服务维护主线", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts");
  expect(store.contexts.map((item) => item.title)).toContain("新服务维护主线");
});

test("opens a linked conversation in the centre chat column", async ({ page }) => {
  // What the fusion changed: "open this conversation" now means "make it the open
  // conversation" in the ONE chat the page already has, instead of embedding a
  // second chat inside a desktop tab.
  const store = await install(page, seededStore());
  await openDesktopRail(page);
  await selectDesktop(page, "语音服务 · 长期维护");

  // The active desktop expands in place to show its linked conversations.
  const linked = page.locator('[data-testid="chat-v2-workdesk-linked"]');
  await expect(linked).toBeVisible();
  await linked.getByRole("button", { name: /短句回归排查/ }).click();

  // It navigates to that conversation's chat route, carrying the FULL locator
  // (the stored id alone would re-hash into a different, empty session).
  await expect(page).toHaveURL(/\/chat\/web\/wc-topic-1/);
  await expect(page).toHaveURL(/workspace_dir=%2Fsrv%2Fapp/);
  // Opening a conversation must not unlink or disturb it.
  expect(store.writes.filter((entry) => entry.startsWith("DELETE"))).toEqual([]);
});

test("saves checkpoints, resources, relations and sessions as real server writes", async ({ page }, info) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await openDesktop(page);
  await expect(boards(page).getByText("检查点 r3", { exact: true }).first()).toBeVisible();
  await page.screenshot({ path: `../../.godex/tmp/work-desktop-${info.project.name}.png`, fullPage: true });

  // Checkpoint: edited in place on the overview (there is no checkpoint modal).
  // One field edit still submits the WHOLE checkpoint document, so untouched
  // fields must survive — a per-field UI could silently lose them.
  await boards(page).getByRole("button", { name: "当前进展", exact: true }).click();
  const summaryInput = page.getByRole("textbox", { name: "当前进展", exact: true });
  await summaryInput.fill("确认范围后继续参数对比");
  await summaryInput.press("Control+Enter");
  await expect(boards(page).getByText("确认范围后继续参数对比", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/checkpoints");
  expect(store.contexts[0]!.checkpoint.completed).toEqual(["补齐评测脚本"]);

  // The desktop's name is edited in place from the dock header, not a modal.
  await boards(page).getByRole("button", { name: "编辑标题", exact: true }).click();
  const titleInput = page.getByRole("textbox", { name: "编辑标题", exact: true });
  await titleInput.fill("语音服务 · 维护中");
  await titleInput.press("Enter");
  await expect(boards(page).getByText("语音服务 · 维护中", { exact: true })).toBeVisible();
  expect(store.contexts[0]!.title).toBe("语音服务 · 维护中");
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // Resource: added to the shared list and referenced by this stream.
  await sections(page).getByText("资源", { exact: true }).click();
  await boards(page).getByRole("button", { name: rx("添加资源") }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("资源标题", { exact: true }).fill("发布 Runbook");
  await dialog.getByLabel("地址 / 定位", { exact: true }).fill("https://docs.example.invalid/runbook");
  await dialog.getByRole("button", { name: rx("保存") }).click();
  await expect(boards(page).getByRole("button", { name: /发布 Runbook/ })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/resources");

  // Relation: a dependency on the other stream, with its condition.
  await sections(page).getByText("概览", { exact: true }).click();
  await boards(page).getByRole("button", { name: rx("添加关联") }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("目标主线", { exact: true }).click();
  await page.locator(".ant-select-item-option-content", { hasText: "模型效果 · 对齐与更新" }).click();
  await dialog.getByLabel("满足条件", { exact: true }).fill("短句门禁通过后继续");
  await dialog.getByRole("button", { name: rx("保存") }).click();
  await expect(boards(page).getByText("短句门禁通过后继续", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/relations");

  // Session: linked to the stream, and the pre-seeded link still resolves.
  await sections(page).getByText("主题", { exact: true }).click();
  await boards(page).getByRole("button", { name: rx("新建主题对话") }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("主题名称", { exact: true }).fill("发布交接");
  await dialog.getByRole("button", { name: rx("保存") }).click();
  await expect(boards(page).getByText("发布交接", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/sessions");
});

test("edits the goal in place in the overview and renders a multi-line value as-is", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  // A goal with markdown and real line breaks: it must render as markdown rather
  // than collapsing into one run-on line. The mock reads the store live, so this
  // mutation is what the first GET serves.
  store.contexts[0]!.goal = "- 先跑固定参数评测\n- 再准备回滚清单";
  await openDesktop(page);

  const goal = boards(page).locator(".wd-inline-rich").filter({ has: page.getByRole("button", { name: "目标", exact: true }) });
  await expect(goal.locator("li")).toHaveCount(2);
  await expect(goal.getByText("先跑固定参数评测", { exact: true })).toBeVisible();

  await goal.getByRole("button", { name: "目标", exact: true }).click();
  const input = page.getByRole("textbox", { name: "目标", exact: true });
  await input.fill("可追溯的发布与回滚");
  await input.press("Control+Enter");
  await expect(goal.getByText("可追溯的发布与回滚", { exact: true })).toBeVisible();
  expect(store.writes).toContain("PATCH /v1/work-contexts/ctx-1");
  expect(store.contexts[0]!.goal).toBe("可追溯的发布与回滚");
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("keeps plan items in place and marks the stream seen", async ({ page }, info) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await openSection(page, "计划");
  await expect(boards(page).getByText("还没有计划项；你和 Agent 都可以添加。", { exact: true })).toBeVisible();

  // Adding a plan item is a real write, not a client-side list.
  await boards(page).getByRole("textbox", { name: "计划内容", exact: true }).fill("跑一轮固定参数评测");
  await boards(page).getByRole("button", { name: rx("添加计划") }).click();
  await expect(boards(page).getByText("跑一轮固定参数评测", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/todos");

  // Editing the item text happens in place (click the value, type, Enter).
  await boards(page).getByRole("button", { name: /^计划内容/ }).click();
  const editor = page.getByRole("textbox", { name: "计划内容: 跑一轮固定参数评测", exact: true });
  await editor.fill("跑两轮固定参数评测");
  await editor.press("Enter");
  await expect(boards(page).getByText("跑两轮固定参数评测", { exact: true })).toBeVisible();
  expect(store.writes.some((entry) => entry.startsWith("PATCH /v1/work-contexts/ctx-1/todos/"))).toBe(true);
  await page.screenshot({ path: `../../.godex/tmp/work-desktop-plan-${info.project.name}.png`, fullPage: true });

  // The overview shows what changed since last seen, and marking seen clears it.
  await sections(page).getByText("概览", { exact: true }).click();
  await expect(boards(page).getByText("自上次查看以来的变动", { exact: true })).toBeVisible();
  await expect(boards(page).getByText("尚未标记已读", { exact: true })).toBeVisible();
  await boards(page).getByRole("button", { name: rx("标记已读") }).click();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/seen");
  await expect(page.getByText("已把此主线标记为已读", { exact: true })).toBeVisible();
});

test("renders plan items as compact two-line bubbles with no visible meta labels", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  // One done item carrying an owner and a due date, plus one open item, so the
  // status marking is observable as a DIFFERENCE rather than a single sample.
  store.todos.push(
    { id: "t-1", context_id: "ctx-1", text: "跑一轮固定参数评测", status: "done", owner: "小林", due_at: "2026-11-20T00:00:00Z", source: "human", position: 1, version: 1, created_at: NOW, updated_at: NOW },
    { id: "t-2", context_id: "ctx-1", text: "整理发布清单", status: "todo", owner: "", due_at: "", source: "human", position: 2, version: 1, created_at: NOW, updated_at: NOW },
  );
  await openSection(page, "计划");

  const done = boards(page).locator(".wd-todo").filter({ hasText: "跑一轮固定参数评测" });
  const open = boards(page).locator(".wd-todo").filter({ hasText: "整理发布清单" });
  await expect(done).toBeVisible();

  // Two lines: the plan text on its own row, the meta pieces on the next.
  await expect(done.locator(".wd-todo-text-row")).toContainText("跑一轮固定参数评测");
  await expect(done.locator(".wd-todo-meta-row")).toContainText("小林");
  // The meta row WRAPS rather than squeezing, which is what makes the bubble survive
  // the dock's narrow width instead of clipping the pieces.
  await expect(done.locator(".wd-todo-meta-row")).toHaveCSS("flex-wrap", "wrap");

  // "Owner" / "Due" are not rendered as visible text any more: a name and a date read
  // on their own, and two labels would eat the width the bubble needs. They survive as
  // accessible names, so the field stays identifiable without a visible label.
  await expect(done).not.toContainText("负责人");
  await expect(done).not.toContainText("截止时间");
  await expect(done.locator('[aria-label="负责人: 跑一轮固定参数评测"]')).toHaveCount(1);
  await expect(done.locator('[aria-label="截止时间: 跑一轮固定参数评测"]')).toHaveCount(1);

  // A done item is marked by COLOUR, not a strikethrough (line-through measurably hurt
  // readability at this size). Asserted as "not struck through" plus "visually
  // distinguishable from an open item", so removing the colour would fail too.
  const decoration = await done.locator(".wd-todo-text .wd-inline-value").evaluate((el) => getComputedStyle(el).textDecorationLine);
  expect(decoration).not.toContain("line-through");
  const doneBorder = await done.evaluate((el) => getComputedStyle(el).borderLeftColor);
  const openBorder = await open.evaluate((el) => getComputedStyle(el).borderLeftColor);
  expect(doneBorder).not.toBe(openBorder);

  // The status pill still names the state, and stays editable in place.
  await expect(done.getByText("完成", { exact: true })).toBeVisible();
});

test("stacks overview sections in one column and folds each one", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  await install(page, seededStore());
  await openSection(page, "概览");

  // Single column: every section starts at the same x, so none are side by side. The
  // old two-column grid squeezed each card at dock width, which is what this replaces.
  const cards = boards(page).locator(".wd-fold");
  const xs = await cards.evaluateAll((els) => els.map((el) => Math.round(el.getBoundingClientRect().x)));
  expect(xs.length).toBeGreaterThan(1);
  expect(new Set(xs).size).toBe(1);

  // Sections start OPEN (this panel is work memory: hiding remembered content by
  // default is the worse failure), and each folds on demand.
  const completed = cards.filter({ hasText: "最近完成" });
  await expect(completed.getByText("补齐评测脚本", { exact: true })).toBeVisible();
  await completed.getByRole("button", { name: "折叠", exact: true }).click();
  await expect(completed.getByText("补齐评测脚本", { exact: true })).toHaveCount(0);
  await completed.getByRole("button", { name: "展开", exact: true }).click();
  await expect(completed.getByText("补齐评测脚本", { exact: true })).toBeVisible();
});

test("scopes the attention inbox and jumps to the board that resolves the item", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  // Give the OTHER stream its own overdue item so the two scopes differ observably
  // instead of both rendering the same single row.
  store.todos.push({
    id: "todo-2", context_id: "ctx-2", text: "对齐印尼语短句评测", status: "todo", owner: "",
    due_at: "2026-10-01T00:00:00Z", source: "human", position: 0, version: 1, created_at: NOW, updated_at: NOW,
  });
  await openSection(page, "待处理");

  const inbox = boards(page).locator(".wd-attention");
  await expect(inbox).toBeVisible();
  // It covers every desktop by default, so the other stream's item is listed.
  await expect(inbox.getByText("对齐印尼语短句评测", { exact: true })).toBeVisible();

  // Narrowing to the open desktop hides the other stream's item.
  await inbox.locator(".ant-segmented-item", { hasText: "当前桌面" }).click();
  await expect(inbox.getByText("对齐印尼语短句评测", { exact: true })).toHaveCount(0);
  await expect(inbox.getByText("补充发布回滚 Runbook", { exact: true })).toBeVisible();

  // Clicking an item lands on the board that resolves it, not the default one:
  // the item says "tasks", which is now the delegation board.
  await inbox.getByRole("button", { name: /补充发布回滚 Runbook/ }).click();
  await expect(sections(page).locator(".ant-segmented-item-selected")).toHaveText("委派");
  // Reading the inbox is read-only: it must not silently mark the stream seen.
  expect(store.seen["ctx-1"]).toBeUndefined();
});

test("selecting another desktop in the rail swaps the boards to it", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await openSection(page, "主题");
  // ctx-1 has the linked conversation.
  await expect(boards(page).getByText("短句回归排查", { exact: true })).toBeVisible();

  await selectDesktop(page, "模型效果 · 对齐与更新");
  // ctx-2 has none, so the board must switch rather than keep showing ctx-1's.
  await expect(boards(page).getByText("短句回归排查", { exact: true })).toHaveCount(0);
  await expect(boards(page).getByText("还没有关联对话", { exact: true })).toBeVisible();
  // Switching desktops is a read: nothing was written or unlinked by looking.
  expect(store.writes.filter((entry) => entry.startsWith("DELETE"))).toEqual([]);
});

test("links an existing conversation to the stream without moving it", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await openSection(page, "主题");

  await boards(page).getByRole("button", { name: rx("关联已有会话") }).click();
  const dialog = page.getByRole("dialog");
  // Only unlinked conversations are offered, so the already-linked one is absent.
  await dialog.getByRole("combobox", { name: rx("选择已有对话") }).click();
  await expect(page.locator(".ant-select-item-option-content", { hasText: "wc-session-1" })).toHaveCount(0);
  await page.locator(".ant-select-item-option-content", { hasText: "历史排查对话" }).click();
  await dialog.getByRole("button", { name: rx("关联已有会话") }).click();

  await expect(boards(page).getByText("历史排查对话", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/sessions");
  expect(store.sessions["ctx-1"]!.some((link) => link.session_id === "wc-session-9")).toBe(true);
  // Linking never moves or deletes the conversation itself.
  expect(store.writes.some((entry) => entry.startsWith("DELETE"))).toBe(false);
});

test("recalls facts by scope, reviews a candidate and submits a memory candidate", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await openSection(page, "知识");
  await expect(boards(page).getByText("退化可能来自解码参数", { exact: true })).toBeVisible();
  // The related stream's fact is out of scope until the scope widens.
  await expect(boards(page).getByText("r12 整体改善但短句回归", { exact: true })).toHaveCount(0);

  await boards(page).getByRole("combobox", { name: "检索范围", exact: true }).click();
  await page.getByText("当前及直接关联", { exact: true }).click();
  await expect(boards(page).getByText("r12 整体改善但短句回归", { exact: true })).toBeVisible();

  await boards(page).getByRole("textbox", { name: "搜索结论、关键词或历史线索", exact: true }).fill("参数");
  await expect(boards(page).locator(".wd-knowledge")).toHaveCount(1);

  await boards(page).getByRole("button", { name: rx("确认") }).click();
  await expect(boards(page).locator(".wd-knowledge").getByText("已确认", { exact: true })).toBeVisible();
  expect(store.writes).toContain("PATCH /v1/work-contexts/ctx-1/facts/fact-1");

  // Durable memory is only touched through the candidate inbox, as an explicit action.
  await boards(page).getByRole("button", { name: rx("提交记忆候选") }).click();
  await expect(page.getByText("已在记忆候选箱", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/facts/fact-1/memory-candidate");
});

test("renders knowledge as markdown, keeps line breaks and folds long entries", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  // A long entry: a heading plus a list, longer than the fold threshold. The title
  // and the excerpt both contain real newlines, which used to collapse into one
  // run-on line.
  const body = ["# 根因分析", "- 固定解码参数", "- 单变量对比", "- 复核短句集合", "- 检查后处理", "- 复跑 r12 基线"].join("\n");
  store.facts[0]!.content = body;
  store.facts[0]!.title = "退化可能来自解码参数\n（待复核）";
  store.facts[0]!.source_excerpt = "9.1% → 10.2%\n短句 8.4% → 9.6%";
  await openSection(page, "知识");

  const card = boards(page).locator(".wd-knowledge").first();
  await expect(card).toBeVisible();

  // (1) Folded: only the opening lines render. Asserting the ITEM COUNT (not "the
  //     card exists") is what makes this fail if the fold is dropped.
  await expect(card.locator("li")).toHaveCount(3);
  await expect(card.getByText("复跑 r12 基线", { exact: true })).toHaveCount(0);
  // A folded entry shows the headline only; the details are part of the fold.
  await expect(card.getByText("适用范围", { exact: false })).toHaveCount(0);

  // (2) Markdown actually renders (the heading becomes a heading element).
  await expect(card.locator("h1")).toHaveText("根因分析");

  // (3) Expanding reveals the rest and brings the details back.
  await card.getByRole("button", { name: "展开全部" }).click();
  await expect(card.locator("li")).toHaveCount(5);
  await expect(card.getByText("复跑 r12 基线", { exact: true })).toBeVisible();
  await expect(card.getByText("适用范围", { exact: false })).toBeVisible();

  // (4) Line breaks survive in the title and the evidence excerpt. Compared as
  //     computed style: textContent keeps the \n either way, so only white-space
  //     distinguishes "preserved" from "collapsed into one line".
  await expect(card.locator(".ant-card-head-title .whitespace-pre-wrap").first()).toHaveCSS("white-space", "pre-wrap");
  const excerpt = card.locator(".ant-typography.whitespace-pre-wrap").first();
  await expect(excerpt).toHaveCSS("white-space", "pre-wrap");
  await expect(excerpt).toContainText("短句 8.4% → 9.6%");

  // (5) And it folds back.
  await card.getByRole("button", { name: "收起" }).click();
  await expect(card.locator("li")).toHaveCount(3);
});

test("delegates to a real card without auto-running it, then executes and reviews on demand", async ({ page }, info) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await openSection(page, "委派");
  await expect(boards(page).getByText("能从部署记录定位回滚目标", { exact: true })).toBeVisible();

  await boards(page).getByRole("button", { name: rx("委派工作") }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("委派目标", { exact: true }).fill("隔离模型与参数影响");
  await dialog.getByLabel("验收条件 · 每行一项", { exact: true }).fill("同一评测集与权重\n附命令与参数 hash");
  await dialog.getByRole("button", { name: rx("委派工作") }).click();
  await expect(boards(page).getByText("隔离模型与参数影响", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/delegations");
  // Creating a delegation must NOT start an execution.
  expect(store.writes).not.toContain("POST /v1/taskboard/cards/card-2/execute");
  expect(store.cards["card-2"]!.status).toBe("in_review");

  // Manual acceptance reuses the board's own patch semantics.
  await boards(page).getByRole("button", { name: rx("验收通过") }).last().click();
  await expect.poll(() => store.cards["card-2"]!.status).toBe("done");
  expect(store.writes).toContain("PATCH /v1/taskboard/cards/card-2");

  // Manual dispatch of the seeded, still-running card.
  await boards(page).getByRole("button", { name: rx("派发执行") }).first().click();
  await expect.poll(() => store.cards["card-1"]!.status).toBe("in_progress");
  expect(store.writes).toContain("POST /v1/taskboard/cards/card-1/execute");
  await page.screenshot({ path: `../../.godex/tmp/work-desktop-delegation-${info.project.name}.png`, fullPage: true });
});

test("archives to read-only and surfaces write conflicts", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await openDesktop(page);

  // Archive is a server write; the desktop then goes read-only until restored.
  page.once("dialog", (dialog) => void dialog.accept());
  await boards(page).getByRole("button", { name: rx("归档主线") }).click();
  await expect.poll(() => store.contexts[0]!.status).toBe("archived");
  await expect(boards(page).getByText("主线已归档，只读。先恢复再编辑。", { exact: true })).toBeVisible();
  // Read-only is enforced on the in-place editors too, not only on the buttons.
  await expect(boards(page).getByRole("button", { name: "编辑标题", exact: true })).toBeDisabled();

  await boards(page).getByRole("button", { name: rx("恢复主线") }).click();
  await expect.poll(() => store.contexts[0]!.status).toBe("active");
  await expect(boards(page).getByRole("button", { name: "编辑标题", exact: true })).toBeEnabled();

  // A 409 is reported as a conflict, not as a silent success or a generic error.
  store.failNextPatch = true;
  await boards(page).getByRole("button", { name: rx("暂停") }).click();
  await expect(page.getByText("数据已被其他更改更新，请刷新后重试。", { exact: true })).toBeVisible();
});

test("honours the English locale in the desktop view", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  await install(page, seededStore());
  await page.route("**/api/providers", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ providers: [] }) }));
  await page.addInitScript(() => localStorage.setItem("godex:web:locale", "en"));
  await openDesktop(page);
  // The desktop's own surfaces are localized: the board switch and the work-memory hint.
  await expect(sections(page).getByText("Overview", { exact: true })).toBeVisible();
  await expect(sections(page).getByText("Needs me", { exact: true })).toBeVisible();
  await expect(page.getByRole("note", { name: /AI maintenance/ })).toBeVisible();
});

// A long conversation must scroll inside the centre column, with the composer
// reachable. This was a real bug when the desktop owned its own page: the column
// grew to its content height and was clipped, so the feed never scrolled and the
// input sat outside the viewport.
test("scrolls a long conversation and keeps the composer in view", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  await install(page, seededStore());
  // A real conversation, opened through the desktop view (the fused path).
  await openDesktopRail(page, "/chat/web/wc-topic-1");
  const feed = page.locator(".chat-v2-center-wrap .chat-feed").first();
  await expect(feed).toBeVisible();

  // The 40-message history must overflow its scroll container.
  const { client, scroll } = await feed.evaluate((el) => ({ client: el.clientHeight, scroll: el.scrollHeight }));
  expect(scroll).toBeGreaterThan(client);

  // And the last message must be reachable by scrolling the feed itself.
  await feed.evaluate((el) => { el.scrollTop = el.scrollHeight; });
  await expect(page.getByText("消息 40", { exact: true })).toBeInViewport();

  // The composer stays on screen without scrolling the page.
  await expect(page.locator(".chat-v2-center-wrap .chat-composer").first()).toBeInViewport();
});
