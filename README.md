# CALM — Context Abstraction Layer for Models

An HTTP service for team-deployed LLM workloads. CALM filters and
compresses tool output before it enters the context window, indexes
the raw content for later search, captures session events that outlive
the conversation, and closes the loop by attributing workload-reported
outcomes back to the specific calls that produced them. Token spend
*and* retrieval quality are CALM's twin observable concerns — both
load-bearing, both instrumented. CALM sits *beside* the workload and
the LLM (a sidecar, not a proxy).

The canonical design lives in [`docs/HLD.md`](docs/HLD.md). This README
is the operator-facing landing page.

## Status

Foundation, auth model, session lifecycle, observability surface, the
content-handling layer (ingest / search / events / snapshot), and the
outcome-feedback wire contract are end-to-end. The correlations DAL that
backs full outcome attribution and the `/v1/manage/*` administrative API
are the remaining server surfaces; ingest/search ranking and compression
quality (BM25/RRF, context budgeting) is the active refinement area.

Working today:

- YAML-config loader with env-var override and bracketed secret references
- Three credential layers: namespace API key (`X-CALM-API-Key`), optional
  per-client bearer token (`Authorization: Bearer`) in credentialed
  namespaces, and server-minted session token (`X-CALM-Session-Token`)
- Three-tier rate limiting: per-IP (pre-auth), per-namespace, and global
- Postgres storage open + embedded migrations
- **Clients** — first-class registered entities: `POST /v1/clients/{name}`
  with optional one-time bearer token in credentialed namespaces;
  `POST /v1/clients/{name}/rotate-token`; delete via the management API
- **Sessions** — server-minted credential, hashed at rest
  (`sha256(namespace || 0x00 || token)`), surrogate `BIGSERIAL` primary
  key; child tables FK on the surrogate. Create returns the raw token
  once; Delete returns `204 No Content` and emits cascade row counts as
  INFO log fields. Monotonic touch, cascaded teardown, bulk delete, and
  TTL scan all share one path through the service layer
- **Idempotency-Key** on `POST /v1/sessions` — bounded LRU dedup
  (default 1h, 10K entries), singleflight serialization so a retry storm
  with the same key collapses to one INSERT
- **Session-metadata LRU cache** keyed on `(namespace, session_token)`,
  invalidated on Delete and namespace-purged on bulk paths
- **TTL scanner** — periodic background reaper, scanner-triggered deletes
  go through the same DAL cascade as explicit close
- **Observability headers** — every response carries `X-CALM-Correlation-Id`
  (server-minted UUIDv7), echoes `X-Workload-Request-Id` when supplied
  (≤256 chars, validated by dedicated middleware), and emits W3C
  `traceresponse` when the request carried a valid inbound `traceparent`.
  The OTel propagator install is config-gated
  (`observability.otel.enabled`) and happens at bootup, not at the
  middleware
- **`POST /v1/feedback`** endpoint wired end-to-end: outcome enum
  (`success | retry | degraded`), per-namespace `feedback_ttl_minutes`
  window (handler-side check via the embedded UUIDv7 timestamp — no DB
  read on the 410 path), session-id cross-check at the DAL boundary as
  the within-namespace forgery defense. Until the correlations DAL
  lands, the in-window path returns `503 feedback_dal_unavailable` so
  workloads see a clean "feature not yet available" status rather than
  an opaque 500
- **Content layer** — `POST /v1/ingest` (chunking, format detection,
  intent filtering), `POST /v1/search` (porter → trigram fallback),
  `POST/GET /v1/events`, `GET /v1/snapshot`, and `GET /v1/sources` are
  routed and return real data; ranking/compression quality is the next
  layer of work
- Server lifecycle (graceful shutdown, OpenAPI request validation,
  core-dump disabled at startup to keep credentials out of process memory
  dumps)
- **MCP adapter** (`cmd/calm-adapter`) — a stdio MCP server a coding agent
  points at. It creates a session on startup (token held in process memory
  only) and exposes two tools that close the capture→retrieve loop:
  `calm_run_command` runs a shell command locally, ingests its output into CALM,
  and returns a compact summary plus a source label — falling through to the
  command's raw output on any CALM failure (the `never-worse` invariant), with
  derived events (`tool_invocation`, `error_observed`, `git_operation`) emitted
  best-effort alongside each run; `calm_search` queries the session and returns
  ranked, verbatim snippets (optionally scoped to a source label), reporting
  "search unavailable" rather than empty results when CALM is unreachable

Still stubbed: the `/v1/manage/*` administrative handlers and the
correlations DAL that backs full outcome attribution (its stub returns
`ErrCorrelationsNotImplemented`, which is why the in-window `/v1/feedback`
path responds `503 feedback_dal_unavailable`). Ingest and search run a
baseline implementation today; the ranking and compression quality work
improves those surfaces in place without changing the wire contract.

## What problem this addresses

LLM APIs charge per input token, and the entire context window is sent
on every turn. Tool outputs (logs, API responses, file dumps) enter
context verbatim, sit there until compaction, and get re-billed each
turn. Long contexts also degrade answer quality independently of cost —
the *Lost in the Middle* effect (Liu et al., 2023) is structural to how
transformers attend. Today, teams can't separate "the LLM struggled
because the model is wrong for this task" from "the LLM struggled
because the context was polluted with stale tool output" — there's no
attribution layer.

CALM's bet is that for a shared team deployment serving multiple LLM
workloads, the right place to solve this is a single piece of
infrastructure that ingests tool output, returns a compact representation
to the model, exposes the raw content via search when the model asks
for it, *and* attributes workload-reported outcomes back to the specific
retrieval calls that produced them. That second leg — outcome
attribution via `/v1/feedback` and the resulting outcome-labeled
metrics — is what turns context management from a black-box optimization
into a measurable quality signal teams can track across deployments. See
the HLD's *Motivation*, *Goals & Non-Goals*, and *A note on RAG*
sections for the full argument; CALM is explicitly not a RAG system,
not an orchestrator, and not for solo install.

## Architecture at a glance

Single statically-linked Go binary, REST API with JSON payloads, all
state behind a DAL backed by Postgres.

- **Three core primitives** (ingest, search, session state) — see HLD's
  primitives section.
- **Workload patterns** identified by `namespace` (server-resolved from
  the `X-CALM-API-Key`) and a `client` identifier (workload-supplied
  metadata or server-verified bearer credential, depending on namespace
  config) — see DL01.
- **Six design invariants** — `never-worse`, `workload-agnostic`, the
  two-layer isolation invariant (`namespace-isolation` for the
  security/trust boundary + `session-isolation` for the content/scope
  boundary), `sidecar-not-proxy`, `content-fidelity`, `idempotent-indexing`.
  See the HLD's design-invariants section.
- **Storage**: Postgres in production, BM25 via `pg_search` or
  `pg_textsearch`, trigram via `pg_trgm` — see DL11.

## Quickstart (local dev)

Prerequisites: Go 1.25.x (pinned in `.go-version`),
[go-task](https://taskfile.dev/), Docker (for the dev Postgres),
and the tools `task tools:install` will fetch (gofumpt, goimports,
mockery v2, golangci-lint v2, oapi-codegen v2, addlicense).

```bash
git clone https://github.com/one-harsh/calm.git && cd calm
task tools:install                 # one-time: install dev tools
task dev:up                        # start dev Postgres in docker-compose

export CALM_DEFAULT_KEY=$(openssl rand -hex 32)
task run:local                     # builds and runs against cmd/calm/config/dev.yaml
```

`task run:local` reads
[`cmd/calm/config/dev.yaml`](cmd/calm/config/dev.yaml), which references
`[env:CALM_DEFAULT_KEY]` for the default namespace's bearer credential —
the service refuses to start if you haven't exported it.

Once running, `/v1/health` is the simplest unauthenticated smoke check
(the content endpoints return real data too, but need auth plus a session
— see below):

```bash
curl -i http://localhost:8080/v1/health
```

The auth middleware is wired and runs before everything: requests
without `X-CALM-API-Key: $CALM_DEFAULT_KEY` get a 401. Health and
version endpoints are exempt. In a credentialed namespace, the
session-touching endpoints additionally require
`Authorization: Bearer <client-token>` (issued at `POST /v1/clients/{name}`)
and `X-CALM-Session-Token: <session-token>` (issued at
`POST /v1/sessions`).

## Configuration

CALM reads its config from the YAML file pointed at by
`CALM_CONFIG_FILE` (required — the service refuses to start without it).
The annotated template lives at
[`cmd/calm/config/example.yaml`](cmd/calm/config/example.yaml); copy it
and point your deployment at the result.

Secrets in the config use a bracketed reference dialect (defined by
`internal/secrets`):

- `[text:<literal>]` — inline value (avoid in committed config)
- `[env:<VAR>]` — value comes from the named environment variable
- `[file:<path>]` — value is the trimmed contents of the file (e.g., a
  mounted Kubernetes Secret, a Vault Agent template, an ESO-rendered
  file)

The config file itself contains no secret material — it's a manifest of
where each credential lives. CALM never mutates the operator's config
file; operators provision secrets via their platform's existing tooling.

Any scalar field can also be overridden by an environment variable:
`CALM_<PATH_IN_UPPERCASE>` with `.` and `-` replaced by `_`. Examples:
`CALM_SERVER_ADDRESS=":9090"`, `CALM_STORAGE_DSN="postgres://..."`,
`CALM_OBSERVABILITY_LOGGING_LEVEL=debug`,
`CALM_OBSERVABILITY_OTEL_ENABLED=true` (installs the W3C
TraceContext propagator at bootup so the Context middleware extracts
inbound `traceparent` and emits `traceresponse`),
`CALM_SESSIONS_CACHE_SIZE=20000` (per-pod LRU cap for the session-metadata
cache; default 10,000; `0` disables),
`CALM_SESSIONS_IDEMPOTENCY_KEY_TTL=1h` and
`CALM_SESSIONS_IDEMPOTENCY_KEY_SIZE=10000` (per-pod dedup window + cap
for `Idempotency-Key` on `POST /v1/sessions`; `0` size disables dedup).
Per-namespace `feedback_ttl_minutes` (default 60, bounded `[1, 1440]`)
sets the per-namespace feedback acceptance window for `/v1/feedback`.
Slice fields under `namespaces` are not env-overridable.

## API

The HTTP contract is generated from
[`docs/api/openapi.yaml`](docs/api/openapi.yaml). The full surface
(`/v1/manage/*` currently stubbed):

```
GET    /v1/health
GET    /v1/version
POST   /v1/clients/{name}
POST   /v1/clients/{name}/rotate-token
POST   /v1/sessions
DELETE /v1/sessions
GET    /v1/sources
GET    /v1/snapshot
POST   /v1/ingest
POST   /v1/search
POST   /v1/events
GET    /v1/events
POST   /v1/feedback
GET    /v1/manage/sessions
DELETE /v1/manage/sessions
GET    /v1/manage/clients
DELETE /v1/manage/clients/{client}
```

Every request carries `X-CALM-API-Key: <namespace-key>`. Namespace is
server-resolved from the key — clients never send a namespace in the
request body. Cross-namespace mismatches return **404, not 403**
(invisibility, not denial — encoded in the OpenAPI spec).

Session-touching endpoints carry `X-CALM-Session-Token` (the credential
returned by `POST /v1/sessions`). In credentialed namespaces, they also
carry `Authorization: Bearer <client-token>`.

Every response carries `X-CALM-Correlation-Id` (server-minted UUIDv7,
unique per request). Workloads supply `X-Workload-Request-Id` to have
it echoed back for their own log correlation. Requests with a valid
inbound `traceparent` get a `traceresponse` on the way out. The
correlation ID is the value workloads echo back to `/v1/feedback` when
reporting outcomes for a prior call.

## Building and testing

Everything reproducible goes through `task`:

```bash
task build              # build both binaries (calm + calm-adapter)
task test               # unit + integration (needs `task dev:up`)
task test:unit          # fast inner-loop tests, no Postgres needed
task ci                 # full pre-merge gate: gen:check + lint + test + build
task gen:api            # regenerate handlers/client from openapi.yaml
task gen:mocks          # regenerate mockery mocks
task fmt                # gofumpt + goimports
```

Integration tests run against a real Postgres started by `task dev:up`
— mocking the DB at the DAL boundary would hide bugs that bite in prod
migrations.

## Repo layout

```
cmd/
  calm/             service entry (thin: config, deps, server.Run)
    config/         operator config templates (example.yaml, dev.yaml)
  calm-adapter/     MCP adapter binary
internal/
  api/              generated handler interface + thin handlers + DTOs
  auth/             API-key registry, namespace resolver, shared
                    token mint/hash helpers, wire-header constants
  clientreg/        client-entity orchestration (register, rotate, resolve)
  config/           YAML config loader (Viper + struct binding)
  db/               Postgres DAL — per-entity files (pg_clients.go,
                    pg_sessions.go, ...), errors, models, tx primitive,
                    embedded migrations
  feedback/         outcome-feedback service: UUIDv7-timestamp TTL check,
                    delegates to CorrelationsRepo.UpdateOutcome
  obs/              context-bound logging + field helpers; OTel
                    propagator install (config-gated at bootup)
  secrets/          [scheme:payload] secret-reference resolver
  server/           HTTP lifecycle + middleware chain (recovery, context,
                    logging, workload-request-id, rate-limit, auth,
                    body-size, timeout, OpenAPI, session-resolve)
  session/          session-lifecycle orchestration service + LRU metadata
                    cache + Idempotency-Key dedup + TTL scanner
adapter/            MCP-only packages (consumed by cmd/calm-adapter)
docs/
  HLD.md            canonical design document
  api/openapi.yaml  formal API contract
test/integration/   real-Postgres end-to-end scenarios
```

## Contributing

Contributor contract — DCO sign-off, SPDX headers on new Go files,
AGPL-free dependency policy — is in
[`CONTRIBUTING.md`](CONTRIBUTING.md). The day-to-day development
discipline (file layout, comment policy, logging conventions,
middleware-chain order, testing rules) is in
[`AGENTS.md`](AGENTS.md). The HLD is the spec; code follows the HLD,
and silence in the HLD on something the code needs is a design gap to
surface, not a license to improvise.

## License

Apache 2.0 — see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
