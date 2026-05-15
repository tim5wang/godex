# GoDex Usage Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an OpenAI-compatible Usage Gateway with proxy API keys, explicit model mappings, credit budgets, usage ledger, and a Web Usage app.

**Architecture:** Implement a backend `internal/services/usage` package with JSON storage, key hashing, model mapping, credit accounting, and summary aggregation. Expose management APIs under `/usage/*`, expose gateway API at `/v1/chat/completions`, and add a Web `Usage` app that consumes the management APIs. Keep MVP focused on Chat Completions and local storage.

**Tech Stack:** Go HTTP handlers and tests, existing GoDex config/model profile types, JSON local storage, React 19, TypeScript, Ant Design, TanStack Query.

---

## Scope

### In

- Proxy key create/list/update with one-time secret return.
- Proxy model create/list/update with `public_model`, `target_profile_id`, `target_model`, `credit_weight`, `enabled`.
- Usage ledger for gateway calls.
- Credit accounting:

```text
billable_tokens = input + output + cache_write + cache_read * 0.25
credits = billable_tokens * credit_weight
```

- Management APIs:
  - `GET /usage/keys`
  - `POST /usage/keys`
  - `PATCH /usage/keys/{id}`
  - `GET /usage/models`
  - `POST /usage/models`
  - `PATCH /usage/models/{id}`
  - `GET /usage/summary?range=day|week&api_key_id=...`
  - `GET /usage/calls?date=YYYY-MM-DD&api_key_id=...`
- Gateway API:
  - `POST /v1/chat/completions`
- Web Usage app:
  - API key panel
  - model mapping panel
  - token/credit summary
  - daily calls table

### Out

- `/v1/responses`, embeddings, images, files, assistants.
- Full tools/function-calling compatibility.
- Remote billing service.
- Plaintext proxy key storage.
- Batch key import/export.

## Task 1: Backend Usage Service Core

**Files:**
- Create: `internal/services/usage/types.go`
- Create: `internal/services/usage/store.go`
- Create: `internal/services/usage/service.go`
- Create: `internal/services/usage/service_test.go`

**Requirements:**

- Generate proxy keys with prefix `gdx_`.
- Store only SHA-256 hash and masked prefix.
- Verify presented keys against stored hashes.
- CRUD-like create/list/update for keys and model mappings.
- Record calls and aggregate summary by day/week.
- Enforce enabled, allowed models, model enabled, and budget.

**Acceptance tests:**

- Creating a key returns `Secret` once and stored/listed records do not expose it.
- Verifying the returned secret resolves the key.
- Disabled key is rejected.
- A key with `allowed_models=["fast"]` rejects `expensive`.
- Disabled model mapping is rejected.
- Credit calculation respects cache read multiplier and credit weight.
- Summary separates input/output/cache read/cache write and credits.

## Task 2: HTTP Management API

**Files:**
- Modify: `internal/runtime/httpapi/httpapi.go`
- Modify/Create tests in: `internal/runtime/httpapi/httpapi_test.go`
- Modify service wiring wherever HTTP API server dependencies are constructed.

**Requirements:**

- Add Usage service to HTTP server dependencies.
- Add Web-token protected routes:
  - `GET /usage/keys`
  - `POST /usage/keys`
  - `PATCH /usage/keys/{id}`
  - `GET /usage/models`
  - `POST /usage/models`
  - `PATCH /usage/models/{id}`
  - `GET /usage/summary`
  - `GET /usage/calls`
- Return JSON errors using existing httpapi error conventions.
- Never return key hashes.
- `POST /usage/keys` returns one-time `secret`.

**Acceptance tests:**

- Management routes require Web bearer token when auth is configured.
- Create key returns a `gdx_` secret once.
- Listing keys returns masked key but not secret or hash.
- Model mapping create/list/update works.
- Summary and calls endpoints return recorded ledger data.

## Task 3: OpenAI-Compatible Chat Completions Gateway

**Files:**
- Modify: `internal/runtime/httpapi/httpapi.go`
- Add tests in: `internal/runtime/httpapi/httpapi_test.go`
- Add helper code in `internal/services/usage` if needed.

**Requirements:**

- Add unauthenticated-by-Web-token route `POST /v1/chat/completions`.
- Authenticate only `Authorization: Bearer gdx_...`.
- Reject missing/invalid/disabled proxy key with OpenAI-style JSON error.
- Resolve `request.model` via usage model mappings.
- Reject disabled/unmapped/not-allowed models.
- Forward non-streaming request to target provider/profile when feasible through existing LLM layer.
- If direct provider forwarding is too large for MVP, implement a narrow gateway adapter with a test fake and return `501` only when no real provider adapter can be built. Do not block ledger/key/model/UI work on full provider coverage.
- Record successful and failed calls in ledger.
- Extract provider usage if returned; otherwise estimate tokens and mark `estimated=true`.

**Acceptance tests:**

- Missing bearer returns `401` OpenAI-style error.
- Invalid proxy key returns `401`.
- Disabled key returns `403`.
- Unmapped model returns `404`.
- Allowed fake provider call records usage and returns OpenAI-compatible response shape.
- Failed provider call records status/error.

## Task 4: Web API Client and Types

**Files:**
- Modify: `ui/web/src/lib/types.ts`
- Modify: `ui/web/src/lib/api.ts`
- Add optional tests under: `ui/web/test/usage*.test.ts`

**Requirements:**

- Add TypeScript types:
  - `UsageKey`
  - `UsageKeyCreateResponse`
  - `UsageModelMapping`
  - `UsageSummary`
  - `UsageCall`
- Add API functions:
  - `listUsageKeys`
  - `createUsageKey`
  - `updateUsageKey`
  - `listUsageModels`
  - `createUsageModel`
  - `updateUsageModel`
  - `getUsageSummary`
  - `listUsageCalls`

**Acceptance tests:**

- TypeScript build succeeds.
- API functions use the expected routes and methods.

## Task 5: Web Usage App

**Files:**
- Create: `ui/web/src/features/usage/UsagePage.tsx`
- Create: `ui/web/src/pages/UsagePage.tsx`
- Modify: `ui/web/src/app/appRegistry.tsx`
- Modify: `ui/web/src/i18n/messages.ts`
- Modify: `ui/web/src/styles.css`

**Requirements:**

- Add nav item `Usage`.
- Page sections:
  - API keys panel with create/edit budget/warning/enabled/allowed models and one-time secret display.
  - Model mappings panel with public model, target profile id, target model, credit weight, enabled.
  - Summary panel with day/week toggle and all-key/single-key selector.
  - Daily calls table with time, key, public model, target model/profile, input/output/cache tokens, weight, credits, status/error.
- Use existing Ant Design patterns.
- Do not expose key hashes.

**Acceptance tests:**

- `pnpm --dir ui/web build` succeeds.
- Usage app appears in builtin app registry.

## Task 6: Final Verification and Docs

**Files:**
- Modify: `docs/SPEC.md`
- Modify: `docs/SPEC.en.md`
- Keep: `docs/superpowers/specs/2026-05-14-godex-usage-gateway-design.md`
- Keep: `docs/superpowers/plans/2026-05-14-godex-usage-gateway.md`

**Verification:**

```bash
go test ./internal/services/usage -count=1
go test ./internal/runtime/httpapi -count=1
pnpm --dir ui/web build
go test ./...
git diff --check
```

**Commit plan:**

```bash
git add internal/services/usage internal/runtime/httpapi docs/superpowers/specs/2026-05-14-godex-usage-gateway-design.md docs/superpowers/plans/2026-05-14-godex-usage-gateway.md
git commit -m "feat(usage): add gateway ledger service"

git add ui/web/src ui/web/test
git commit -m "feat(web): add usage app"

git add docs/SPEC.md docs/SPEC.en.md
git commit -m "docs(spec): document usage gateway"
```

## Notes for Workers

- Do not commit `docs/superpowers/tmp/`.
- Keep worker concurrency low. One implementation worker at a time.
- If provider forwarding is too large, finish key/model/ledger/management/Web first and clearly mark gateway provider forwarding as adapter-limited with tests around fake provider behavior.
- Keep OpenAI-compatible errors small and deterministic.
