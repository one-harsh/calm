# CALM — Context Abstraction Layer for Models

CALM is context-management infrastructure for team-deployed LLM
workloads. When an LLM app calls a tool — runs a command, reads a file,
hits an API — the raw output normally lands in the model's context
window and is re-billed on every turn. CALM sits *beside* the workload
and the LLM (a sidecar, not a proxy): it captures that output, hands the
model a compact representation, indexes the raw content so the model can
search it back when it actually needs it, and — the part that makes it
more than a compressor — attributes the outcomes a workload reports back
to the specific calls that produced them, so a team can *measure*
whether their context management is helping. Token spend *and* retrieval
quality are its twin observable concerns.

**CALM is workload-agnostic.** Any LLM workload that produces bulky tool
output is a fit — coding agents, eval pipelines, retrieval-heavy
assistants. This repo ships two *worked integrations* (a coding-agent
adapter and a Python eval harness) as examples of the HTTP contract, not
as the product — the product is the HTTP service and its contract; the
integrations are showcases you can copy or ignore.

**New here?** Read the HLD's *Motivation* and *Goals & Non-Goals*
([`docs/HLD.md`](docs/HLD.md)) for *why* CALM exists, then this page for
how to run it and wire a workload in. The HLD is the canonical design;
this README is the operator-facing landing page. Contributing (or an AI
coding assistant working in this repo)? The engineering directive is
[`CLAUDE.md`](CLAUDE.md).

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

## How it works

A single statically-linked Go binary exposing a REST API, with all state
behind a data-access layer backed by Postgres. The moving parts, in
plain terms:

- **Three primitives** — *ingest* (capture tool output and index it),
  *search* (retrieve it on demand — ranked queries, or a whole capture
  reread in order), and *session state* (events that outlive the
  conversation). The HLD's primitives section is the formal treatment.
- **Outcome attribution** — every value-producing response carries a
  correlation ID; a workload echoes it back to `/v1/feedback` to report
  whether that call helped, and CALM labels its metrics by the reported
  outcome. That's the mechanism behind the measurable-quality-signal the
  intro promises.
- **A sidecar, never a proxy.** CALM is beside the workload, not between
  it and the LLM. If CALM is slow or down, the workload's own path still
  works — this "never-worse" guarantee is the first of six design
  invariants (workload-agnostic, isolation, fidelity, idempotency, …)
  named and argued in the HLD.
- **Two isolation boundaries.** Data is partitioned by *namespace* (the
  security/trust boundary, resolved server-side from the API key) and by
  *session* (the content/scope boundary). A cross-namespace read isn't
  denied, it's *invisible* — it returns 404.
- **Postgres storage**, with full-text ranking (prose and code) and
  trigram matching for partial identifiers via standard Postgres
  extensions.

## Status

Working end-to-end: ingest, search, session events, outcome attribution
(a correlation row per value-producing call, updated via `/v1/feedback`),
and the coding-agent adapter (capture→retrieve through an MCP server or a
shell hook). Search ranks results across prose and code (BM25), with a
trigram fallback that matches partial identifiers an exact term misses;
it byte-budgets each response and can reread a whole capture in document
order, and each response surfaces the most distinctive terms it saw. Still stubbed (returns `501`): the
`/v1/manage/*` administrative API.

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
`.calm/calm_api.key` (gitignored) lets the integrations below read the *same*
key from that file, so the server and any adapter always agree.

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

## Worked integrations

CALM ships integrations as examples of the HTTP contract, not as
libraries a workload must depend on. Two today, as peers:

- **Coding agents** — the `calm-adapter` MCP server and the
  `calm-capture` shell hook route a coding agent's tool calls through
  CALM. This is the *hardest* integration case (CALM can't see a host's
  native tools, so the adapter runs alongside them), which is why it's
  the most fully worked — deep-dived below.
- **LLM-eval pipelines** — the Python eval harness triages eval runs
  (prompt diffs, run summaries, tool traces, judge rationales, golden
  investigation queries) through the same HTTP contract, with no coding
  agent in sight. It reports exact UTF-8 byte counts and intentionally
  does not estimate model tokens. See
  [`examples/eval-harness/README.md`](examples/eval-harness/README.md).

A third or fourth workload wires in the same way: talk to the HTTP
contract. The rest of this section is the coding-agent walkthrough — one
example, in depth — so skip to [Configuration](#configuration) if that's
not what you're integrating.

### Coding agents

Two ways to route a coding agent's actions through CALM — use either or both:

- **MCP tools** — the `calm-adapter` MCP server exposes `calm_*` tools the host calls
  directly. Utilization is discretionary (the agent picks the tool), so it pairs with a
  `CLAUDE.md` directive.
- **A shell hook** — the `calm-capture` CLI, installed as a harness-native hook, rewrites
  every native shell command to run through capture. Utilization is structural: the hook
  fires on every shell execution, no directive needed.

The adapter's own design contract lives in
[`internal/adapter/docs/DESIGN.md`](internal/adapter/docs/DESIGN.md) (and
its source-label grammar in
[`internal/adapter/docs/LABELING.md`](internal/adapter/docs/LABELING.md)) —
read those for depth; what follows is enough to get wired.

The commands below wire against the **local dev** CALM from the Quickstart
(`http://localhost:8080`, the `.calm/calm_api.key` file). For a **deployed CALM**, point
`CALM_ADAPTER_CALM_URL` at it and supply your team's namespace key the way your secret tooling
delivers it (`[env:…]` / `[file:…]`) instead of the local `openssl`-generated file.

#### Via the MCP adapter (`calm-adapter`)

The adapter is a standard MCP stdio server, so any MCP host — Claude Code, Codex, Cursor,
Claude Desktop, … — can use it. Build it (`task build:adapter`), then register it.

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

Two rules this encodes, both learned the hard way:

- **Absolute paths only** for the binary and the `[file:…]` path — Claude Code execs the
  binary directly (no shell, so `~` isn't expanded) and the secret resolver rejects `~`/relative
  paths; either one silently becomes "failed to connect." Running from the repo root makes
  `$(pwd)` resolve both.
- **The key file holds only the bare key** — `[file:…]` uses the file's whole trimmed contents,
  so `.calm/calm_api.key` must be *just* the hex (no `export`, no `VAR=`, no quotes). It's the
  same file CALM is running with, so server and adapter agree by construction.

`claude mcp list` then shows `calm: ✓ Connected`. On connect the adapter registers its client
(idempotent) and creates a session; both are torn down on disconnect.

**Other hosts** (Cursor, Claude Desktop, Codex) use the `mcpServers` convention — same
`command` + `env`; only the config file's location differs:

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

These files are usually committed, so they reference `[env:CALM_DEFAULT_KEY]` rather than
inlining the key — export it where the host launches
(`export CALM_DEFAULT_KEY=$(cat .calm/calm_api.key)`). `CALM_ADAPTER_CALM_API_KEY` uses the
secret-reference dialect (`[text:..]` / `[env:..]` / `[file:..]`; raw values are rejected), and
adapter logs go to `CALM_ADAPTER_LOG_FILE` — never stdout, which is the JSON-RPC channel.

**One key, many agents.** What authenticates you to CALM is the *namespace* credential — the
trust boundary between your deployment and CALM, not a per-agent key. Run Claude Code and Codex
against the same CALM and they **share** the namespace key; the host's `clientInfo.name`
(`claude-code`, `codex`, …) becomes the CALM `client` that distinguishes them. Per-agent secrets
(a server-minted bearer token per client, for real within-namespace isolation) are the
*credentialed* namespace mode (`require_client_credentials: true`).

Registration exposes the tools: `calm_run_command` (run a shell command locally, output captured
and returned compact), the file tools (`calm_read_file` / `calm_edit_file` / `calm_write_file` /
`calm_list_dir` / `calm_grep`), git inspection (`calm_git_status` / `calm_git_diff`), and
`calm_search` (retrieve captured output on demand).

**Make CALM the default (not just available).** Registration only *exposes* the tools; their
descriptions nudge the agent, but to make routing-through-CALM the default, add a directive to
your project's `CLAUDE.md` / `AGENTS.md`:

```markdown
- Use the `calm_*` tool for each operation instead of the native equivalent: `calm_run_command`
  for shell commands, `calm_read_file` / `calm_edit_file` / `calm_write_file` / `calm_list_dir` /
  `calm_grep` for file operations, `calm_git_status` / `calm_git_diff` for git inspection — they
  keep raw output out of the context window and index it for retrieval.
- Use `calm_search` to retrieve earlier output instead of re-running the command: queries when
  you know what you're looking for (ranked snippets), or `source` without queries when you need
  the surrounding flow — it rereads the capture in document order.
```

Without it, the agent often falls back to its native shell tool — which the `calm-capture` hook
below fixes structurally. To confirm a fresh setup works, have the agent run a command through
`calm_run_command` and then `calm_search` a term from that output; each tool call and CALM
request is logged with a shared `correlation_id` (in both `/tmp/calm-adapter.log` and CALM's own
log), so a single call joins across the boundary. `task smoke:adapter` is an offline check that
the binary speaks MCP.

#### Via a shell hook (`calm-capture`)

`calm-capture` is a single-invocation CLI a harness-native hook rewrites shell tool calls onto,
so capture is structural rather than discretionary. Build it (`task build:capture`). Its commands:

- `calm-capture exec -- <command>` — runs the command, captures its full output into the CALM
  session, and prints the engine's presentation in place of the raw output (exit code propagates
  verbatim; on capture failure the raw output is shown unchanged — `never-worse`). This is the
  form the hook rewrites to; you don't invoke it directly.
- `calm-capture search [source=<label>] <terms>` — retrieve captured output (the same primitive
  as the MCP shell's `calm_search`).
- `calm-capture feedback <ref> <outcome>` — report an outcome (`success` / `retry` / `degraded`).
- `calm-capture hook` — the harness-facing entry point: reads a hook payload on stdin, emits the
  rewrite (or a pass-through) on stdout. The harness invokes this, not you.
- `calm-capture init --harness=claude` — installs the hook for Claude Code (below).

Every non-degraded capture prints a compact trailer pairing the source label with the feedback
ref, so recall and outcome reporting are both discoverable from the result:

```text
↳ source=calm:v1:shell:sh#1@ab12cd · feedback: calm-capture feedback <ref>
```

**Install for Claude Code.** `init` writes a durable config home under `$CALM_HOME` (default
`~/.calm`) — `adapter.yaml` plus a `0600` credentials file it references — and a plugin under
`$CALM_HOME/plugins/claude/`, validates the credential pairing against CALM, then prints the
one-time install step:

```bash
export CALM_ADAPTER_CALM_URL=http://localhost:8080
export CALM_ADAPTER_CALM_API_KEY="[file:$(pwd)/.calm/calm_api.key]"
bin/calm-capture init --harness=claude
# then, as init prints:
claude plugin marketplace add ~/.calm/plugins/claude
claude plugin install calm-capture
```

The plugin installs a PreToolUse (Bash) hook that rewrites each shell command through
`calm-capture exec`, and a SessionStart hook that injects the retrieval card and reclaims idle
capture state. The rewrite never approves a command — normal permission prompts still run, and the
wrapped command stays legible in the approval dialog. Because the plugin consent screen does not
itemize hooks, `init` is the disclosure surface: it prints exactly what installs and never writes a
permission rule itself. One caution it repeats — never choose "don't ask again" for the
`calm-capture exec` wrapper, which would write a blanket allow rule that auto-approves every
wrapped command.

## Configuration

*Skip this section unless you're deploying CALM.* Local dev is covered by
the Quickstart above; this is the operator reference.

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
`CALM_STORAGE_MAX_OPEN_CONNS=25` and `CALM_STORAGE_MAX_IDLE_CONNS=25`
(database/sql connection-pool caps; defaults 25/25, idle must be ≤ open;
`0` falls through to the driver default of unlimited open / 2 idle, which
causes reconnect churn under load), `CALM_STORAGE_CONN_MAX_LIFETIME=30m`
(recycles connections so credential rotation and LB changes take effect;
`0` keeps them indefinitely),
`CALM_STORAGE_TRIGRAM_SIMILARITY_THRESHOLD=0.5`
(pg_trgm `strict_word_similarity_threshold` for the layer-2 search fallback;
raise to tighten partial-identifier recall, lower to loosen; bounded `(0, 1]`),
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

The full HTTP contract — every route, request/response shape, and status
code — is defined in [`docs/api/openapi.yaml`](docs/api/openapi.yaml),
and the handlers are generated from it, so this README doesn't mirror the
route list. `/v1/manage/*` (session and client administration) is
currently stubbed (`501`). Request auth is covered in the
[Quickstart](#quickstart-local-dev); the response-header contract
(correlation id, `X-Workload-Request-Id`, W3C trace propagation) is in the
HLD's response-headers section.

## Building and testing

Everything reproducible goes through `task`:

```bash
task build              # build all binaries (calm + calm-adapter + calm-capture)
task test               # Go unit + integration (needs `task dev:up`)
task test:unit          # fast inner-loop tests, no Postgres needed
task example:eval:check # Python eval-harness tests
task ci                 # full pre-merge gate: dco + gen + lint + test + examples + coverage + build
task gen:api            # regenerate handlers/client from openapi.yaml
task gen:mocks          # regenerate mockery mocks
task fmt                # gofumpt + goimports
task example:eval:test  # tests for the Python eval-harness example
```

Integration tests run against a real Postgres started by `task dev:up`
— mocking the DB at the DAL boundary would hide bugs that bite in prod
migrations.

## Repo layout

*For contributors — a map before you open the source; per-package detail
lives in the code (and, for the adapter, in
[`internal/adapter/README.md`](internal/adapter/README.md)).*

```
cmd/         the three binaries: calm (the service), calm-adapter (MCP
             shell), calm-capture (capture-CLI shell)
internal/    the service, one Go package per concern — api, auth, clientreg,
             config, db (Postgres DAL), events, feedback, ingest, obs, search,
             secrets, server, session, snapshot — plus adapter/ (the capture
             engine and its two shells)
examples/    worked integrations (eval-harness/, the Python LLM-eval showcase)
docs/        HLD.md (canonical design) and api/openapi.yaml (the contract)
test/        real-Postgres end-to-end integration scenarios
```

## Contributing

The engineering directive — code-side conventions, file layout, comment
policy, logging, middleware order, testing rules, and the day-to-day
development discipline — is [`CLAUDE.md`](CLAUDE.md). (Non-Claude coding
harnesses: [`AGENTS.md`](AGENTS.md) is a one-line pointer to it, so agents
converge on the same rules.) The contributor contract — DCO sign-off,
SPDX headers on new Go files, AGPL-free dependency policy — is in
[`CONTRIBUTING.md`](CONTRIBUTING.md). The HLD is the spec; code follows
the HLD, and silence in the HLD on something the code needs is a design
gap to surface, not a license to improvise.

## License

Apache 2.0 — see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE).
