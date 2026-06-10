# CALM - Context Abstraction Layer for Models

---

# 1. Motivation & Problem Statement

LLM APIs charge per token on input. The entire context window is sent as input on every turn. Anything sitting in context — whether the model needs it or not — gets billed again and again until the conversation compacts.

Teams running shared AI workloads — internal LLM applications, agent-based pipelines, coding-agent integrations against shared infrastructure — hit this problem at scale. When an application's tool call returns 50 KB of logs, that 50 KB enters the LLM's context window verbatim. It sits there for 10–15 turns, gets re-charged each time, before compaction clears it. There is no filtering, no relevance gating, no observability. The context window is treated as a pipe, not a managed resource.

The shared deployment shape makes this worse, not better. A team running an internal slackbot, an eval harness, a debug agent, and several developers' coding agents through one deployment multiplies the unfiltered-tool-output problem across workloads, none of which has the leverage to solve it alone. The team needs shared infrastructure that compresses tool output before it enters the LLM's context — and that infrastructure must serve every workload uniformly, without each workload's authors having to know it exists. This is the audience CALM is built for: team-deployed AI workloads, not individual developer installs.

## What this addresses

- **Context bloat.** Raw tool output enters the LLM context verbatim. A paginated API response, a verbose log dump, a directory listing — none of it is filtered before reaching the model. It sits in context, gets re-charged per turn, until compaction.

- **Worse answers as sessions progress.** LLMs attend poorly to information in the middle of long contexts (Liu et al., "Lost in the Middle: How Language Models Use Long Contexts", Stanford, 2023 — [arxiv.org/abs/2307.03172](https://arxiv.org/abs/2307.03172)). As the window fills with stale tool output, the model has more to attend to and does it worse. This is structural to how transformers work, not a bug providers will fix.

- **State loss on compaction.** When context fills up, the platform summarizes and drops older messages. The model forgets which files were edited, what errors occurred, what the user already tried. Session restarts and re-prompting follow.

- **No visibility into context quality.** Teams can't answer basic questions: how much of the token spend goes to data the model never uses? When a session degrades, is the model failing or is the context being polluted? Without instrumentation, the answer is a guess.

- **Token spend scales with data volume, not with value.** Real tool outputs — logs, API responses, query results, file dumps — typically compress by an order of magnitude into a compact representation that preserves what the model actually needs: section titles, preview lines, and a searchable vocabulary of indexed terms. The raw content stays in a queryable store; only the compact form enters context. Specific compression ratios and per-team spend impact vary by workload mix and content shape; the structural argument holds regardless of empirical numbers.

## Cost of inaction

Two dimensions, both real:

**Financial.** Tool-heavy session token spend grows with payload size, not with what the model uses. Across a team running multiple AI workloads at typical engineering throughput, this compounds — a single misbehaving slackbot ingesting verbose API responses can dominate the team's monthly API bill. Concrete figures vary by model pricing and deployment shape; the per-team multiplier is non-trivial regardless.

**Qualitative.** The same context pollution that wastes tokens degrades answer quality on every model — hosted or self-hosted. A model attending to 45 KB of stale tool output produces worse answers than one attending to a few KB of relevant content, regardless of who runs the inference. For self-hosted deployments the cost is not financial — it's quality. For hosted-API teams, it's both.

## Measurable quality

Both the cost and the quality dimensions are instrumented, not asserted. Every session exposes signals — re-ingest rate, intent coverage, search-match-layer distribution, snapshot injection frequency, and more (§10 documents the full set) — that let operators measure recovered token spend AND detect when compression is quietly degrading answer quality, alongside their existing workload-side outcome metrics (task completion, retry rates, user corrections). Cost and quality are CALM's twin observable concerns, both load-bearing.

## Other related work

This problem space has parallel efforts. [context-mode](https://github.com/mksglu/context-mode) (Mert Koseoğlu, source-available under Elastic License 2.0) addresses the same surface area through a different architecture: an MCP server adopted directly by individual developers, with prescriptive routing rules that direct the LLM toward sandboxed code execution patterns. CALM occupies a parallel but distinct lane — shared infrastructure for teams, deployed by a platform-ops engineer, serving multiple internal LLM applications through a uniform HTTP API; the MCP adapter binary CALM ships is one of several integration surfaces, not the primary architectural commitment.

---

# 2. Goals & Non-Goals

## Goals

- **Reduce token waste.** Filter and compress tool output before it enters the LLM context. Raw content stays in the service; only the compact form (section titles, preview lines, searchable vocabulary) goes to the model.

- **Index and retrieve on demand.** Don't stuff everything into context upfront. Index it, search it, pull in what's relevant for the current turn — driven by the LLM's own follow-up search queries when it needs to drill into the indexed content.

- **Maintain session state across LLM call boundaries.** Capture structured events during a session (files edited, errors observed, user decisions, tool invocations) and reconstruct working state — after platform-driven compaction in interactive sessions, or across iterations of an automated agent loop. The events outlive the conversation's text window.

- **Make context quality observable.** Token consumption, compression ratios, search hit rates, session continuity. Today these can't be answered without instrumentation that doesn't exist; CALM exposes them as first-class metrics.

- **One HTTP API, many workloads.** Any LLM application reaches CALM through the same HTTP API, identified by namespace + (optional) client identifier. The MCP adapter binary CALM ships is one such workload's integration surface, not a privileged architectural category. The same API serves an internal slackbot, a CI eval harness, a multi-step automated pipeline, or a developer's coding agent equally.

- **Standalone service.** CALM owns its own storage, exposes its own API, runs as its own binary. No hard dependencies on internal platform components. Deployable to a team's cluster with docker-compose; scalable to a platform team's Helm chart.

## Non-Goals

- **Not an orchestrator.** CALM doesn't make LLM calls and doesn't decide what the agent does next. It sits in the data path — what content is available to the model — never in the control path.

- **Not a corpus management system.** No crawlers, no embedding pipelines, no persistent knowledge bases. Content arrives because an LLM application just fetched or produced it, and it expires when the session ends. Ephemeral by design.

- **Not a prompt engineering tool.** CALM manages what data is available for context — it doesn't touch prompts themselves, doesn't inject system messages, doesn't shape the model's reasoning patterns.

- **Doesn't replace compaction.** Compaction is the LLM platform's responsibility. CALM reduces how often it fires and how much state is lost when it does, but doesn't eliminate the need.

- **Not a workflow record.** A CALM session has one durable service state: active. Explicit close or TTL expiry deletes the session and all its child rows; no terminal state — completed, failed, abandoned — is retained. Workload outcomes are workload-resident, correlated via workload-side telemetry and via labels attached at session creation. Operators who need long-tail row-level retention configure an exporter sink to operator-resident storage (Postgres, OTLP, file, multi-sink); CALM does not mandate a sink. See Decision Log [DL14](#dl14).

- **Not for solo install.** CALM is shared infrastructure deployed by a team or platform-ops engineer. Running a single-process CALM against a local data store on an individual developer's laptop is explicitly out of scope; that case is well-served by simpler tools optimized for single-process MCP.

- **Not an identity provider.** CALM delegates identity orchestration to the platform layer. Workload auth models are heterogeneous (internal apps have their own auth, automated pipelines have service accounts or CI tokens, MCP-mediated coding agents have dev-machine identity), and the operator running CALM is already the trust authority for the workloads talking to it. CALM consumes `API key → namespace` and trusts the platform layer to map whatever workload identity exists onto that. No per-action authz, no IDP integration, no refresh-token flow, no principals/roles/policies. See Decision Log [DL10](#dl10).

## A note on RAG

CALM uses retrieval over indexed content to manage what enters context. The obvious question is *why not just use RAG?* — and the answer is that context management and retrieval-augmented generation solve different problems:

- **Content economics.** RAG augments generation with facts from a pre-curated, long-lived corpus (docs, knowledge bases, manuals) where embedding cost amortizes over thousands of queries. Context management deals with content that arrived as a side-effect of agent actions (tool outputs, logs, API responses), lives for the session, and is often read once or never. The RAG-shaped overhead (embedding generation, vector store, corpus lifecycle) is a tax on ephemeral content.

- **Search-quality shape.** RAG needs semantic similarity because heterogeneous natural-language docs require bridging vocabulary gaps ("authentication failures" ≈ "login errors"). Context management handles technical tool output with predictable per-session vocabulary, where the LLM has already seen the distinctive terms via the ingest response. Lexical match with morphology handling carries most of the load.

- **Position in the system.** RAG is an *answer mechanism* the workload invokes when it chooses to. Context management is invariant *infrastructure* — every managed tool call passes through it. Designing context management as a search product pulls in surface (corpus lifecycle, relevance tuning, embedding refresh) the problem doesn't have.

So: no document pipeline, no persistent corpus, no embedding model, no vector store. Content is indexed ephemerally during a session and the index expires when the session ends.

---

# 3. Workloads & Clients

Any LLM application reaches CALM through one HTTP API, identified by namespace + (optional) client identifier (see Decision Log [DL01](#dl01)). This section sketches the workload patterns CALM is typically run against — illustrative, not an exhaustive taxonomy. New workload patterns integrate without architectural changes; the section's purpose is to ground the reader in *what kinds of things* hit CALM, not to define a closed set of supported consumers.

## Internal LLM applications

Slackbots, internal copilots, query-answering assistants, debug bots — applications a team builds for its own use, deployed as services that call CALM directly over HTTP. The application owns its session lifecycle (create on conversation start, delete on conversation end or rely on TTL); the application's tool-call handler decides what to ingest, what to search, and what events to capture.

Tool output from these applications varies widely — API responses, query results, file contents, conversation context — and is mostly prose-shaped or structured-data-shaped. The compression value comes from the size of typical responses (often 10–100 KB) versus what the LLM actually needs to keep.

## Automated agent pipelines

Multi-step automated workflows that run LLM-driven agents — batch processing jobs, CI eval harnesses, scheduled report generators. Each step (or each pipeline run) typically maps to a session: created at step start, used during the agent loop, deleted at step end. Pipelines run server-side, often headless, with their own service accounts and namespaces.

These workloads are sensitive to per-step token cost (at pipeline throughput, per-call savings compound) and to the iteration ceiling that unmanaged context imposes — the common "at most N tool calls" mitigation in prompts is a capability ceiling that compression and on-demand retrieval lift.

## Coding agents via the MCP adapter

External coding agents — Claude Code, Cursor, Codex, and similar — used by developers on the team. These reach CALM through the MCP adapter binary CALM ships: the developer runs the adapter on their laptop, points it at the team's CALM cluster, and the coding agent talks MCP to the local adapter while the adapter talks HTTP to CALM. The adapter is a local-execution surface for the developer's coding agent — file reads, shell commands, build invocations all run on the developer's machine and only their output is forwarded to CALM (see Decision Log [DL02](#dl02)).

Tool output from coding agents is substantially code-shaped — file reads, build output, git logs, test results, source dumps. The MCP adapter passes format hints to CALM based on tool-call patterns (`Read('foo.py')` → code, `Bash('git log')` → prose) so the indexing pipeline can apply the right tokenization strategy.

## Common thread

All three workload patterns above — and any new workload that integrates — share the same primitives:

- A namespace credential authenticates the workload to CALM; an optional `client` identifier names which application is calling.
- A session is created explicitly by the workload via `POST /v1/sessions` (see Decision Log [DL03](#dl03)), lives for the workload's chosen lifetime, and is cleaned up by explicit `DELETE` or TTL expiry.
- Tool output is ingested before entering the LLM context — CALM sits beside the LLM call, never between, and is invoked by the workload itself rather than transparently proxying any traffic (see Decision Log [DL04](#dl04)). CALM returns a compact representation; the workload uses it in place of the raw output.
- The LLM can issue follow-up search queries against the indexed content via a search tool the workload's middleware exposes, returning exact-text snippets within byte budget.
- Structured events captured during the session (file edits, errors, decisions, tool invocations) inform state reconstruction when needed.

The integration shape is uniform across workloads. Specific API surfaces, failure modes, and the integration contract for tool handlers live in §6.

---

# 4. Core Primitives & Design Invariants

## Core Primitives

Three core primitives.

### Content Ingestion

The universal entry point. Workloads POST raw content — tool output, query results, search snippets, file contents, whatever the workload just produced or fetched — and CALM makes it compact and searchable. CALM does not fetch external data on its own; the workload owns the fetch, CALM owns what happens next.

The ingestion layer chunks the input, indexes it into the knowledge store, and returns a compact representation: section titles, preview lines, and a vocabulary of distinctive indexed terms. The raw content stays in the store for on-demand retrieval; only the compact form returns to the workload for inclusion in the LLM context.

**Format handling** is two-tiered.

Auto-detected, no workload effort:
- **JSON** — parsed and chunked by key paths
- **Markdown** — chunked by heading hierarchy; code blocks kept intact
- **Plain text** — chunked by line groups

Format-hinted (workload passes an optional `format` field):
- **Log output** — chunked by time window or error grouping rather than arbitrary line splits
- **Stack traces** — kept as single logical units, never split across chunks
- **CSV/TSV** — header row attached to every chunk so each chunk is self-describing
- **Metrics** — chunked by metric name so searches return complete series

If no hint is provided, auto-detection handles the basics. New format-aware chunkers slot in behind the same interface.

Workloads can pass `intents` (up to 3) alongside content to shape the compact summary's ordering. When intents are provided and content exceeds a configurable size threshold, CALM runs a search per intent against the just-indexed content and fuses the per-intent rankings via Reciprocal Rank Fusion (RRF) to order `summary`. The per-intent search uses the same two-layer fallback as workload-issued `/v1/search` queries — one search semantics, no carve-out for the ingest path. Each section in the summary declares which intents it addresses through a `matches` array — derived from the section's rank in each intent's individual top-K results, not from raw scores (see Decision Log [DL05](#dl05)).

The summary always contains all indexed section titles; intents shape *ordering*, not inclusion. There is no binary match-or-fallback semantics — sections matching no intents simply appear lower in the ordering with empty `matches`. Without `intents`, the summary is in document order and `matches` is omitted per section.

The compact representation also carries a **distinctive-terms** vocabulary derived from the indexed content — the top-N terms by IDF — so the LLM has a concrete handle on what's searchable in the indexed content without having to guess.

### Knowledge Store

The query layer over the ingested content. Search uses ranked retrieval with a two-layer fallback:

1. **Stemmed / identifier-preserving search.** Porter stemming on prose-shaped chunks ("caching" matches "cached"); identifier-preserving tokenization with no stemming on code-shaped chunks (`getUserById` survives as a single token). Tokenization branches on the chunk's `content_type` to give each shape its appropriate strategy (see Decision Log [DL06](#dl06)). AND across query terms first; falls back to OR.
2. **Trigram substring matching.** Partial-term matches that the layer-1 tokenizers miss — `connPool` finds `connectionPool`.

Results are exact indexed text with smart snippet extraction around matching terms. No summaries, no paraphrases.

BM25 ranking weights title fields higher than content, so heading matches surface first. Backed by a BM25-capable Postgres extension (`pg_search` or `pg_textsearch`) with `pg_trgm` for the trigram layer. The choice of BM25 over vector/embedding search is discussed in Decision Log [DL07](#dl07).

Search responses are byte-budgeted: workloads pass a response-level byte budget on each `/v1/search` call, and an allocator across queries decides which exact-text hits fit. The default allocator (rank-round) preserves multi-query coverage by offering every query's first candidate before any query's second; alternative allocators — score-proportional, knapsack-greedy, equal-budget, and MMR diversification — are configurable per namespace, with per-request override available. The allocator reports per-query omission counts so workloads can observe when budgets were tight. See Decision Log [DL15](#dl15).

### Session State

Captures structured events during a session. Events are workload-defined; CALM's contract is the priority taxonomy that drives snapshot triage when context needs to be reconstructed.

Each event is a JSON object the workload POSTs to `/v1/events` (HLD §6). CALM persists it with `(type, priority, data, created_at)` — `type` is a workload-defined string, `priority` is one of P1–P4, `data` is a JSON payload the workload structures however it likes. CALM does not validate `type` against a fixed set or interpret `data`.

**Priority semantics (CALM-defined):**

- **P1 — critical.** State that must survive snapshot reconstruction at all costs.
- **P2 — important.** State useful for context reconstruction but tolerable to lose under tight byte budgets.
- **P3 — contextual.** Background activity that's nice to have but rarely load-bearing.
- **P4 — noise.** Diagnostic metadata that almost never matters for reconstruction.

Workloads classify their events into these tiers when posting. `priority` is required; CALM rejects missing or out-of-range values but does not validate the *distribution* — degenerate assignments like everything-as-P1 are accepted as-sent, with the predictable consequence of snapshot ordering collapsing to recency. CALM's snapshot endpoint returns events ordered by priority and recency, accumulating until a configurable byte budget is reached.

**Example event taxonomy** (illustrative — the MCP adapter's event taxonomy, not CALM's contract):

| Type | Priority | Required fields | Optional fields |
|---|---|---|---|
| `file_touched` | 1 | `path`, `operation` (read/write/create/delete) | `outcome` (ok/error), `error_message` |
| `task_in_progress` | 1 | `description` | `status` (active/blocked/completed) |
| `project_rule` | 1 | `rule` | `source` (e.g. CLAUDE.md) |
| `error_observed` | 2 | `message`, `source` | `exit_code`, `trace_snippet` |
| `user_decision` | 2 | `decision` | `context`, `rejected_alternatives` |
| `git_operation` | 2 | `command` | `branch`, `outcome`, `exit_code` |
| `env_state` | 2 | `key`, `value` | — |
| `tool_invocation` | 3 | `tool_name` | `count`, `last_input_summary` |
| `delegated_work` | 3 | `description` | `status` (pending/completed/failed) |
| `session_intent` | 4 | `intent` | — |

When developers use the MCP adapter, priority assignment for these events is handled by the adapter itself — events derived from tool calls arrive at CALM with priorities already set per the taxonomy above. Other workloads define their own taxonomies — a slackbot's event types differ from an eval harness's. CALM doesn't enforce a closed set.

Representative payloads:

```json
{ "type": "file_touched", "data": { "path": "src/db/pool.go", "operation": "write", "outcome": "ok" } }
{ "type": "error_observed", "data": { "message": "connection pool exhausted", "source": "clickhouse", "exit_code": 1, "trace_snippet": "dial tcp: connection refused" } }
{ "type": "user_decision", "data": { "decision": "skip retrying clickhouse, move to read replica", "context": "three consecutive timeouts", "rejected_alternatives": ["retry with backoff", "fallback to cache"] } }
```

**Capture path.** Workloads POST events to `/v1/events`. The MCP adapter does this on behalf of the coding agent it serves — inspecting tool calls passing through it and deriving event records from the tool name, input, and result. Internal LLM applications and pipelines POST events directly from their own code.

**Snapshot is a generic event store.** State reconstruction returns events ordered by priority and recency within a byte budget. CALM does not interpret events' content to build a structured state representation — workloads needing structured shapes build them in their own middleware from the returned event stream. Pluggable per-client snapshot strategies are not part of CALM's surface (see Decision Log [DL08](#dl08)).

---

## Design Invariants

Six rules. Non-negotiable.

**1. Never makes things worse.** CALM unavailable? The workload falls back to raw content. CALM too slow? Same. LLM calls always work. The worst case is higher token cost, never a broken request.

**2. Workload-agnostic.** Same API for any LLM application that can speak HTTP. No special integration contracts per workload type (see Decision Log [DL01](#dl01)).

**3. Namespace + session isolation.** Two boundaries, both load-bearing, each enforcing isolation at a different layer. **Namespace** is the security/trust boundary: cross-namespace queries are forbidden, and a mismatch returns 404 (invisibility, not denial). **Session** is the content/scope boundary inside a namespace: indexed content and events are bound to a session and invisible to other sessions in the same namespace. Both apply to every operation. Sessions are cleaned up on explicit close or inactivity TTL, whichever comes first; TTL is configurable per session at creation, bounded by an operator-set ceiling. Cross-namespace mismatch is a confidentiality breach; cross-session-within-a-namespace leakage is a workload-contract violation. Bugs in either are bugs.

**4. Never in the LLM request path.** CALM sits beside the LLM call, not between the workload and the LLM. The workload calls CALM, then calls the LLM. Two separate calls (see Decision Log [DL04](#dl04)).

**5. Content fidelity.** CALM decides *which* content to return. Never alters what's in it. A code block goes in, the same code block comes out.

**6. Idempotent indexing.** Same source label indexed twice within a session? The second replaces the first. No stale duplicates from iterative workflows. This makes a source label both the *identity* of content and the *dedup key*, so constructing labels that are stable yet collision-free — semantically distinct outputs never sharing one label — is a discipline every workload that ingests repeated or iterative tool output must get right.

---

# 5. Architecture Overview

## System Topology

```
┌─────────────────────┐  ┌─────────────────────┐  ┌──────────────────────────┐
│ Internal LLM app    │  │ Automated pipeline  │  │      Coding Agent        │
│  (slackbot, eval    │  │  (factory, batch    │  │   (Claude Code, Cursor)  │
│   harness, ...)     │  │   workflow, ...)    │  │                          │
└──────────┬──────────┘  └──────────┬──────────┘  └────────────┬─────────────┘
           │                        │                          │
           │ HTTP                   │ HTTP              MCP/stdio
           │                        │                          │
           │                        │               ┌──────────▼─────────────┐
           │                        │               │    MCP Adapter         │
           │                        │               │    (dev's machine)     │
           │                        │               │                        │
           │                        │               │ - MCP/stdio ←→ HTTP    │
           │                        │               │ - Local code execution │
           │                        │               │ - Event extraction     │
           │                        │               └──────────┬─────────────┘
           │                        │                          │ HTTP
           │                        │                          │
           ▼                        ▼                          ▼
┌───────────────────────────────────────────────────────────────┐
│                        CALM Service                           │
│                                                               │
│  ┌────────────────────────────────────────────────────────┐   │
│  │                      HTTP API                          │   │
│  │  /v1/sessions  /v1/ingest  /v1/search  /v1/events      │   │
│  │  /v1/snapshot  /v1/sources  /v1/feedback  /v1/manage/* │   │
│  └────────┬──────────┬─────────┬──────────┬───────────────┘   │
│           │          │         │          │                   │
│  ┌────────▼──────────▼───┐  ┌──▼──────────▼───────────────┐   │
│  │  Content Ingestion    │  │  Session State              │   │
│  │                       │  │                             │   │
│  │  - Format detection   │  │  - Event capture            │   │
│  │  - Format-hinted      │  │  - Priority categorization  │   │
│  │    chunking           │  │  - Snapshot builder         │   │
│  │  - Intent filtering   │  │                             │   │
│  │  - Distinctive terms  │  │                             │   │
│  └───────────┬───────────┘  └──────────────┬──────────────┘   │
│              │                             │                  │
│  ┌───────────▼─────────────────────────────▼──────────────┐   │
│  │                  Knowledge Store                       │   │
│  │                                                        │   │
│  │  - BM25 ranked search                                  │   │
│  │  - Tokenization branches on content_type               │   │
│  │  - Trigram fallback                                    │   │
│  │  - Smart snippet extraction                            │   │
│  │  - Per-session vocabulary index                        │   │
│  └───────────────────────┬────────────────────────────────┘   │
│                          │                                    │
└──────────────────────────┼────────────────────────────────────┘
                           │
                  ┌────────▼────────────────────────┐
                  │           Postgres              │
                  │  (with pg_search/pg_textsearch  │
                  │           + pg_trgm)            │
                  └─────────────────────────────────┘
```

The workload boxes are illustrative — three common patterns, not three privileged architectural categories (see Decision Log [DL01](#dl01)). Any LLM application that speaks HTTP and authenticates with a namespace credential is a valid CALM consumer.

## How the pieces connect

**CALM is a single HTTP service.** It exposes a REST API over JSON. Any workload that can make HTTP calls is a candidate consumer — there is no special integration contract per workload type (see Decision Log [DL01](#dl01)). The service is stateless; all state lives in the Postgres backend.

**The MCP Adapter is one workload's integration shim.** It is a separate binary that a coding agent (Claude Code, Cursor, Codex, similar) spawns as a child process on the developer's machine. It speaks MCP over stdio on the agent side and HTTP on the CALM side, translating between the two so the coding agent doesn't need to know CALM exists as a service. It calls `POST /v1/sessions` on startup, persists the returned session token, closes the session on shutdown, and inspects tool calls passing through it to derive structured events (the same `/v1/events` surface any workload would use). Internal LLM applications and pipelines bypass the adapter entirely and call CALM's HTTP API directly.

When the agent wants to run code, the adapter runs it locally as a subprocess — it has access to the project directory, filesystem, git, local CLIs. It captures stdout and sends the output to CALM's `/v1/ingest`. CALM never sees or runs the code; it only receives the text output (see Decision Log [DL02](#dl02)).

The adapter captures tool output through its own explicit tools: the agent runs commands via the adapter's shell-command tool rather than the host's native shell, and the adapter ingests the captured output and posts the derived event. This explicit-tool path is the host-agnostic capture mechanism — it works on any agent that can call an MCP tool, regardless of whether the host exposes an extension surface of its own. Platform hooks (PreToolUse, PostToolUse) that intercept the host's *native* tools — capturing output from commands the agent runs outside the adapter — are an optional, host-specific enhancement: they require a hook surface the host may not provide, so they sit outside the baseline integration and are deferred. Where a host supports them, they are thin shims that translate to the same `/v1/ingest` and `/v1/events` calls the explicit-tool path uses.

**Storage.** Postgres in production, with a BM25-capable extension (`pg_search` or `pg_textsearch`) and `pg_trgm` for the trigram layer.

**Workload integration is not trivial.** Every workload that uses CALM needs middleware that manages sessions, calls ingest with format hints, handles timeouts with fallback to raw content, and posts events. The MCP adapter adds protocol translation and subprocess management on top of this. This complexity is the deliberate cost of keeping CALM out of the LLM request path (see Decision Log [DL04](#dl04)). The alternative — a transparent proxy — would hide integration complexity but create a single point of failure for every LLM call. CALM ships reference middleware as working examples of the integration contract, not as libraries to take a hard dependency on.

## Request flows

### Internal LLM app: ingest tool output with intent-ordered summary

```
Slackbot's tool handler executes a ClickHouse query, gets 50 KB of results
  → POST /v1/ingest (X-CALM-Session-Token) { content, format: "log", intents: ["connection errors"] }
  → CALM chunks by log format, indexes into knowledge store
  → Runs per-intent search against just-indexed content; orders summary by RRF
  → Returns compact summary (~2 KB) with sections matching "connection errors" ranked first
  → Tool handler injects compact summary into the LLM's next message
```

### Automated pipeline: agent step

```
Pipeline workflow enters agent step
  → Middleware POST /v1/sessions (labels.run = workflow_run_id + step_index)
  → CALM returns session_token; middleware persists it for the step's lifetime
  → Middleware injects search_prior_output tool into the agent's tool set

  Agent loop, tool call 1:
    → LLM requests web search tool
    → Tool handler executes web search, gets 30 KB of results
    → Handler: POST /v1/ingest (X-CALM-Session-Token) { content, source: "web_search" }
    → CALM returns compact representation (1 KB)
    → Handler returns compact version (placed in message history instead of raw output)
    → POST /v1/events (X-CALM-Session-Token) { events: [{type: "tool_invocation", priority: 3, data: { tool_name: "web_search" }}] }

  Agent loop, tool call 2:
    → LLM wants more detail from the earlier search
    → LLM calls search_prior_output { query: "specific topic", source: "web_search" }
    → Tool handler: POST /v1/search (X-CALM-Session-Token) { queries: ["specific topic"] }
    → CALM returns matching chunks from the indexed content
    → Handler returns search results to the agent
    → Continue...

  Loop ends
  → Middleware: DELETE /v1/sessions (X-CALM-Session-Token)
```

### Coding agent via MCP adapter

```
Developer starts Claude Code session
  → Claude Code spawns MCP Adapter as child process on dev's machine
  → Adapter POST /v1/sessions; persists returned session_token in process memory

  Developer asks agent to inspect a log file
  → Agent calls the adapter's shell-command tool (calm_run_command) with `cat app.log`
  → Adapter runs the command locally (has access to project dir, filesystem, git)
  → Captures stdout
  → Adapter POST /v1/ingest (X-CALM-Session-Token) { content, format: "log" }
  → CALM returns compact representation
  → Adapter returns it as the MCP tool response

  Agent runs `git log` the same way
  → calm_run_command captures stdout
  → Adapter POST /v1/ingest (X-CALM-Session-Token) { content, content_type: "prose" }
  → Adapter POST /v1/events (X-CALM-Session-Token) { events: [{type: "git_operation", priority: 2, data: { command: "git log" }}] }
  → Adapter returns the compact version as the tool response

  (Optional, host-specific) A command run through the host's NATIVE shell — bypassing
  the adapter — is captured only where the host exposes hooks: a PostToolUse hook fires
  and performs the same ingest + event POST. Hosts without a hook surface route such
  commands through calm_run_command instead; the capture is identical either way.

  Context compacts
  → (Optional, host-specific) Where the host exposes lifecycle hooks, a PreCompact /
    SessionStart hook GETs /v1/snapshot and injects the priority-tiered snapshot into
    the refreshed context, so the agent resumes from where it left off.

  Developer ends session
  → Claude Code kills MCP Adapter process
  → Adapter deletes the session: DELETE /v1/sessions (X-CALM-Session-Token)
  → If adapter crashes without cleanup, inactivity TTL handles it
```

---

# 6. API Surface

The API is HTTP REST with JSON request and response bodies (see Decision Log [DL09](#dl09) for the protocol choice). All requests carry the namespace API key via the `X-CALM-API-Key` header. API keys are mapped to namespaces in service configuration (see Decision Log [DL10](#dl10)); CALM resolves the key to its namespace — a content-agnostic partition, not a hierarchical tenant (see Decision Log [DL11](#dl11)) — and enforces it on every operation. Workloads never pass a namespace directly. Per-workload attribution comes from the `client` identifier. In namespaces configured with `require_client_credentials: true`, workloads additionally present a per-client bearer token via `Authorization: Bearer <token>` — that token authenticates the client identity, replacing the body-field claim with a server-verified credential. The MCP adapter follows the same model — developers configure the adapter binary with the team's namespace API key and the adapter self-identifies via the registered client (token in credentialed mode, name in body otherwise).

## Response headers

Every CALM response carries three observability headers:

- **`X-CALM-Correlation-Id`** — server-minted UUIDv7 unique to this request. Present on every response (2xx, 4xx, 5xx). Workloads retain this for per-call correlation in their own logs and observability surfaces.
- **`X-Workload-Request-Id`** — echoed when the workload provided one on the inbound request. Optional, intended for the workload's own log correlation; CALM never uses it internally. Bounded to 256 characters; longer values are rejected with 400.
- **`traceresponse`** — W3C trace-context. Emitted on responses to requests that carried a valid inbound `traceparent`; the header carries the trace-id from the inbound context. Workloads with OTel-compatible tracing infrastructure join their distributed trace to CALM's span chain. Requests without an inbound `traceparent` receive no `traceresponse` — CALM does not start unsolicited server spans for the purpose of populating this header.

These three are independent — workloads opt into whichever subset their observability stack supports.

## Path groups

Two path groups on the same service:

- `/v1/*` — core API. Called by workloads in the hot path. Every request is scoped to a session. The session must belong to the API key's namespace.
- `/v1/manage/*` — management API. Called by ops tooling. Operates across sessions, but still scoped to the API key's namespace.

---

## Integration contract

Every workload that uses CALM follows the same six-obligation pattern, plus an optional seventh for workloads with outcome signals. The shape is uniform across internal LLM applications, automated pipelines, and the MCP adapter; only the surrounding code differs by workload.

0. **Register the workload's client at install time.** `POST /v1/clients/{name}` once per workload-deployment. Establishes the client as a first-class entity in the namespace. Returns 409 if the name is already taken; workloads that intend restart-safety catch 409 and treat as success. In namespaces configured with `require_client_credentials: true`, registration returns a one-time client token that the workload must persist and present via `Authorization: Bearer <token>` on subsequent operations — providing real per-workload isolation within a shared namespace. Lost tokens require `POST /v1/clients/{name}/rotate-token` (requires the current token) or operator intervention via the management API.

1. **Create the session at the start of work.** `POST /v1/sessions` with optional `client` identifier (when the namespace doesn't require client credentials, the client name in the body must reference a previously-registered client; when it does require them, the body's `client` is ignored — the session is bound to the client whose token was presented), optional `ttl_minutes` (bounded by an operator ceiling). CALM mints the session credential server-side and returns it once as `session_token` in the response. The workload persists this token and presents it on every subsequent session-touching call via the `X-CALM-Session-Token` header. Lost tokens are unrecoverable — operationally equivalent to losing the session. Workloads that want a human-readable handle attach it as a label (e.g., `labels.run: "pipeline-step-2"`). The session lives for the duration of the workload's unit of work — one conversation, one pipeline step, one batch job.

2. **Execute tools normally.** Raw output is the workload's responsibility. CALM never runs tools — it receives the output after the workload has produced it.

3. **Ingest tool output before it enters the LLM context.** `POST /v1/ingest` (carrying `X-CALM-Session-Token: <token>`) with the raw content, a `source` label (for idempotent re-indexing), and optional `format` / `content_type` hints. CALM returns a compact representation; the workload uses the compact form in place of the raw output in the LLM message stream.
   - **On CALM success:** use the compact representation.
   - **On CALM timeout or error:** fall back to the raw output. Never fail the LLM call because CALM is unavailable (see Decision Log [DL04](#dl04), invariant #1).

4. **Capture structured events as work progresses.** `POST /v1/events` (with `X-CALM-Session-Token`) for state-relevant events (files edited, errors observed, user decisions). Each event has a workload-chosen `type`, a `priority` in P1–P4, and a `data` JSON payload. CALM does not interpret event content; it categorizes by priority for snapshot triage.

5. **Tear down explicitly when work completes.** `DELETE /v1/sessions` (with `X-CALM-Session-Token`) at end of work. If the workload crashes or disconnects, inactivity TTL handles the cleanup.

6. **(Optional) Post outcome feedback when you can.** `POST /v1/feedback` (with `X-CALM-Session-Token`) referencing the `X-CALM-Correlation-Id` from a value-producing call (`/v1/ingest`, `/v1/search`, `/v1/snapshot`), plus an outcome enum (`success | retry | degraded`). Workloads with outcome signals (eval harnesses with ground truth, pipelines with step verifiers, slackbots with user-reaction signals) post within the feedback TTL window while the session is still alive. Workloads without outcome signals (notably the MCP adapter) skip this obligation. Workloads whose outcome signal arrives only after session teardown (delayed human review, batch verifiers) either keep the session alive until feedback fires or accept no outcome attribution for those calls — the session-token requirement is the within-namespace forgery boundary.

The MCP adapter implements this contract on behalf of the coding agent it serves — calling createSession, persisting the returned session_token, calling ingest as tool calls pass through, posting events derived from the tool calls, deleting the session on shutdown. Internal LLM applications and automated pipelines implement the same six obligations directly from their own middleware; workloads with outcome signals additionally post feedback (see obligation 6). Any HTTP client in any language that satisfies these obligations is CALM-compatible. CALM ships reference middleware as working examples of the contract, not as libraries to take a hard dependency on.

---

## Core API

### `POST /v1/sessions`

Creates a session. CALM mints the session credential server-side. The namespace is resolved from the API key. Accepts an optional `client` identifier (must reference a previously-registered client; defaults to `default`), optional metadata labels, and optional `ttl_minutes`. Optionally carries an `Idempotency-Key` header so retries return the same `session_token`.

```json
{
  "client": "factory-pipeline",
  "labels": {
    "env": "production",
    "workflow": "report-generator",
    "run": "pipeline-step-2"
  },
  "ttl_minutes": 30
}
```

- `client` — optional. Identifies which workload is creating the session. Must reference a previously-registered client. If omitted, the session attributes to the `default` client.
- `labels` — arbitrary key/value metadata; queryable via the management API. Workloads that want a human-readable handle for a session attach it here (e.g., `run: pipeline-step-2`).
- `ttl_minutes` — inactivity timeout. CALM clamps to the operator-configured maximum if the workload requests longer (rather than rejecting with 4xx). The clamp choice is deliberate: CALM's consumers are LLM-orchestration glue layers — coding-agent harnesses, pipeline runners, MCP adapters — that typically fall back to no-CALM mode when they see a 4xx on session create. For a context-management service, *absent CALM* is a worse outcome than *degraded CALM* (shorter session than requested). Clamping keeps CALM useful for the work unit; the response echoes the committed value so workloads that do check can detect the clamp. Server emits a WARN per clamp event so operators can see when workloads consistently hit the ceiling and decide whether to raise it.

**Response (201):**

```json
{
  "session_token": "rTxN3kP4...",
  "namespace": "factory-prod",
  "client": "factory-pipeline",
  "ttl_minutes": 30,
  "created_at": "2026-05-15T14:30:00Z"
}
```

`session_token` is the secret credential the workload must persist and present via `X-CALM-Session-Token` on every subsequent session-touching call. Returned once; lost tokens are unrecoverable. The remaining fields echo what CALM committed — `namespace` resolved from the API key, `client` resolved from the request, and `ttl_minutes` after operator-ceiling clamping. The workload uses the echoed values to confirm what CALM actually committed.

### `DELETE /v1/sessions`

Tears down a session. The target session is identified by `X-CALM-Session-Token`. All indexed content and events for this session are removed. If the workload doesn't call this, inactivity TTL handles cleanup.

**Response (204):** No body.

### `POST /v1/ingest`

The primary endpoint. Takes raw content, chunks it, indexes it, returns a compact representation. The workload puts the compact version into the LLM's context instead of the raw content.

Requires the `X-CALM-Session-Token` header.

```json
{
  "content": "<raw tool output>",
  "source": "web-search-results",
  "format": "log",
  "content_type": "prose",
  "intents": ["connection timeout errors", "retry configuration"]
}
```

- `source` — a label for this content. Used for scoped searches and for idempotent re-indexing (same source label replaces previous content, per invariant #6).
- `format` — optional hint. If absent, CALM auto-detects (JSON, markdown, plain text). If present, uses format-aware chunking (log, stacktrace, csv, metrics).
- `content_type` — optional workload-provided default for tokenization branching (see Decision Log [DL06](#dl06)). `code` for source-file or build-output content; `prose` for natural-language content (default if omitted). The chunker may override per chunk when the format strongly signals otherwise — e.g., fenced code blocks inside a markdown ingest are classified as `code` regardless of the top-level hint, and stacktrace frames are always `code`. Values other than `code`/`prose` are accepted and persisted but treated as `prose` for tokenization until their strategies are implemented.
- `intents` — optional array, up to 3. When provided and content exceeds a configurable size threshold, CALM runs a search per intent against the just-indexed content and orders `summary` by Reciprocal Rank Fusion across the per-intent rankings. Each section gains a `matches` array listing which intents it addresses. Below the threshold, intents are accepted but ignored — `summary` returns in document order. See Decision Log [DL05](#dl05) for the rejected alternatives (binary match/no-match, per-intent numeric scores).

**Response (no intents):**

```json
{
  "sections_indexed": 12,
  "sections_total": 12,
  "summary_truncated": false,
  "source": "web-search-results",
  "summary": [
    { "title": "Connection Pool Exhaustion", "preview": "..." },
    { "title": "Timeout Configuration", "preview": "..." },
    { "title": "Cache Configuration", "preview": "..." }
  ],
  "distinctive_terms": ["timeout", "connection", "retry", "postgres"]
}
```

**Response (with intents):**

```json
{
  "sections_indexed": 12,
  "sections_total": 12,
  "summary_truncated": false,
  "source": "web-search-results",
  "summary": [
    {
      "title": "Timeout Configuration",
      "preview": "...",
      "matches": ["connection timeout errors", "retry configuration"]
    },
    {
      "title": "Connection Pool Exhaustion",
      "preview": "...",
      "matches": ["connection timeout errors"]
    },
    {
      "title": "Retry Backoff Errors",
      "preview": "...",
      "matches": ["retry configuration"]
    },
    {
      "title": "Cache Configuration",
      "preview": "...",
      "matches": []
    }
  ],
  "distinctive_terms": ["timeout", "connection", "retry", "postgres"]
}
```

**Response guarantees:**

- `summary` contains one entry per indexed section (title + preview line), capped at 50 entries. If the content produced more sections, `summary_truncated` is `true` and `sections_total` reflects the actual count. A workload that never calls `/v1/search` can still answer "what was in this content" from the ingest response alone — this is a design constraint, not best-effort.
- When `intents` are provided, `summary` is ordered by RRF-aggregated relevance across the intents, and each section carries a `matches` array (possibly empty) listing the intents it addresses.
- `distinctive_terms` contains the top terms by IDF, capped at 40. The vocabulary the LLM should use to issue follow-up search queries.
- `source` is always echoed. On re-ingestion of the same source label, the response reflects the new content, not the previous.

### `POST /v1/search`

Queries indexed content within a session. Supports multiple queries in a single call.

Requires the `X-CALM-Session-Token` header.

```json
{
  "queries": ["connection timeout", "retry configuration"],
  "source": "web-search-results",
  "limit": 3,
  "budget_bytes": 4096
}
```

- `source` — optional. Scopes the search to a specific source label.
- `limit` — maximum results per query.
- `budget_bytes` — optional. Response-level byte budget across all queries. Defaults to 4 KB; bounded by an operator-configurable ceiling (default 64 KB). Over-ceiling requests are clamped, not rejected (parallel to session-TTL clamping; the response echoes the committed value).

Returns exact indexed text with smart snippets around matching terms. The two-layer fallback (primary tokenizer → trigram) is internal — the workload just sends a query and gets ranked results.

Search is not scoped by `content_type`. A session that has indexed a mix of prose and code chunks (e.g., a coding-agent run that ingested both API docs and source files) gets results from both tokenization paths in one ranked list — the layer-1 query runs against both the prose and code FTS indexes and the two rankings are fused (see [§7](#7-data-model--storage) for the fusion mechanics). The workload sees a single result list and does not need to know which tokenizer matched.

`results` is an ordered array with one entry per query, in the order the queries were submitted; each entry pairs the `query` with its ranked `hits`. (An array rather than a query-keyed object preserves request order, tolerates duplicate queries, and leaves room to attach per-query metadata without a breaking change.)

```json
{
  "budget_bytes": 4096,
  "byte_budget_used": 1893,
  "results": [
    {
      "query": "connection timeout",
      "budget_omitted": 0,
      "hits": [
        {
          "title": "Connection Pool Exhaustion",
          "snippet": "<excerpt around matching terms>",
          "source": "web-search-results",
          "match_layer": "primary"
        }
      ]
    },
    {
      "query": "retry configuration",
      "budget_omitted": 2,
      "hits": [
        {
          "title": "Retry Backoff Errors",
          "snippet": "<excerpt around matching terms>",
          "source": "web-search-results",
          "match_layer": "trigram"
        }
      ]
    }
  ]
}
```

Result assembly is byte-budgeted. The allocator runs in rank rounds: every query's first-ranked candidate is offered before any query's second, every second before any third. A candidate is included only when its compact-JSON-serialized size (UTF-8 bytes of its standalone `SearchHit` representation) fits the remaining budget. Snippets are never further truncated — their sizing was already settled at index time by smart-snippet extraction.

Per-query `budget_omitted` reports the count of otherwise-returnable candidates (from that query's top-`limit` set) that were not included because the budget didn't accommodate them. Response-level `byte_budget_used` reports the actual bytes consumed. `budget_bytes` echoes the committed budget (after operator-ceiling clamping); workloads detect clamping by comparing requested vs echoed.

Overshoot rule: if no candidate fits the budget, the first-considered candidate (query[0]'s first-ranked hit) is included anyway, with `byte_budget_used` reflecting the actual size. This preserves the never-worse property — workloads with too-tight budgets still receive their highest-confidence content rather than an empty result. Parallel to the snapshot endpoint's P1 overshoot rule (§8). See Decision Log [DL15](#dl15).

The default allocator is rank-round. Operators select alternatives per namespace via configuration; workloads may override per request by setting `X-CALM-Allocator-Variant` (when the namespace allows override). Supported variants: `rank-round` (default — multi-query coverage), `score-proportional` (allocates proportionally to per-query rank scores), `knapsack-greedy` (DP knapsack maximizing sum-of-relevance), `equal-budget` (`budget / N` per query), `mmr` (Maximal Marginal Relevance — diversifies against near-duplicate hits across queries). All variants honor the same budget contract: no overshoot beyond the first-considered candidate, no snippet truncation, per-query `budget_omitted` accounting unchanged.

`match_layer` is one of `primary`, `trigram` — indicating which fallback layer produced the match. `primary` covers both the prose and code FTS indexes (which are RRF-fused at layer 1, per [§7](#7-data-model--storage)) — the workload does not see, and does not need to disambiguate, which tokenizer the underlying chunk was indexed with. The field is diagnostic context: operators read it alongside `search.hit_rate` and `search.zero_results` when investigating quality issues, not as a standalone signal.

The response surface deliberately excludes raw BM25/RRF scores, tokenizer identity (which FTS index produced each match — `match_layer=primary` covers both), per-request ranking weights, and generated explanations of result ordering. Exposing these would tempt workloads to re-rank or filter results based on signals CALM doesn't standardize across deployments or time. The diagnostic surface (`match_layer`, `byte_budget_used`, `budget_bytes`, per-query `budget_omitted`) is the full set of observability fields the response carries.

### `GET /v1/sources`

Lists everything indexed in this session. Requires the `X-CALM-Session-Token` header.

```json
{
  "sources": [
    { "label": "web-search-results", "chunks": 12, "indexed_at": "..." },
    { "label": "api-response", "chunks": 5, "indexed_at": "..." }
  ]
}
```

Useful for end-of-work observability — what was indexed and how much.

### `POST /v1/events`

Records session events. Workloads send `type`, `priority` (integer 1–4, **required**), and `data`. CALM persists each event with `(type, priority, data, created_at)`. CALM validates the priority range (400 on missing or out-of-range) but does not validate `type` against a fixed set, does not interpret `data`, and does not validate the priority distribution.

Requires the `X-CALM-Session-Token` header.

```json
{
  "events": [
    { "type": "tool_invocation", "priority": 3, "data": { "tool_name": "clickhouse_query", "last_input_summary": "SELECT * FROM traces WHERE ..." } },
    { "type": "error_observed", "priority": 2, "data": { "message": "connection pool exhausted", "source": "clickhouse", "exit_code": 1 } }
  ]
}
```

Priority semantics are defined in §4 — workloads classify events into the four-tier scheme so the snapshot endpoint can triage by importance.

### `GET /v1/events`

Reads events for a session. Supports filtering by `types` and `min_priority` via query parameters. Requires the `X-CALM-Session-Token` header.

```
GET /v1/events?types=error_observed,user_decision&min_priority=1
```

The workload can read events before session close to extract anything worth persisting to its own long-term storage.

### `GET /v1/snapshot`

Returns the session's events ordered by priority and recency, accumulating until a byte budget is reached. Generic event store — CALM does not interpret event content or build a structured state representation (see Decision Log [DL08](#dl08)). Workloads needing structured shapes build them in their own middleware from the returned event stream. Requires the `X-CALM-Session-Token` header.

```
GET /v1/snapshot?budget_bytes=2048
```

```json
{
  "byte_budget_used": 1893,
  "omitted_by_priority": {
    "1": 0,
    "2": 5,
    "3": 18,
    "4": 12
  },
  "events": [
    { "type": "user_decision", "priority": 1, "data": {}, "created_at": "..." },
    { "type": "error_observed", "priority": 2, "data": {}, "created_at": "..." }
  ]
}
```

The response carries explicit coverage diagnostics: `omitted_by_priority` breaks the omission count down by priority tier (1-4). Workloads detect budget pressure via `sum(omitted_by_priority) > 0`; the returned event count is `events.length`; the session's total event count is `events.length + sum(omitted_by_priority)`. The P1 overshoot rule remains intact — if no event fits and the most-recent P1 alone exceeds the budget, that one event is returned anyway and `byte_budget_used > budget_bytes` signals the overshoot.

Consumed by any workload that needs a compressed view of session state. On the coding-agent side, where the host exposes lifecycle hooks (PreCompact, SessionStart), those hooks GET this endpoint and inject the result on compaction or resume — an optional, host-specific enhancement rather than a baseline requirement.

### `POST /v1/feedback`

Records per-call workload outcome that joins CALM's internal signals at metric emission time. Workloads with outcome signals (eval harnesses with ground truth, pipelines with step verifiers, slackbots with user-reaction signals) post feedback referencing the `X-CALM-Correlation-Id` from a value-producing call (`/v1/ingest`, `/v1/search`, `/v1/snapshot`). Workloads without outcome signals don't call this endpoint.

Requires the `X-CALM-Session-Token` header.

```json
{
  "correlation_id": "019e9638-bf05-7402-ad13-540049ea9480",
  "outcome": "success"
}
```

- `correlation_id` — the UUIDv7 value from the `X-CALM-Correlation-Id` response header of the originating call. Required.
- `outcome` — workload-state declaration. One of `success`, `retry`, `degraded`. Required. Unordered — CALM does not interpret which outcome is "better"; the workload owns the semantics.

**Response paths:**

| Status | Code | Cause |
|---|---|---|
| 204 | — | Feedback recorded |
| 400 | `invalid_outcome` | `outcome` not in enum |
| 400 | `invalid_correlation_id` | `correlation_id` not a valid UUIDv7 |
| 404 | `correlation_not_found` | correlation row absent within the resolved session — forgery attempt or stale workload state |
| 409 | `feedback_already_submitted` | second feedback for the same correlation_id |
| 410 | `feedback_window_expired` | correlation_id's UUIDv7 timestamp is older than the feedback TTL window |

Feedback must arrive within the per-namespace feedback TTL window (default 60 minutes from the originating call; configurable per namespace, bounded `[1, 1440]`). The handler enforces this via the embedded UUIDv7 timestamp in `correlation_id` — no DB lookup is required to determine if a feedback POST is in-window. The `correlation_id` is one-shot: a successful feedback POST cannot be revised by a subsequent POST. Correlations that never receive feedback contribute to the implicit coverage gap, which operators compute as `(total value-producing calls) − (feedback received)` from existing counters.

---

## Management API

### `GET /v1/manage/sessions`

Lists sessions in the API key's namespace. Supports filtering by client and arbitrary labels.

```
GET /v1/manage/sessions?client=factory-pipeline&labels.env=production
```

Returns metadata for currently-active sessions in the namespace — labels, the client they belong to, creation time, last activity time, event counts. Session credentials are not surfaced; operators correlate by `client` and labels. CALM does not retain terminal-state records (see Decision Log [DL14](#dl14)); closed or TTL-expired sessions are not queryable.

### `DELETE /v1/manage/sessions`

Bulk deletes sessions in the namespace by client or label query. Supports `dry_run=true` to preview without deleting.

```
DELETE /v1/manage/sessions?client=slackbot&labels.env=staging&dry_run=true
→ { "affected_sessions": 47 }

DELETE /v1/manage/sessions?client=slackbot&labels.env=staging
→ { "deleted_sessions": 47 }
```

For namespace lifecycle management — "staging environment reset, wipe all sessions belonging to this client."

### `GET /v1/manage/clients`

Lists clients in the API key's namespace.

```json
{
  "clients": [
    { "name": "default", "session_count": 12, "last_activity": "..." },
    { "name": "slackbot", "session_count": 3, "last_activity": "..." },
    { "name": "factory-pipeline", "session_count": 47, "last_activity": "..." }
  ]
}
```

Read-only — operators can see which clients exist and their activity. Per-client policy configuration is not part of the management API surface. Useful for spotting typo-clients and dead workloads.

### `DELETE /v1/manage/clients/{client}`

Removes a client and all its sessions — all the client's sessions, sources, chunks, events, and labels are removed.

```
DELETE /v1/manage/clients/slackbot-old?dry_run=true
→ { "affected_sessions": 12 }

DELETE /v1/manage/clients/slackbot-old
→ {
    "deleted_client": "slackbot-old",
    "deleted_sessions": 12
  }
```

Cannot delete the `default` client — that's the bootstrap fallback for sessions without a `client` field.

---

# 7. Data Model & Storage

## Storage Backend

Postgres in production, with a BM25-capable extension (`pg_search` or `pg_textsearch`) and `pg_trgm` for trigram matching (see Decision Log [DL12](#dl12) for why Postgres specifically and which alternatives were rejected). Both extensions provide proper BM25 scoring with IDF — native `tsvector` + `ts_rank` does not implement IDF and produces unreliable ranking on any non-trivial corpus, so one of the BM25 extensions is a hard prerequisite.

- **ParadeDB `pg_search`** — full BM25 with IDF via a Tantivy-based index. Reported 20× faster ranking than native `ts_rank` on large tables. Actively maintained, well-documented. Recommended for production.
- **Timescale `pg_textsearch`** — pure-Postgres BM25 scoring built on standard `tsvector` infrastructure. Lighter footprint; viable alternative if ParadeDB is not an option.

Trigram matching uses `pg_trgm` (standard Postgres contrib extension) with either choice.

## Logical Data Model

Everything is scoped to a session. There are no cross-session relationships — this is a hard boundary, not a convention (see Decision Log [DL13](#dl13)).

### Clients

The container for workloads within a namespace. A client is registered explicitly via `POST /v1/clients/{name}` before a session can reference it; a session naming an unregistered client is rejected. The `default` client is pre-created at bootstrap so workloads can omit the `client` field and attribute to it.

```
clients
  namespace        TEXT                -- partitions clients by namespace
  name             TEXT                -- workload-provided identifier
  created_at       TIMESTAMP
  last_activity_at TIMESTAMP           -- updated on session activity within the client
  PRIMARY KEY (namespace, name)
```

The namespace registry itself lives in service configuration, not in the database (see Decision Log [DL10](#dl10)).

### Sessions

The root entity for everything below. Created by the workload with optional `client` identifier, optional metadata labels, optional `ttl_minutes`. CALM mints the session credential server-side; only its sha256 hash is stored. The surrogate `id` (a BIGSERIAL) is the FK target for all child tables.

```
sessions
  id                  BIGSERIAL PRIMARY KEY  -- surrogate; namespace-stamped at creation
  namespace           TEXT NOT NULL          -- resolved from API key, server-enforced
  session_token_hash  BYTEA NOT NULL         -- sha256(namespace || 0x00 || raw_token)
  client              TEXT NOT NULL          -- defaults to 'default' if workload omits it
  created_at          TIMESTAMP
  last_activity       TIMESTAMP              -- updated on every request, drives TTL
  ttl_minutes         INTEGER                -- workload-set, clamped to operator ceiling
  UNIQUE (namespace, session_token_hash)     -- auth-side lookup index
  FOREIGN KEY (namespace, client) → clients(namespace, name)
  INDEX (namespace, client)
  INDEX (last_activity)                      -- supports TTL scanner
```

Every operation that touches a session validates that the session belongs to the requesting namespace. If it doesn't, the response is 404 — not 403; from the caller's perspective, the session doesn't exist. All downstream entities (sources, chunks, events, vocabulary, labels) inherit isolation through the FK chain back to `sessions.id`.

### Session Labels

Key-value metadata attached at session creation. Indexed for management API queries (list by label, delete by label). Semantics are the workload's concern — CALM doesn't interpret them.

```
session_labels
  session_id     BIGINT                -- FK to sessions(id) ON DELETE CASCADE
  key            TEXT
  value          TEXT
  PRIMARY KEY (session_id, key)
  INDEX (key, value)                   -- supports management API queries
```

### Sources

Tracks what's been ingested into a session. Each ingest call creates or replaces a source (idempotent by label within a session, per invariant #6).

```
sources
  id             BIGSERIAL PRIMARY KEY
  session_id     BIGINT                -- FK to sessions(id) ON DELETE CASCADE
  label          TEXT                  -- workload-provided, e.g. "web-search-results"
  indexed_at     TIMESTAMP
  UNIQUE (session_id, label)           -- enforces idempotent re-indexing
```

### Chunks

The indexed content. Each chunk belongs to a source. Carries a `content_type` field that drives tokenization branching at index time (see Decision Log [DL06](#dl06)).

```
chunks
  id             INTEGER PRIMARY KEY
  source_id      INTEGER               -- FK to sources
  title          TEXT                  -- heading hierarchy or auto-generated
  content        TEXT                  -- the actual chunk text
  content_type   TEXT                  -- derived per-chunk by the chunker; defaults to the workload's ingest-level hint when format is ambiguous
```

Two FTS indexes are maintained alongside `chunks`, with each chunk routed to one or the other at insert time based on `content_type`:

- `chunks_fts_prose` — FTS index with porter stemming over standard unicode tokenization. Prose-shaped chunks index here. Catches morphological matches like "caching" → "cached".
- `chunks_fts_code` — FTS index with identifier-preserving tokenization, no stemming. Code-shaped chunks index here. Preserves identifiers like `getUserById` as whole tokens.

**Layer-1 fusion.** A search query at layer 1 (the primary-tokenizer layer) runs against both indexes. Each index produces a BM25-ranked result list (top `2 × limit` per index, to give the fusion enough material). The two lists are fused via Reciprocal Rank Fusion with `k = 60` (the conventional default), and the top-`limit` results returned. The two indexes are fused at every layer-1 query; there is no caller-side scoping to only-prose or only-code.

**Layer 2.** The `pg_trgm` trigram index over `chunks.content` is a single index serving layer 2 (substring fallback) — no fusion at that layer.

This is a distinct RRF use from the intent-ordering RRF on ingest (DL05): there, RRF fuses *per-intent rankings* to order the compact summary; here, RRF fuses *per-tokenizer rankings* within a single search query. Same algorithm, different inputs, different purposes.

BM25 field weights: title at 2.0, content at 1.0. Heading matches surface first.

### Vocabulary

Distinct terms extracted from ingested content per session. Powers the distinctive-terms output in the ingest response.

```
vocabulary
  session_id     BIGINT                -- FK to sessions(id) ON DELETE CASCADE
  word           TEXT
  doc_freq       INTEGER               -- chunks in this session containing this word
  PRIMARY KEY (session_id, word)
```

IDF is computed on demand at query time as `log(N / doc_freq)`, where N is the chunk count in the session. The top-N terms by IDF surface in the ingest response as `distinctive_terms` — what the LLM should consider searching against the indexed content.

Vocabulary is per-session for isolation. On idempotent re-ingest of a source, the prior source's contribution to `doc_freq` is decremented before the new chunks' contributions are added. Rows reaching `doc_freq` zero are deleted.

### Session Events

Structured events captured during a session. Used for state reconstruction via the snapshot endpoint.

```
session_events
  id             BIGSERIAL PRIMARY KEY
  session_id     BIGINT                -- FK to sessions(id) ON DELETE CASCADE
  type           TEXT                  -- workload-chosen
  priority       INTEGER               -- 1 (critical) to 4 (noise); workload-set
  data           JSONB                 -- workload-structured payload
  data_hash      BYTEA                 -- SHA-256 of (type, data), for dedup
  created_at     TIMESTAMP
  INDEX (session_id, priority, created_at)   -- supports snapshot ordering
```

`namespace` and `client` are not denormalized onto events; they inherit through the FK chain to `sessions`. Cross-session queries that need either dimension JOIN through `sessions`.

Deduplication: before inserting, the last N events in the session (configurable, default 5) are checked for matching `data_hash`. Prevents duplicate events from repeated tool calls against the same resource.

FIFO eviction: max events per session is capped (default 1000). When exceeded, the lowest-priority oldest events are deleted first. P1 events are never evicted while lower-priority events exist.

### Correlations

Per-call observability rows. Created on every value-producing call (`/v1/ingest`, `/v1/search`, `/v1/snapshot`); identified by the call's `X-CALM-Correlation-Id` (UUIDv7, stored raw as 16 bytes). The row holds enough context for outcome-attributed metric emission when feedback arrives.

```
correlations
  correlation_id        BYTEA PRIMARY KEY            -- UUIDv7 (16 bytes raw)
  session_id            BIGINT                       -- FK to sessions(id) ON DELETE CASCADE
  request_type          TEXT NOT NULL                -- 'ingest' | 'search' | 'snapshot'
  request_meta          JSONB NOT NULL               -- signal-pertinent dimensions at call time
  outcome               TEXT NOT NULL DEFAULT 'unset' -- UPDATEd by feedback handler
  feedback_received_at  TIMESTAMPTZ                  -- nullable; null until feedback arrives
  created_at            TIMESTAMPTZ NOT NULL
  INDEX (session_id)
```

`request_meta` carries the signal dimensions the metric emitter needs to label the outcome-attributed metric series — for an ingest call: `source`, intent zero-match flag, summary truncated flag; for a search call: `match_layer` distribution, `allocator` variant, `budget_omitted` total; for a snapshot call: `omitted_by_priority` summary. The shape is workload-emitter-specific; CALM does not interpret it beyond its role in metric labeling at feedback receipt.

Feedback received within the TTL window UPDATEs `outcome` and `feedback_received_at` on the existing row, and emits the outcome-attributed metric. The `correlation_id` PK enforces single-shot — a second feedback for the same correlation hits a 409 at the handler. The handler enforces the feedback-acceptance window via the embedded UUIDv7 timestamp + per-namespace `feedback_ttl_minutes`; no stored `expires_at` is required. Rows are removed when the session is torn down (explicit close or inactivity TTL). Correlations that never receive feedback contribute to the `outcome=unset` aggregate, which operators compute as `(total value-producing calls) − (feedback received)` via PromQL — CALM does not emit an explicit `outcome=unset` metric series.

## Cleanup

Two paths, whichever fires first:

- **Explicit close.** Workload calls `DELETE /v1/sessions` with `X-CALM-Session-Token`. Everything under that session — sources, chunks, vocabulary, events, labels, correlations — is removed. The `clients.last_activity_at` for the session's client is updated.
- **Inactivity TTL.** A background scanner finds sessions where `now() - last_activity > ttl_minutes` and removes them. Catches workloads that crash or disconnect without calling DELETE.

The TTL scan interval is configurable; default 60 seconds.

`clients` rows are not deleted automatically when their last session ends — operators clear them via `DELETE /v1/manage/clients/{client}` when they want to. The `default` client is permanent.

## Future optimization path

The research into semantic search without embeddings identified a high-impact technique: LLM-generated document enrichment at index time (synthetic queries, summaries, key concepts stored as separate boosted FTS fields). This closes 50–70% of the gap to dense neural retrieval with zero query-time overhead.

This is deferred as premature optimization. CALM's typical content — logs, metrics, stack traces, CLI output, structured tool responses — has predictable vocabulary that BM25 with the two-layer fallback handles well. The ephemeral session model also limits the amortization window for enrichment. The schema can accommodate enrichment fields without structural changes if search quality data later justifies it.

---

# 8. Session Lifecycle

Sessions are explicit. Created by the workload, used during the workload's unit of work, torn down when done. CALM does not auto-create sessions — a request bearing an unknown `X-CALM-Session-Token` returns 404 (see Decision Log [DL03](#dl03)). The token is server-minted; presenting one CALM didn't issue (or one whose session has been deleted or TTL-expired) is structurally distinguishable from a fresh create request.

A CALM session has one durable service state: **active**. Explicit close or TTL expiry deletes the session and all its child rows; no terminal state — completed, failed, abandoned, expired — is retained. Aggregate observability (counts, distributions, rates labeled by namespace and client) is emitted via OpenTelemetry to the operator's metric backend. Operators who need long-tail row-level retention configure an exporter sink to operator-resident storage (Postgres, OTLP, file, multi-sink); CALM does not mandate a sink, does not expose terminal-state queries, and does not retain a durable session ledger. See Decision Log [DL14](#dl14).

## Creation

The workload calls `POST /v1/sessions` with the namespace API key, optional `client` identifier, optional labels, and optional `ttl_minutes`. CALM resolves the namespace from the API key, mints a fresh session credential server-side, persists only its hash, and returns 201 with the raw `session_token` (shown once). The workload persists the token and presents it as `X-CALM-Session-Token` on every subsequent call. The `client` must reference a previously-registered client (`POST /v1/clients/{name}`) or be omitted to attribute to `default` — see Decision Log [DL01](#dl01).

Workloads that want a human-readable handle for a session attach it as a label (e.g., `labels.run: "wf-abc-step-2"`) rather than encoding it into the credential.

Example patterns:

- **Automated pipeline:** middleware calls createSession when an agent step begins and tags the session with `labels.run` derived from the workflow run ID and step index. API key is in the pipeline's service config; the returned token lives for the step's duration.
- **Internal LLM application:** the application calls createSession when a user starts a new conversation and tags the session with `labels.conversation` set to the conversation ID. API key is in the application's server config.
- **MCP adapter:** the adapter calls createSession on startup when the coding agent spawns it and holds the returned token in process memory. API key is in the adapter's config file.

In each case, the namespace API key is configured in the workload's deploy artifact (config file, env var, service account secret). The session token is ephemeral and per-session; the workload never picks it.

## Active State

Every request (ingest, search, events) updates `last_activity` on the session. This drives the TTL clock — the inactivity timer resets on every interaction.

During the active phase, content is ingested and indexed, search queries run against the session's indexed content, and events are captured. All operations carry `X-CALM-Session-Token`; CALM hashes it and looks the session up by `(namespace, session_token_hash)`.

## State Reconstruction

When a workload needs to recover state — after platform compaction in a coding-agent session, after a crash-and-replay in an automated pipeline, on resume from a paused conversation — it calls `GET /v1/snapshot` (with `X-CALM-Session-Token`).

CALM reads the session's events, sorts them by `(priority asc, created_at desc)` — recall that priority `1` is critical and `4` is noise (HLD §4), so ascending priority means most-important-first — and accumulates into the response until a configurable byte budget is reached. The endpoint returns a generic event list — no merging, no deduplication, no per-event-type synthesis. Workloads that want structured state representations (e.g., "active files" extracted from `file_touched` events, "open decisions" extracted from `user_decision` events) build them in their own middleware from the returned events. See Decision Log [DL08](#dl08).

The snapshot is built on demand from current events, not pre-computed and stored. It's cheap — a database read plus serialization, single-digit milliseconds for typical session sizes.

**Budget overflow.** If higher-priority events alone exceed the budget, CALM still returns the most recent P1 events that fit; `omitted_by_priority` shows which tiers got cut. The workload gets a partial snapshot rather than nothing; it can request a larger budget on retry if its context allows. If even the single most-critical event (the most-recent P1) exceeds the entire budget on its own, CALM returns that one event anyway — overshooting the budget rather than returning an empty snapshot — consistent with P1 being state that must survive reconstruction at all costs; the overshoot is visible via `byte_budget_used > budget_bytes`. Lower-priority tiers get no such exception: if no event fits and none is P1, the events array is empty and `omitted_by_priority` reflects what got cut.

**Who calls this and when:**

- **Coding agents:** Where the host exposes lifecycle hooks (PreCompact, SessionStart), they fire on compaction and session resume, call the snapshot endpoint, and inject the result into the refreshed context. This is an optional, host-specific enhancement — hosts without a hook surface get no automatic snapshot injection; the adapter's explicit tools remain the baseline integration.
- **Automated pipelines:** Typically not needed; the workflow engine's durable execution handles replay. The snapshot is available if the workload wants it for logging or debugging crash recovery.
- **Internal LLM applications:** Available for session-resume scenarios. If the app supports reconnecting to an earlier conversation, the snapshot provides the state summary as a starting point for the workload's own reconstruction logic.

## Teardown

Two paths, whichever fires first.

**Explicit close.** Workload calls `DELETE /v1/sessions` with `X-CALM-Session-Token`. Everything under that session — sources, chunks, vocabulary, events, labels, correlations — is removed. The workload is done and says so.

**Inactivity TTL.** A background scanner finds sessions where `now() - last_activity > ttl_minutes` and removes them. Catches workloads that crashed, disconnected, or didn't clean up.

TTL is configurable per session at creation time (clamped to an operator-set ceiling). Default depends on the workload pattern — a pipeline step might set 30 minutes; an interactive session might set 4 hours; an MCP adapter session might leave the deployment default.

**Who does what:**

- **Automated pipeline:** middleware calls `DELETE` when the agent step completes. If middleware crashes, no delete call is made — TTL handles cleanup.
- **Internal LLM application:** application calls `DELETE` when the user ends the conversation. If the user closes the browser tab without cleanup, TTL handles it.
- **MCP adapter:** adapter's shutdown hook calls `DELETE` when the coding agent kills the process. If the adapter crashes without cleanup, TTL handles it.

## Lifecycle Summary

```
                    ┌─────────────┐
     POST /v1/sessions            │
     ───────────────►  Created    │
                    └──────┬──────┘
                           │
              ingest / search / events
              (each resets last_activity)
                           │
                    ┌──────▼──────┐
                    │   Active    │◄──── snapshot available at any point
                    └──────┬──────┘
                           │
              ┌────────────┼────────────┐
              │                         │
    DELETE /v1/sessions          TTL expires
              │                         │
              ▼                         ▼
        ┌─────────────────────────────────┐
        │           Cleaned Up            │
        │  (all data for session deleted) │
        └─────────────────────────────────┘
```

---

# 9. Failure Mode & Degradation

CALM sits in the data path but never in the critical path. Invariant #1 governs everything here: if CALM can't help, it gets out of the way. The workload's LLM call always works — the worst case is higher token cost, never a broken request (see Decision Log [DL04](#dl04)).

The workload's middleware is responsible for implementing the fallback. CALM doesn't inject itself transparently — the workload explicitly calls CALM and handles failure. A typical middleware pattern:

```
result = callTool(toolName, input)
try:
    compact = calm.ingest(sessionId, result, timeout=200ms)
    use compact in LLM context
catch timeout, connection error, 5xx:
    use raw result in LLM context
```

## Failure Scenarios

### CALM is completely down

The workload's middleware gets a connection refused or DNS failure on ingest/search. The middleware catches it and puts the raw tool output into context. The session continues with no context management — higher token spend, potential quality degradation on longer sessions, but no interruption.

If the failure persists, the workload is effectively running without CALM for the duration. No data is lost — there was nothing indexed yet (or if there was, it's inaccessible until CALM recovers). The workload doesn't need to track whether CALM was available on previous turns.

### CALM is slow

The workload's middleware enforces a timeout on every CALM call. If CALM doesn't respond within the latency budget (see §11), the middleware treats it the same as CALM being down — raw result goes into context.

The timeout is per-call, not cumulative. A slow ingest on turn 5 doesn't affect turn 6 — each call gets its own budget.

### Database is down or slow

From the workload's perspective, this is identical to CALM being slow or down. CALM returns a 5xx or times out. The middleware falls back.

Internally, CALM distinguishes between "database unreachable" and "query slow" for its own alerting. The workload doesn't see the difference and doesn't need to.

### Ingest succeeds, search fails

Content was indexed on an earlier turn. A follow-up search fails — CALM returns a 5xx or times out. The workload doesn't get the search results.

This is the messiest scenario because the workload already has a compact summary from the ingest. It committed to the context-managed path. Now it can't retrieve the detail it needs.

The workload's options:

- Retry the search once. Transient failures (connection blip, slow query) may resolve.
- If retry fails, re-fetch the original data via the tool and put it into context raw. This costs tokens but recovers the information.
- If re-fetching isn't possible (the tool call was destructive or time-bound), the workload works with what it has — the compact summary plus distinctive terms from the original ingest response.

The ingest response always contains enough information (section titles, preview lines, distinctive terms) to be useful even if search is never called. This is a design constraint on the ingest response format, not just a nice-to-have.

### TTL scanner crashes or stalls

Sessions that should have been cleaned up remain in storage. Not workload-facing — no requests fail, no behavior changes. The impact is storage growth.

When multiple CALM instances are running, each runs its own scanner. The delete operation is idempotent — concurrent scans produce redundant queries, not conflicts. To avoid thundering herd on the database, each instance jitters its scan interval randomly within the configured window (e.g., if the interval is 60 seconds, each instance picks a random offset between 0–60 s on startup).

The scanner is a separate background process with its own health check. If it crashes, CALM's request-serving path is unaffected. Monitoring alerts on: scanner not running, sessions exceeding 2× their configured TTL, total session count growing beyond expected bounds.

## Degradation, not failure

CALM is designed so that every failure mode results in degradation (more tokens, less search, no state reconstruction) rather than breakage (workload errors, blocked LLM calls, lost data). The workload always has a path forward — it just costs more when CALM isn't available.

This also means CALM failures are low-severity from an incident response perspective. A CALM outage doesn't page anyone at 3am — it shows up as increased token spend in the observability dashboard and gets addressed during business hours.

---

# 10. Observability & Metrics

CALM uses OpenTelemetry for metrics and traces, and structured logging (JSON) for operational events. The OTel collector endpoint is configurable — the operator points it at Prometheus, Datadog, Grafana Cloud, whatever they run. CALM doesn't mandate a backend.

Metrics carry `namespace` and (where relevant) `client` labels so operators can slice by tenant and workload. Session-scoped metrics aggregate within session bounds; cross-session metrics within a namespace are the management-API and dashboard surface.

## Metrics

Exposed as OTel metrics, scraped by the configured backend.

Metric names use OTel-canonical dotted hierarchy (matches the log-field convention). When exported via the Prometheus exporter, `.` becomes `_` per the OTel-Prometheus mapping spec — Grafana / PromQL users see e.g. `session_create_ttl_clamped`, while code and docs reference `session.create.ttl_clamped`. The `service.name` OTel resource attribute (`calm`) carries deployment-level scoping, so individual metric names don't include a `calm_` prefix.

### Context savings

The core value proposition. If you can't measure savings, you can't justify the system.

- `ingest.bytes_received` — raw bytes received per ingest call
- `ingest.bytes_returned` — bytes in the compact representation returned to the workload
- `ingest.savings_ratio` — `1 - (returned / received)`, per call
- `session.tokens_saved` — estimated tokens saved per session (cumulative across all ingests in the session)

These are the numbers that answer "is CALM paying for itself."

### Search quality

- `search.hit_rate` — percentage of queries that returned at least one result
- `search.zero_results` — count of queries with no matches
- `search.match_layer` — distribution across `primary` and `trigram`. Diagnostic context, not an action trigger. When other metrics (`search.hit_rate`, `search.zero_results`, `ingest.reingest_rate`) surface a quality issue, the match_layer distribution helps explain what the fallback was doing — for example, a drop in `hit_rate` combined with elevated trigram rate suggests workload query patterns are shifting away from what layer-1 tokenization handles cleanly.
- `search.latency_ms` — per-query latency
- `ingest.intent.coverage` — when intents are provided on ingest, the average fraction of sections in the response with non-empty `matches`. Low coverage suggests workloads are providing intents that don't align with the content vocabulary.

The search-budget metrics below carry an `allocator` label identifying which variant produced the budgeted result, enabling A/B comparison across `rank-round`, `score-proportional`, `knapsack-greedy`, `equal-budget`, and `mmr`.

- `search.budget_exhausted` — count of `/v1/search` calls where budget forced at least one omission (any `budget_omitted > 0` across the response's queries). Elevated rates suggest workloads consistently request too-tight budgets or that the content set has hits exceeding typical budget sizes.
- `search.results.omitted` — total count of `SearchHit` omissions across all queries due to budget exhaustion.

### Session lifecycle

- `sessions.active` — gauge of currently active sessions
- `session.duration_seconds` — histogram of session lifetimes
- `session.events.count` — events captured per session
- `session.cleanup.explicit` — sessions closed by the workload
- `session.cleanup.ttl` — sessions cleaned up by TTL scanner. A high ratio of TTL-to-explicit cleanups suggests workloads are crashing or not closing sessions properly.
- `session.create.ttl_clamped` — sessions whose requested `ttl_minutes` exceeded the operator ceiling and were clamped. A non-trivial rate indicates workloads consistently want longer TTLs than the deployment allows; operators can use this to decide whether to raise `sessions.max_ttl_minutes`. Each clamp also emits a WARN log line with the requested-vs-committed values.

### Answer quality

Cost metrics tell you CALM is saving tokens. These tell you whether the model is still getting what it needs.

- `ingest.reingest_rate` — how often the same source label is re-indexed within a session. A workload re-ingesting a source it already indexed is a signal that the compact representation wasn't sufficient.
- `search.after_ingest_rate` — how often a `/v1/search` call follows a `/v1/ingest` on the same source within the same session turn. Expected behavior for iterative workflows; elevated rates on first turns suggest compact summaries aren't landing.
- `snapshot.injection_count` — how often `/v1/snapshot` is called. Tracks how frequently session continuity is actually exercised, not just available.
- `snapshot.events.returned` — counter of events included in snapshot responses, summed across calls.
- `snapshot.events.omitted` — counter of events the snapshot budget forced to drop, summed across calls.
- `snapshot.priority.coverage` — gauge of events returned at each priority divided by total events at that priority, labeled by priority (1–4). Operators monitor for sustained tier degradation — e.g., P1 coverage falling below 100% repeatedly indicates session events are structurally exceeding the snapshot budget.
- `ingest.intent.zero_match_rate` — when intents are provided, the percentage of ingest calls where every section ended up with an empty `matches` array (no section was addressed by any intent). High rates suggest the intents don't align with the content's vocabulary — a signal to revisit either the intent phrasing or the workload's chunking strategy for that content type.

### Outcome attribution

When a workload posts feedback referencing a value-producing call's `correlation_id`, CALM emits outcome-attributed metric series mirroring the signal-bearing metrics, labeled with the declared outcome.

**Direct feedback counters** (labeled by namespace + client):

- `feedback.received_total` — count of successful `/v1/feedback` POSTs, additionally labeled by `outcome` (`success`, `retry`, `degraded`).
- `feedback.late_arrival_total` — count of `/v1/feedback` POSTs rejected with 410 because the correlation's UUIDv7 timestamp was outside the per-namespace feedback TTL window. Elevated rates suggest workloads consistently miss the TTL — either widen the TTL or fix the integration's feedback latency.

**Outcome-attributed mirror metrics.** For each signal-bearing metric, an `outcome_attributed.<family>` series is emitted at feedback receipt, carrying the same dimensions as the original plus `outcome` and `client` labels:

- `outcome_attributed.search.match_layer{layer=primary|trigram, outcome=...}`
- `outcome_attributed.search.budget_exhausted{allocator=..., outcome=...}`
- `outcome_attributed.ingest.reingest_rate{outcome=...}`
- `outcome_attributed.ingest.intent.zero_match_rate{outcome=...}`
- `outcome_attributed.snapshot.priority.coverage{priority=..., outcome=...}`

Operators query: "of the search calls where `match_layer=trigram` fired, what fraction got outcome `degraded`?" → `outcome_attributed.search.match_layer{layer=trigram, outcome=degraded} / outcome_attributed.search.match_layer{layer=trigram}`. With `client` labels, the same query partitions by workload.

**Implicit coverage gap.** Workloads that don't post feedback (notably the MCP adapter) don't contribute to any `outcome_attributed.*` series. Their traffic shows up in the underlying signal metrics (`search.match_layer`, etc.) but not in the outcome-attributed counterparts. Operators compute the unmeasured portion as `(total value-producing calls) − sum(outcome_attributed by outcome)` per signal family — the gap is the operator's `outcome=unset` view.

The outcome-attribution pattern is opt-in workload-side: workloads with outcome signals post feedback; workloads without don't. CALM does not emit anything to fill the gap automatically.

### Service health

- `request.latency_ms` — per-endpoint, p50/p95/p99
- `request.errors` — per-endpoint, by status code
- `db.query.latency_ms` — database operation latency
- `ttl_scanner.last_run` — timestamp of the last successful scan
- `ttl_scanner.sessions_cleaned` — sessions removed per scan cycle

## Traces

OTel traces for request flows through CALM. Each ingest call is a span that includes: chunking duration, indexing duration, intent search duration (if applicable). Each search call is a span with: query parsing, FTS execution per layer (primary + trigram fallback if needed), snippet extraction.

Traces help diagnose latency — is it the database, the chunking, or the search that's slow? They also make it possible to trace a request from the workload through CALM and back, if the workload propagates trace context in its HTTP headers.

## Structured Logging

JSON-formatted logs to stdout. Each log entry includes: timestamp, level, namespace, client, session id (when applicable — the surrogate id; raw session tokens are credentials and never logged), endpoint, `correlation_id` (the `X-CALM-Correlation-Id` minted for the request), W3C trace fields (`trace_id`, `span_id` when CALM has produced a span), and request-specific fields. No unstructured string messages.

Key events at INFO level:

- Session created (with namespace, client, TTL config)
- Session closed (explicit or TTL, with duration and event count)
- Ingest completed (source label, chunk count, bytes received, bytes returned)
- Search completed (query count, results returned, match layers used)

Errors and warnings:

- Database connection failures
- TTL scanner failures
- Ingest failures (malformed content, unsupported format)
- Namespace validation failures (wrong API key for session — logged as warning, returned as 404 to caller)

Session IDs in every relevant log entry make it possible to reconstruct the full history of a session from logs alone.

### Audit events

Security- and lifecycle-significant operations carry a structured audit marker so a compliance pipeline can filter them out of the log stream: authentication outcomes (success and failure), authorization denials, and the create/update/delete lifecycle of sessions, clients, sources, and events. Each marker identifies whether the action was request-driven or system-initiated (background jobs, bootstrap).

Successful authentications are emitted at a verbose (debug) level, not as a standing always-on record: every request authenticates, and the per-request completion summary already carries the authenticated namespace and client, so a separate always-on success line would only duplicate it. The audit-tagged success event exists for deep traceability when verbose logging is enabled; authentication failures and authorization denials, which are rare and security-relevant, are always recorded.

Namespace isolation is enforced as **invisibility** — cross-namespace access returns 404, indistinguishable from not-found — and is deliberately not surfaced as an authorization-denial event, since doing so would leak which namespaces hold which resources.

## Quality risk

Of CALM's twin observable concerns, quality is the harder to detect. Token spend shows up in workload bills directly; degraded answer quality can be invisible to a workload that runs faster and cheaper but missed a critical detail that was filtered out or buried in a low-ranked chunk.

The answer quality metrics above are the detection mechanism. Elevated re-ingest rates, high intent zero-match rates, and frequent search-after-ingest patterns are all signals that CALM is not surfacing the right content. These should be monitored from first production deployment and reviewed alongside workload-side outcome metrics (task completion, retry rates, user corrections) that the workload's owners already track.

---

# 11. Latency Budget

CALM adds a round trip to every tool call it manages. The tool call itself takes 200 ms to 30 seconds. The LLM call after takes 1–5 seconds. CALM sits between these two — its contribution needs to be small enough that it disappears into the noise.

## Targets

**Ingest (no intents):** 50–100 ms in practice. The hottest path — every managed tool call hits it. Involves chunking and indexing into FTS.

**Ingest (with intents):** `base + 30–50 ms per intent` for the per-intent search and RRF aggregation. With 1–3 intents at typical payload sizes, this lands in the 80–250 ms range. The per-intent cost is the same search workload-issued `/v1/search` queries make — full two-layer fallback (see [DL05](#dl05)). Linear in intent count, not in content size or vocabulary. The upper end may exceed the workload's 200 ms middleware timeout, in which case the workload falls back to raw context per invariant #1 (see [DL04](#dl04)) — correct behavior, not failure. Workloads needing guaranteed intent ordering should size intent count and payload accordingly.

**Search:** 50–100 ms in practice. Called on follow-up queries against previously indexed content.

**Session create/delete/snapshot:** Not latency-sensitive. Called once or twice per session. Sub-second is fine.

**Workload middleware timeout:** 200 ms default. If CALM doesn't respond within this window, the middleware falls back to raw context (per §9). The 200 ms ceiling gives headroom for database load and large payloads without making the user wait.

## Caching Strategy

Two in-memory caches help CALM stay within budget. Both are process-local — no Redis, no external cache. Multiple CALM instances each have their own. Cache misses just hit the database.

### Session metadata cache

Every request validates the session token against the namespace. Without caching, that's a database read on every call.

The cache is keyed by `(namespace, session_token)` jointly — the raw token, not the hash, because the cache sits at the service-API surface above the hashing boundary. Value is `(session_id surrogate, client, TTL config, created_at)`. LRU with a size cap (e.g., 10,000 entries). Explicitly invalidated when a session is deleted. No time-based TTL — the mapping is correct until the session is closed, so time-based expiry would be arbitrary. Active sessions stay hot naturally because they're accessed on every request. Abandoned sessions drift to the LRU tail and get evicted.

### Search result cache

Search result caching is not part of CALM's design. Cache invalidation works within a single pod; under multi-pod deployment with round-robin load balancing, ingest may land on pod A while a subsequent search hits pod B, and pod B's cache for that session would be stale until LRU eviction. Unlike the session-metadata cache, search-result staleness doesn't self-heal (the cached result is returned, not re-validated against the DB).

Cold-search latency targets (50–100 ms) are met without a result cache — it would be a perf optimization for repeat-query patterns, not a load-bearing budget mechanism. The natural shape if added later: the LB hashes on `X-CALM-Session-Token` (already on every session-touching request) to pin a session to a pod, keeping cache invalidation in-process. No new wire-contract change is needed; the header is already there for auth.

## What the targets assume

The 50–100 ms target assumes:

- Content per ingest call is in the tens-of-KB range, not megabytes. A 50 KB log file chunks and indexes in under 50 ms. A 5 MB data dump might not.
- The database is co-located or low-latency from CALM (same datacenter, sub-5 ms round trip).
- FTS indexing and querying on the expected corpus size (hundreds to low thousands of chunks per session) stays fast. FTS performance degrades on very large corpora, but per-session scoping keeps the working set small.

Expected latency profile by payload size:

- **Under 100 KB:** Within the 50–100 ms target.
- **100 KB–500 KB:** 100–300 ms. Within the workload's 200 ms timeout for smaller payloads in this range, potentially exceeding it for larger ones.
- **500 KB–1 MB:** Likely exceeds the 200 ms timeout.

In all cases where the timeout fires, the workload falls back to raw context — this is correct behavior per invariant #1 (see Decision Log [DL04](#dl04)), not a failure. The 1 MB operational limit exists to reject truly degenerate payloads; the 200 ms workload timeout handles everything in between.

## Operational Limits

Hard limits, not tuning guidelines. CALM enforces these and returns explicit errors when exceeded.

- **Max ingest payload:** 1 MB per call. Payloads above this are rejected with 413. The workload should split or pre-filter before calling ingest. This cap exists because chunking and indexing a multi-megabyte payload in under 200 ms is not reliable.
- **Max chunks per source:** 500. If chunking produces more, CALM indexes the first 500 and sets `truncated: true` in the response.
- **Max events per session:** 1000 (already specified in §7). FIFO eviction by lowest priority, oldest first.
- **Max search queries per call:** 10. More than 10 queries in a single `/v1/search` call are rejected with 400.
- **Max snapshot budget:** 8 KB. Workloads can request less via `budget_bytes`, but not more.
- **Max search budget:** 64 KB default ceiling, operator-configurable. Workloads request via `budget_bytes` on `/v1/search`; over-ceiling requests are clamped to the ceiling rather than rejected (parallel to TTL clamping). Default request budget when omitted is 4 KB.
- **Max `X-Workload-Request-Id` length:** 256 characters. Inbound values exceeding this are rejected with 400.
- **Feedback TTL:** 60 minutes default per namespace; configurable in `[1, 1440]` (1 min to 24 hours). Sets the feedback-acceptance window for `/v1/feedback`; enforced by the handler via the embedded UUIDv7 timestamp in `correlation_id`. No background scanner; row cleanup happens at session teardown.
- **Rate limiting (three tiers, in-app, per-pod token buckets, burst = 2× rate).** Tier order: IP → namespace → global. IP is pre-Auth (cheapest check that doesn't need namespace context). Namespace runs *before* global at the post-Auth tier — this is load-bearing for the namespace-isolation invariant. With global-first, a misbehaving namespace would burn global tokens on requests it was always going to 429 at the namespace tier, leaking its overload pressure into other namespaces' shared global headroom. Namespace-first keeps each namespace's misbehavior contained to its own bucket.
    1. **Per-IP, pre-auth.** Bounded per-client-IP bucket sitting *before* the Auth middleware. Defends against unauthenticated DDoS that would otherwise saturate registry-lookup CPU on every bad-key attempt. Bounded store (idle buckets evicted under fanout). The in-app middleware is a backstop; production deployments should also enforce IP rate limits at the LB layer, and configure `trust_proxy_headers` correctly when behind a trusted LB.
    2. **Per-namespace** (primary). A runaway workload (looping pipeline, misconfigured MCP adapter, internal LLM app in an error spiral) cannot flood CALM or saturate the shared database. Rate comes from the namespace registry, falling back to the global default.
    3. **Global aggregate** (optional, default disabled). Single shared bucket as defense-in-depth against many-small-namespaces sum-overload that per-namespace caps alone wouldn't catch. Operators opt in.

  Exceeding any tier returns 429 with `Retry-After: 1` and `detail` indicating which tier throttled. Buckets are per-pod; cluster-wide effective rates scale with pod count.

---

# 12. Deployment Model

## Deployment topology

CALM pods sit behind a Kubernetes Service (or equivalent load balancer). Any pod handles any request — no session affinity, no leader election, no coordination between pods. Postgres is the shared state.

Scaling horizontally is architecturally supported (stateless service, shared database), but CALM's compute is lightweight — chunking is string splitting, search is a database query, snippet extraction is string slicing. A single pod can comfortably handle thousands of requests per second. The database is the bottleneck long before CALM's compute is.

Each pod runs its own LRU caches (session metadata, search results). If one pod deletes a session, other pods may serve a stale cache hit on the next request — the database query returns empty, the stale entry evicts naturally. Harmless inconsistency, not worth distributed cache invalidation.

TTL scanner runs in every pod with jittered intervals (§9). Concurrent scans produce redundant deletes, not conflicts.

For airgapped or on-prem deployments, the same topology applies — CALM pods deployed alongside Postgres in the operating org's cluster, no external dependencies, no outbound network requirements. API keys and namespace mappings are loaded from a ConfigMap or mounted Secret (see Decision Log [DL10](#dl10)). Workloads reach CALM via cluster-internal DNS.

When CALM sits behind a trusted load balancer, set `server.trust_proxy_headers=true` so the pre-auth IP rate-limit tier reads the original client IP from `X-Forwarded-For` (left-most entry per RFC 7239) instead of the LB's IP. Leave it false otherwise — trusting the header without a stripping LB allows clients to spoof their source IP and bypass the IP tier.

Postgres requires either `pg_search` or `pg_textsearch` before CALM can serve correct ranked results — neither ships with a standard Postgres distribution and both require operator installation. The Helm chart includes a preflight check that validates extension availability at deploy time and fails the rollout if neither is present. This is a hard gate, not a warning.

## Evaluation onramp

For evaluators or teams trying CALM before committing to a production deployment, `docker-compose up` brings up CALM, Postgres, and the required extensions in one command:

```
git clone <calm-repo>
docker-compose up
```

A bootstrap API key is printed on first run; the operator uses it to authenticate against the running CALM instance. This is not a recommended production setup — it lacks redundancy, backup, observability — but it's the fastest path from "I want to try this" to "it's serving requests."

## Distribution

- **Docker image** — for containerized deployments at any scale.
- **Helm chart** — for Kubernetes deployments (cloud or airgapped). Includes CALM pods, Postgres dependency (or external Postgres config), ConfigMap for API keys and namespace mappings, and Service definition.
- **MCP adapter** — distributed as a standalone binary via Homebrew (`brew install calm-adapter`). The adapter is the only component a developer needs to register with their coding agent — it handles the CALM URL as a startup argument. Developers configure their team's namespace API key in the adapter's config file or environment.

## Security considerations

CALM stores tool output that may contain logs, code, telemetry, and potentially sensitive data embedded in those things. Transport security is assumed — TLS via service mesh or ingress controller for production deployments. Encryption at rest follows the storage backend's capabilities (Postgres TDE or volume-level encryption). All session data is ephemeral and cleaned up by explicit close or TTL — CALM is not a long-term data store, which limits the exposure window. Management API endpoints (`/v1/manage/*`) should be restricted to ops-scoped API keys that are not shared with workload service accounts.

---

# 13. Decision Log

Settled decisions and the reasoning behind them. Captured here so the design isn't relitigated by someone who wasn't in the room.

### DL01

**Consumer-type categories collapse to clients in a namespace**

CALM's data model and API do not enumerate a closed set of consumer types. A session belongs to a namespace and carries a free-form `client` identifier; the schema does not branch on consumer category, and the API does not document distinct integration contracts per consumer type. Any LLM workload that can speak HTTP and authenticate with a namespace credential is a valid CALM consumer.

The alternative — three first-class architectural categories (e.g., debug agent / automated pipeline / coding-agent-via-MCP), each with dedicated schema columns and integration contracts — was considered and rejected. The team-deployed audience runs whatever mix of LLM workloads it has, and CALM has no principled basis for declaring three of those types more first-class than others. Heterogeneous workloads in a single deployment do not fit a fixed taxonomy: an eval harness in CI, a slackbot, a debug-style internal LLM application, and several coding agents through MCP all coexist without category overlap helping anything.

The MCP adapter binary CALM ships is one client among many, not a privileged architectural surface — just the one CALM happens to ship in the box because external coding-agent integration via MCP is harder for teams to roll on their own than a direct HTTP integration is.

Practically, the collapse means: the `session_events` schema does not carry a `category` column or a `source_hook` column; the API does not require workloads to declare which "type" they are; new workload patterns integrate without architectural change.

### DL02

**Executor demoted from architectural primitive to adapter capability**

The sandbox executor exists because coding agents need to run local code (git, shell, build tools) and capture stdout. But the executor needs access to the developer's filesystem, project directory, and local CLIs — it must run on the developer's machine, not in CALM's service.

This makes it a capability of the MCP adapter, not a CALM component. The adapter receives a tool call, runs it locally, captures stdout, sends the output to CALM via `/v1/ingest`. CALM never sees or runs the code. Calling it an architectural primitive overstated its role.

### DL03

**Explicit session creation over auto-create**

With namespace scoping, every session must be tagged with the correct namespace at creation time. Auto-creation on first ingest would silently create orphaned sessions and remove a meaningful invariant — that the lifecycle of a session is bookended by explicit workload action.

Because the session credential is server-minted, "wrong session token" is a security concern rather than a config-typo concern: presenting a bogus, expired, or someone-else's `X-CALM-Session-Token` returns 404. Explicit creation forces the workload to acknowledge it's starting a unit of work; the 404-on-unknown-token surface protects against credential misuse.

### DL04

**Sidecar service over transparent proxy**

A proxy model would give consuming applications zero-code integration — change one URL config and CALM intercepts all LLM traffic. But it puts CALM in the LLM request path, making it a single point of failure for every LLM call. A bug in message inspection could corrupt the message array before it hits the LLM. And every call pays a latency tax even when CALM has nothing to do.

The sidecar model avoids all of this. CALM sits beside the LLM call, not between. The workload calls CALM, then calls the LLM — two separate calls. The integration point is defined by CALM's HTTP API contract and adopted by each workload via its own middleware. Each workload remains in control of its LLM request path; CALM never becomes a single point of failure for inference.

### DL05

**Intent filtering: multi-intent input, RRF-ranked summary, no binary match/no-match outcome**

The natural design for intent-driven content filtering is a single intent string with a binary outcome: matches or no-match-fallback. CALM diverges on three axes.

1. **Multi-intent input** (`intents: [string]`, capped at 3). Workloads often have parallel concerns — errors *and* slow queries, build failures *and* test failures. A single-intent design forces awkward "pick one" semantics when the workload's interest is genuinely multi-faceted.

2. **No binary match/no-match.** The compact summary always contains all indexed section titles; intents shape *ordering*, not inclusion. When intents are provided, CALM runs a search per intent and fuses the per-intent rankings via Reciprocal Rank Fusion to order `summary`. Sections matching no intents simply appear lower in the ranking. There is no `intent_matched: false` fallback path — the response shape is the same whether intents matched anything or not.

3. **Per-section `matches` array, not numeric scores.** Each section in `summary` declares which intents it addresses, derived from the section's rank in each intent's individual top-K results. Per-intent numeric scores were rejected: RRF (the ordering mechanism) operates on ranks, not scores, so exposing per-intent normalized BM25 scores would create an inconsistency where the displayed numbers don't correspond directly to the ordering they're attached to.

Alternatives considered:

- **Single intent with binary outcome.** Rejected: forces multi-concern callers into a pick-one decision; the no-match fallback path is awkward dead-letter handling that callers have to reason about ("did I get matches, or did I get the fallback?").
- **Per-section numeric relevance scores per intent.** Rejected: inconsistency with RRF ordering, as described above.
- **No per-intent attribution (ordering signal only).** Rejected: the LLM benefits from knowing which intents each section addresses to issue targeted follow-up `/v1/search` queries; operators benefit from per-intent coverage signal.

The per-section `matches` array is the minimum useful per-intent signal — it tells the caller which intents a section addresses without exposing scoring machinery that would muddle the ordering semantics.

**Layer behavior on intent search.** Intent search uses the same two-layer semantics as workload-issued `/v1/search` queries — layer 1 RRF fusion across the prose and code FTS indexes, layer 2 trigram fallback via `pg_trgm` if layer 1 underfills the limit. One operation, one set of observable behaviors, one mental model.

### DL06

**Tokenization branches on code vs prose**

CALM dispatches tokenization on the chunk's `content_type`: prose-shaped chunks are indexed with porter stemming over standard unicode tokenization (helpful for morphological matches like "caching" → "cached"); code-shaped chunks are indexed with identifier-preserving tokenization and no stemming, so identifiers like `getUserById` survive as whole tokens.

The alternative — uniform porter stemming for all content — was rejected. Porter on code-identifier-heavy content silently degrades exact-match queries; trigram fallback partially compensates but not fully (multi-token code queries hit ranking issues). A second alternative — uniform identifier-preserving tokenization with no stemming for all content — was also rejected: prose-heavy workloads (eval harnesses, slackbots, factory tool outputs) lose morphological recall when the stemmer is removed. Branching gives both workload shapes the right tokenizer.

The architectural cost is two FTS indexes (one per tokenizer, with chunks dispatched at insert time based on `content_type`) and rank fusion at search time across both indexes. Adding more format-specific tokenizations later (e.g., a structured-data tokenizer for `csv` or `json` chunks) is a concrete code change in the dispatch path, not a plugin surface — speculative-abstraction architecture is explicitly avoided here.

`content_type` is determined per chunk by the chunker — not by the workload as a single label for the whole ingest. The workload supplies an ingest-level default (the MCP adapter passes it based on tool patterns; other workloads pass it explicitly); the chunker overrides per chunk when format signals demand it (a fenced code block inside a markdown ingest is `code`; a stacktrace frame is `code`; otherwise the default applies). This per-chunk granularity is load-bearing for mixed-content ingests — markdown with embedded code blocks, log output containing JSON payloads, etc. — where the right tokenizer differs across chunks of the same ingest. Unknown values are treated as `prose`. CALM does not maintain content-detection logic beyond the per-format dispatch in the chunker.

### DL07

**FTS with BM25 over vector/embedding search**

Vector search requires an embedding model — either a remote API (latency, cost, availability dependency) or a local model (memory, deployment complexity). CALM's typical content is technical — logs, stack traces, metrics, CLI output, structured tool responses — with predictable vocabulary. BM25 with porter stemming and trigram substring matching handles this well. The semantic gap that embeddings solve ("authentication failures" matching "login errors") is a natural-language problem that the two-layer fallback partially addresses, and the ephemeral session model limits the payoff of an embedding dependency.

Research identified a middle path: LLM-generated document enrichment at index time, which closes 50-70% of the gap to neural retrieval without an embedding dependency. This is deferred as premature optimization but the schema can accommodate it without structural changes if search quality data justifies it later.

### DL08

**Snapshot is a generic event store; pluggable strategies deferred**

CALM's snapshot endpoint returns events ordered by priority and recency, accumulating until a byte budget is reached. It does not interpret event content, does not build a structured state representation, does not branch on event type or category. Workloads needing structured state shapes build them in their own middleware.

The alternative — a built-in snapshot strategy shaped around a particular workload (e.g., coding-agent state: active files, errors, decisions, tasks) — was rejected. Under team-first deployment with heterogeneous workloads, no single state shape generalizes. A slackbot's snapshot is about active threads; an eval harness's about the current run; a custom internal app may not need a snapshot at all. Building one workload's state model into CALM's snapshot privileges that workload's shape and forces others to either accept the wrong shape or build their own state model anyway.

A pluggable strategy mechanism (operator registers per-client snapshot logic; CALM dispatches at snapshot time) is not part of CALM's surface. The evidence gate for adding one: multiple workloads in a single deployment independently implement the same structured reduction. The current design discipline is *expressiveness without commitment*: the event schema is rich enough that a future strategy could read it, and the HTTP response shape is generic enough to be extended without breaking existing workloads — but no interface, plugin registry, or per-client `snapshot_strategy` column exists.

### DL09

**HTTP REST over gRPC / Connect RPC**

CALM's traffic pattern is request/response with text-heavy JSON payloads. No streaming. No bidirectional communication. Binary encoding doesn't help. HTTP REST is the simplest protocol that does the job. Every language has an HTTP client. No codegen step. No protobuf compilation. Open-source contributors don't need a toolchain to integrate.

### DL10

**Config-file API keys over IAM integration**

CALM has a handful of workloads per deployment — a slackbot, an eval harness, a few MCP adapters, an internal copilot. New workloads are onboarded occasionally, not continuously. A config file mapping API keys to namespaces covers this. No admin API, no key provisioning endpoint, no token exchange protocol. The operator writes the config and deploys. A key management API is deferred until the workload count justifies it.

**Why the platform layer owns identity, not CALM.** Two reasons, jointly load-bearing. *First, workload auth models are heterogeneous in real deployments.* Internal LLM applications carry whatever auth their org uses; automated pipelines have service accounts or CI runner identities; MCP-mediated coding agents have dev-machine identity. Forcing convergence on a CALM-shaped identity is backwards — the platform that wires CALM into each context is the only place that can sensibly bridge those primitives. *Second, the platform layer is already the trust authority for the workloads it runs.* Whoever runs CALM also configures (or runs) the workloads talking to it. Identity decisions happen upstream of CALM in the operator's wiring — secret stores, deploy pipelines, service-mesh policies. Consuming `API key → namespace` is the right contract for an infra component sitting behind that wiring. This matches the pattern of analogous infra (Postgres delegates SSO to pgbouncer / Cloud SQL Auth Proxy; Redis treats ACLs as optional; Kafka takes identity from Kerberos/mTLS outside the broker; gRPC in a service mesh delegates to the mesh). CALM-as-IAM would be the anomaly, not the norm.

**What's deferred but anticipated within the config-file model.** Three operator-facing capabilities are likely needed before CALM serves broader audiences, and all fit naturally inside the existing `API key → namespace` model without introducing IAM-shaped surface: (a) **workload-scoped API key lifecycle** — issue, rotate, revoke per workload without redeploying the operator config; (b) **credential rotation flow** with overlapping validity so rotation is downtime-free; (c) **basic credential observability** — last-used timestamps, simple leak-detection signals. Each is hygiene around the existing model, not a step toward IAM. Adding them now would over-engineer for an audience that hasn't asked. The pre-existing position is: keep CALM at the simplest model that meets current adoption, and revisit when a real trigger appears (credential leak incident, larger-org adoption, OSS distribution to less-mature orgs).

**What stays explicitly out of scope.** Per-action authorization, IDP integration (OIDC/SAML/AD), refresh-token flows, and principals/roles/policies machinery. A SaaS-shape CALM would need most of those, but SaaS is a structurally different product — multi-tenant data model, IDP at the edge, tenant isolation primitives, audit/billing surface — and is not a roadmap direction in this iteration. Treat this as the bar for future PRs: features that fit "hygiene around API key → namespace" are on the table; features that introduce IAM concepts (subjects beyond API keys, per-resource permissions, third-party identity sources) are not, absent a deliberate product-shape change discussed upstream of any code.

**Secret-reference URI dialect.** Each row's secret value is a bracketed reference: `[text:<literal>]`, `[env:<VAR>]`, or `[file:<path>]`. The keys file itself contains no secret material — it's a manifest of where each namespace's credential lives. Operators wire CALM into their platform's existing secret-management tooling (k8s Secrets, Vault Agent, External Secrets Operator, ECS task secrets) by populating env vars or rendering files; CALM consumes via the bracket dialect without linking any provider SDK. Resolution happens at startup; failures are Fatal.

The same dialect is reusable for any future operator-facing secret-bearing config (Postgres DSN, TLS certs, OTel exporter tokens, etc.).

Rejected dialect entries:

- **`[secret:<provider-key>]` (direct Vault / AWS-SM / GCP-SM / Azure-KV fetch).** Would require linking provider SDKs (~50–100 MB each plus per-provider auth, region, retry plumbing), duplicating what platform tools already do better. The static-standalone-binary property loses meaning. Operators bridge to their secret store via env or file rendering using existing tooling.
- **Bare literals (no brackets).** Considered for ergonomic concision, rejected for grammar consistency. Bracket-everywhere costs ~4 extra characters per literal in exchange for unambiguous parsing and uniform eye-scanning.

### DL11

**Namespace isolation over tenant model**

"Tenant" implies a specific organizational hierarchy. CALM doesn't interpret what a namespace means. It partitions on it. A namespace can represent an org, a team, a user, or a deployment environment — the deployer decides. Same mechanism at any granularity, same as Kubernetes namespaces.

Per-user isolation within a namespace (e.g., user foo can't see user bar's debug sessions) is the workload's responsibility. CALM prevents cross-namespace leakage. Within a namespace, the workload controls access by managing which session tokens are visible to which users.

### DL12

**Postgres as sole storage backend**

The data-access layer is abstracted as a port boundary to enable testing in isolation from storage, not to enable swapping storage backends. There is one production storage backend: Postgres.

Rejected alternatives:

- **SQLite** — limited FTS5 (no real BM25/IDF), single-writer, no `pg_trgm` equivalent for trigram indexing.
- **MySQL / MariaDB** — InnoDB FTS isn't BM25 and isn't configurable. No trigram-index equivalent of `pg_trgm`. No partial indexes (which CALM uses to route prose vs. code chunks into separate FTS indexes per [DL06](#dl06)). JSON support is less mature than JSONB. Delivering the search architecture would require bolting Elasticsearch or Tantivy on the side, breaking the "single store, no external search system" property.
- **Portable data-access layer with multiple backend implementations** — false flexibility. Doubles maintenance surface (every storage operation needs N implementations + parity tests) for a portability story with no concrete demand.
- **Embedded KV (BoltDB / BadgerDB)** — no full-text search, no relational queries for the management API, no way to express FTS partial indexes.
- **Distributed SQL (CockroachDB, TiDB)** — Postgres-wire-compatible but lacks the BM25 extensions; operational complexity exceeds what the use case demands.

Why Postgres specifically: BM25 via `pg_search` or `pg_textsearch` ([DL07](#dl07) rationale), `pg_trgm` for trigram fallback, JSONB for opaque event data, partial indexes for the prose/code routing, atomic transactions for re-index (invariant #6). Single backend, no external search system, no exotic dependencies.

Re-introducing a portability layer requires HLD discussion before code lands.

### DL13

**Session-scoped storage, not cross-session memory**

CALM manages what enters context during a session. What the agent remembers across sessions is a different system — the agent's long-term memory layer. The industry consensus (MemGPT, Mem0, LangMem, AgeMem) draws the same boundary: short-term/working memory is session-scoped, long-term memory is a separate persistent store. CALM is the working memory manager. Cross-session knowledge is the agent's responsibility.

This keeps CALM simple. No promotion mechanisms, no corpus lifecycle management, no deciding what's worth keeping. Content expires with the session.

### DL14

**Session is an active scope, not a durable workflow record**

A CALM session has one durable service state: active. Explicit close or TTL expiry deletes the session and all its child rows; CALM retains no terminal state — completed, failed, abandoned, expired. Aggregate observability (counts, distributions, rates labeled by namespace and client) is emitted via OpenTelemetry to the operator's metric backend; row-level retention beyond the active scope, when an operator needs it, is delegated to an exporter sink (Postgres, OTLP, file, multi-sink) that receives short-lived observability rows before deletion. CALM does not mandate a sink and does not expose terminal-state queries.

The alternative — durable terminal session records, à la workflow engines (Temporal, Airflow, durable-execution platforms) — was considered and rejected. Three reasons, jointly load-bearing.

*First, workload outcomes are workload-resident.* Whether a session "succeeded" is defined by the workload's own task semantics, not by anything CALM observes. A coding-agent session "succeeded" if the user accepted the agent's work; a pipeline step "succeeded" if its downstream verifier passed. CALM's vantage is the data path, never the control path; building a terminal-state taxonomy here would require CALM to interpret outcomes it cannot define.

*Second, aggregate observability is sufficient for cross-session analytics.* Counters and histograms emitted via OpenTelemetry — labeled by namespace and client — answer cross-session questions ("what is the per-workload distribution of session durations? of event counts? of search hit rates?") without per-row persistence. Per-row forensics, when needed, live in workload-side telemetry the workload already operates.

*Third, the exporter seam is the right escape hatch.* Operators who genuinely need long-tail row-level retention configure an exporter sink that receives short-lived rows before deletion. This delegates retention to operator-resident storage rather than embedding a workflow ledger into CALM. CALM does not mandate a sink, and which sink implementations ship in the binary is an LLD choice.

This is distinct from [DL13](#dl13) (session-scoped *content* storage, no cross-session memory). DL13 governs the content-scope axis: what gets indexed and how it expires. DL14 governs the lifecycle-shape axis: what state a session can be in and how its termination is recorded. Cross-session content sharing (DL13) and terminal session records (DL14) are independent product-boundary decisions.

### DL15

**Response-level byte budget for `/v1/search`**

`/v1/search` accepts a workload-controlled `budget_bytes` value that caps the total bytes of serialized `SearchHit` objects across all queries in the response. A deterministic rank-round allocator across queries decides which exact-text hits fit — every query's first-ranked candidate is offered before any query's second, every second before any third. Each candidate is accounted as its compact JSON UTF-8 representation; included only when its size fits the remaining budget; snippets are never further truncated. Per-query `budget_omitted` reports the count of otherwise-returnable candidates (from that query's top-`limit` set) that were not included. Default budget is 4 KB; bounded by an operator-configurable ceiling (default 64 KB). Over-ceiling requests are clamped, not rejected (per [DL04](#dl04)'s never-worse stance; parallel to session-TTL clamping).

The allocator is pluggable. Five variants ship: **rank-round** (default — offers every query's first-ranked candidate before any query's second, preserving multi-query coverage); **score-proportional** (allocates budget proportionally to per-query rank scores); **knapsack-greedy** (DP knapsack maximizing sum-of-relevance under budget); **equal-budget** (`budget / N` per query); **MMR** (Maximal Marginal Relevance — re-ranks for diversity against near-duplicate hits across queries). Operators set the namespace default; workloads override per request via `X-CALM-Allocator-Variant` (gated by a namespace flag). All variants honor the same budget contract: no overshoot beyond the first-considered candidate, no snippet truncation, per-query `budget_omitted` accounting unchanged. Rank-round is the default because it preserves multi-query coverage — no single query's tail starves another query's head, which is the canonical complaint about count-based and score-proportional allocators.

Considered alternatives:

- **No byte budget at all** — return all hits up to `limit`, workload sizes its own context downstream. Rejected: count caps don't predict bytes (a hit could be 100 B or 10 KB), so workloads can't reason about context consumption; the diagnostic surface (`byte_budget_used`, `budget_omitted`) becomes the means by which workloads observe budget pressure.
- **Per-query budget instead of response-level.** Each query gets its own `budget_bytes`. Rejected: workloads' downstream context windows are response-level concerns — they care about total bytes consumed, not which query produced which bytes. Per-query budgets force workloads to do response-level summation client-side. Response-level matches what workloads actually budget against. Workloads that want per-query control can issue separate `/v1/search` calls per query.
- **Per-hit count limit only** (the existing `limit`). Rejected for the same reason as no-budget — counts don't predict bytes; the diagnostic surface is moot without a byte axis. `limit` and `budget_bytes` are not alternatives but composable gates: both apply, the tighter wins.
- **Single fixed allocator (rank-round only).** Rejected: workloads' optimal allocation depends on their query patterns. Multi-query searches over independent topics benefit from rank-round's coverage preservation; searches over near-duplicate queries benefit from MMR's diversity; cost-sensitive workloads with stable query relevance distributions can prefer knapsack-greedy. Pluggability lets operators tune per namespace without code changes, and per-request override lets workloads A/B test for their specific access patterns. The variants are a small, well-understood set — not an open plugin surface.

**Overshoot rule.** When no candidate fits the budget, the first-considered candidate (query[0]'s first-ranked hit) is included anyway — `byte_budget_used` reflects the actual size, exceeding the requested budget. This preserves the never-worse property (invariant #1): workloads with budget-too-small misconfigurations still receive their highest-confidence content rather than an empty result. Workloads detect overshoot by comparing `byte_budget_used` against the echoed `budget_bytes`. Parallel to the snapshot endpoint's P1 overshoot rule (§8).

---
