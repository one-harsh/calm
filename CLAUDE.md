# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

CALM (Context Abstraction Layer for Models) is an HTTP service that reduces LLM token waste by filtering and compressing tool output before it enters the context window. It sits beside (never between) the workload and the LLM — a sidecar, not a proxy.

The full design is in `docs/HLD.md`. **The HLD is canonical.** Code follows the HLD; this file is the development directive that translates the HLD's intent into day-to-day rules.

## HLD is the design directive

- Code follows the HLD. Deviations in code are **temporary**, not canonical — mark with `// HLD-DEVIATION:` and a one-line reason; expect reconciliation.
- If the HLD is **silent or ambiguous** on something the code needs, **do NOT improvise**. Surface the gap, propose a resolution, evolve the HLD, then implement. Order: design agreement → HLD → code.
- The HLD is a long-lived spec, not a historical artifact — edits are normal. It stays language-agnostic; implementation choices (Go, drivers, libraries, Postgres wiring) live in CLAUDE.md and code.

### Don't duplicate HLD content in this file

CLAUDE.md is the development directive (disciplines, layout, patterns); the HLD is the spec (what the system is). Restated HLD content goes stale silently when the HLD evolves. When you'd paraphrase or enumerate the HLD, write a one-line pointer instead ("see HLD's design-invariants section"). Reserve CLAUDE.md bullets for code-level guidance the HLD doesn't carry: file layout, mockery rules, comment policy, middleware order, log levels.

### Cite by stable name, not section number

HLD section numbers (`§3`, `§11`) rot silently when the HLD is reorganized. Cite by stable name instead:

- **Invariants** by name — `never-worse`, `workload-agnostic`, `namespace-isolation`, `session-isolation` (the two halves of the one isolation invariant — cite whichever applies), `sidecar-not-proxy`, `content-fidelity`, `idempotent-indexing`.
- **Topical sections** by topic — "HLD's storage section", not "HLD §7".
- **Decision Log entries** (`DL01`, `DL08`, …) directly — DL IDs are append-only and stable.

Applies everywhere (CLAUDE.md, AGENTS.md, comments, Taskfile descs). The OpenAPI spec is exempt — its descriptions ship as the public contract under their own rev policy.

### Canonical surfaces describe the design, not the history

The HLD, OpenAPI spec, code, and CLAUDE.md discipline bullets describe **what CALM is** — present tense, self-contained, reimplementable by a reader with no project history. Never write into them:

- **Project-internal planning labels** — phase numbers, work-item IDs, milestone/sprint names.
- **Transition narratives** — "previously X, now Y", "since the auth rewrite".
- **Cause-and-effect tied to historical events** — "because the audit found…", "changed after incident-X".

That reasoning belongs in commits, PR descriptions, plan files, and planning docs — surfaces that are allowed to age out. Write "X is Y" (present tense); if the *reason* matters to a reimplementer, write the reason; if the reason is "we changed our minds," it doesn't belong at all.

### HLD describes the design, not the implementation

The HLD is the **driver**; the code is the **passenger** — one implementation, which happens to be Go. The HLD must not reference:

- **Code symbols** — package names, file paths, type/method names, struct field identifiers as code.
- **Language/stack-specific concepts** — Go interfaces, struct tags, build tags, generics.
- **Tooling and libraries** — `mockery`, `oapi-codegen`, `pgx`, `viper`, `chi`, lint/format tools.
- **Implementation patterns named in code** — middleware function names, error sentinels, mock outputs.

The HLD **may** reference:

- **External wire contract** — HTTP paths, headers, JSON fields, status codes (the customer contract).
- **Storage schema** — tables, columns, SQL types, indexes (the data model is part of the design).
- **External standards** — Postgres, BM25, RRF, sha256, TLS, OpenTelemetry, Kubernetes, MCP, JSON.
- **External-extension surface** — `pg_search`, `pg_textsearch`, `pg_trgm` (operator-visible deployment dependencies).

Test: if a fact survives a rewrite in another language by another team, it belongs in the HLD; if it depends on this codebase's shape, it belongs in CLAUDE.md or the code. ("Could a Rust/Python team rebuild a wire- and schema-compatible CALM from this passage?")

## API contract: OpenAPI is the formal source

- `docs/api/openapi.yaml` is the **canonical formal contract**; the HLD's API contract section is intent in prose. Workflow: **design agreement → HLD prose → openapi.yaml → codegen → handlers**. Don't hand-write request/response types or routes.
- Never edit `*.gen.go` (currently `internal/api/genapi/genapi.gen.go`) — edit the YAML and run `task gen:api`. `task gen:check` (in `task ci`) fails if generated files drift from the committed tree.
- Response-body schemas use the `Result` suffix (`IngestResult`, `SearchResult`) to avoid colliding with oapi-codegen's `<OperationID>Response` client wrappers.
- Request validation is enforced by `internal/server/middleware/validation.go` against the embedded spec — required fields, enums, formats, path/query types — so handlers can trust the parsed types.
- The spec also drives the `calm-adapter`'s HTTP client (generated `ClientWithResponses`). No hand-rolled HTTP requests against CALM from the adapter.

## Architecture (orientation only; HLD is authoritative)

Single statically-linked binary, REST API with JSON payloads, all state behind a DAL.

- **Three core primitives** (ingest, search, session state) — see HLD's primitives section.
- **Workload patterns** identified by namespace + optional client — see DL01.
- **Six design invariants** — named above; see HLD's design-invariants section.
- **Storage**: Postgres in production, BM25 via `pg_search` or `pg_textsearch`, trigram via `pg_trgm` — see DL11. The DAL (`internal/db`) is a mockery port for testability, **not** a portability layer; there is only one backend.

## Isolation is two boundaries, not one — namespace is security, session is content

Two distinct isolation primitives, both load-bearing, enforcing different boundaries; review them as separate disciplines that fail differently.

**Namespace-isolation is the security/trust boundary.** Cross-namespace queries are forbidden; mismatch returns 404 (invisibility-not-denial); every per-request log line carries `namespace`. **Bugs here are confidentiality breaches** — data crosses trust units.

**Session-isolation is the content/scope boundary.** Per-session data (chunks, sources, events, labels, vocabulary) is bound to a session and invisible to other sessions in the same namespace; caches are session-keyed; search and snapshots return only this session's content. **Bugs here are workload-contract violations** — the LLM context window gets contaminated. Session is *not* a cleanup primitive or observability artifact; it's the content boundary defining what each workload-unit sees.

**Client-isolation is the optional third layer.** With `require_client_credentials: false` (the default), `client` is workload-supplied metadata — any holder of the namespace API key can claim any client. With `true`, each client is registered with a server-minted bearer token verified by auth middleware, making within-namespace workload isolation a real boundary (shared-namespace tenants isolate without an operator minting a namespace per workload). The default code path treats `client` as a tag.

Most bugs that quietly degrade CALM start as a missed `namespace`/session filter, a cache that wasn't session-keyed, or a "convenience" cross-{namespace,session} query.

**Three-term terminology** — never blur these; names map to types so the type system catches confusion:

- `session_token` (`string`, ~43 chars base64url) — raw secret. Server mints; workload presents via `X-CALM-Session-Token`. Never in logs, URLs, mgmt responses, or DB columns.
- `session_id` / `Session.ID` (`int64`) — surrogate BIGSERIAL PK; the only "id" of a session. Non-secret, safe to log.
- `session_token_hash` (`[]byte`, 32) — `sha256(namespace || 0x00 || session_token)`. Storage form and auth-side lookup key. Never logged.

Concrete disciplines:

- **Every DAL method touching per-session data takes `namespace` + the session lookup key explicitly** (or an input struct carrying them): `sessionTokenHash []byte` on the handler path, `sessionID int64` on the TTL-scanner path. See `internal/db/dal.go` — no exceptions. The management API (`/v1/manage/*`) is namespace-scoped too. The TTL scanner returns `(id, namespace)` pairs and feeds them back into the namespace-scoped delete path — one cascade semantics, two entry points distinguished only by lookup-key type.
- **Every domain function touching per-session data takes `namespace` and the session credential explicitly** — never from ambient context. The service layer takes raw `sessionToken` and hashes at its boundary; the DAL only ever sees the hash.
- **Caches are keyed by `(namespace, session_token)` jointly.** The search-result cache keys by `namespace + session_token + query + source` and is invalidated on ingest into that session. A cache without joint scoping is a bug.
- **Per-session tables FK on `sessions(id) ON DELETE CASCADE`** — the surrogate, namespace-stamped at session creation. Children carry no namespace column; the FK chain preserves scope (each surrogate id belongs to exactly one session in one namespace). Cleanup-by-session is the only cleanup path; orphans are forbidden. The DAL surface still enforces namespace at the API boundary (Get/Touch/Delete take `(namespace, lookup-key)`).
- **`sessions` composite indexes lead with `namespace`**; filter-by-label queries on child tables join through `sessions` for namespace scope rather than carrying a redundant predicate.
- **Cross-namespace mismatch returns 404** — invisibility, not "you don't have access". The OpenAPI spec encodes this.
- **Integration tests assert both isolations explicitly**: write ns-A / read ns-B → 404 (security); write session-A / read session-B in the same ns → empty (content).
- **Logging carries `namespace` and `session_id` (int64)** on every per-request line via `obs.Namespace(...)` / `obs.SessionID(...)`. The raw `session_token` has no logging helper by design. Cross-session log entries (e.g., the TTL scanner before it holds a specific ref) intentionally omit `session_id` — absence is deliberate, not a leak.

Wanting a query that crosses session boundaries within a namespace means: it belongs in `/v1/manage/*` (the only legitimate cross-session surface, still namespace-scoped), or it's a design gap needing HLD discussion first. Cross-namespace queries never appear in code outside the TTL scanner.

The other two invariants that generate code-level discipline: **`never-worse`** (adapter and workload middleware must catch CALM failures and fall through to raw content — the LLM call always works) and **`content-fidelity`** (search snippets and ingest chunks return *exact* indexed text, never paraphrased or truncated). The rest are architectural and rarely surface in PR review.

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
  search/                2-layer fallback (porter → trigram)
  events/                event capture + priority-range validation (1–4)
  snapshot/              generic event-store snapshot builder (HLD DL08)
  session/               session lifecycle + TTL scanner
  db/                    DAL interface + Postgres impl (DAL is a mockery port for testability)
  auth/                  API-key registry + namespace resolver
  obs/                   logging init, OTel wiring, CALM-specific field helpers
  config/                env-driven config loader
  adapter/               packages used only by cmd/calm-adapter (self-contained; optionally carved into its own module later)
    calm/                CALM client port + DTOs (genapi client confined to genapi_client.go)
    config/              adapter config loader (viper); api_key is a secrets.Secret resolved in main via ReadSecret
    mcp/                 stdio MCP protocol (JSON-RPC 2.0)
    extract/             event extraction + source labeling from tool calls; LABELING.md is the canonical idempotent-indexing labeling/event contract
    exec/                local subprocess execution (developer machine)
test/integration/        user-facing scenarios — see HLD's workload-scenarios section
```

The adapter lives under `internal/` because only `cmd/calm-adapter` consumes it — CALM's public surface is the OpenAPI spec, not a client SDK (DL09). A boundary test keeps the adapter importing only **extraction-portable** server packages — `internal/api/genapi` (codegen from the spec, confined to `genapi_client.go`) and `internal/secrets` (slim, dependency-free) — and nothing else, so a future carve-out into its own repo is a lift: codegen the client, copy `secrets`, move the tree.

**Layered responsibility:**

- `cmd/<bin>/main.go`: thin (~30–60 lines). Config load, logger init, dependency wiring, hand off to `server.Run(ctx)`.
- `internal/server`: HTTP lifecycle. Knows nothing about specific routes or domain logic.
- `internal/api/handlers`: thin — parse request → call domain package → marshal response. **Never put business logic in handlers.**
- `internal/{ingest,search,events,snapshot,session}`: domain logic. Most integration tests target these.
- `internal/db`: DAL is a port. Mockery generates against it.

## Build / test / run discipline

- All reproducible operations go through `task`: `task build`, `task test`, `task test:unit`, `task test:integration`, `task lint`, `task fmt`, `task tidy`, `task ci`, `task run:calm`, `task docker:build`, `task gen:api`, `task gen:mocks`, `task gen:check`.
- `task test` runs unit then integration. Inner-loop dev: `task test:unit` for fast feedback; `go test ./<pkg>` and `go vet ./...` are fine. Anything whose result must match across machines (CI, builds, lint, format) goes through `task`.
- Never edit `go.mod` by hand for routine adds — `task tidy` is the entry.
- `task ci` is the gate: lint + test + build, all green.
- **Dogfood the adapter when it's registered.** If the `calm` MCP server is connected, route shell commands through `calm_run_command` (not the native shell) and retrieve prior output via `calm_search source=<label>` instead of re-running. Native shell is the fallback when CALM is unreachable (`never-worse`).

## HTTP server boundaries (canonical — do not drift)

- `internal/server` owns the HTTP server lifecycle (listener, middleware chain, graceful shutdown). No route or domain code here.
- **TLS is opt-in, edge-terminated by default.** Plain HTTP unless `server.tls.enabled` with `cert_file`/`key_file` (`secrets.Secret` → PEM, resolved in `cmd/calm/main.go`, passed as a loaded keypair into `server.Config`); then `ListenAndServeTLS` (server-auth only, TLS 1.2 floor). Not mTLS — client/org-membership gating stays an edge concern.
- `internal/api` owns handlers and DTOs. Handlers parse → domain call → marshal. No business logic.
- **Middleware chain order is canonical**: do not reorder without an HLD discussion.

  ```
  Recovery
    → Context (correlation-id mint + W3C trace extract+respond + logging-context bind)
      → Logging (start log + on-completion summary)
        → WorkloadRequestID (length-validate + echo X-Workload-Request-Id; 400 on >256)
          → RateLimit:IP (per-IP, pre-auth → 429)
            → Auth (API key → namespace)
              → RateLimit:NS+Global (per-namespace + global aggregate → 429)
                → BodySizeLimit (1MB cap → 413)
                  → Timeout (per-request budgets)
                    → OpenAPIValidator (kin-openapi against embedded spec)
                      → SessionResolve (X-CALM-Session-Token → SessionMetadata; post-handler Touch on 2xx)
                        → Handler
  ```

- **Recovery** is outermost and holds the base logger so it can record panics even if `ctx` was never hydrated.
- **Context** before Logging so every log line carries `correlation_id` and trace IDs.
- **Logging** before Auth so failed-auth attempts are still recorded with full context.
- **WorkloadRequestID** after Logging (the 400 path still emits a completion log line), before RateLimit:IP (a malformed workload-id is rejected without burning rate-limit tokens).
- **RateLimit:IP** before Auth so unauthenticated DDoS can't burn registry-lookup CPU.
- **Auth** before RateLimit:NS+Global (the namespace tier reads the auth-stamped namespace from context).
- **RateLimit:NS+Global** checks namespace tier first, then global. Namespace-first is load-bearing for namespace-isolation: global-first would let a misbehaving namespace burn shared global tokens on requests it would 429 anyway, leaking overload pressure across the boundary.
- **BodySizeLimit** before Timeout — rejecting an oversized body shouldn't consume the timeout budget.
- **SessionResolve** is presence-based on `X-CALM-Session-Token`: when set, calls `session.Service.Lookup` (404 on miss or cross-namespace), stuffs `SessionMetadata` into context via `session.WithMetadata`, best-effort `Touch`es after 2xx. Sits after OpenAPIValidator so missing-required-header is caught before a DB lookup. Handlers read via `session.MetadataFromContext(ctx)` — they never touch the raw token.

## Correlation IDs and the response-header surface

Three IDs live on the request/response path; they answer different questions and must not be conflated. Adding a fourth without HLD discussion is a fork. See HLD's response-headers section.

- **`X-CALM-Correlation-Id`** — server-minted UUIDv7 in `internal/server/middleware/context.go`; set on every response including 4xx/5xx, regardless of inbound headers. Handlers read the context-bound value (`logging.Bind` exposes it as `correlation_id` on every log line and audit event) and never mint their own. UUIDv7 (not v4) is load-bearing: the embedded ms timestamp drives the feedback-TTL window check without a stored `expires_at` or scanner.
- **`X-Workload-Request-Id`** — workload-supplied, optional, echoed when present; opaque to CALM (the workload's join key to its own request log). The ≤256-char cap is enforced by the `WorkloadRequestID` middleware; the OpenAPI parameter declaration is documentation, not enforcement.
- **`traceparent` / `traceresponse`** — inbound `traceparent` is extracted by the OTel propagator. Outbound `traceresponse` is **not** emitted by Go OTel HTTP middleware — `context.go` sets it explicitly; don't remove that line assuming the SDK handles it.
- Logging-context bind happens once at middleware time (`logging.Bind(ctx, ...)`); downstream inherits `correlation_id` / `trace_id` / `span_id` — no per-call-site re-binding. Audit events inherit the three fields via the chain — don't add them manually.

## Transactions live in services, not the DAL

- DAL repo methods are **transaction-agnostic** — no `BeginTx`/`inTx`; they run on the injected `queryer` (`*sql.DB` or `*sql.Tx`). **Write** methods are single SQL statements (a bulk multi-row INSERT or UPSERT-RETURNING is still one statement); any multi-step write — even a same-aggregate delete-then-insert "replace" or a lock → count → delete cascade — is composed in the service's `WithTx`, never a DAL method. **Read** methods return data (a SELECT, possibly fanned over inputs) and open no transaction.
- **Services own transaction boundaries and orchestration** via `store.WithTx(ctx, func(db.Repos) error { … })` — the choreography and rollback surface are legible at the service layer, and this is the seam where distributed coordination (outbox/saga) will land. `db.Repos` is a struct: access fields directly (`r.Sources.Upsert`).
- `WithTx` is the only sanctioned transaction entry point; `inTx` is its private engine. Both return the closure's error verbatim so sentinels propagate to the handler's `mapXError` unchanged; error-wrapping stays in the granular repo methods.
- Reads and single-statement writes need no transaction — handlers call the DAL directly; don't add a forward-only service.
- Repos are aggregate-scoped, not table-scoped: `SourcesRepo` owns the source/content surface (Upsert, List, `Search` — which reads chunks because chunks belong to the source aggregate); `ChunksRepo` holds the chunk-row write primitives (`DeleteForSource`, `Insert`) that ingest composes. Atomicity comes from `WithTx`, not from co-locating ops on one repo.
- **Validation locus:** business-rule validation (ranges, required fields) lives in the service; the DAL keeps structural sentinels (FK/PK/constraint → sentinel) and the namespace-isolation guard.
- **The namespace guard folds into the statement that touches the data — one canonical guard, no separate verify step.** `sessions`/`clients` predicate on namespace directly; child tables (sources/events) use an inline `EXISTS (SELECT 1 FROM sessions WHERE id = <session_id> AND namespace = …)` — a `WHERE` predicate on reads, inside the `INSERT … SELECT … WHERE EXISTS` on writes. EXISTS, not JOIN: one form fits reads and writes alike (you can't `JOIN` an `INSERT`) and avoids column collisions. Cross-namespace collapses to the table's natural no-match: empty for list/search reads, `ErrSessionNotFound` for point lookups/writes via no-rows-returned. Defense-in-depth only — `SessionResolve` 404s cross-namespace before the handler. The chunk and vocabulary leaf-writes (`DeleteForSource`, `Insert`, `DecrementForSource`, `IncrementForSource`) are the deliberate exception: they key off a `source_id` minted only by the namespace-verified `Upsert` in the same transaction — a capability, like FK-cascade children on session delete. (`verifySessionInNamespace` is a transitional shim for `eventsRepo.Write` only.)

## Outcome attribution: correlations + feedback

CALM captures a `correlations` row per value-producing call and updates it on `/v1/feedback`. See HLD's outcome-attribution section and DL14.

- **Every 2xx from a value-producing handler (ingest / search / snapshot) INSERTs a correlation row before returning.** The service layer owns the INSERT; the DAL is the leaf primitive. The correlation_id comes from the context-bound value — never minted at the handler.
- **The INSERT is best-effort.** A failed INSERT logs WARN and does **not** fail the request — missing observability never blocks the workload (`never-worse`). Wrapping the value-producing operation and the correlation INSERT in one transaction inverts the discipline.
- **`request_meta` JSONB carries CALM-derived signal dimensions only** (`match_layer` distribution, `allocator` variant, `intent_zero_match`, `omitted_by_priority`, …). Workload-supplied fields stay in JSONB and **must not** become metric labels — the cardinality discipline.
- **Outcome enum is `success | retry | degraded`, period.** `unset` is the DB default for never-received feedback — not a workload-submittable value, not a metric series; operators compute the coverage gap via PromQL. A fourth value is HLD-touching.
- **Feedback is single-shot via PK** — a second submission for the same `correlation_id` returns 409. The PK enforces it; no application-level "already-submitted" pre-check.
- **The 410 path is computable without a DB lookup**: parse the inbound `correlation_id` as UUIDv7, compare its embedded ms timestamp against the namespace's `feedback_ttl_minutes`. No `expires_at` column, no scanner, no row read.

## Session lifecycle: active-only, teardown mechanism agnostic

- **A session has exactly one durable service state: active.** No `completed/failed/abandoned/expired` states. Workload outcomes live on `correlations.outcome`, **not** on `sessions`. Adding outcome columns to `sessions` or mgmt endpoints for terminal sessions is HLD-touching (DL14).
- **Teardown mechanism is implementer's choice** — the HLD is mechanism-agnostic (sync FK cascade, chunked cascade, async reclaim, soft-mark + reclaim). Today's implementation is sync FK cascade; alternatives are LLD-level work, not HLD changes. Transitional divergence gets `// HLD-DEVIATION:`.
- **Wire contract is fixed and mechanism-independent.** `DELETE /v1/sessions` → 204. Management DELETEs return `{deleted_sessions: N}` only — no nested `cascaded` block. Cascade row counts emit as INFO log fields (`session.delete.cascaded_*`).
- **Long-tail row-level retention is delegated to operator-resident exporter sinks** (HLD-named seam in DL14); not a v1 code-level concern.

## Search allocator is pluggable behind one interface

- **Five variants** (`rank-round` default; `score-proportional`, `knapsack-greedy`, `equal-budget`, `mmr`) behind a single allocator interface in `internal/search`. New variants go behind the same interface; never branch on variant identity in handler or service code.
- **Per-namespace `search.default_allocator` selects the default**; per-request `X-CALM-Allocator-Variant` overrides when `search.allow_allocator_override: true`. When override is off, the header is silently ignored — **never 400**; it's a hint, same shape as the `client` field in uncredentialed namespaces.
- **`allocator=<variant>` is a bounded metric label** (cardinality = 5). Never accept caller-supplied strings as labels — same discipline as `match_layer` and the outcome enum. New variant = code change, not a config string.
- See DL15 (whole-response byte budget + allocator pluggability).

## Logging > comments

Logs are not optional decoration; they're the runtime record.

- Use `github.com/one-harsh/context-logging` everywhere. **Pass `ctx context.Context` as the first parameter** of every non-trivial function; hydrate request-scoped fields as early as possible (HTTP middleware, MCP adapter entry).
- Pattern: `logger.WithContext(ctx).Info("event_name", fields...)`; per-request summary via `logger.SummaryWithContext(ctx).Info(...)` at the end of a handler.
- Field helpers live in `internal/obs`, two shapes:
  - **Per-call helpers** (`obs.SessionID(id)`, `obs.Namespace(ns)`, `obs.Client(c)`, `obs.Source(s)`, `obs.Endpoint(p)`, `obs.EventType(t)`, `obs.FormatHint(h)`) — for caller-supplied values.
  - **Pre-constructed `LoggingField` vars** (`obs.MatchLayerPrimary`, `obs.CloseReasonTTLExpired`, …) — for code-determined closed enums; the type system enforces the value set. New value = new `var` in the group; new closed-enum field = new `Key…` constant + var group.
- **Field naming**: identifier/categorical fields are flat snake_case (`session_id`, `close_reason`); measurement fields use hierarchical dotted namespacing scoped to entity + action (`session.delete.cascaded_events`, `ttl_scan.duration_ms`) so tools can group on the path. Plural for entity counts (`sessions.scanned`), singular for per-entity measurements (`session.delete.cascaded_events`). Generic timings still get the namespace — never bare `duration_ms`.
- **Metric names follow the same dotted schema**; the OTel-Prometheus exporter converts `.` → `_` at emission, so PromQL sees the underscored form. No `calm_` prefix — the OTel `service.name` resource attribute carries that scoping.
- **DEBUG**: log liberally — if tempted to write a comment describing state or flow, write a DEBUG log instead. **INFO**: follow HLD's INFO-event taxonomy; a new INFO event is HLD-touching. **WARN**: degraded behavior the operator should know about (CALM-down fallback, intent fallback). **ERROR/FATAL**: actual failures.

## Comments — when they're allowed

A comment is allowed only for a **non-obvious business or design constraint** that forces the code's shape: an HLD invariant enforced at a specific line, a workaround for a known incident, a subtle ordering requirement.

- Keep short; cite the source by stable name — `// never-worse invariant: never block on CALM failure`.
- Don't restate what the code obviously does. Don't reference the current task or PR.
- Heuristic: if the comment would still be true after a refactor that changed *what* the code does, it's the wrong comment — delete it or convert to a DEBUG log.

## Testing

- Test as much as possible. Coverage isn't a target, but every non-trivial path should have a test.
- **Integration tests are full-loop scenarios** named after what CALM promises — `IngestAndSearch`, `SessionBreachNotAllowed`, `IdempotentReingest`, `CrossNamespaceInvisible404`. Frame each as "workload X does Y, expects Z", not "function F returns G" — scanning test names should enumerate the project's promises. They live in `test/integration/` and run via the harness against the real generated client.
- **Each integration test opens with a 2-3 line scenario header** — prose stating the promise/invariant under test, not step-by-step narration (that duplicates the body and rots). This is the **one sanctioned relaxation** of the no-comments default, and applies to `test/integration/` only — unit tests and production code keep the strict policy.
- **Integration tests run in parallel by default** (`t.Parallel()` first line, sub-tests included). The suite shares one per-run database, so a test opts in only if scoped to its own session/namespace; tests asserting global/namespace-wide counts, rate-limit counters, or fixed-name fixtures stay serial. When in doubt, leave it serial.
- **Run integration tests against real Postgres** (with `pg_search`). The developer brings Postgres up via `docker compose up` — no programmatic container management in tests; tests connect to a known location and fail clearly if unreachable. Mocking the DB hides bugs that bite in prod migrations. Unit tests that don't need the DB use the mockery-generated DAL mock.
- **No hand-rolled mocks.** `mockery` generates only against **port interfaces** (DAL, MCP transport, clock, FTS capability — narrow set, expanded only at real boundaries). Internal helpers stay concrete, tested via integration. Mockery escape requires a `// MOCKERY-ESCAPE:` comment with the reason.
- **Mocks are in-package** (`mock_<name>.go` beside the interface), build-tagged `//go:build mocks`; tests and CI run with `-tags=mocks` (wired in `Taskfile.yml`).
- Don't introduce interfaces just to enable mocking — if a struct isn't at a port boundary, test it through integration.

## Tooling

- **Go**: pinned via `.go-version` (goenv). Currently 1.25.5.
- **Build/run**: `go-task` via `Taskfile.yml`.
- **Lint**: `golangci-lint v2` via `.golangci.yaml`. Revive's "must have comment on exported X" and "unused-parameter" are intentionally disabled (they collide with the comment policy and interface-matching stubs). Generated files are excluded from lint.
- **Format**: `gofumpt` + `goimports` (via `task fmt`).
- **Mocks**: `mockery v2` via `.mockery.yaml` — in-package, build-tagged `mocks`.
- **API codegen**: `oapi-codegen v2` via `oapi-codegen.yaml` — types, chi server interface, HTTP client, embedded spec from `docs/api/openapi.yaml`. Run with `task gen:api`.
- **Validation**: `github.com/getkin/kin-openapi` + `github.com/oapi-codegen/nethttp-middleware`.
- **Logging**: `github.com/one-harsh/context-logging` (zap-wrapped, context-bound).
- **HTTP routing**: `github.com/go-chi/chi/v5`.
- **Storage**: `pgx/v5`. **No CGO** — preserves the static-binary invariant.
- **OTel**: `go.opentelemetry.io/otel` for trace propagation; OTLP exporter when wired.
- **License**: Apache 2.0. See [`CONTRIBUTING.md`](CONTRIBUTING.md) — DCO sign-off, SPDX headers on new Go files, AGPL-free dependency policy.

Install local dev tools with `task tools:install`.

## Misc

- Keep `pkg/` empty. Everything is `internal/` until something is genuinely meant for external import.
- Default namespace / default client / master-key bootstrap behavior — see DL01.
