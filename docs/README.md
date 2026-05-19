# CALM — Executive Summary

---

## The problem you probably haven't noticed

Every LLM API charges per token on input, and the entire context window is sent as input on every turn. So anything sitting in context — whether the model needs it or not — gets billed again and again until the platform compacts the conversation.

Today, when a team's LLM workloads call tools (pull logs, fetch docs, query APIs, read files), the raw output goes straight into the context window. A 50 KB log dump sits there for 10–15 turns, re-charged each time, before compaction clears it. There is no filtering, no relevance gating, no observability. The context window is treated as a pipe, not a managed resource.

This compounds in two directions at once. **Cost** scales with data volume rather than with value: real tool outputs (logs, API responses, query results, file dumps) typically compress by an order of magnitude into a compact representation that preserves what the model actually needs — section titles, preview lines, a vocabulary of distinctive indexed terms. **Quality** degrades structurally: LLMs attend poorly to information in the middle of long contexts ([Liu et al., Stanford 2023](https://arxiv.org/abs/2307.03172)). As windows fill with stale tool output, answers get worse. This is structural to how transformers work, not a bug that providers will fix.

## What this costs

The cost compounds across workloads. A team running an internal slackbot, an eval harness in CI, a few developers' coding agents, and any number of internal LLM applications through one stack multiplies the unfiltered-tool-output problem across every workload. A single misbehaving client — say, an internal copilot ingesting verbose API responses — can dominate the team's monthly API bill on its own.

For self-hosted models, the unit of cost is different but the loss is the same: a model attending to 45 KB of stale output produces worse answers than one attending to a few KB of relevant content, regardless of who runs the inference.

## What CALM is

**CALM (Context Abstraction Layer for Models)** is a small standalone HTTP service that a team deploys alongside its AI workloads. It sits *beside* the LLM call, never between, and exposes three primitives over one HTTP API:

1. **Ingest** — workloads hand CALM raw tool output; CALM chunks and indexes it; CALM returns a compact representation (section titles, preview lines, distinctive vocabulary terms). Only the compact representation goes into the LLM context. Raw content stays in the service.
2. **Search** — the LLM (via a search tool the workload's middleware exposes) drills into the indexed content on demand. BM25 ranking with a three-layer fallback (primary tokenizer → trigram → fuzzy correction). Returns exact-text snippets within byte budget, never summaries.
3. **Session state** — captures workload-defined structured events during a session — typically things like files touched, errors observed, decisions, tool invocations — and returns them ordered by priority within a byte budget when state reconstruction is needed. CALM stores and serves but does not interpret event content.

The workload's middleware orchestrates calls; CALM is never in the LLM request path. **If CALM is down, the workload falls back to raw output — the LLM call always works.** Invariant #1 of the design: never makes things worse.

One HTTP API serves any LLM application a team runs:

- **Internal LLM apps** — slackbots, internal copilots, query-answering assistants, debug bots.
- **Automated agent pipelines** — batch workflows, eval harnesses, scheduled report generators.
- **Coding agents** (Claude Code, Cursor, Codex) — via a thin MCP adapter binary that CALM ships.

Those are illustrative, not architectural categories. Any LLM application that speaks HTTP and authenticates with a namespace credential is a valid CALM workload.

## What changes for the reader

- **Token spend drops by an order of magnitude** on tool-heavy sessions. Compact representations replace raw payloads in the LLM context.
- **Workloads stop hitting the "max N tool calls" prompt ceiling.** Today many automated pipelines throttle themselves in *what they can do* because there's no way to control *what enters context*. That ceiling becomes unnecessary.
- **Interactive sessions survive longer before compaction.** When compaction does fire, state is reconstructable from the captured event stream — the model doesn't forget which files were edited, what errors occurred, what the user already decided.
- **Context quality becomes observable.** Re-ingest rate, intent zero-match rate, search-match-layer distribution, snapshot injection frequency. Today, when a session degrades, the team can't tell if it's a model problem or a context problem. CALM exposes the signals.

## Measurable quality is part of the product, not an afterthought

A context-management layer's worst failure mode is invisible to the workload: compression saves tokens while quietly degrading answer quality, and the team never notices because the session is faster and cheaper. CALM treats this as a first-class risk and instruments self-assessment. Every session exposes signals (the metrics above) that let operators detect quiet degradation alongside their existing workload-side outcome metrics (task completion, retry rates, user corrections). The value claim is verifiable, not asserted.

## What this isn't

The shape pattern-matches to several other things it deliberately is not:

- **Not RAG.** No document ingestion, no embedding pipeline, no persistent corpus. Content is indexed ephemerally during a session and expires when the session ends.
- **Not a proxy.** Doesn't sit on the LLM call path. Doesn't make LLM calls. Doesn't decide what the agent does next.
- **Not a prompt engineering tool.** Manages what data is available for context; doesn't touch prompts.
- **Not cross-session memory.** Session-scoped storage with TTL cleanup. Long-term agent memory is a separate system with different invariants.
- **Not a replacement for compaction.** Compaction is the LLM platform's responsibility. CALM reduces how often it fires and how much state is lost when it does.
- **Not for solo install.** CALM is shared team infrastructure. Individual developers running it solo on their laptop are out of scope — that case is well-served by simpler tools.

## The ask

Read the [HLD](HLD.md) if any of the above is surprising or unwelcome — the reasoning lives there, including the Decision Log (`DL01`–`DL13`) that captures the architectural forks and the alternatives rejected. The HLD is a living spec; pushback is welcome.

Concrete contributions of value, in rough order of usefulness:

- A candidate workload (internal LLM app, eval harness, coding-agent integration) willing to integrate and report what works and what doesn't.
- Visibility into existing token-spend dashboards so the compression claim can be validated against measured data rather than asserted.
- Review of the API contract at [`api/openapi.yaml`](api/openapi.yaml) and the architectural forks in HLD's Decision Log.
