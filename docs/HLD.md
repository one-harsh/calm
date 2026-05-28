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

- **Token spend scales with data volume, not with value.** Real tool outputs — logs, API responses, query results, file dumps — typically compress by an order of magnitude into a compact representation that preserves what the model actually needs: section titles, preview lines, and a searchable vocabulary of indexed terms. The raw content stays in a queryable store; only the compact form enters context. Specific compression ratios and per-team spend impact will be quantified against a workload-representative benchmark; the structural argument holds regardless of where the empirical numbers land.

## Cost of inaction

Two dimensions, both real:

**Financial.** Tool-heavy session token spend grows with payload size, not with what the model uses. Across a team running multiple AI workloads at typical engineering throughput, this compounds — a single misbehaving slackbot ingesting verbose API responses can dominate the team's monthly API bill. Concrete figures vary by model pricing and deployment shape; the per-team multiplier is non-trivial regardless.

**Qualitative.** The same context pollution that wastes tokens degrades answer quality on every model — hosted or self-hosted. A model attending to 45 KB of stale tool output produces worse answers than one attending to a few KB of relevant content, regardless of who runs the inference. For self-hosted deployments the cost is not financial — it's quality. For hosted-API teams, it's both.

## Measurable quality

CALM's claim to save tokens is instrumented, not asserted. Every session exposes signals — re-ingest rate, intent coverage, search-match-layer distribution, snapshot injection frequency, and more (§10 documents the full set) — that let operators detect quiet degradation alongside their existing workload-side outcome metrics (task completion, retry rates, user corrections). The risk that compression silently hurts answer quality is real; CALM's value isn't credible without making that risk observable.

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

Workloads can pass `intents` (up to 3) alongside content to shape the compact summary's ordering. When intents are provided and content exceeds a configurable size threshold, CALM runs a search per intent against the just-indexed content and fuses the per-intent rankings via Reciprocal Rank Fusion (RRF) to order `summary`. The per-intent search uses the same three-layer fallback as workload-issued `/v1/search` queries — one `Search()` semantics, no carve-out for the ingest path. Each section in the summary declares which intents it addresses through a `matches` array — derived from the section's rank in each intent's individual top-K results, not from raw scores (see Decision Log [DL05](#dl05)).

The summary always contains all indexed section titles; intents shape *ordering*, not inclusion. There is no binary match-or-fallback semantics — sections matching no intents simply appear lower in the ordering with empty `matches`. Without `intents`, the summary is in document order and `matches` is omitted per section.

The compact representation also carries a **distinctive-terms** vocabulary derived from the indexed content — the top-N terms by IDF — so the LLM has a concrete handle on what's searchable in the indexed content without having to guess.

### Knowledge Store

The query layer over the ingested content. Search uses ranked retrieval with a multi-layer fallback:

1. **Stemmed / identifier-preserving search.** Porter stemming on prose-shaped chunks ("caching" matches "cached"); identifier-preserving tokenization with no stemming on code-shaped chunks (`getUserById` survives as a single token). Tokenization branches on the chunk's `content_type` to give each shape its appropriate strategy (see Decision Log [DL06](#dl06)). AND across query terms first; falls back to OR.
2. **Trigram substring matching.** Partial-term matches that the layer-1 tokenizers miss — `connPool` finds `connectionPool`.
3. **Fuzzy correction.** Levenshtein distance against the per-session indexed vocabulary — `postres` corrects to `postgres` and re-runs through layers 1 and 2.

Results are exact indexed text with smart snippet extraction around matching terms. No summaries, no paraphrases.

BM25 ranking weights title fields higher than content, so heading matches surface first. Backed by a BM25-capable Postgres extension (`pg_search` or `pg_textsearch`) with `pg_trgm` for the trigram layer. The choice of BM25 over vector/embedding search is discussed in Decision Log [DL07](#dl07).

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

**Snapshot is a generic event store.** State reconstruction returns events ordered by priority and recency within a byte budget. CALM does not interpret events' content to build a structured state representation — workloads needing structured shapes build them in their own middleware from the returned event stream. Pluggable per-client snapshot strategies are deferred as part of MVP scoping (see Decision Log [DL08](#dl08)).

---

## Design Invariants

Six rules. Non-negotiable.

**1. Never makes things worse.** CALM unavailable? The workload falls back to raw content. CALM too slow? Same. LLM calls always work. The worst case is higher token cost, never a broken request.

**2. Workload-agnostic.** Same API for any LLM application that can speak HTTP. No special integration contracts per workload type (see Decision Log [DL01](#dl01)).

**3. Namespace + session isolation.** Two boundaries, both load-bearing, each enforcing isolation at a different layer. **Namespace** is the security/trust boundary: cross-namespace queries are forbidden, and a mismatch returns 404 (invisibility, not denial). **Session** is the content/scope boundary inside a namespace: indexed content and events are bound to a session and invisible to other sessions in the same namespace. Both apply to every operation. Sessions are cleaned up on explicit close or inactivity TTL, whichever comes first; TTL is configurable per session at creation, bounded by an operator-set ceiling. Cross-namespace mismatch is a confidentiality breach; cross-session-within-a-namespace leakage is a workload-contract violation. Bugs in either are bugs.

**4. Never in the LLM request path.** CALM sits beside the LLM call, not between the workload and the LLM. The workload calls CALM, then calls the LLM. Two separate calls (see Decision Log [DL04](#dl04)).

**5. Content fidelity.** CALM decides *which* content to return. Never alters what's in it. A code block goes in, the same code block comes out.

**6. Idempotent indexing.** Same source label indexed twice within a session? The second replaces the first. No stale duplicates from iterative workflows.

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
│  │  /v1/sessions/{id}/snapshot      /v1/manage/*          │   │
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
│  │  - Trigram → Fuzzy fallback                            │   │
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

**The MCP Adapter is one workload's integration shim.** It is a separate binary that a coding agent (Claude Code, Cursor, Codex, similar) spawns as a child process on the developer's machine. It speaks MCP over stdio on the agent side and HTTP on the CALM side, translating between the two so the coding agent doesn't need to know CALM exists as a service. It generates a session ID on startup, closes the session on shutdown, and inspects tool calls passing through it to derive structured events (the same `/v1/events` surface any workload would use). Internal LLM applications and pipelines bypass the adapter entirely and call CALM's HTTP API directly.

When the agent wants to run code, the adapter runs it locally as a subprocess — it has access to the project directory, filesystem, git, local CLIs. It captures stdout and sends the output to CALM's `/v1/ingest`. CALM never sees or runs the code; it only receives the text output (see Decision Log [DL02](#dl02)).

Platform hooks (PreToolUse, PostToolUse) on the coding-agent side fire on tool calls that don't go through CALM's own MCP tools — for instance, a developer-invoked Bash command. The hooks are thin shims that call CALM's HTTP API to ingest the tool output and post the corresponding event. The logic lives in CALM; the hooks just translate.

**Storage.** Postgres in production, with a BM25-capable extension (`pg_search` or `pg_textsearch`) and `pg_trgm` for the trigram layer.

**Workload integration is not trivial.** Every workload that uses CALM needs middleware that manages sessions, calls ingest with format hints, handles timeouts with fallback to raw content, and posts events. The MCP adapter adds protocol translation and subprocess management on top of this. This complexity is the deliberate cost of keeping CALM out of the LLM request path (see Decision Log [DL04](#dl04)). The alternative — a transparent proxy — would hide integration complexity but create a single point of failure for every LLM call. CALM ships reference middleware as working examples of the integration contract, not as libraries to take a hard dependency on.

## Request flows

### Internal LLM app: ingest tool output with intent-ordered summary

```
Slackbot's tool handler executes a ClickHouse query, gets 50 KB of results
  → POST /v1/ingest { session_id, content, format: "log", intents: ["connection errors"] }
  → CALM chunks by log format, indexes into knowledge store
  → Runs per-intent search against just-indexed content; orders summary by RRF
  → Returns compact summary (~2 KB) with sections matching "connection errors" ranked first
  → Tool handler injects compact summary into the LLM's next message
```

### Automated pipeline: agent step

```
Pipeline workflow enters agent step
  → Middleware creates CALM session (session_id = workflow_run_id + step_index)
  → Middleware injects search_prior_output tool into the agent's tool set

  Agent loop, tool call 1:
    → LLM requests web search tool
    → Tool handler executes web search, gets 30 KB of results
    → Handler: POST /v1/ingest { session_id, content, source: "web_search" }
    → CALM returns compact representation (1 KB)
    → Handler returns compact version (placed in message history instead of raw output)
    → POST /v1/events { session_id, type: "tool_invocation", priority: 3, data: { tool_name: "web_search" } }

  Agent loop, tool call 2:
    → LLM wants more detail from the earlier search
    → LLM calls search_prior_output { query: "specific topic", source: "web_search" }
    → Tool handler: POST /v1/search { session_id, queries: ["specific topic"] }
    → CALM returns matching chunks from the indexed content
    → Handler returns search results to the agent
    → Continue...

  Loop ends
  → Middleware: DELETE /v1/sessions/{session_id}
```

### Coding agent via MCP adapter

```
Developer starts Claude Code session
  → Claude Code spawns MCP Adapter as child process on dev's machine
  → Adapter generates session_id, calls POST /v1/sessions to create it

  Developer asks agent to analyze a log file
  → Agent calls a CALM MCP tool (e.g., calm_ingest_file)
  → Adapter runs the code locally (has access to project dir, filesystem, git)
  → Captures stdout
  → Adapter calls POST /v1/ingest { session_id, content, format: "log" }
  → CALM returns compact representation
  → Adapter returns it as MCP tool response

  Agent also calls native Bash tool to run git log
  → PostToolUse hook fires
  → Hook calls POST /v1/ingest { session_id, content, content_type: "prose" }
  → Hook calls POST /v1/events { session_id, type: "git_operation", priority: 2, data: { command: "git log" } }
  → Raw git output is replaced with compact version in context

  Context compacts
  → Platform fires SessionStart hook
  → Hook calls GET /v1/sessions/{session_id}/snapshot
  → CALM returns priority-tiered state snapshot
  → Hook injects snapshot into refreshed context
  → Agent continues from where it left off

  Developer ends session
  → Claude Code kills MCP Adapter process
  → Adapter's shutdown hook calls DELETE /v1/sessions/{session_id}
  → If adapter crashes without cleanup, inactivity TTL handles it
```

---

# 6. API Surface

The API is HTTP REST with JSON request and response bodies (see Decision Log [DL09](#dl09) for the protocol choice). All requests carry an API key via header (`Authorization: Bearer <key>`). API keys are mapped to namespaces in service configuration (see Decision Log [DL10](#dl10)); CALM resolves the key to its namespace — a content-agnostic partition, not a hierarchical tenant (see Decision Log [DL11](#dl11)) — and enforces it on every operation. Workloads never pass a namespace directly. Per-workload attribution comes from the `client` identifier described below, not from per-user credentials. The MCP adapter follows the same model — developers configure the adapter binary with the team's namespace API key and the adapter self-identifies via `client` on each request.

Two path groups on the same service:

- `/v1/*` — core API. Called by workloads in the hot path. Every request is scoped to a session. The session must belong to the API key's namespace.
- `/v1/manage/*` — management API. Called by ops tooling. Operates across sessions, but still scoped to the API key's namespace.

---

## Integration contract

Every workload that uses CALM follows the same five-obligation pattern. The shape is uniform across internal LLM applications, automated pipelines, and the MCP adapter; only the surrounding code differs by workload.

1. **Create the session at the start of work.** `POST /v1/sessions` with a workload-chosen session ID (opaque to CALM), optional `client` identifier, optional `ttl_minutes` (bounded by an operator ceiling). The session lives for the duration of the workload's unit of work — one conversation, one pipeline step, one batch job.

2. **Execute tools normally.** Raw output is the workload's responsibility. CALM never runs tools — it receives the output after the workload has produced it.

3. **Ingest tool output before it enters the LLM context.** `POST /v1/ingest` with the raw content, a `source` label (for idempotent re-indexing), and optional `format` / `content_type` hints. CALM returns a compact representation; the workload uses the compact form in place of the raw output in the LLM message stream.
   - **On CALM success:** use the compact representation.
   - **On CALM timeout or error:** fall back to the raw output. Never fail the LLM call because CALM is unavailable (see Decision Log [DL04](#dl04), invariant #1).

4. **Capture structured events as work progresses.** `POST /v1/events` for state-relevant events (files edited, errors observed, user decisions). Each event has a workload-chosen `type`, a `priority` in P1–P4, and a `data` JSON payload. CALM does not interpret event content; it categorizes by priority for snapshot triage.

5. **Tear down explicitly when work completes.** `DELETE /v1/sessions/{id}` at end of work. If the workload crashes or disconnects, inactivity TTL handles the cleanup.

The MCP adapter implements this contract on behalf of the coding agent it serves — generating the session ID, calling ingest as tool calls pass through, posting events derived from the tool calls, deleting the session on shutdown. Internal LLM applications and automated pipelines implement the same five obligations directly from their own middleware. Any HTTP client in any language that satisfies these obligations is CALM-compatible. CALM ships reference middleware as working examples of the contract, not as libraries to take a hard dependency on.

---

## Core API

### `POST /v1/sessions`

Creates a session. The workload provides the session ID — CALM treats it as an opaque string. The namespace is resolved from the API key, not from the request body. Accepts an optional `client` identifier (auto-registered on first reference), optional metadata labels, and optional `ttl_minutes`.

```json
{
  "session_id": "pipeline-run-abc-step-2",
  "client": "factory-pipeline",
  "labels": {
    "env": "production",
    "workflow": "report-generator",
    "user": "alice"
  },
  "ttl_minutes": 30
}
```

- `client` — optional. Identifies which workload is creating the session. Auto-registers on first reference if not already known. If omitted, the session attributes to the `default` client.
- `labels` — arbitrary key/value metadata; queryable via the management API.
- `ttl_minutes` — inactivity timeout. CALM clamps to the operator-configured maximum if the workload requests longer (rather than rejecting with 4xx). The clamp choice is deliberate: CALM's consumers are LLM-orchestration glue layers — coding-agent harnesses, pipeline runners, MCP adapters — that typically fall back to no-CALM mode when they see a 4xx on session create. For a context-management service, *absent CALM* is a worse outcome than *degraded CALM* (shorter session than requested). Clamping keeps CALM useful for the work unit; the response echoes the committed value so workloads that do check can detect the clamp. Server emits a WARN per clamp event so operators can see when workloads consistently hit the ceiling and decide whether to raise it.

**Response (201):**

```json
{
  "session_id": "pipeline-run-abc-step-2",
  "namespace": "factory-prod",
  "client": "factory-pipeline",
  "ttl_minutes": 30,
  "created_at": "2026-05-15T14:30:00Z"
}
```

The response echoes the persisted session — `namespace` resolved from the API key, `client` resolved (or auto-registered) from the request, and `ttl_minutes` after operator-ceiling clamping. The workload uses the echoed values to confirm what CALM actually committed.

### `DELETE /v1/sessions/{session_id}`

Tears down a session. All indexed content and events for this session are deleted (cascading via FK). If the workload doesn't call this, inactivity TTL handles cleanup.

**Response (200):**

```json
{
  "deleted_session_id": "pipeline-run-abc-step-2",
  "cascaded": {
    "sources": 12,
    "chunks": 84,
    "events": 47,
    "labels": 3
  }
}
```

The cascade counts let the workload (and any audit logging) record what was removed without a follow-up read.

### `POST /v1/ingest`

The primary endpoint. Takes raw content, chunks it, indexes it, returns a compact representation. The workload puts the compact version into the LLM's context instead of the raw content.

```json
{
  "session_id": "pipeline-run-abc-step-2",
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

```json
{
  "session_id": "pipeline-run-abc-step-2",
  "queries": ["connection timeout", "retry configuration"],
  "source": "web-search-results",
  "limit": 3
}
```

- `source` — optional. Scopes the search to a specific source label.
- `limit` — maximum results per query.

Returns exact indexed text with smart snippets around matching terms. The multi-layer fallback (primary tokenizer → trigram → fuzzy) is internal — the workload just sends a query and gets ranked results.

Search is not scoped by `content_type`. A session that has indexed a mix of prose and code chunks (e.g., a coding-agent run that ingested both API docs and source files) gets results from both tokenization paths in one ranked list — the layer-1 query runs against both the prose and code FTS indexes and the two rankings are fused (see [§7](#7-data-model--storage) for the fusion mechanics). The workload sees a single result list and does not need to know which tokenizer matched.

```json
{
  "results": {
    "connection timeout": [
      {
        "title": "Connection Pool Exhaustion",
        "snippet": "<excerpt around matching terms>",
        "source": "web-search-results",
        "match_layer": "primary"
      }
    ],
    "retry configuration": [
      {
        "title": "Retry Backoff Errors",
        "snippet": "<excerpt around matching terms>",
        "source": "web-search-results",
        "match_layer": "trigram"
      }
    ]
  }
}
```

`match_layer` is one of `primary`, `trigram`, `fuzzy` — indicating which fallback layer produced the match. `primary` covers both the prose and code FTS indexes (which are RRF-fused at layer 1, per [§7](#7-data-model--storage)) — the workload does not see, and does not need to disambiguate, which tokenizer the underlying chunk was indexed with. Operators use `match_layer` to spot quality issues (frequent `fuzzy` matches suggest the indexed vocabulary doesn't match what workloads search for).

### `GET /v1/sessions/{session_id}/sources`

Lists everything indexed in this session.

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

```json
{
  "session_id": "debug-conv-xyz",
  "events": [
    { "type": "tool_invocation", "priority": 3, "data": { "tool_name": "clickhouse_query", "last_input_summary": "SELECT * FROM traces WHERE ..." } },
    { "type": "error_observed", "priority": 2, "data": { "message": "connection pool exhausted", "source": "clickhouse", "exit_code": 1 } }
  ]
}
```

Priority semantics are defined in §4 — workloads classify events into the four-tier scheme so the snapshot endpoint can triage by importance.

### `GET /v1/events/{session_id}`

Reads events for a session. Supports filtering by `type` and `min_priority` via query parameters.

```
GET /v1/events/debug-conv-xyz?type=error_observed,user_decision&min_priority=1
```

The workload can read events before session close to extract anything worth persisting to its own long-term storage.

### `GET /v1/sessions/{session_id}/snapshot`

Returns the session's events ordered by priority and recency, accumulating until a byte budget is reached. Generic event store — CALM does not interpret event content or build a structured state representation (see Decision Log [DL08](#dl08)). Workloads needing structured shapes build them in their own middleware from the returned event stream.

```
GET /v1/sessions/debug-conv-xyz/snapshot?budget_bytes=2048
```

```json
{
  "session_id": "debug-conv-xyz",
  "byte_budget_used": 1893,
  "events": [
    { "type": "user_decision", "priority": 1, "data": {}, "created_at": "..." },
    { "type": "error_observed", "priority": 2, "data": {}, "created_at": "..." }
  ]
}
```

Used by platform hooks (PreCompact, SessionStart) on the coding-agent side, and available to any workload that needs a compressed view of session state.

---

## Management API

### `GET /v1/manage/sessions`

Lists sessions in the API key's namespace. Supports filtering by client and arbitrary labels.

```
GET /v1/manage/sessions?client=factory-pipeline&labels.env=production
```

Returns session IDs, labels, the client they belong to, creation time, last activity time, event counts.

### `DELETE /v1/manage/sessions`

Bulk deletes sessions in the namespace by client or label query. Supports `dry_run=true` to preview without deleting.

```
DELETE /v1/manage/sessions?client=slackbot&labels.env=staging&dry_run=true
→ { "affected_sessions": 47 }

DELETE /v1/manage/sessions?client=slackbot&labels.env=staging
→ {
    "deleted_sessions": 47,
    "cascaded": {
      "sources": 188,
      "chunks": 1240,
      "events": 632,
      "labels": 96
    }
  }
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

Read-only at v1 — operators can see which clients exist and their activity, but per-client policy configuration is deferred as part of MVP scoping. Useful for spotting typo-clients and dead workloads.

### `DELETE /v1/manage/clients/{client}`

Removes a client and all its sessions. Cascading delete through `sessions.client` FK — all the client's sessions, sources, chunks, events, and labels are removed.

```
DELETE /v1/manage/clients/slackbot-old?dry_run=true
→ { "affected_sessions": 12 }

DELETE /v1/manage/clients/slackbot-old
→ {
    "deleted_client": "slackbot-old",
    "deleted_sessions": 12,
    "cascaded": {
      "sources": 48,
      "chunks": 312,
      "events": 188,
      "labels": 24
    }
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

The container for workloads within a namespace. Created lazily: when a session arrives with a `client` field CALM hasn't seen before, the client row auto-registers (see Decision Log [DL01](#dl01)). The `default` client is pre-created at bootstrap so workloads can omit the field.

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

The root entity for everything below. Created by the workload with an opaque string ID, optional `client` identifier, optional metadata labels, optional `ttl_minutes`.

```
sessions
  session_id     TEXT PRIMARY KEY      -- workload-provided, opaque
  namespace      TEXT NOT NULL         -- resolved from API key, server-enforced
  client         TEXT NOT NULL         -- defaults to 'default' if workload omits it
  created_at     TIMESTAMP
  last_activity  TIMESTAMP             -- updated on every request, drives TTL
  ttl_minutes    INTEGER               -- workload-set, clamped to operator ceiling
  FOREIGN KEY (namespace, client) → clients(namespace, name)
  INDEX (namespace, client)
  INDEX (last_activity)                -- supports TTL scanner
```

Every operation that touches a session validates that the session belongs to the requesting namespace. If it doesn't, the response is 404 — not 403; from the caller's perspective, the session doesn't exist. All downstream entities (sources, chunks, events, vocabulary, labels) inherit isolation through the session boundary.

### Session Labels

Key-value metadata attached at session creation. Indexed for management API queries (list by label, delete by label). Semantics are the workload's concern — CALM doesn't interpret them.

```
session_labels
  session_id     TEXT                  -- FK to sessions
  key            TEXT
  value          TEXT
  PRIMARY KEY (session_id, key)
  INDEX (key, value)                   -- supports management API queries
```

### Sources

Tracks what's been ingested into a session. Each ingest call creates or replaces a source (idempotent by label within a session, per invariant #6).

```
sources
  id             INTEGER PRIMARY KEY
  session_id     TEXT                  -- FK to sessions
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

**Layer 2 and beyond.** The `pg_trgm` trigram index over `chunks.content` is a single index serving layer 2 (substring fallback) — no fusion at that layer. Layer 3 (fuzzy correction against `vocabulary`) re-runs the corrected query through layers 1 and 2; results from layer 3 can themselves be RRF-fused at layer 1 if the corrected query matches both content-type indexes.

This is a distinct RRF use from the intent-ordering RRF on ingest (DL05): there, RRF fuses *per-intent rankings* to order the compact summary; here, RRF fuses *per-tokenizer rankings* within a single search query. Same algorithm, different inputs, different purposes.

BM25 field weights: title at 2.0, content at 1.0. Heading matches surface first.

### Vocabulary

Distinct terms extracted from ingested content per session. Powers the distinctive-terms output in the ingest response and the fuzzy correction layer.

```
vocabulary
  session_id     TEXT                  -- FK to sessions
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
  id             INTEGER PRIMARY KEY
  session_id     TEXT                  -- FK to sessions
  type           TEXT                  -- workload-chosen
  priority       INTEGER               -- 1 (critical) to 4 (noise); workload-set
  data           TEXT                  -- JSON payload, workload-structured
  data_hash      TEXT                  -- SHA-256 of (type, data), for dedup
  created_at     TIMESTAMP
  INDEX (session_id, priority, created_at)   -- supports snapshot ordering
```

`namespace` and `client` are not denormalized onto events; they inherit through the `session_id` FK. Cross-session queries that need either dimension JOIN through `sessions`.

Deduplication: before inserting, the last N events in the session (configurable, default 5) are checked for matching `data_hash`. Prevents duplicate events from repeated tool calls against the same resource.

FIFO eviction: max events per session is capped (default 1000). When exceeded, the lowest-priority oldest events are deleted first. P1 events are never evicted while lower-priority events exist.

## Cleanup

Two paths, whichever fires first:

- **Explicit close.** Workload calls `DELETE /v1/sessions/{id}`. Everything under that session — sources, chunks, vocabulary, events, labels — is deleted via cascade. The `clients.last_activity_at` for the session's client is updated.
- **Inactivity TTL.** A background scanner finds sessions where `now() - last_activity > ttl_minutes` and deletes them. Same cascade as explicit close. Catches workloads that crash or disconnect without calling DELETE.

The TTL scan interval is configurable; default 60 seconds.

`clients` rows are not deleted automatically when their last session ends — operators clear them via `DELETE /v1/manage/clients/{client}` when they want to. The `default` client is permanent.

## Future optimization path

The research into semantic search without embeddings identified a high-impact technique: LLM-generated document enrichment at index time (synthetic queries, summaries, key concepts stored as separate boosted FTS fields). This closes 50–70% of the gap to dense neural retrieval with zero query-time overhead.

This is deferred as premature optimization. CALM's typical content — logs, metrics, stack traces, CLI output, structured tool responses — has predictable vocabulary that BM25 with the three-layer fallback handles well. The ephemeral session model also limits the amortization window for enrichment. The schema can accommodate enrichment fields without structural changes if search quality data later justifies it.

---

# 8. Session Lifecycle

Sessions are explicit. Created by the workload, used during the workload's unit of work, torn down when done. CALM does not auto-create sessions — a request against a nonexistent session ID returns 404 (see Decision Log [DL03](#dl03)). This catches misconfigurations early rather than silently creating orphaned sessions.

## Creation

The workload calls `POST /v1/sessions` with a session ID, API key, optional `client` identifier, optional labels, and optional `ttl_minutes`. CALM resolves the namespace from the API key, creates the session (auto-registering the client if new — see Decision Log [DL01](#dl01)), and returns 201.

Example patterns:

- **Automated pipeline:** middleware creates a session when an agent step begins; session ID is constructed from the workflow run ID and step index (e.g., `wf-abc-step-2`). API key is in the pipeline's service config.
- **Internal LLM application:** the application creates a session when a user starts a new conversation; session ID is the conversation ID. API key is in the application's server config.
- **MCP adapter:** the adapter creates a session on startup when the coding agent spawns it; session ID is a generated UUID. API key is in the adapter's config file.

In each case, the namespace API key is configured in the workload's deploy artifact (config file, env var, service account secret).

## Active State

Every request (ingest, search, events) updates `last_activity` on the session. This drives the TTL clock — the inactivity timer resets on every interaction.

During the active phase, content is ingested and indexed, search queries run against the session's indexed content, and events are captured. All operations are scoped to the session ID and validated against the API key's namespace.

## State Reconstruction

When a workload needs to recover state — after platform compaction in a coding-agent session, after a crash-and-replay in an automated pipeline, on resume from a paused conversation — it calls `GET /v1/sessions/{id}/snapshot`.

CALM reads the session's events, sorts them by `(priority asc, created_at desc)` — recall that priority `1` is critical and `4` is noise (HLD §4), so ascending priority means most-important-first — and accumulates into the response until a configurable byte budget is reached. The endpoint returns a generic event list — no merging, no deduplication, no per-event-type synthesis. Workloads that want structured state representations (e.g., "active files" extracted from `file_touched` events, "open decisions" extracted from `user_decision` events) build them in their own middleware from the returned events. See Decision Log [DL08](#dl08).

The snapshot is built on demand from current events, not pre-computed and stored. It's cheap — a database read plus serialization, single-digit milliseconds for typical session sizes.

**Budget overflow.** If higher-priority events alone exceed the budget, CALM still returns the most recent P1 events that fit and sets `budget_exceeded: true` on the response. The workload gets a partial snapshot rather than nothing; it can request a larger budget on retry if its context allows.

**Who calls this and when:**

- **Coding agents:** Platform hooks fire on compaction (PreCompact) and session resume (SessionStart). The hook calls the snapshot endpoint and the MCP adapter injects the result into the refreshed context.
- **Automated pipelines:** Typically not needed; the workflow engine's durable execution handles replay. The snapshot is available if the workload wants it for logging or debugging crash recovery.
- **Internal LLM applications:** Available for session-resume scenarios. If the app supports reconnecting to an earlier conversation, the snapshot provides the state summary as a starting point for the workload's own reconstruction logic.

## Teardown

Two paths, whichever fires first.

**Explicit close.** Workload calls `DELETE /v1/sessions/{id}`. Everything under that session — sources, chunks, vocabulary, events, labels — is deleted via cascade. The workload is done and says so.

**Inactivity TTL.** A background scanner finds sessions where `now() - last_activity > ttl_minutes` and deletes them. Same cascade as explicit close. Catches workloads that crashed, disconnected, or didn't clean up.

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
    DELETE /v1/sessions/{id}     TTL expires
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

### Context savings

The core value proposition. If you can't measure savings, you can't justify the system.

- `calm_ingest_bytes_received` — raw bytes received per ingest call
- `calm_ingest_bytes_returned` — bytes in the compact representation returned to the workload
- `calm_ingest_savings_ratio` — `1 - (returned / received)`, per call
- `calm_session_tokens_saved` — estimated tokens saved per session (cumulative across all ingests in the session)

These are the numbers that answer "is CALM paying for itself."

### Search quality

- `calm_search_hit_rate` — percentage of queries that returned at least one result
- `calm_search_zero_results` — count of queries with no matches
- `calm_search_match_layer` — distribution across `primary`, `trigram`, and `fuzzy`. If `fuzzy` is firing frequently, the indexed vocabulary doesn't match query vocabulary — a signal that the workload's content shape may need a different chunking strategy.
- `calm_search_latency_ms` — per-query latency
- `calm_ingest_intent_coverage` — when intents are provided on ingest, the average fraction of sections in the response with non-empty `matches`. Low coverage suggests workloads are providing intents that don't align with the content vocabulary.

### Session lifecycle

- `calm_sessions_active` — gauge of currently active sessions
- `calm_session_duration_seconds` — histogram of session lifetimes
- `calm_session_events_count` — events captured per session
- `calm_session_cleanup_explicit` — sessions closed by the workload
- `calm_session_cleanup_ttl` — sessions cleaned up by TTL scanner. A high ratio of TTL-to-explicit cleanups suggests workloads are crashing or not closing sessions properly.
- `calm_session_create_ttl_clamped` — sessions whose requested `ttl_minutes` exceeded the operator ceiling and were clamped. A non-trivial rate indicates workloads consistently want longer TTLs than the deployment allows; operators can use this to decide whether to raise `sessions.max_ttl_minutes`. Each clamp also emits a WARN log line with the requested-vs-committed values.

### Answer quality

Cost metrics tell you CALM is saving tokens. These tell you whether the model is still getting what it needs.

- `calm_reingest_rate` — how often the same source label is re-indexed within a session. A workload re-ingesting a source it already indexed is a signal that the compact representation wasn't sufficient.
- `calm_search_after_ingest_rate` — how often a `/v1/search` call follows a `/v1/ingest` on the same source within the same session turn. Expected behavior for iterative workflows; elevated rates on first turns suggest compact summaries aren't landing.
- `calm_snapshot_injection_count` — how often `/v1/sessions/{id}/snapshot` is called. Tracks how frequently session continuity is actually exercised, not just available.
- `calm_intent_zero_match_rate` — when intents are provided, the percentage of ingest calls where every section ended up with an empty `matches` array (no section was addressed by any intent). High rates suggest the intents don't align with the content's vocabulary — a signal to revisit either the intent phrasing or the workload's chunking strategy for that content type.

### Service health

- `calm_request_latency_ms` — per-endpoint, p50/p95/p99
- `calm_request_errors` — per-endpoint, by status code
- `calm_db_query_latency_ms` — database operation latency
- `calm_ttl_scanner_last_run` — timestamp of the last successful scan
- `calm_ttl_scanner_sessions_cleaned` — sessions removed per scan cycle

## Traces

OTel traces for request flows through CALM. Each ingest call is a span that includes: chunking duration, indexing duration, intent search duration (if applicable). Each search call is a span with: query parsing, FTS execution per layer (primary + trigram fallback if needed), snippet extraction.

Traces help diagnose latency — is it the database, the chunking, or the search that's slow? They also make it possible to trace a request from the workload through CALM and back, if the workload propagates trace context in its HTTP headers.

## Structured Logging

JSON-formatted logs to stdout. Each log entry includes: timestamp, level, session ID (when applicable), namespace, client, endpoint, and request-specific fields. No unstructured string messages.

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

## Quality risk

CALM's primary risk is not availability or cost — it's the possibility that filtering content saves tokens while quietly degrading the model's answers. A workload may never notice because the session runs faster and cheaper, but the model missed a critical detail that was filtered out or buried in a low-ranked chunk.

The answer quality metrics above are the detection mechanism. Elevated re-ingest rates, high intent zero-match rates, and frequent search-after-ingest patterns are all signals that CALM is not surfacing the right content. These should be monitored from first production deployment and reviewed alongside workload-side outcome metrics (task completion, retry rates, user corrections) that the workload's owners already track.

---

# 11. Latency Budget

CALM adds a round trip to every tool call it manages. The tool call itself takes 200 ms to 30 seconds. The LLM call after takes 1–5 seconds. CALM sits between these two — its contribution needs to be small enough that it disappears into the noise.

## Targets

**Ingest (no intents):** 50–100 ms in practice. The hottest path — every managed tool call hits it. Involves chunking and indexing into FTS.

**Ingest (with intents):** `base + 30–50 ms per intent` for the per-intent search and RRF aggregation. With 1–3 intents at typical payload sizes, this lands in the 80–250 ms range. The per-intent cost is the same `Search()` call workload-issued `/v1/search` queries make — full three-layer fallback (see [DL05](#dl05)). Linear in intent count, not in content size or vocabulary. The upper end may exceed the workload's 200 ms middleware timeout, in which case the workload falls back to raw context per invariant #1 (see [DL04](#dl04)) — correct behavior, not failure. Workloads needing guaranteed intent ordering should size intent count and payload accordingly.

**Search:** 50–100 ms in practice. Called on follow-up queries against previously indexed content.

**Session create/delete/snapshot:** Not latency-sensitive. Called once or twice per session. Sub-second is fine.

**Workload middleware timeout:** 200 ms default. If CALM doesn't respond within this window, the middleware falls back to raw context (per §9). The 200 ms ceiling gives headroom for database load and large payloads without making the user wait.

## Caching Strategy

Two in-memory caches help CALM stay within budget. Both are process-local — no Redis, no external cache. Multiple CALM instances each have their own. Cache misses just hit the database.

### Session metadata cache

Every request validates the session ID against the namespace. Without caching, that's a database read on every call.

The cache maps session ID → namespace + client + TTL config. LRU with a size cap (e.g., 10,000 entries). Explicitly invalidated when a session is deleted. No time-based TTL — the mapping is correct until the session is closed, so time-based expiry would be arbitrary. Active sessions stay hot naturally because they're accessed on every request. Abandoned sessions drift to the LRU tail and get evicted.

### Search result cache (deferred to v2)

A per-session LRU cache of search results — keyed by `(session_id, query, source)`, invalidated on ingest into the session — was specified for v1 but is **deferred to v2 pending a multi-pod topology decision**.

The gap: cache invalidation works within a single pod. Under multi-pod deployment with round-robin load balancing, ingest may land on pod A while a subsequent search hits pod B; pod B's cache for that session is stale until LRU eviction, and *serves stale results* in the meantime. Unlike the session-metadata cache, search-result staleness doesn't self-heal (the cached result is returned, not re-validated against the DB).

For v1, cold-search latency targets (50–100 ms) are met without the cache — it was a perf optimization for repeat-query patterns, not a load-bearing budget mechanism. Removing it from v1 is the simplest correct option in any deployment topology.

When the cache returns in v2, the most likely shape is: workloads set `X-Session-ID` on body-carrying endpoints, LB hashes on that header to pin a session to a pod, cache invalidation stays in-process. The header-on-API-contract change is the price for the perf gain — worth paying when measurement justifies it.

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
- **Per-namespace rate limiting:** Enforced at a configurable requests/second cap per namespace. A runaway workload (looping pipeline, misconfigured MCP adapter, internal LLM app in an error spiral) cannot flood CALM or saturate the shared database. Exceeding the rate limit returns 429.

---

# 12. Deployment Model

## Deployment topology

CALM pods sit behind a Kubernetes Service (or equivalent load balancer). Any pod handles any request — no session affinity, no leader election, no coordination between pods. Postgres is the shared state.

Scaling horizontally is architecturally supported (stateless service, shared database), but CALM's compute is lightweight — chunking is string splitting, search is a database query, snippet extraction is string slicing. A single pod can comfortably handle thousands of requests per second. The database is the bottleneck long before CALM's compute is.

Each pod runs its own LRU caches (session metadata, search results). If one pod deletes a session, other pods may serve a stale cache hit on the next request — the database query returns empty, the stale entry evicts naturally. Harmless inconsistency, not worth distributed cache invalidation.

TTL scanner runs in every pod with jittered intervals (§9). Concurrent scans produce redundant deletes, not conflicts.

For airgapped or on-prem deployments, the same topology applies — CALM pods deployed alongside Postgres in the operating org's cluster, no external dependencies, no outbound network requirements. API keys and namespace mappings are loaded from a ConfigMap or mounted Secret (see Decision Log [DL10](#dl10)). Workloads reach CALM via cluster-internal DNS.

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

With namespace scoping, every session must be tagged with the correct namespace at creation time. Auto-creation on first ingest would silently create orphaned sessions when a workload sends a typo'd session ID. In an automated pipeline running thousands of iterations per day, that's thousands of orphaned sessions consuming storage until TTL cleans them up.

Explicit creation catches misconfigurations immediately — wrong session ID returns 404 on the first call.

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

**Layer behavior on intent search.** Intent search reuses the same `Search()` implementation as workload-issued `/v1/search` queries — full three-layer fallback: layer 1 RRF fusion across the prose and code FTS indexes, layer 2 trigram fallback via `pg_trgm` if layer 1 underfills the limit, layer 3 Levenshtein correction against the session's `vocabulary` table if layers 1+2 return zero hits. One call signature, one set of observable behaviors, one mental model.

Considered alternative: skip layer 3 for intent search, since intents are workload-provided (typo-free in the common case) and Levenshtein-against-vocabulary scales with session vocabulary size. Rejected because:

- **Two `Search()` semantics is a bug surface.** One semantics for `/v1/search`, another for the intent path — every DAL caller has to decide which path to use, and every future feature that calls `Search()` from a new code path has to think about which it wants. Single semantics is simpler and harder to misuse.
- **No measurement justifies the carve-out yet.** v1's latency budget (§11) accommodates the full three-layer cost via the per-intent budget increment. A code-path split today is speculative optimization without data.
- **Marginal value of layer 3 remains for vocabulary-adjacent terms.** Intent term `timeout` against chunk vocabulary `timeouts` / `timeouted`, where porter stemming doesn't bridge and trigram similarity falls below threshold, can still be caught by layer 3. Not common, but not zero.

The optimization remains available as a one-line `Search()` flag follow-up if measurement shows layer 3 cost dominating intent-ingest latency in production. v1 ships the simpler path; perf tuning is a follow-up exercise grounded in measurement, not pre-stamped in the architecture.

### DL06

**Tokenization branches on code vs prose**

CALM dispatches tokenization on the chunk's `content_type`: prose-shaped chunks are indexed with porter stemming over standard unicode tokenization (helpful for morphological matches like "caching" → "cached"); code-shaped chunks are indexed with identifier-preserving tokenization and no stemming, so identifiers like `getUserById` survive as whole tokens.

The alternative — uniform porter stemming for all content — was rejected. Porter on code-identifier-heavy content silently degrades exact-match queries; trigram fallback partially compensates but not fully (multi-token code queries hit ranking issues). A second alternative — uniform identifier-preserving tokenization with no stemming for all content — was also rejected: prose-heavy workloads (eval harnesses, slackbots, factory tool outputs) lose morphological recall when the stemmer is removed. Branching gives both workload shapes the right tokenizer.

The architectural cost is two FTS indexes (one per tokenizer, with chunks dispatched at insert time based on `content_type`) and rank fusion at search time across both indexes. Adding more format-specific tokenizations later (e.g., a structured-data tokenizer for `csv` or `json` chunks) is a concrete code change in the dispatch path, not a plugin surface — speculative-abstraction architecture is explicitly avoided here.

`content_type` is determined per chunk by the chunker — not by the workload as a single label for the whole ingest. The workload supplies an ingest-level default (the MCP adapter passes it based on tool patterns; other workloads pass it explicitly); the chunker overrides per chunk when format signals demand it (a fenced code block inside a markdown ingest is `code`; a stacktrace frame is `code`; otherwise the default applies). This per-chunk granularity is load-bearing for mixed-content ingests — markdown with embedded code blocks, log output containing JSON payloads, etc. — where the right tokenizer differs across chunks of the same ingest. Unknown values are treated as `prose`. CALM does not maintain content-detection logic beyond the per-format dispatch in the chunker.

### DL07

**FTS with BM25 over vector/embedding search**

Vector search requires an embedding model — either a remote API (latency, cost, availability dependency) or a local model (memory, deployment complexity). CALM's typical content is technical — logs, stack traces, metrics, CLI output, structured tool responses — with predictable vocabulary. BM25 with porter stemming, trigram substring matching, and fuzzy correction handles this well. The semantic gap that embeddings solve ("authentication failures" matching "login errors") is a natural-language problem that the three-layer fallback partially addresses, and the ephemeral session model limits the payoff of an embedding dependency.

Research identified a middle path: LLM-generated document enrichment at index time, which closes 50-70% of the gap to neural retrieval without an embedding dependency. This is deferred as premature optimization but the schema can accommodate it without structural changes if search quality data justifies it later.

### DL08

**Snapshot is a generic event store; pluggable strategies deferred**

CALM's snapshot endpoint returns events ordered by priority and recency, accumulating until a byte budget is reached. It does not interpret event content, does not build a structured state representation, does not branch on event type or category. Workloads needing structured state shapes build them in their own middleware.

The alternative — a built-in snapshot strategy shaped around a particular workload (e.g., coding-agent state: active files, errors, decisions, tasks) — was rejected. Under team-first deployment with heterogeneous workloads, no single state shape generalizes. A slackbot's snapshot is about active threads; an eval harness's about the current run; a custom internal app may not need a snapshot at all. Building one workload's state model into CALM's snapshot privileges that workload's shape and forces others to either accept the wrong shape or build their own state model anyway.

A pluggable strategy mechanism (operator registers per-client snapshot logic; CALM dispatches at snapshot time) is deferred as part of MVP scoping. If multiple workloads in a single deployment need the same structured shape, that's the signal to build it. The current design discipline is *expressiveness without commitment*: the event schema is rich enough that any future strategy could read it, and the HTTP response shape is generic enough to be extended without breaking existing workloads — but no interface, plugin registry, or per-client `snapshot_strategy` column exists today.

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

The same dialect is reusable for any future operator-facing secret-bearing config (Postgres DSN, TLS certs, OTel exporter tokens, etc.) via `internal/secrets`.

Rejected dialect entries:

- **`[secret:<provider-key>]` (direct Vault / AWS-SM / GCP-SM / Azure-KV fetch).** Would require linking provider SDKs (~50–100 MB each plus per-provider auth, region, retry plumbing), duplicating what platform tools already do better. The static-standalone-binary property loses meaning. Operators bridge to their secret store via env or file rendering using existing tooling.
- **Bare literals (no brackets).** Considered for ergonomic concision, rejected for grammar consistency. Bracket-everywhere costs ~4 extra characters per literal in exchange for unambiguous parsing and uniform eye-scanning.

### DL11

**Namespace isolation over tenant model**

"Tenant" implies a specific organizational hierarchy. CALM doesn't interpret what a namespace means. It partitions on it. A namespace can represent an org, a team, a user, or a deployment environment — the deployer decides. Same mechanism at any granularity, same as Kubernetes namespaces.

Per-user isolation within a namespace (e.g., user foo can't see user bar's debug sessions) is the workload's responsibility. CALM prevents cross-namespace leakage. Within a namespace, the workload controls access by managing which session IDs are visible to which users.

### DL12

**Postgres as sole storage backend**

The DAL interface exists for unit-test mocking via mockery, not for portability across backends. There is one production storage backend: Postgres.

Rejected alternatives:

- **SQLite** — limited FTS5 (no real BM25/IDF), single-writer, no `pg_trgm` equivalent for trigram indexing, no `fuzzystrmatch` for Levenshtein.
- **MySQL / MariaDB** — InnoDB FTS isn't BM25 and isn't configurable. No trigram-index equivalent of `pg_trgm`. No native Levenshtein. No partial indexes (which CALM uses to route prose vs. code chunks into separate FTS indexes per [DL06](#dl06)). JSON support is less mature than JSONB. Delivering the three-layer search architecture would require bolting Elasticsearch or Tantivy on the side, breaking the "single store, no external search system" property.
- **Portable DAL with multiple backend impls** — false flexibility. Doubles maintenance surface (every DAL method needs N implementations + parity tests) for a portability story with no concrete demand.
- **Embedded KV (BoltDB / BadgerDB)** — no full-text search, no relational queries for the management API, no way to express FTS partial indexes.
- **Distributed SQL (CockroachDB, TiDB)** — Postgres-wire-compatible but lacks the BM25 extensions; operational complexity exceeds what the use case demands.

Why Postgres specifically: BM25 via `pg_search` or `pg_textsearch` ([DL07](#dl07) rationale), `pg_trgm` for trigram fallback, `fuzzystrmatch` for Levenshtein, JSONB for opaque event data, partial indexes for the prose/code routing, atomic transactions for re-index (invariant #6). Single backend, no external search system, no exotic dependencies.

Re-introducing a portability layer requires HLD discussion before code lands.

### DL13

**Session-scoped storage, not cross-session memory**

CALM manages what enters context during a session. What the agent remembers across sessions is a different system — the agent's long-term memory layer. The industry consensus (MemGPT, Mem0, LangMem, AgeMem) draws the same boundary: short-term/working memory is session-scoped, long-term memory is a separate persistent store. CALM is the working memory manager. Cross-session knowledge is the agent's responsibility.

This keeps CALM simple. No promotion mechanisms, no corpus lifecycle management, no deciding what's worth keeping. Content expires with the session.

---
