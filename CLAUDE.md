# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

CALM (Context Abstraction Layer for Models) is an HTTP service that reduces LLM token waste by filtering and compressing tool output before it enters the context window. It sits beside (never between) the workload and the LLM — a sidecar, not a proxy.

The full design is in `docs/HLD.md`. **The HLD is canonical.** Code follows the HLD; this file (CLAUDE.md) is the development directive that translates the HLD's intent into day-to-day rules.

## HLD is the design directive

- The HLD in `docs/HLD.md` drives the design. Code follows the HLD.
- Deviations in code are **temporary**, not canonical. Mark them with `// HLD-DEVIATION:` and a one-line reason; expect them to be reconciled.
- If the HLD is **silent or ambiguous** on something the code needs, **do NOT improvise**. Stop, surface the gap, propose a resolution, evolve the HLD, then implement. Order: design agreement → HLD → code.
- The HLD is a long-lived spec, not a historical artifact. Edits to it are normal and expected.
- The HLD stays language-agnostic. Implementation choices (Go, drivers, libraries, Postgres extension wiring) live here in `CLAUDE.md` and in the code, not in the HLD.

### Don't duplicate HLD content in this file

CLAUDE.md is the **development directive** — what disciplines apply when writing code, where things live, what patterns to follow. The HLD is the **spec** — what the system is. When CLAUDE.md restates HLD content as its own bullets (enumerating primitives, listing invariants, copying tables), that content goes stale the moment the HLD evolves and there's no automated check to catch the drift.

Rule: when you'd otherwise paraphrase or enumerate something from the HLD, write a one-line pointer to the HLD section instead. "See HLD §4" beats a six-bullet recap that has to be hand-synced. Reserve CLAUDE.md bullets for code-level guidance the HLD doesn't carry: file layout, mockery rules, comment policy, middleware-chain order, log levels, etc.

## API contract: OpenAPI is the formal source

- `docs/api/openapi.yaml` is the **canonical formal contract** for the HTTP API. The HLD §6 prose describes intent; this YAML pins the precise wire shape that code is generated from.
- Workflow: **design agreement → HLD prose → openapi.yaml → codegen → handlers**. Don't hand-write request/response types or routes — they come from the spec.
- Never edit files matching `*.gen.go` (currently `internal/api/genapi/genapi.gen.go`). Edit the YAML and run `task gen:api`.
- `task gen:check` (in `task ci`) re-runs codegen and fails if generated files drift from the committed tree. This catches "I changed the spec but forgot to commit the regenerated code."
- A naming convention exists to avoid colliding with oapi-codegen's client wrapper types (named `<OperationID>Response`): response-body schemas use the `Result` suffix (e.g., `IngestResult`, `SearchResult`). Don't name a schema `<X>Response` if there's an operation with `operationId: x`.
- Request validation is enforced by `internal/server/middleware/validation.go` against the embedded spec — required fields, enum values, formats, path/query types. It's the innermost middleware before handlers, so handlers can trust the parsed types.
- The OpenAPI spec also drives the `calm-adapter`'s HTTP client (generated `ClientWithResponses` in `internal/api/genapi`). Don't write hand-rolled HTTP requests against CALM from the adapter.

## Architecture (orientation only; HLD is authoritative)

Single statically-linked binary, REST API with JSON payloads, all state behind a DAL.

- **Three core primitives** (ingest, search, session state) — see HLD §4.
- **Workload patterns** identified by namespace + optional client — see HLD §3 / DL01.
- **Six design invariants** (never-worse, workload-agnostic, session-scoped, sidecar-not-proxy, content-fidelity, idempotent-indexing) — see HLD §4.
- **Storage**: Postgres in production, BM25 via `pg_search` or `pg_textsearch`, trigram via `pg_trgm` — see HLD §7 / DL11. The DAL (`internal/db`) is a mockery port for testability, **not** a portability layer; there is only one backend.

## Session isolation is the load-bearing invariant

Of the six invariants in HLD §4, **session isolation is the one that surfaces in every code change**. HLD §7 calls it out as a hard boundary, HLD §13 records the named decision, HLD §6 makes it observable (cross-namespace mismatch returns **404, not 403** — invisibility, not denial).

Treat it as a first-class property the codebase enforces, not just a fact. Most bugs that would quietly degrade CALM start as a missed `session_id` filter, a cache that wasn't session-keyed, or a "convenience" cross-session query.

Concrete disciplines that fall out of it:

- **Every DAL method takes a `session_id`** (or an input struct that carries one). See `internal/db/dal.go` — no exceptions. Even the management API (`/v1/manage/*`) is namespace-scoped, not session-scoped, and never returns another namespace's sessions.
- **Every domain function that touches per-session data takes `sessionID string` explicitly** — never pulled from ambient context. Makes the dependency visible at the call site and forces every caller to think about which session they mean.
- **Caches are session-keyed.** The search-result cache (HLD §11) keys by `session_id + query + source` and is invalidated on ingest into that session. Adding a cache without session-scoping is a bug, not a feature.
- **New tables FK to `sessions(session_id)` with `ON DELETE CASCADE`.** Cleanup-by-session (explicit close or TTL) is the only cleanup path; orphans are forbidden.
- **Cross-namespace mismatch returns 404.** Per HLD §6 — invisibility, not "you don't have access." The latter leaks existence of resources you can't see. The OpenAPI spec already encodes this.
- **Integration tests assert isolation explicitly.** Standard pattern: write to session A, read from session B, expect empty / 404. This is the durable proof of the invariant in CI.
- **Logging carries `session_id`** in every per-request log line. Use `obs.SessionID(...)`. Cross-session log entries (e.g., the TTL scanner) are explicitly logged without `session_id` so the absence is intentional, not a leak.

If you find yourself wanting a query that intentionally crosses session boundaries, stop. Either it belongs in `/v1/manage/*` (the only legitimate cross-session surface, still namespace-scoped), or it's a design gap that requires HLD discussion before code lands.

The other two invariants that generate code-level discipline (worth knowing, less code-pervasive than isolation): **never makes things worse** (#1 — adapter and workload middleware must catch CALM failures and fall through to raw content; the LLM call always works) and **content fidelity** (#5 — search snippets and ingest chunks return *exact* indexed text, never paraphrased or truncated). The remaining invariants are architectural and rarely surface as PR-review questions.

## Repo layout (where things live)

```
cmd/
  calm/                  service entry — thin: config, logger, deps, server.Run
  calm-adapter/          MCP adapter binary entry
internal/
  server/                HTTP lifecycle, middleware chain assembly, graceful shutdown
    middleware/          recovery, context, logging, auth, ratelimit, bodylimit, timeout
  api/
    routes.go            mounts the generated chi-aware handler tree
    handlers/            thin handlers implementing genapi.ServerInterface
    genapi/              generated from docs/api/openapi.yaml — DO NOT EDIT
  ingest/                chunking, format detection, intent filtering
  search/                3-layer fallback (porter → trigram → fuzzy)
  events/                event capture + priority-range validation (1–4)
  snapshot/              generic event-store snapshot builder (HLD DL08)
  session/               session lifecycle + TTL scanner
  db/                    DAL interface + Postgres impl (DAL is a mockery port for testability)
  auth/                  API-key registry + namespace resolver
  obs/                   logging init, OTel wiring, CALM-specific field helpers
  config/                env-driven config loader
adapter/                 packages used only by cmd/calm-adapter
  mcp/                   stdio MCP protocol
  extract/               event extraction from tool calls
  exec/                  local subprocess execution (developer machine)
test/integration/        user-facing scenarios per HLD §3 / §5
```

**Layered responsibility:**

- `cmd/<bin>/main.go`: thin (~30–60 lines). Config load, logger init, dependency wiring, hand off to `server.Run(ctx)`.
- `internal/server`: HTTP lifecycle. Knows nothing about specific routes or domain logic.
- `internal/api/handlers`: handlers stay **thin** — parse request → call into domain package → marshal response. **Never put business logic in handlers** — it goes in the domain packages.
- `internal/{ingest,search,events,snapshot,session}`: domain logic. Most integration tests target these.
- `internal/db`: DAL is a port. Mockery generates against it.

## Build / test / run discipline

- All reproducible operations go through `task`: `task build`, `task test`, `task test:unit`, `task test:integration`, `task lint`, `task fmt`, `task tidy`, `task ci`, `task run:local`, `task docker:build`, `task gen:api`, `task gen:mocks`, `task gen:check`.
- `task test` runs unit then integration. Inner-loop dev: `task test:unit` for fast feedback. CI runs the umbrella via `task ci`.
- Inner-loop dev: `go test ./<pkg>` and `go vet ./...` are fine. Anything whose result must match across machines (CI, full builds, deploys, lint, format) goes through `task`.
- Never edit `go.mod` by hand for routine adds — `task tidy` is the entry.
- `task ci` is the gate: lint + test + build, all green.

## HTTP server boundaries (canonical — do not drift)

- `internal/server` owns the HTTP server lifecycle (listener, middleware chain, graceful shutdown). No route or domain code here.
- `internal/api` owns handlers and DTOs. Handlers parse → domain call → marshal. No business logic.
- **Middleware chain order is canonical**: do not reorder without an HLD discussion.

  ```
  Recovery
    → Context (RequestID + OTel trace extraction)
      → Logging (start log + on-completion summary)
        → Auth (API key → namespace; HLD §6)
          → RateLimit (per-namespace, HLD §11 → 429)
            → BodySizeLimit (1MB cap, HLD §11 → 413)
              → Timeout (per-request, HLD §11 budgets)
                → OpenAPIValidator (kin-openapi against embedded spec)
                  → Handler
  ```

- **Recovery** is outermost and holds a reference to the base logger so it can record panics even if `ctx` was never hydrated by Context middleware.
- **Context** must run before Logging so every log line carries `request_id` and trace IDs.
- **Logging** runs before Auth so failed-auth attempts are still recorded with full context.
- **Auth** must run before RateLimit (rate limit is per-namespace).
- **BodySizeLimit** before Timeout — rejecting an oversized body shouldn't consume the timeout budget.

## Logging > comments

Logs are not optional decoration; they're the runtime record.

- Use `github.com/one-harsh/context-logging` everywhere. **Pass `ctx context.Context` as the first parameter** of every non-trivial function. Hydrate the context with request-scoped fields as early as possible (HTTP middleware, MCP adapter entry).
- Pattern: `logger.WithContext(ctx).Info("event_name", fields...)`. Per-request summary via `logger.SummaryWithContext(ctx).Info(...)` at the end of a handler — drives end-of-request observability.
- Domain-specific field helpers live in `internal/obs` (e.g., `obs.SessionID`, `obs.Source`, `obs.Namespace`, `obs.Client`, `obs.MatchLayer`, `obs.Endpoint`, `obs.EventType`, `obs.FormatHint`). Add new helpers there, not at call sites.
- **DEBUG**: log liberally. If you're tempted to write a comment describing state or flow, write a DEBUG log instead. Log characters are free; in-prod cost is near-zero with the level disabled.
- **INFO**: follow HLD §10's enumerated set. Adding a new INFO-level event is an HLD-touching change.
- **WARN**: degraded behavior the operator should know about (CALM-down fallback in adapter, intent fallback to full summary, etc.).
- **ERROR / FATAL**: actual failures.

## Comments — when they're allowed

A comment is allowed only when it documents a **non-obvious business or design constraint** that forces the shape of the code: an HLD invariant being enforced at a specific line, a workaround for a known incident, a subtle ordering requirement.

- Format: keep short, cite the source — `// HLD §6 invariant 1: never block on CALM failure`.
- Don't restate what the code obviously does. Don't reference the current task or the PR.
- Heuristic: if the comment would still be true after a refactor that changed *what* the code does, it's the wrong comment — delete it or convert to a DEBUG log.

## Testing

- Test as much as possible. Coverage isn't a target, but every non-trivial path should have a test.
- **Integration tests are full-loop scenarios** named after what CALM promises — `IngestAndSearch`, `SessionBreachNotAllowed`, `IdempotentReingest`, `CrossNamespaceInvisible404`. Frame each test as "workload X does Y, expects Z" — not "function F returns G." A reader scanning test names should see the project's promises enumerated. Scenarios live in `test/integration/` and run via the harness against the real generated client. 
- **Run integration tests against real Postgres** (with `pg_search`, per HLD §7). The developer brings Postgres up explicitly via `docker compose up` before running the suite — no programmatic container management inside tests. Tests connect to a known location (env var or default), fail clearly if Postgres isn't reachable. Mocking the DB hides bugs that bite in prod migrations. Unit tests that don't need the DB use the mockery-generated DAL mock instead.
- **No hand-rolled mocks.** Use `mockery` for all mocks. Mockery generates only against **port interfaces** (DAL, MCP transport, clock, FTS extension capability — narrow set, expanded only at real boundaries). Internal helpers stay concrete and are tested via integration.
- **Mocks are in-package**: the generated `mock_<name>.go` lives in the same Go package as the interface. Build-tagged `//go:build mocks` so prod binaries exclude it. Tests and CI run with `-tags=mocks` (already wired in `Taskfile.yml`).
- Hand-rolled mocks are allowed only when mockery genuinely cannot express the scenario. State the reason in a `// MOCKERY-ESCAPE:` comment.
- Don't introduce interfaces just to enable mocking. If a struct doesn't sit at a port boundary, it doesn't need an interface — test it through integration.

## Tooling

- **Go**: pinned via `.go-version` (goenv). Currently 1.25.5.
- **Build/run**: `go-task` via `Taskfile.yml`.
- **Lint**: `golangci-lint v2` via `.golangci.yaml`. Revive's "must have comment on exported X" and "unused-parameter" rules are intentionally disabled — they collide with the comment policy and with stub method signatures matching interfaces. Generated files (`*.gen.go`, `internal/api/genapi/*`) are excluded from lint.
- **Format**: `gofumpt` + `goimports` (run via `task fmt`).
- **Mocks**: `mockery v2` via `.mockery.yaml`. In-package, build-tagged `mocks`.
- **API codegen**: `oapi-codegen v2` via `oapi-codegen.yaml`. Drives types, chi server interface, HTTP client, and embedded spec from `docs/api/openapi.yaml`. Run with `task gen:api`.
- **Validation**: `github.com/getkin/kin-openapi` + `github.com/oapi-codegen/nethttp-middleware` for request validation against the embedded spec.
- **Logging**: `github.com/one-harsh/context-logging` (zap-wrapped, context-bound).
- **HTTP routing**: `github.com/go-chi/chi/v5`.
- **Storage**: `pgx/v5` for Postgres. **No CGO** — preserves the static-binary property in HLD §12.
- **OTel**: `go.opentelemetry.io/otel` for trace propagation; OTLP exporter when wired.
- **License**: Apache 2.0. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the contributor contract — DCO sign-off, SPDX headers on new Go files, AGPL-free dependency policy.

Install local dev tools with `task tools:install`.

## Misc

- Keep `pkg/` empty. Everything is `internal/` until something is genuinely meant for external import.
- Default namespace / default client / master-key bootstrap behavior — see HLD §6 + DL01.
