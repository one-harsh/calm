# CALM — Context Abstraction Layer for Models

An HTTP service that reduces LLM token waste by filtering and compressing
tool output before it enters the context window. CALM sits *beside* the
workload and the LLM (a sidecar, not a proxy) — workloads call CALM
explicitly to ingest tool output, search the indexed content on later
turns, and capture session events that outlive the conversation's text
window.

The canonical design lives in [`docs/HLD.md`](docs/HLD.md). This README
is the operator-facing landing page.

## Status

Pre-implementation. The scaffolding, configuration, auth, storage, and
migration layers are in place; the three core primitives (ingest, search,
session state) are stubbed and return `501 Not Implemented` while the
WI sequence in [`docs/milestones/v1.md`](docs/milestones/v1.md) lands the
real handlers. Concretely working today:

- YAML-config loader with env-var override and bracketed secret references
- Namespace registry built from operator config; bearer-auth middleware
- Postgres storage open + embedded migrations + per-namespace client seed
- Server lifecycle (graceful shutdown, OpenAPI request validation,
  per-namespace rate limiting wired through the middleware chain)
- MCP adapter binary skeleton (`cmd/calm-adapter`) that creates a session
  and falls through to raw output on any CALM failure (the `never-worse`
  invariant)

Routes that return real data don't exist yet. Treat the binary as a
chassis, not a service you can put behind a workload today.

## What problem this addresses

LLM APIs charge per input token, and the entire context window is sent
on every turn. Tool outputs (logs, API responses, file dumps) enter
context verbatim, sit there until compaction, and get re-billed each
turn. Long contexts also degrade answer quality independently of cost —
the *Lost in the Middle* effect (Liu et al., 2023) is structural to how
transformers attend.

CALM's bet is that for a shared team deployment serving multiple LLM
workloads, the right place to solve this is a single piece of
infrastructure that ingests tool output, returns a compact representation
to the model, and exposes the raw content via search when the model asks
for it. See the HLD's *Motivation*, *Goals & Non-Goals*, and *A note on
RAG* sections for the full argument; CALM is explicitly not a RAG system,
not an orchestrator, and not for solo install.

## Architecture at a glance

Single statically-linked Go binary, REST API with JSON payloads, all
state behind a DAL backed by Postgres.

- **Three core primitives** (ingest, search, session state) — see HLD's
  primitives section.
- **Workload patterns** identified by `namespace` (server-resolved from
  the bearer API key) and an optional `client` identifier — see DL01.
- **Six design invariants** (`never-worse`, `workload-agnostic`,
  `session-isolation`, `sidecar-not-proxy`, `content-fidelity`,
  `idempotent-indexing`) — see the HLD's design-invariants section.
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

Once running, the only handler that returns real data today is
`/v1/health`:

```bash
curl -i http://localhost:8080/v1/health
```

The auth middleware is wired and runs before everything: requests
without `Authorization: Bearer $CALM_DEFAULT_KEY` get a 401.

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
`CALM_SERVER_ADDRESS=":9090"`, `CALM_STORAGE_DSN="postgres://..."`.
Slice fields under `namespaces` are not env-overridable.

## API

The HTTP contract is generated from
[`docs/api/openapi.yaml`](docs/api/openapi.yaml). The full surface (most
of it currently stubbed):

```
GET    /v1/health
GET    /v1/version
POST   /v1/sessions
DELETE /v1/sessions/{session_id}
GET    /v1/sessions/{session_id}/sources
GET    /v1/sessions/{session_id}/snapshot
POST   /v1/ingest
POST   /v1/search
POST   /v1/events
GET    /v1/events/{session_id}
GET    /v1/manage/sessions
GET    /v1/manage/clients
DELETE /v1/manage/clients/{client}
```

Every request needs `Authorization: Bearer <key>`. Namespace is
server-resolved from the key — clients never send a namespace in the
request body. Cross-namespace mismatches return **404, not 403**
(invisibility, not denial — encoded in the OpenAPI spec).

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
  auth/             API-key registry, namespace resolver
  config/           YAML config loader (Viper + struct binding)
  db/               Postgres DAL, embedded migrations
  obs/              context-bound logging + field helpers
  secrets/          [scheme:payload] secret-reference resolver
  server/           HTTP lifecycle + middleware chain
adapter/            MCP-only packages (consumed by cmd/calm-adapter)
docs/
  HLD.md            canonical design document
  api/openapi.yaml  formal API contract
  milestones/v1.md  work-item sequence for the v1 implementation
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
