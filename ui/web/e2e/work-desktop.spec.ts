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
// The owning stream travels alongside the API shape so the mock can scope a delegation to
// one context, mirroring the real store's context_id column.
type StoredDelegation = Delegation & { ctx: string };
const delegationCtx = (delegation: Delegation) => (delegation as StoredDelegation).ctx ?? "";

type Store = {
  contexts: Ctx[]; resources: Res[]; relations: Rel[]; facts: Fact[]; sessions: Record<string, Link[]>;
  delegations: StoredDelegation[]; cards: Record<string, Card>; events: Record<string, Event[]>;
  todos: Todo[]; seen: Record<string, string>;
  checkpointHistory: Record<string, unknown[]>;
  writes: string[];
  failNextPatch: boolean;
};

const NOW = "2026-10-30T09:00:00Z";

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
  return { contexts: [], resources: [], relations: [], facts: [], sessions: {}, delegations: [], cards: {}, events: {}, todos: [], seen: {}, checkpointHistory: {}, writes: [], failNextPatch: false, ...seed };
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

    // Sessions list is used only to resolve a stored session_id into a FULL locator before
    // navigating; a partial locator would re-hash into a different session.
    if (path === "/sessions" && method === "GET") {
      return json([{ session_id: "wc-session-1", locator: { channel: "web", key: "wc-topic-1", metadata: { project_dir: "/srv/app" } }, title: "Topic", created_at: NOW, updated_at: NOW, last_activity_at: NOW }]);
    }
    if (path === "/agent-templates" && method === "GET") {
      return json([{ id: "tpl-1", name: "Reviewer" }]);
    }
    if (path === "/v1/taskboard/projects" && method === "GET") {
      return json({ projects: [{ id: "prod-1", name: "Platform", work_dirs: ["/srv/app"] }] });
    }

    if (path === "/v1/work-context-resources" && method === "GET") return json(store.resources);
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

test("an empty store is a real first-run state, and creating a desktop writes through the API", async ({ page }) => {
  const store = await install(page);
  await page.goto("/work-desktop");
  // The page must not invent a demo stream when the store is empty.
  await expect(page.getByText("还没有工作主线", { exact: true })).toBeVisible();
  await expect(page.getByText("个人 SQLite 存储", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  await page.getByRole("button", { name: rx("新建桌面") }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: rx("创建桌面") }).click();
  await expect(dialog.getByText("请输入非空内容").first()).toBeVisible();
  await dialog.getByLabel("主线名称", { exact: true }).fill("新服务维护主线");
  await dialog.getByLabel("背景", { exact: true }).fill("长期维护服务和模型更新");
  await dialog.getByLabel("目标", { exact: true }).fill("能快速找回工作背景");
  await dialog.getByRole("button", { name: rx("创建桌面") }).click();

  await expect(page.getByRole("heading", { name: "新服务维护主线", exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts");
  expect(store.contexts.map((item) => item.title)).toContain("新服务维护主线");
  await expect(page).toHaveURL(/context=ctx-1/);
});

test("saves checkpoints, resources, relations and sessions as real server writes", async ({ page }, info) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await page.goto("/work-desktop?context=ctx-1");
  await expect(page.getByRole("heading", { name: "语音服务 · 长期维护", exact: true })).toBeVisible();
  await expect(page.getByText("检查点 r3", { exact: true }).first()).toBeVisible();
  await page.screenshot({ path: `../../.godex/tmp/work-desktop-${info.project.name}.png`, fullPage: true });

  // Checkpoint: optimistic-locked write, reflected from the server response.
  await page.getByRole("button", { name: rx("编辑检查点") }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("当前进展", { exact: true }).fill("确认范围后继续参数对比");
  await dialog.getByLabel("下一步 · 每行一项", { exact: true }).fill("跑一轮固定参数评测\n准备回滚清单");
  await dialog.getByRole("button", { name: rx("保存") }).click();
  await expect(page.getByText("确认范围后继续参数对比", { exact: true }).first()).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/checkpoints");
  expect(store.contexts[0]!.checkpoint.revision).toBe(4);
  expect(store.contexts[0]!.checkpoint.next_actions).toEqual(["跑一轮固定参数评测", "准备回滚清单"]);

  // Resource: added to the shared list and referenced by this stream.
  await page.getByRole("button", { name: rx("添加资源") }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("资源标题", { exact: true }).fill("发布 Runbook");
  await dialog.getByLabel("地址 / 定位", { exact: true }).fill("https://docs.example.invalid/runbook");
  await dialog.getByRole("button", { name: rx("保存") }).click();
  await expect(page.getByRole("button", { name: /发布 Runbook/ })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/resources");

  // Relation: a dependency on the other stream, with its condition.
  await page.getByRole("button", { name: rx("添加关联") }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("目标主线", { exact: true }).click();
  await page.locator(".ant-select-item-option-content", { hasText: "模型效果 · 对齐与更新" }).click();
  await dialog.getByLabel("满足条件", { exact: true }).fill("短句门禁通过后继续");
  await dialog.getByRole("button", { name: rx("保存") }).click();
  await expect(page.getByText("短句门禁通过后继续", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/relations");

  // Session: created and then opened through the resolved full locator.
  await page.getByRole("tab", { name: "主题对话" }).click();
  await page.getByRole("button", { name: rx("新建主题对话") }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("主题名称", { exact: true }).fill("发布交接");
  await dialog.getByRole("button", { name: rx("保存") }).click();
  await expect(page.getByText("发布交接", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/sessions");
  // The pre-seeded link resolves to its full locator, so the jump carries project_dir.
  await page.getByRole("button", { name: rx("打开对话") }).first().click();
  await expect(page).toHaveURL(/\/chat\/web\/wc-topic-1/);
  await expect(page).toHaveURL(/workspace_dir=%2Fsrv%2Fapp/);
});

test("keeps plan items in place and marks the stream seen", async ({ page }, info) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await page.goto("/work-desktop?context=ctx-1&tab=plan");
  await expect(page.getByText("还没有计划项；你和 Agent 都可以添加。", { exact: true })).toBeVisible();

  // Adding a plan item is a real write, not a client-side list.
  await page.getByRole("textbox", { name: "计划内容", exact: true }).fill("跑一轮固定参数评测");
  await page.getByRole("button", { name: rx("添加计划") }).click();
  await expect(page.getByText("跑一轮固定参数评测", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/todos");

  // Editing the item text happens in place (click the value, type, Enter).
  await page.getByRole("button", { name: /^计划内容/ }).click();
  const editor = page.getByRole("textbox", { name: "计划内容: 跑一轮固定参数评测", exact: true });
  await editor.fill("跑两轮固定参数评测");
  await editor.press("Enter");
  await expect(page.getByText("跑两轮固定参数评测", { exact: true })).toBeVisible();
  expect(store.writes.some((entry) => entry.startsWith("PATCH /v1/work-contexts/ctx-1/todos/"))).toBe(true);
  await page.screenshot({ path: `../../.godex/tmp/work-desktop-plan-${info.project.name}.png`, fullPage: true });

  // The overview surfaces what changed since last seen, and marking seen clears it.
  await page.getByRole("tab", { name: "概览" }).click();
  await expect(page.getByText("自上次查看以来的变动", { exact: true })).toBeVisible();
  await expect(page.getByText("尚未标记已读", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: rx("标记已读") }).click();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/seen");
  await expect(page.getByText("已把此主线标记为已读", { exact: true })).toBeVisible();
});

test("recalls facts by scope, reviews a candidate and submits a memory candidate", async ({ page }) => {
  const store = await install(page, seededStore());
  await page.goto("/work-desktop?context=ctx-1&tab=knowledge");
  await expect(page.getByText("退化可能来自解码参数", { exact: true })).toBeVisible();
  await expect(page.getByText("r12 整体改善但短句回归", { exact: true })).toHaveCount(0);

  await page.getByRole("combobox", { name: "检索范围", exact: true }).click();
  await page.getByText("当前及直接关联", { exact: true }).click();
  await expect(page.getByText("r12 整体改善但短句回归", { exact: true })).toBeVisible();

  await page.getByRole("textbox", { name: "搜索结论、关键词或历史线索", exact: true }).fill("参数");
  await expect(page.locator(".wd-knowledge")).toHaveCount(1);

  await page.getByRole("button", { name: rx("确认") }).click();
  await expect(page.locator(".wd-knowledge").getByText("已确认", { exact: true })).toBeVisible();
  expect(store.writes).toContain("PATCH /v1/work-contexts/ctx-1/facts/fact-1");
  await expect(page.getByRole("button", { name: rx("确认") })).toHaveCount(0);

  // Durable memory is only touched through the candidate inbox, as an explicit action.
  await page.getByRole("button", { name: rx("提交记忆候选") }).click();
  await expect(page.getByText("已在记忆候选箱", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/facts/fact-1/memory-candidate");
});

test("delegates to a real card without auto-running it, then executes and reviews on demand", async ({ page }, info) => {
  test.skip((page.viewportSize()?.width ?? 0) < 1024);
  const store = await install(page, seededStore());
  await page.goto("/work-desktop?context=ctx-1&tab=tasks");
  // The delegation boundary is a hover hint now (it used to occupy a full-width alert);
  // the icon still carries the full explanation as its accessible name.
  const delegHint = page.getByRole("note", { name: /委派会创建真实任务卡/ });
  await expect(delegHint).toHaveAttribute("aria-label", /委派保存即时输入快照（检查点版本、资源与开放点），任务状态以看板卡为准。$/);
  await expect(page.getByText("能从部署记录定位回滚目标", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: rx("委派工作") }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("委派目标", { exact: true }).fill("隔离模型与参数影响");
  await dialog.getByLabel("验收条件 · 每行一项", { exact: true }).fill("同一评测集与权重\n附命令与参数 hash");
  await dialog.getByRole("button", { name: rx("委派工作") }).click();
  await expect(page.getByText("隔离模型与参数影响", { exact: true })).toBeVisible();
  expect(store.writes).toContain("POST /v1/work-contexts/ctx-1/delegations");
  // Creating a delegation must NOT start an execution.
  expect(store.writes).not.toContain("POST /v1/taskboard/cards/card-2/execute");
  expect(store.cards["card-2"]!.status).toBe("in_review");

  // Manual accept reuses the existing board patch semantics.
  await page.getByRole("button", { name: rx("验收通过") }).last().click();
  await expect.poll(() => store.cards["card-2"]!.status).toBe("done");
  expect(store.writes).toContain("PATCH /v1/taskboard/cards/card-2");

  // Manual dispatch of the seeded, still-running card.
  await page.getByRole("button", { name: rx("派发执行") }).first().click();
  await expect.poll(() => store.cards["card-1"]!.status).toBe("in_progress");
  expect(store.writes).toContain("POST /v1/taskboard/cards/card-1/execute");
  await page.screenshot({ path: `../../.godex/tmp/work-desktop-tasks-${info.project.name}.png`, fullPage: true });
});

test("archives to read-only, resolves an unknown stream and surfaces write conflicts", async ({ page }) => {
  const store = await install(page, seededStore());
  await page.goto("/work-desktop?context=ctx-1");
  // Archive is a server write; the page then goes read-only until restored.
  page.once("dialog", (dialog) => void dialog.accept());
  await page.getByRole("button", { name: rx("归档主线") }).click();
  await expect.poll(() => store.contexts[0]!.status).toBe("archived");
  await expect(page.getByText("主线已归档，只读。先恢复再编辑。", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: rx("编辑检查点") })).toBeDisabled();
  await expect(page.getByRole("button", { name: rx("委派工作") })).toBeDisabled();
  await page.getByRole("button", { name: rx("恢复主线") }).click();
  await expect.poll(() => store.contexts[0]!.status).toBe("active");
  await expect(page.getByRole("button", { name: rx("编辑检查点") })).toBeEnabled();

  // A 409 is reported as a conflict, not as a silent success or a generic error.
  store.failNextPatch = true;
  await page.getByRole("button", { name: rx("暂停") }).click();
  await expect(page.getByText("数据已被其他更改更新，请刷新后重试。", { exact: true })).toBeVisible();

  // An unknown/forbidden stream states the problem instead of showing the wrong desktop.
  await page.goto("/work-desktop?context=does-not-exist&tab=not-a-tab");
  await expect(page.getByText("该桌面不存在或无权访问，已改为展示第一条主线。", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "语音服务 · 长期维护", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "概览", exact: true })).toHaveAttribute("aria-selected", "true");
});

test("opens without a configured provider and honours the English locale", async ({ page }) => {
  await install(page, seededStore());
  await page.route("**/api/providers", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ providers: [] }) }));
  await page.addInitScript(() => localStorage.setItem("godex:web:locale", "en"));
  await page.goto("/work-desktop?context=ctx-1");
  await expect(page.getByRole("heading", { name: "Speech service · operations", exact: false })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "语音服务 · 长期维护", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: rx("Restore context") })).toBeVisible();
  // The permanent store banner was removed to reclaim vertical space; the English
  // locale is now proven by the localized tab labels and the work-memory hint.
  await expect(page.getByRole("tab", { name: "Overview" })).toBeVisible();
  await expect(page.getByRole("note", { name: /AI maintenance/ })).toBeVisible();
});
