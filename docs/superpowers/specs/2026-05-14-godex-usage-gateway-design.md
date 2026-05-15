# GoDex Usage Gateway Design

## Summary

GoDex Usage Gateway adds an OpenAI-compatible proxy surface for external clients. A client calls GoDex with a generated proxy API key, GoDex authenticates that key, maps the public model name to a configured provider/profile/model, forwards the request, and records usage for budgets and monitoring.

This is not just a Web dashboard. The first release must produce real usage data from gateway calls, then expose it in a new Web app named `Usage`.

## Product Goals

- Let users create GoDex proxy API keys for external clients.
- Let each key define a credit budget, warning threshold, enabled state, and allowed public models.
- Let users define public model mappings independent of existing Chat UI model profile selection.
- Provide OpenAI-compatible `POST /v1/chat/completions` for external clients.
- Track token usage and weighted credits by API key, model, day, and week.
- Show day-level call details with model, token split, cache tokens, status, latency, and errors.

## Non-Goals

- No full OpenAI API compatibility beyond `POST /v1/chat/completions`.
- No `/v1/responses`, embeddings, images, files, assistants, or tools/function-calling compatibility in this phase.
- No distributed billing backend.
- No real money settlement.
- No direct reuse of the active Chat page model selection; gateway models are configured separately.
- No plaintext storage of generated proxy keys after creation.

## Concepts

### Proxy API Key

A proxy key is a GoDex-issued key with prefix `gdx_`. It is shown once at creation time. GoDex stores only a hash and a short masked form.

Fields:

- `id`
- `name`
- `key_hash`
- `key_prefix`
- `enabled`
- `budget_credits`
- `warning_threshold`
- `allowed_models`
- `created_at`
- `updated_at`

### Proxy Model

A proxy model maps the public model name seen by external clients to a real configured provider profile and model.

Fields:

- `id`
- `public_model`
- `target_profile_id`
- `target_model`
- `credit_weight`
- `enabled`
- `created_at`
- `updated_at`

The same `target_profile_id` may be used by normal GoDex chat, but the gateway does not automatically expose all chat profiles. A model must be explicitly mapped before external clients can use it.

### Usage Call

Every accepted gateway call records:

- `id`
- `timestamp`
- `api_key_id`
- `public_model`
- `target_profile_id`
- `target_model`
- `input_tokens`
- `output_tokens`
- `cache_read_tokens`
- `cache_write_tokens`
- `billable_tokens`
- `credit_weight`
- `credits`
- `estimated`
- `status`
- `error`
- `latency_ms`

## Credit Accounting

Budget is measured in credits, not currency. The MVP formula is:

```text
billable_tokens =
  input_tokens
  + output_tokens
  + cache_write_tokens
  + cache_read_tokens * 0.25

credits = billable_tokens * credit_weight
```

If a provider returns exact usage fields, GoDex records exact token values. If not, GoDex records estimated token values and sets `estimated=true`.

Budget enforcement happens before and after a call:

- Before call: reject if the key is disabled, model not allowed, model disabled, or current credits already exceed budget.
- After call: record usage even when provider returns an error; successful token usage is charged if available.

## HTTP API

### OpenAI-Compatible Gateway

```text
POST /v1/chat/completions
Authorization: Bearer gdx_...
```

Supported request fields:

- `model`
- `messages`
- `temperature`
- `max_tokens`
- `stream`

MVP response shape follows OpenAI Chat Completions for non-streaming. Streaming may be passed through as Server-Sent Events when the target provider supports it; otherwise gateway returns a clear OpenAI-style error.

### Management API

All management APIs use the existing Web bearer token, not proxy keys.

```text
GET    /usage/keys
POST   /usage/keys
PATCH  /usage/keys/{id}

GET    /usage/models
POST   /usage/models
PATCH  /usage/models/{id}

GET    /usage/summary?range=day|week&api_key_id=...
GET    /usage/calls?date=YYYY-MM-DD&api_key_id=...
```

## Web Usage App

Add a new built-in app:

- nav label: `Usage`
- route: `/usage`
- page: `ui/web/src/features/usage/UsagePage.tsx`

The first screen has four dense operational areas:

- API keys: create key, copy one-time secret, edit budget/warning/enabled/allowed models.
- Model mappings: public model, target profile, target model, credit weight, enabled.
- Token and credit stats: day/week, all keys or one key, input/output/cache/non-cache, credits.
- Calls table: one date, time, key, public model, target model/profile, token split, credit weight, credits, status/error.

## Security

- Proxy keys must never be stored plaintext.
- Created key secret is returned once.
- Management endpoints require existing Web auth.
- Gateway endpoints must not accept the Web token as a proxy key.
- Logs and Web UI display only masked keys.

## Storage

MVP storage can be local JSON under `cfg.StateDir/usage-gateway.json`. The store must be encapsulated behind a service interface so a later SQLite backend can replace it without changing handlers or Web API contracts.

## Testing

- Unit tests for key generation/hash verification, model mapping, budget enforcement, credit accounting, and summary aggregation.
- HTTP tests for management endpoints and OpenAI-compatible unauthorized/authorized calls.
- Web TypeScript tests for usage summary transformation where practical.
- `go test ./internal/services/usage ./internal/runtime/httpapi -count=1`
- `pnpm --dir ui/web build`
- `go test ./...`

## Open Questions Deferred

- Exact provider usage field coverage by provider type.
- Full streaming compatibility semantics.
- Per-model input/output/cache price tables.
- Multi-tenant remote deployment.
