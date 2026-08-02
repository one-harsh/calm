# CALM — Context Abstraction Layer for Models

**Keep bulky tool output out of live LLM context without losing access to it.**

CALM is a shared context-management service for LLM workloads.

When a tool returns a large build log, API response, file, or query result, a workload sends the raw output to CALM before calling the model. CALM stores and indexes the full content, then returns a compact representation for the model’s context. If the model later needs a detail, it can retrieve exact excerpts from the original output.

```text
tool or API produces a large result
                 │
                 ▼
       workload sends it to CALM
                 │
        ┌────────┴─────────┐
        │                  │
        ▼                  ▼
 full output stored   compact representation
 and indexed          returned to the workload
        │                  │
        │                  ▼
        │           sent to the model
        │                  │
        │           needs more detail?
        │                  │
        │                  ▼
        └─────────► search CALM for exact text
```

For example, instead of placing a 5,000-line test run into context, the model might receive a short section summary, a set of distinctive terms, and a source label. If it needs the failing assertions later, it searches that source and retrieves only the relevant lines.

Trimming context this way is a gamble unless you can see its effect on results. So CALM also lets a workload report how its work turned out — success, retry, degraded — tied back to the calls that shaped its context. Operators get metrics labeled by outcome: not just tokens saved, but whether the model still had what it needed.

CALM is designed for teams operating multiple LLM workloads through shared infrastructure. It runs beside those workloads as an HTTP service; it is not an LLM proxy and does not control the model call.

## One service, many workloads

CALM’s API does not know whether its caller is a coding agent, an evaluation pipeline, an internal assistant, or an automated workflow. Every integration uses the same primitives:

- Create a session for a unit of work.
- Ingest bulky or durable context.
- Give the model CALM’s compact representation instead of the raw content.
- Search the stored content when exact details are needed.
- Record important session events for later reconstruction.
- Optionally report workload outcomes against CALM calls.

This repository includes two worked integrations:

1. **Coding-agent adapter** — an MCP server and a harness-native capture CLI for tools such as Claude Code, Codex, and other coding-agent hosts.
2. **Evaluation harness** — a Python workflow that calls the HTTP API directly while investigating synthetic evaluation regressions.

These integrations demonstrate two different ways to adopt the same service. They are not separate CALM products, and workloads do not need to use either one.

## What CALM provides

### Compact, recoverable context

`POST /v1/ingest` indexes raw content and returns:

- section titles and previews
- distinctive searchable terms
- optional intent-informed summary ordering
- a stable source label for later retrieval

The original content remains available for the lifetime of the session.

### Exact-text retrieval

`POST /v1/search` returns ranked excerpts from previously ingested content. Search responses are byte-budgeted so the caller controls how much material re-enters model context.

CALM uses full-text ranking for prose and code, with trigram fallback for partial identifiers and similar technical strings.

### Session state

Workloads can record structured events such as errors, decisions, file changes, or tool invocations. `/v1/snapshot` returns the highest-priority events within a caller-selected byte budget.

CALM stores these events; it does not impose a workflow-specific state model.

### Outcome-linked telemetry

Value-producing calls return a correlation ID. A workload can later report whether the associated result was successful, degraded, or required a retry.

This attaches workload-reported outcomes to CALM operations so operators can compare retrieval behavior and downstream results. The workload remains responsible for defining what success means.

### Isolation and expiration

CALM separates data by:

- **Namespace** — the deployment trust boundary, resolved from an API key.
- **Session** — the content boundary for one conversation, run, or pipeline step.

Sessions expire after inactivity or can be deleted explicitly. CALM is an ephemeral context store, not a permanent knowledge base.

## What CALM is not

CALM is deliberately narrow:

- It is not an agent orchestrator.
- It is not a long-lived RAG corpus or document-management system.
- It does not replace provider-side compaction.
- It does not proxy LLM requests.
- It does not automatically see tool output; each workload needs an integration point.
- It does not determine whether a workload succeeded.

The [High-Level Design](docs/HLD.md) contains the full motivation, architecture, invariants, and decision record.

## Status

Implemented end to end:

- session and client lifecycle
- ingest and compact representation
- ranked and document-order retrieval
- session events and snapshots
- workload feedback and outcome-labeled metrics
- namespace and session isolation
- coding-agent MCP adapter
- Claude Code shell-output capture
- Python evaluation-harness example

The `/v1/manage/*` administrative endpoints are currently stubbed and return `501`.

The public HTTP contract is defined in [docs/api/openapi.yaml](docs/api/openapi.yaml).

## Try CALM locally

### Prerequisites

- Go 1.25.x
- [go-task](https://taskfile.dev/)
- Docker
- `curl` 7.76 or newer (the walkthrough uses `--fail-with-body`)
- `jq`
- `openssl`
- Python 3 with `pip` (evaluation-harness example only)

Clone the repository and start the development database:

```bash
git clone https://github.com/one-harsh/calm.git
cd calm

task dev:up
```

Create a local namespace key:

```bash
mkdir -p .calm
openssl rand -hex 32 > .calm/calm_api.key
export CALM_DEFAULT_KEY="$(cat .calm/calm_api.key)"
```

The `.calm` directory is gitignored. The key file must contain only the key itself.

Start CALM:

```bash
task run:calm
```

In another terminal, from the repository root, load the same key and check the service:

```bash
export CALM_DEFAULT_KEY="$(cat .calm/calm_api.key)"
export CALM_URL="http://localhost:8080"

curl --fail-with-body "$CALM_URL/v1/health" | jq
```

### Exercise the core loop

Create a session:

```bash
export CALM_SESSION_TOKEN="$(
  curl --silent --show-error --fail-with-body \
    --request POST "$CALM_URL/v1/sessions" \
    --header "X-CALM-API-Key: $CALM_DEFAULT_KEY" \
    --header "Content-Type: application/json" \
    --data '{}' |
  jq -r '.session_token // empty'
)"

[ -n "$CALM_SESSION_TOKEN" ] || echo "session creation failed — see the curl error above"
```

Ingest a sample tool result:

```bash
curl --silent --show-error --fail-with-body \
  --request POST "$CALM_URL/v1/ingest" \
  --header "X-CALM-API-Key: $CALM_DEFAULT_KEY" \
  --header "X-CALM-Session-Token: $CALM_SESSION_TOKEN" \
  --header "Content-Type: application/json" \
  --data-binary @- <<'JSON' |
{
  "source": "checkout-tests",
  "format": "log",
  "content": "PASS cart totals\nPASS coupon validation\nFAIL checkout persistence\nERROR db-primary: connection refused\nretry 1 failed after 500ms\nretry 2 failed after 1000ms\nexpected order status persisted, received pending\nFAIL inventory reservation\nexpected reserved=3, received reserved=0"
}
JSON
jq '{source, summary, distinctive_terms}'
```

The response is the compact representation a workload would place into model context.

Retrieve an exact detail from the stored output:

```bash
curl --silent --show-error --fail-with-body \
  --request POST "$CALM_URL/v1/search" \
  --header "X-CALM-API-Key: $CALM_DEFAULT_KEY" \
  --header "X-CALM-Session-Token: $CALM_SESSION_TOKEN" \
  --header "Content-Type: application/json" \
  --data '{
    "queries": ["db-primary connection refused"],
    "limit": 3,
    "budget_bytes": 2048
  }' |
jq
```

The returned snippets contain exact text from the original capture.

Delete the session when finished:

```bash
curl --silent --show-error --fail-with-body \
  --request DELETE "$CALM_URL/v1/sessions" \
  --header "X-CALM-API-Key: $CALM_DEFAULT_KEY" \
  --header "X-CALM-Session-Token: $CALM_SESSION_TOKEN"
```

Stop the development database with:

```bash
task dev:down
```

## Worked integration: coding agents

Coding-agent hosts are a demanding integration case because CALM cannot automatically observe their native tools. The included adapter exposes one capture engine through two complementary surfaces.

### MCP tools

`calm-adapter` is a standard MCP stdio server. It exposes CALM-backed command, file, grep, git, and retrieval tools.

Build it:

```bash
task build:adapter
```

For Claude Code against the local CALM service:

```bash
claude mcp add calm \
  --env CALM_ADAPTER_CALM_URL=http://localhost:8080 \
  --env CALM_ADAPTER_CALM_API_KEY="[file:$(pwd)/.calm/calm_api.key]" \
  --env CALM_ADAPTER_LOG_FILE=/tmp/calm-adapter.log \
  -- "$(pwd)/bin/calm-adapter"
```

Other MCP hosts use the same binary and environment variables in their stdio-server configuration. Running several agents against the same CALM? Don't mint a key per agent: the API key identifies the deployment (the namespace), each host reports its own product name (`claude-code`, `codex`, …) as the CALM `client`, and every conversation gets its own isolated session. Two people running the same host share a `client` name, never a session.

MCP utilization is discretionary: the host chooses whether to call a CALM tool or its native equivalent. To make CALM the normal path, add a short project instruction such as:

```markdown
Prefer the `calm_*` tools over native command, file, grep, and git tools.
Use `calm_search` to retrieve earlier captured output instead of rerunning
a command when possible.
```

Run the offline MCP smoke test with:

```bash
task smoke:adapter
```

### Automatic shell capture for Claude Code

`calm-capture` integrates with harness-native hooks. For Claude Code, it observes Bash results after native execution, stores the full output in CALM, and substitutes a compact result when the hook permits it. Native permissions and approval handling remain in place.

Build and install it:

```bash
task build:capture

export CALM_ADAPTER_CALM_URL=http://localhost:8080
export CALM_ADAPTER_CALM_API_KEY="[file:$(pwd)/.calm/calm_api.key]"

bin/calm-capture init --harness=claude
```

The installer prints the final plugin commands:

```bash
claude plugin marketplace add ~/.calm/plugins/claude
claude plugin install calm-capture
```

The installed hooks cover native Bash output structurally. The MCP adapter remains useful for CALM-backed file, grep, git, and explicit retrieval operations. You can use either surface or both.

### Troubleshooting

**`claude mcp list` shows `calm: ✗ Failed to connect`.** The two most common causes are silent: a relative or `~` path for the binary or in the `[file:…]` reference (the host execs the binary without a shell, so `~` is never expanded, and the secret resolver rejects non-absolute paths), or a key file that contains anything besides the bare key. Use absolute paths for both and keep `.calm/calm_api.key` to just the hex value.

**Confirming capture actually works.** Ask the agent to run a command through `calm_run_command`, then `calm_search` a term from that output. Every tool call is logged with the same `correlation_id` in `/tmp/calm-adapter.log` and in CALM's own log, so you can trace one call across the boundary and see where it stopped.

Adapter architecture and contributor documentation live in [internal/adapter/README.md](internal/adapter/README.md).

## Worked integration: evaluation pipelines

The Python evaluation harness demonstrates CALM without a coding agent or MCP.

It ingests synthetic evaluation artifacts, searches them using investigation-style queries, verifies expected evidence, reports exact UTF-8 byte reduction, and submits workload feedback.

With the local CALM service running:

```bash
task example:eval:deps
task example:eval:demo
task example:eval:bench
task example:eval:verify
```

The example reads `CALM_DEFAULT_KEY` automatically. Its implementation talks only to the public HTTP API.

See [examples/eval-harness/README.md](examples/eval-harness/README.md) for its workload shape, benchmark definitions, and limitations.

## Integrating another workload

A custom integration follows the same lifecycle as the examples:

1. Register a client if the workload needs a name other than `default`.
2. Create a session at the beginning of a conversation, run, or workflow step.
3. Ingest bulky tool results before constructing the next model request.
4. Put CALM’s compact response into context instead of the raw result.
5. Expose CALM search to the model or invoke it from workload middleware.
6. Record important state as session events when reconstruction matters.
7. Report workload outcomes when they become known.
8. Delete the session when the unit of work ends.

Integrations should define timeouts and fall back to raw content if ingest is unavailable. CALM is a sidecar, so the workload retains control of its model request path.

Use [docs/api/openapi.yaml](docs/api/openapi.yaml) as the canonical API contract. No CALM-specific SDK is required.

## Configuration and deployment

CALM reads YAML configuration from the file specified by `CALM_CONFIG_FILE`.

The annotated template is [cmd/calm/config/example.yaml](cmd/calm/config/example.yaml).

Secrets use explicit references:

- `[env:NAME]` — read from an environment variable
- `[file:/path/to/secret]` — read from a mounted file
- `[text:value]` — inline text; avoid this in committed production configuration

Production deployments require Postgres with CALM’s full-text/BM25 and trigram extensions. TLS is normally terminated by the deployment’s ingress or service mesh.

Namespaces can optionally require per-client credentials in addition to the namespace API key. See the OpenAPI authentication description and the HLD’s isolation model before exposing CALM to multiple trust domains.

## Building and testing

Reproducible development commands use `task`:

```bash
task tools:install        # install formatting, lint, and generation tools
task build                # build calm, calm-adapter, and calm-capture
task test:unit            # fast tests without Postgres
task test:integration     # integration tests against the dev database
task fmt                  # gofumpt, goimports, and license headers
task lint                 # static analysis and license checks
task ci                   # complete pre-merge gate
```

Integration tests use a real Postgres instance started by `task dev:up`.

## Repository layout

```text
cmd/
  calm/                   CALM HTTP service
  calm-adapter/           coding-agent MCP server
  calm-capture/           harness-native capture CLI

internal/
  api, auth, db, ingest,
  search, session, ...    service implementation
  adapter/                shared coding-agent capture engine

examples/
  eval-harness/           direct-HTTP evaluation workflow

docs/
  HLD.md                  canonical architecture and decisions
  api/openapi.yaml        canonical HTTP contract

test/
  integration/            real-Postgres integration scenarios
```

## Contributing

Read [CLAUDE.md](CLAUDE.md) before changing the repository. It contains the shared engineering directive for human and AI-assisted development. [AGENTS.md](AGENTS.md) points non-Claude harnesses to the same rules.

Contribution requirements, DCO sign-off, and dependency policy are documented in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
