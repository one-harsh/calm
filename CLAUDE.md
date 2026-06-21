# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

CALM (Context Abstraction Layer for Models) is an HTTP service that reduces LLM token waste by filtering and compressing tool output before it enters the context window. It sits beside (never between) the workload and the LLM — a sidecar, not a proxy.

The full design is in `docs/HLD.md`. **The HLD is canonical.** Code follows the HLD; this file is the development directive that translates the HLD's intent into day-to-day rules. The user-facing landing page is `README.md` (status, quickstart, deployment, ops surface); this file is for engineering contributors and AI coding assistants.

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
- **Layering**: `cmd/<bin>/main.go` stays thin (~30–60 lines: config load, logger init, dependency wiring, hand off to the run loop); domain logic lives in `internal/{ingest,search,events,snapshot,session}`, behind thin handlers.
- **The adapter lives under `internal/adapter`** because only `cmd/calm-adapter` consumes it — CALM's public surface is the OpenAPI spec, not a client SDK (DL09). A boundary test pins its server-package imports to the extraction-portable set (`internal/api/genapi`, `internal/secrets`) so a future carve-out is a lift, not a refactor. `internal/adapter/docs/DESIGN.md` is the MCP adapter design contract; `internal/adapter/docs/LABELING.md` is the canonical idempotent-indexing labeling/event contract.

## Isolation is two boundaries, not one — namespace is security, session is content

Two distinct isolation primitives, both load-bearing, failing differently. **Namespace-isolation is the security/trust boundary**: cross-namespace queries are forbidden; mismatch returns 404 (invisibility-not-denial); bugs here are confidentiality breaches — data crosses trust units. **Session-isolation is the content/scope boundary**: per-session data (chunks, sources, events, labels, vocabulary) is invisible to other sessions in the same namespace; bugs here are workload-contract violations — the LLM context window gets contaminated. **Client-isolation is the optional third layer**: with `require_client_credentials: true`, server-minted client bearer tokens make within-namespace workload isolation a real boundary; with the default `false`, `client` is a workload-supplied tag (DL01).

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

## Build / test / run discipline

- All reproducible operations go through `task`: `task build`, `task test`, `task test:unit`, `task test:integration`, `task lint`, `task fmt`, `task tidy`, `task ci`, `task run:calm`, `task docker:build`, `task gen:api`, `task gen:mocks`, `task gen:check`.
- `task test` runs unit then integration. Inner-loop dev: `task test:unit` for fast feedback; `go test ./<pkg>` and `go vet ./...` are fine. Anything whose result must match across machines (CI, builds, lint, format) goes through `task`.
- Never edit `go.mod` by hand for routine adds — `task tidy` is the entry.
- `task ci` is the gate: lint + test + build, all green.
- **Dogfood the adapter when it's registered.** If the `calm` MCP server is connected, route shell commands through `calm_run_command` (not the native shell) and retrieve prior output via `calm_search source=<label>` instead of re-running. Pure-inspection file reads (`cat`, `sed`, `head`, line-numbered slices for verification) also go through `calm_run_command` rather than the native `Read` tool — `Read` dumps content straight into context with no indexing. Reserve `Read` for cases where it's a hard precondition (e.g., `Edit` requires a prior `Read` of the target file in the same conversation). Native shell is the fallback when CALM is unreachable (`never-worse`). The conversation's MCP tool surface is sticky to the adapter PID it bound to at session start — if that PID had a failed `initialize`, or dies mid-conversation, the host doesn't auto-rebind. Detect via the absence of `Captured N/M sections under "<label>"` on `calm_run_command` output, or `CALM not connected` from `calm_search`; when detected, ask the user to restart the conversation (`/resume` in Claude Code) so a new session binds to a fresh adapter.
- **Run `task closeout` at task / plan completion.** Rebuilds `bin/calm-adapter` and prints the host-reload command. The MCP host launches the adapter at connect time and never picks up new bytes until the user reloads the host's MCP config, so closeout is what makes a fresh adapter binary available to the next conversation.

## HTTP server boundaries (canonical — do not drift)

- `internal/server` owns the HTTP server lifecycle (listener, middleware chain, graceful shutdown). No route or domain code here.
- **TLS is opt-in, edge-terminated by default** — server-auth only when enabled (see `server.Config.TLSCert`). Not mTLS; client/org-membership gating stays an edge concern.
- `internal/api` owns handlers and DTOs. Handlers parse → domain call → marshal. No business logic.
- **Middleware chain order is canonical**: do not reorder without an HLD discussion. The chain is assembled in `internal/server/server.go` (`NewHandler`), where each link's ordering rationale is commented; behavioral rationale lives on the middleware constructors.
- Handlers read session state via `session.MetadataFromContext(ctx)` — they never touch the raw session token.

## Correlation IDs and the response-header surface

Three IDs live on the request/response path (`X-CALM-Correlation-Id`, `X-Workload-Request-Id`, `traceparent`/`traceresponse`) — see HLD's response-headers section; adding a fourth without HLD discussion is a fork. Handlers never mint correlation ids: the Context middleware binds the server-minted value once (`logging.Bind`), and downstream code — log lines and audit events alike — inherits `correlation_id` / `trace_id` / `span_id` from context. No per-call-site re-binding.

## Transactions live in services, not the DAL

- DAL repo methods are **transaction-agnostic** — no `BeginTx`/`inTx`; they run on the injected `queryer` (`*sql.DB` or `*sql.Tx`). **Write** methods are single SQL statements (a bulk multi-row INSERT or UPSERT-RETURNING is still one statement); any multi-step write — even a same-aggregate delete-then-insert "replace" or a lock → count → delete cascade — is composed in the service's `WithTx`, never a DAL method. **Read** methods return data (a SELECT, possibly fanned over inputs) and open no transaction.
- **Services own transaction boundaries and orchestration** via `store.WithTx(ctx, func(db.Repos) error { … })` — the choreography and rollback surface are legible at the service layer, and this is the seam where distributed coordination (outbox/saga) will land. `db.Repos` is a struct: access fields directly (`r.Sources.Upsert`).
- `WithTx` is the only sanctioned transaction entry point; `inTx` is its private engine. Both return the closure's error verbatim so sentinels propagate to the handler's `mapXError` unchanged; error-wrapping stays in the granular repo methods.
- Reads and single-statement writes need no transaction — handlers call the DAL directly; don't add a forward-only service.
- Repos are aggregate-scoped, not table-scoped: `SourcesRepo` owns the source/content surface (Upsert, List, `Search` — which reads chunks because chunks belong to the source aggregate); `ChunksRepo` holds the chunk-row write primitives (`DeleteForSource`, `Insert`) that ingest composes. Atomicity comes from `WithTx`, not from co-locating ops on one repo.
- **Validation locus:** business-rule validation (ranges, required fields) lives in the service; the DAL keeps structural sentinels (FK/PK/constraint → sentinel) and the namespace-isolation guard.
- **The namespace guard folds into the statement that touches the data — one canonical guard, no separate verify step.** `sessions`/`clients` predicate on namespace directly; child tables (sources/events) use an inline `EXISTS (SELECT 1 FROM sessions WHERE id = <session_id> AND namespace = …)` — a `WHERE` predicate on reads, inside the `INSERT … SELECT … WHERE EXISTS` on writes. EXISTS, not JOIN: one form fits reads and writes alike (you can't `JOIN` an `INSERT`) and avoids column collisions. Cross-namespace collapses to the table's natural no-match: empty for list/search reads, `ErrSessionNotFound` for point lookups/writes via no-rows-returned. Defense-in-depth only — `SessionResolve` 404s cross-namespace before the handler. The chunk and vocabulary leaf operations (`DeleteForSource`, `Insert`, `DecrementForSource`, `IncrementForSource`, and the session-keyed `PruneZeros`/`TopByIDF`) are the deliberate exception: they run in the same transaction as the namespace-verified `Upsert`, keying off the `source_id` it minted or the `session_id` it verified — a capability, like FK-cascade children on session delete. (`verifySessionInNamespace` is a transitional shim for `eventsRepo.Write` only.)

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
- **Wire contract is mechanism-independent** — teardown internals never leak into responses; cascade row counts emit as INFO log fields (`session.delete.cascaded_*`), not response bodies. Long-tail row-level retention is delegated to operator-resident exporter sinks (DL14).

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
- **Run integration tests against real Postgres** (with `pg_textsearch`). The developer brings Postgres up via `docker compose up` — no programmatic container management in tests; tests connect to a known location and fail clearly if unreachable. Mocking the DB hides bugs that bite in prod migrations. Unit tests that don't need the DB use the mockery-generated DAL mock.
- **No hand-rolled mocks.** `mockery` generates only against **port interfaces** (DAL, MCP transport, clock, FTS capability — narrow set, expanded only at real boundaries). Internal helpers stay concrete, tested via integration. Mockery escape requires a `// MOCKERY-ESCAPE:` comment with the reason.
- **Mocks are in-package** (`mock_<name>.go` beside the interface), build-tagged `//go:build mocks`; tests and CI run with `-tags=mocks` (wired in `Taskfile.yml`).
- Don't introduce interfaces just to enable mocking — if a struct isn't at a port boundary, test it through integration.

## Tooling

Versions and the dependency inventory live in the tree, not here: `.go-version` (goenv), `go.mod`, `Taskfile.yml`, `.golangci.yaml`, `.mockery.yaml`, `oapi-codegen.yaml`. Install local dev tools with `task tools:install`.

- **No CGO** — preserves the static-binary invariant.
- **License**: Apache 2.0. See [`CONTRIBUTING.md`](CONTRIBUTING.md) — DCO sign-off, SPDX headers on new Go files, AGPL-free dependency policy.

## Misc

- Keep `pkg/` empty. Everything is `internal/` until something is genuinely meant for external import.
- Default namespace / default client / master-key bootstrap behavior — see DL01.
