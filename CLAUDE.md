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

Rule: when you'd otherwise paraphrase or enumerate something from the HLD, write a one-line pointer instead. "See HLD's design-invariants section" beats a six-bullet recap that has to be hand-synced. Reserve CLAUDE.md bullets for code-level guidance the HLD doesn't carry: file layout, mockery rules, comment policy, middleware-chain order, log levels, etc.

### Cite by stable name, not section number

HLD section numbers (`§3`, `§6`, `§11`) are positional — the moment the HLD is reorganized, every `HLD §11` reference in code or docs rots silently and nothing catches the drift. The fix is to cite by stable name:

- **Invariants** — use the name (`never-worse`, `workload-agnostic`, `namespace-isolation`, `session-isolation`, `sidecar-not-proxy`, `content-fidelity`, `idempotent-indexing`), not "HLD §4 invariant N". `namespace-isolation` and `session-isolation` are the two halves of the one isolation invariant; cite whichever half applies to the discussion.
- **Topical sections** — name the topic ("HLD's storage section", "HLD's API contract section"), not "HLD §7".
- **Decision Log entries** (`DL01`, `DL08`, …) — cite directly. DL IDs are append-only and stable.

Applies to CLAUDE.md, AGENTS.md, code comments, doc comments, Taskfile descs, anywhere. The OpenAPI spec is exempt — its descriptions ship as the public API contract and follow their own rev policy.

### Canonical surfaces describe the design, not the history

The HLD, OpenAPI spec, code, and CLAUDE.md discipline bullets describe **what CALM is** — in present tense, self-contained, reimplementable from scratch by a reader who knows none of the project history. They do not describe the sequence of changes that produced the current shape.

Concretely, never write into these surfaces:

- **Project-internal planning labels** — phase numbers, work-item IDs, milestone names, sprint labels. These are scaffolding for the work in flight; a reader picking up the HLD a year later has no map for them.
- **Transition narratives** — "previously X, now Y", "since the auth rewrite", "after the schema flattening". The contract that survives is "Y"; the "previously X" rots into nostalgia.
- **Cause-and-effect framings tied to historical events** — "because the WI-09 audit found...", "this was changed because of incident-2026-04". The discipline survives, the framing doesn't.

Equivalent reasoning belongs in: commit messages (history of the diff), planning bookkeeping docs, PR descriptions (review context), plan files (in-flight design work). Those surfaces are explicitly historical — they're allowed to age out.

When you'd otherwise write "Phase 2 reshaped X to Y", write "X is Y" (present-tense, no transition). If the *reason* X-is-Y matters to a reimplementer, write the reason ("X is Y because Z"). If the reason is just "we used to do it differently and changed our minds," it doesn't belong in canonical surfaces at all.

### HLD describes the design, not the implementation

The HLD is the **driver**; the code is the **passenger**. The HLD specifies what CALM is — its entities, contracts, primitives, invariants, storage shape — in language that a reimplementer in any stack could read and rebuild from. The code is one implementation that follows the HLD. The HLD doesn't know or care that the implementation happens to be Go.

This means the HLD must not reference implementation artifacts:

- **Code symbols** — package names (`internal/secrets`, `internal/db`), file paths, type names, method names (`Service.Create`, `Search()`), struct field identifiers as code.
- **Language/stack-specific concepts** — Go interfaces, struct tags, build tags, generics, the word "interface" when meaning a Go interface rather than the design-level extensibility concept.
- **Tooling and libraries** — `mockery`, `oapi-codegen`, `pgx`, `viper`, `chi`, `singleflight`, lint/format tool names.
- **Implementation patterns named in code** — middleware function names, error sentinel names, mock generator outputs.

The HLD **may** reference:

- **External wire contract** — HTTP paths, headers, JSON field names, status codes. This is the customer contract; both HLD and code follow it by definition.
- **Storage schema** — table/column names, SQL types, indexes, constraints. The data model is part of the design; the SQL is one precise notation for it (an ERD would do equally well).
- **External standards and dependencies** — Postgres, BM25, RRF, sha256, TLS, OpenTelemetry, Kubernetes, MCP, JSON. These exist independently of CALM's implementation.
- **External-extension surface** — `pg_search`, `pg_textsearch`, `pg_trgm`, `fuzzystrmatch`. These are operator-visible deployment dependencies, not internal code.

The rule of thumb: if a fact about CALM survives a rewrite in a different language by a different team, it belongs in the HLD. If it depends on this codebase's specific shape — package layout, type names, library choices, lint rules — it belongs in CLAUDE.md or in the code itself, not in the HLD.

When in doubt, re-read the HLD passage and ask: "Could a team writing CALM in Rust / Python / TypeScript from scratch read this and produce a system compatible with this one's wire contract and data model?" If yes, the passage is at the right level. If a Rust team would have to reverse-engineer your Go-specific reference, the passage is leaking implementation into the spec.

## API contract: OpenAPI is the formal source

- `docs/api/openapi.yaml` is the **canonical formal contract** for the HTTP API. The HLD's API contract section describes intent in prose; this YAML pins the precise wire shape that code is generated from.
- Workflow: **design agreement → HLD prose → openapi.yaml → codegen → handlers**. Don't hand-write request/response types or routes — they come from the spec.
- Never edit files matching `*.gen.go` (currently `internal/api/genapi/genapi.gen.go`). Edit the YAML and run `task gen:api`.
- `task gen:check` (in `task ci`) re-runs codegen and fails if generated files drift from the committed tree. This catches "I changed the spec but forgot to commit the regenerated code."
- A naming convention exists to avoid colliding with oapi-codegen's client wrapper types (named `<OperationID>Response`): response-body schemas use the `Result` suffix (e.g., `IngestResult`, `SearchResult`). Don't name a schema `<X>Response` if there's an operation with `operationId: x`.
- Request validation is enforced by `internal/server/middleware/validation.go` against the embedded spec — required fields, enum values, formats, path/query types. It's the innermost middleware before handlers, so handlers can trust the parsed types.
- The OpenAPI spec also drives the `calm-adapter`'s HTTP client (generated `ClientWithResponses` in `internal/api/genapi`). Don't write hand-rolled HTTP requests against CALM from the adapter.

## Architecture (orientation only; HLD is authoritative)

Single statically-linked binary, REST API with JSON payloads, all state behind a DAL.

- **Three core primitives** (ingest, search, session state) — see HLD's primitives section.
- **Workload patterns** identified by namespace + optional client — see DL01.
- **Six design invariants** — `never-worse`, `workload-agnostic`, the two-layer isolation invariant (`namespace-isolation` for the security/trust boundary + `session-isolation` for the content/scope boundary), `sidecar-not-proxy`, `content-fidelity`, `idempotent-indexing`. See HLD's design-invariants section.
- **Storage**: Postgres in production, BM25 via `pg_search` or `pg_textsearch`, trigram via `pg_trgm` — see DL11. The DAL (`internal/db`) is a mockery port for testability, **not** a portability layer; there is only one backend.

## Isolation is two boundaries, not one — namespace is security, session is content

CALM has two distinct isolation primitives, both load-bearing, that enforce different boundaries at different layers. Code reviews and PR design should treat them as two separate disciplines that fail in different ways.

**Namespace-isolation enforces the security/trust boundary.** Cross-namespace queries are forbidden; mismatch returns 404 (invisibility-not-denial); every per-request log line carries `namespace`. The wall between cooperating-but-distinct workload patterns (the eval harness, the slackbot, the coding agents). **Bugs here are confidentiality breaches** — data crosses trust units.

**Session-isolation enforces the content/scope boundary.** Per-session data (chunks, sources, events, labels, vocabulary) is bound to a session and invisible to other sessions in the same namespace; caches are session-keyed; search returns only this session's content; snapshots return only this session's events. The wall between workload-units inside a namespace. **Bugs here are workload-contract violations** — the LLM context window gets contaminated; CALM's value proposition fails. Session is *not* a cleanup primitive or observability artifact; it's the content boundary that defines what each workload-unit sees of its own data.

**Client-isolation is the optional third layer.** When `require_client_credentials: false` (the default), `client` is workload-supplied metadata — any holder of the namespace API key can claim any client. When `require_client_credentials: true`, each client is registered with a server-minted bearer token and the auth middleware verifies it; within-namespace workload isolation becomes a real boundary. This is the layer that lets shared-namespace tenants (e.g., `eval-shared` for a dev team) actually isolate from each other without requiring an operator to mint a new namespace per workload. The discipline applies only when the namespace opts in; the default code path treats `client` as a tag.

Most bugs that quietly degrade CALM start as a missed `namespace` or session-lookup-key filter, a cache that wasn't session-keyed, or a "convenience" cross-{namespace,session} query.

**Three-term terminology** (introduced when the session credential moved server-side). Never blur these — names map to types, and the type system catches confusion:

- `session_token` (`string`, ~43 chars base64url) — raw secret. Server mints; workload presents on every call via `X-CALM-Session-Token`. Never in logs, URLs, mgmt responses, or DB columns.
- `session_id` / `Session.ID` (`int64`) — surrogate BIGSERIAL PK. The only "id" of a session. Non-secret. Safe to log.
- `session_token_hash` (`[]byte`, 32) — `sha256(namespace || 0x00 || session_token)`. Storage form and auth-side lookup key. Never logged.

Concrete disciplines that fall out:

- **Every DAL method that touches per-session data takes `namespace` + the session lookup key explicitly** (or an input struct that carries them). The lookup key is `sessionTokenHash []byte` on the handler path; `sessionID int64` on the TTL scanner path. See `internal/db/dal.go` — no exceptions. Even the management API (`/v1/manage/*`) is namespace-scoped, never returning another namespace's sessions. The TTL scanner returns `(id, namespace)` pairs and feeds them back into the namespace-scoped delete path — one cascade semantics, two entry points distinguished only by lookup-key type.
- **Every domain function that touches per-session data takes `namespace` and the session credential explicitly** — never pulled from ambient context. Service layer takes `sessionToken string` (raw); it hashes at its boundary so the DAL only ever sees the hash. Makes both dependencies visible at the call site.
- **Caches are keyed by `(namespace, session_token)` jointly.** The search-result cache keys by `namespace + session_token + query + source` and is invalidated on ingest into that session. Adding a cache without joint scoping is a bug, not a feature.
- **Tables that hold per-session data FK on `sessions(id) ON DELETE CASCADE`** — the BIGSERIAL surrogate, namespace-stamped at session creation. Children don't carry their own namespace column; the FK chain preserves namespace scope because each surrogate id refers to exactly one session in exactly one namespace. Cleanup-by-session is the only cleanup path; orphans are forbidden. The DAL surface still enforces namespace at the API boundary (Get/Touch/Delete take `(namespace, lookup-key)` explicitly) — child tables don't need a redundant column to repeat that guard.
- **`sessions`-table composite indexes lead with `namespace`** for cache locality. Filter-by-label queries on child tables join through `sessions` for namespace scope rather than carrying a redundant predicate.
- **Cross-namespace mismatch returns 404.** Invisibility, not "you don't have access." The OpenAPI spec encodes this.
- **Integration tests assert both isolations explicitly.** Standard patterns: write to namespace A, read from namespace B → 404 (security); write to session-A in namespace X, read from session-B in namespace X → empty (content). Both are durable proofs in CI.
- **Logging carries `namespace` and `session.id` (the int64 surrogate)** in every per-request log line. Use `obs.Namespace(...)` and `obs.SessionID(...)` (the latter takes `int64`). The raw `session_token` has no logging helper by design — it's a credential. Cross-session log entries (e.g., the TTL scanner before it has a specific ref in hand) are explicitly logged without `session.id` so the absence is intentional, not a leak.

If you find yourself wanting a query that intentionally crosses session boundaries within a namespace, stop. Either it belongs in `/v1/manage/*` (the only legitimate cross-session surface, still namespace-scoped), or it's a design gap that requires HLD discussion before code lands. Cross-namespace queries should never appear in code at all outside the TTL scanner.

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
test/integration/        user-facing scenarios — see HLD's workload-scenarios section
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
        → RateLimit:IP (per-IP, pre-auth → 429)
          → Auth (API key → namespace)
            → RateLimit:NS+Global (per-namespace + global aggregate → 429)
              → BodySizeLimit (1MB cap → 413)
                → Timeout (per-request budgets)
                  → OpenAPIValidator (kin-openapi against embedded spec)
                    → SessionResolve (X-CALM-Session-Token → SessionMetadata; post-handler Touch on 2xx)
                      → Handler
  ```

- **Recovery** is outermost and holds a reference to the base logger so it can record panics even if `ctx` was never hydrated by Context middleware.
- **Context** must run before Logging so every log line carries `request_id` and trace IDs.
- **Logging** runs before Auth so failed-auth attempts are still recorded with full context.
- **RateLimit:IP** sits before Auth so unauthenticated DDoS can't burn registry-lookup CPU on every bad-key attempt.
- **Auth** must run before RateLimit:NS+Global (the namespace tier reads the auth-stamped namespace from context).
- **RateLimit:NS+Global** checks namespace tier first, then global aggregate. Namespace-first is load-bearing for namespace-isolation: with global-first, a misbehaving namespace would burn shared global tokens on requests it was always going to 429 at its own tier, leaking overload pressure across the isolation boundary.
- **BodySizeLimit** before Timeout — rejecting an oversized body shouldn't consume the timeout budget.
- **SessionResolve** is presence-based on `X-CALM-Session-Token`: when set, it calls `session.Service.Lookup` (404 on miss or cross-namespace), stuffs `SessionMetadata` into context via `session.WithMetadata`, and best-effort `Touch`es after 2xx. Sits after OpenAPIValidator so the validator catches missing-required-header before a DB lookup. Handlers read via `session.MetadataFromContext(ctx)` — they never touch the raw token.

## Transactions live in services, not the DAL

- DAL repo methods are **transaction-agnostic** and never open their own transaction (no `BeginTx`/`inTx`); they run on the injected `queryer` (`*sql.DB` or `*sql.Tx`). **Write** methods are single SQL statements (INSERT / UPDATE / DELETE / UPSERT — a bulk multi-row INSERT or an UPSERT-RETURNING is still one statement); a multi-step write — even a same-aggregate "replace" (delete-then-insert) or a lock → count → delete cascade — is NOT a DAL method, it's composed in the service's `WithTx`. **Read/query** methods return data (a SELECT, or the same SELECT fanned over inputs as in multi-query search) and likewise open no transaction.
- **Services own transaction boundaries and the orchestration.** To make multiple primitives atomic, a service wraps them in `store.WithTx(ctx, func(db.Repos) error { … })`, so the choreography and rollback surface are legible at the service layer — you don't read SQL to learn what's atomic, or that a re-ingest clears prior chunks. This is also the seam where distributed coordination (outbox/saga) will land. `db.Repos` is a struct: access fields directly inside the closure (`r.Sources.Upsert`, not `r.Sources()`).
- `WithTx` is the only sanctioned transaction entry point; `inTx` is its private engine. Error handling stays clean: `WithTx`/`inTx` return the closure's error verbatim, so sentinels propagate to the handler's `mapXError` unchanged; error-wrapping stays in the granular repo methods.
- Reads and single-statement writes need no transaction — handlers call the DAL directly; don't add a forward-only service.
- Repos are aggregate-scoped, not strictly table-scoped: `SourcesRepo` owns the source/content surface (Upsert, List, and `Search` — the workload's content query, which reads chunks because chunks belong to the source aggregate), while `ChunksRepo` holds the chunk-row write primitives (`DeleteForSource`, `Insert`) that ingest composes. Atomicity comes from `WithTx`, not from co-locating ops on one repo.
- **Validation locus:** business-rule validation (ranges, required fields, non-empty) lives in the service; the DAL keeps structural/domain sentinels (FK/PK/constraint → sentinel) and the namespace-isolation guard.
- **Namespace-isolation folds into the statement that touches the data — one canonical guard, no separate verify step.** The `sessions`/`clients` surface predicates on namespace directly; child tables (sources/events) reach it with an inline `EXISTS (SELECT 1 FROM sessions WHERE id = <session_id> AND namespace = …)` — a `WHERE` predicate on reads, inside the `INSERT … SELECT … WHERE EXISTS` on writes. EXISTS, not a JOIN: it's the one form that fits reads and writes alike (you can't `JOIN` an `INSERT`) and avoids column-name collisions with `session_events`. Cross-namespace collapses to the table's natural no-match: empty for list/search reads (consistent with `sessionRepo.List`), `ErrSessionNotFound` for point lookups/writes via no-rows-returned (consistent with `Get`/`Create`). Defense-in-depth only — `SessionResolve` 404s cross-namespace before the handler. The chunk leaf-write primitives (`DeleteForSource`, `Insert`) are the deliberate exception: they key off a `source_id` minted only by the namespace-verified `Upsert` in the same transaction — a capability, like the FK-cascade children on session delete — so re-guarding would diverge from the cascade pattern and burden the thinnest primitive. (`verifySessionInNamespace` is a transitional shim for `eventsRepo.Write` only.)
- **Rollout is incremental** (DL: services-own-tx). `sourcesRepo.Index` migrated (→ `ingest.Service`); `eventsRepo.Write`, `sessionRepo.{Create,Delete,DeleteByID,DeleteAll}`, `clientRepo.Delete` still open their own `inTx` until migrated. Until then `inTx` has those callers in addition to `WithTx`.

## Logging > comments

Logs are not optional decoration; they're the runtime record.

- Use `github.com/one-harsh/context-logging` everywhere. **Pass `ctx context.Context` as the first parameter** of every non-trivial function. Hydrate the context with request-scoped fields as early as possible (HTTP middleware, MCP adapter entry).
- Pattern: `logger.WithContext(ctx).Info("event_name", fields...)`. Per-request summary via `logger.SummaryWithContext(ctx).Info(...)` at the end of a handler — drives end-of-request observability.
- Domain-specific field helpers live in `internal/obs`. Two shapes, picked by what the value is:
  - **Per-call helpers** (`obs.SessionID(id)`, `obs.Namespace(ns)`, `obs.Client(c)`, `obs.Source(s)`, `obs.Endpoint(p)`, `obs.EventType(t)`, `obs.FormatHint(h)`) — for caller-supplied values obs can't know in advance (workload-chosen session IDs, request paths, etc.).
  - **Pre-constructed `LoggingField` vars** (`obs.MatchLayerPrimary`, `obs.CloseReasonTTLExpired`, ...) — for code-determined closed-enum values. Call sites pass the named constant directly; the type system enforces the documented value set. Add a new value as another `var` in the same group; add a new closed-enum *field* by adding a `Key…` constant and a new var group.
- **Field naming convention**: identifier/categorical fields are flat snake_case (`session_id`, `namespace`, `close_reason`); measurement fields use hierarchical dotted namespacing scoped to the entity + action that produced them (`session.delete.cascaded_events`, `sessions.scanned`, `ttl_scan.duration_ms`). The hierarchy lets downstream tools group on the path — "everything under `session.delete.*`" or "all `sessions.*` counters." Counts of how many entities were touched use the plural form (`sessions.scanned`); per-action measurements scoped to a single entity use the singular (`session.delete.cascaded_events`). Generic timings still get the namespace — `ttl_scan.duration_ms`, not bare `duration_ms`.
- **Metric names follow the same dotted schema** as log fields (`session.create.ttl_clamped`, `sessions.active`, `ttl_scanner.last_run`). The OTel-Prometheus exporter converts `.` → `_` at emission per the OTel-Prometheus mapping spec, so PromQL queries see the underscored form. No `calm_` prefix in canonical names; the OTel `service.name` resource attribute (`calm`) carries that scoping.
- **DEBUG**: log liberally. If you're tempted to write a comment describing state or flow, write a DEBUG log instead. Log characters are free; in-prod cost is near-zero with the level disabled.
- **INFO**: follow HLD's INFO-event taxonomy. Adding a new INFO-level event is an HLD-touching change.
- **WARN**: degraded behavior the operator should know about (CALM-down fallback in adapter, intent fallback to full summary, etc.).
- **ERROR / FATAL**: actual failures.

## Comments — when they're allowed

A comment is allowed only when it documents a **non-obvious business or design constraint** that forces the shape of the code: an HLD invariant being enforced at a specific line, a workaround for a known incident, a subtle ordering requirement.

- Format: keep short, cite the source by stable name — `// never-worse invariant: never block on CALM failure`.
- Don't restate what the code obviously does. Don't reference the current task or the PR.
- Heuristic: if the comment would still be true after a refactor that changed *what* the code does, it's the wrong comment — delete it or convert to a DEBUG log.

## Testing

- Test as much as possible. Coverage isn't a target, but every non-trivial path should have a test.
- **Integration tests are full-loop scenarios** named after what CALM promises — `IngestAndSearch`, `SessionBreachNotAllowed`, `IdempotentReingest`, `CrossNamespaceInvisible404`. Frame each test as "workload X does Y, expects Z" — not "function F returns G." A reader scanning test names should see the project's promises enumerated. Scenarios live in `test/integration/` and run via the harness against the real generated client. 
- **Run integration tests against real Postgres** (with `pg_search`). The developer brings Postgres up explicitly via `docker compose up` before running the suite — no programmatic container management inside tests. Tests connect to a known location (env var or default), fail clearly if Postgres isn't reachable. Mocking the DB hides bugs that bite in prod migrations. Unit tests that don't need the DB use the mockery-generated DAL mock instead.
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
- **Storage**: `pgx/v5` for Postgres. **No CGO** — preserves the static-binary invariant.
- **OTel**: `go.opentelemetry.io/otel` for trace propagation; OTLP exporter when wired.
- **License**: Apache 2.0. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the contributor contract — DCO sign-off, SPDX headers on new Go files, AGPL-free dependency policy.

Install local dev tools with `task tools:install`.

## Misc

- Keep `pkg/` empty. Everything is `internal/` until something is genuinely meant for external import.
- Default namespace / default client / master-key bootstrap behavior — see DL01.
