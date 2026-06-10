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
content-handling layer (ingest / search / events / snapshot), outcome
attribution (a correlation row per value-producing call, updated via
`/v1/feedback`), and the MCP adapter (capture→retrieve through a coding
agent) are end-to-end.

Still stubbed (`501`): the `/v1/manage/*` administrative API. Ingest and
search run a baseline implementation; ranking and compression quality
(BM25/RRF, context budgeting) is the active refinement area, improved in place
without changing the wire contract.

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

mkdir -p .calm                              # .calm/ is gitignored — keys never get committed
openssl rand -hex 32 > .calm/calm_api.key   # the namespace key, a bare value (no `export`, no var name)
export CALM_DEFAULT_KEY=$(cat .calm/calm_api.key)
task run:calm                      # builds and runs against cmd/calm/config/dev.yaml
```

`task run:calm` reads
[`cmd/calm/config/dev.yaml`](cmd/calm/config/dev.yaml), which references
`[env:CALM_DEFAULT_KEY]` for the default namespace's bearer credential —
the service refuses to start if you haven't exported it. Keeping the key in
`.calm/calm_api.key` (gitignored) lets the MCP adapter below read the *same*
key from that file, so the server and adapter always agree.

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

## Wiring into a coding agent (MCP host)

The adapter is a standard MCP stdio server, so any MCP host — Claude Code, Codex, Cursor,
Claude Desktop, … — can use it. Build it first:

```bash
task build:adapter        # produces bin/calm-adapter
```

The commands below wire against the **local dev** CALM from the Quickstart
(`http://localhost:8080`, the `.calm/calm_api.key` file). For a **deployed CALM**, point
`CALM_ADAPTER_CALM_URL` at it and supply your team's namespace key the way your secret tooling
delivers it (`[env:…]` / `[file:…]`) instead of the local `openssl`-generated file.

**Claude Code** registers it from the CLI. Run this **from the repo root** (so `$(pwd)`
expands to absolute paths):

```bash
claude mcp add calm \
  --env CALM_ADAPTER_CALM_URL=http://localhost:8080 \
  --env CALM_ADAPTER_CALM_API_KEY="[file:$(pwd)/.calm/calm_api.key]" \
  --env CALM_ADAPTER_LOG_FILE=/tmp/calm-adapter.log \
  --env CALM_ADAPTER_LOG_LEVEL=debug \
  -- "$(pwd)/bin/calm-adapter"
```

Two rules this encodes — both learned the hard way:

- **Absolute paths only.** Both the `command` and the `[file:…]` path must be absolute.
  Claude Code execs the binary directly (no shell, so `~` is *not* expanded), and the secret
  resolver rejects `~`/relative paths — either one silently becomes "failed to connect."
  Running from the repo root makes `$(pwd)` resolve both.
- **The key file holds only the bare key.** `[file:…]` uses the file's whole trimmed
  contents as the API key, so `.calm/calm_api.key` must contain *just* the hex — no
  `export`, no `VAR=`, no quotes. It's the same file the Quickstart created and that CALM is
  running with, so server and adapter agree by construction; `.calm/` is gitignored, so the
  key is never committed.

`claude mcp list` then shows `calm: ✓ Connected`. On connect the adapter registers its
client (idempotent — `409 already-registered` is treated as success) and creates a session;
both are torn down on disconnect.

**Other hosts** (Cursor, Claude Desktop, Codex) use the `mcpServers` convention — same
`command` + `env`; only the file location differs (`~/.cursor/mcp.json` for Cursor, Codex's
own config format, …):

```json
{
  "mcpServers": {
    "calm": {
      "type": "stdio",
      "command": "/absolute/path/to/bin/calm-adapter",
      "env": {
        "CALM_ADAPTER_CALM_URL": "http://localhost:8080",
        "CALM_ADAPTER_CALM_API_KEY": "[env:CALM_DEFAULT_KEY]",
        "CALM_ADAPTER_LOG_FILE": "/tmp/calm-adapter.log",
        "CALM_ADAPTER_LOG_LEVEL": "debug"
      }
    }
  }
}
```

These config files are usually committed, so they reference `[env:CALM_DEFAULT_KEY]` rather
than inlining the key or a machine-specific path — export it where the host launches
(`export CALM_DEFAULT_KEY=$(cat .calm/calm_api.key)`). The host's `clientInfo.name`
(e.g. `claude-code`, `codex`) becomes the CALM `client` for the session.

**One key, many agents.** What authenticates you to CALM is the *namespace* credential — the
trust boundary between your deployment and CALM, not a per-agent key. (`CALM_DEFAULT_KEY` and
`.calm/calm_api.key` are just how *local dev* holds that key for this walkthrough; in
production the namespace key comes from operator config and platform-provisioned secrets —
see [Configuration](#configuration).) Run Claude Code and Codex against the same CALM and they
**share** the namespace key; the `client` identifier is what distinguishes them. In an
*uncredentialed* namespace `client` is metadata only — any holder of the namespace key can
claim any client name. Per-agent *secrets* (a server-minted bearer token per client, for real
within-namespace isolation) are the *credentialed* namespace mode
(`require_client_credentials: true`).

The agent then has `calm_run_command` (run a shell command locally; its output is captured
into CALM and returned compact) and `calm_search` (retrieve captured output on demand).
`stdout` is the JSON-RPC channel, so adapter logs go to `CALM_ADAPTER_LOG_FILE` (or stderr)
— never stdout. `CALM_ADAPTER_CALM_API_KEY` uses the secret-reference dialect
(`[text:..]` / `[env:..]` / `[file:..]`; raw values are rejected).

**Debugging the integration.** Each tool call stamps a `workload_request_id` and a
`trace_id`, and every CALM request the adapter makes is logged with its latency, status,
and the server-minted `correlation_id` — all of which also appear in CALM's own logs, so
you can join a single tool call across adapter ↔ CALM. For an offline check that the
binary speaks MCP correctly, run `task smoke:adapter`.

### Verifying it works

With CALM running (`task dev:up` + `task run:calm`, `CALM_DEFAULT_KEY` exported) and the
adapter registered as above:

1. **Connect** — start the host and confirm the `calm` server lists `calm_run_command` and
   `calm_search` (`claude mcp list` shows `✓ Connected`; `/mcp` inside a session lists the
   tools). On connect the adapter registers its client and creates a session — both visible
   in `/tmp/calm-adapter.log`.
2. **Capture** — have the agent run a command through `calm_run_command` (e.g. *"run `ls -la`
   with calm_run_command"*); it returns a compact summary plus a `source=` label.
3. **Retrieve** — have the agent `calm_search` a term from that output (e.g. a filename); it
   returns the matching snippet.
4. **Confirm the join** — `tail /tmp/calm-adapter.log` shows `calm call` lines carrying
   `correlation_id`, `workload_request_id`, and `http.duration_ms`; the same `correlation_id`
   appears in CALM's server log. The client is registered and a session created when the host
   connects; the session is deleted on disconnect (all logged).

If all four hold, the capture→retrieve loop is working end-to-end and is debuggable across the
adapter ↔ CALM boundary.

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
task ci                 # full pre-merge gate: dco:check + gen:check + lint + test + test:cover + build
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
  adapter/          MCP adapter packages — CALM client port, MCP stdio
                    protocol, local exec, extraction (consumed only by
                    cmd/calm-adapter)
  api/              generated handler interface + thin handlers + DTOs
  auth/             API-key registry, namespace resolver, shared
                    token mint/hash helpers, wire-header constants
  clientreg/        client-entity orchestration (register, rotate, resolve)
  config/           YAML config loader (Viper + struct binding)
  db/               Postgres DAL — per-entity files (pg_clients.go,
                    pg_sessions.go, ...), errors, models, tx primitive,
                    embedded migrations
  events/           session-event capture + priority-range validation
  feedback/         outcome-feedback service: UUIDv7-timestamp TTL check,
                    delegates to CorrelationsRepo.UpdateOutcome
  ingest/           chunking, format detection, intent filtering
  obs/              context-bound logging + field helpers; OTel
                    propagator install (config-gated at bootup)
  search/           two-layer search (porter → trigram fallback),
                    pluggable allocator
  secrets/          [scheme:payload] secret-reference resolver
  server/           HTTP lifecycle + middleware chain (recovery, context,
                    logging, workload-request-id, rate-limit, auth,
                    body-size, timeout, OpenAPI, session-resolve)
  session/          session-lifecycle orchestration service + LRU metadata
                    cache + Idempotency-Key dedup + TTL scanner
  snapshot/         generic event-store snapshot builder
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
