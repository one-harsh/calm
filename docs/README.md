# CALM — Executive Summary

---

## The problem you probably haven't noticed

Every LLM API charges per token on input, and the entire context window is sent as input on every turn. So anything sitting in context — whether the model needs it or not — gets billed again and again until the platform compacts the conversation.

When a team's LLM workloads call tools (logs, docs, API queries), the raw output goes straight into the context window. A 50 KB log dump sits there for 10–15 turns, re-billed each time, before compaction clears it. No filtering, no relevance gating, no observability — the context window is a pipe, not a managed resource.

This compounds in two directions. **Cost** scales with data volume rather than value: real tool outputs typically compress by an order of magnitude without losing what the model needs. **Quality** degrades structurally: LLMs attend poorly to information in the middle of long contexts ([Liu et al., Stanford 2023](https://arxiv.org/abs/2307.03172)), so as context fills with stale output, answers get worse. That's how transformers work — not something providers will fix.

## What this costs

The cost compounds across workloads. A team running an internal slackbot, an eval harness, and a handful of coding agents through one stack multiplies the problem across every workload. A single misbehaving client — say, an internal copilot ingesting verbose API responses — can dominate the team's monthly API bill on its own.

For self-hosted models, the unit of cost is different but the loss is the same: a model attending to 45 KB of stale output produces worse answers than one attending to a few KB of relevant content.

## What CALM is

**CALM (Context Abstraction Layer for Models)** is a small standalone HTTP service that a team deploys alongside its AI workloads. It sits *beside* the LLM call, never between, and exposes three primitives over one HTTP API:

1. **Ingest** — workloads hand CALM raw tool output; CALM chunks and indexes it; CALM returns a compact representation (section titles, preview lines, distinctive vocabulary terms). Only the compact representation goes into the LLM context; raw content stays in the service.
2. **Search** — the LLM (via a search tool the workload's middleware exposes) drills into the indexed content on demand. Returns exact-text snippets ranked by relevance, within a byte budget, never paraphrased.
3. **Session state** — captures workload-defined structured events during a session (files touched, errors observed, decisions made) and returns them ordered by priority within a byte budget when state needs reconstruction. CALM stores and serves; it doesn't interpret event content.

The workload's middleware orchestrates calls; CALM is never in the LLM request path. **If CALM is down, the workload falls back to raw output — the LLM call always works.** Invariant #1: never make things worse.

Outcomes close the loop. Workloads report back what eventually happened with each CALM-supplied piece of context, and CALM joins those verdicts to the specific call that produced them — so quality is attributable to call shapes, not just measured in aggregate.

One HTTP API serves any LLM application a team runs:

- **Internal LLM apps** — slackbots, internal copilots, query-answering assistants.
- **Automated agent pipelines** — batch workflows, eval harnesses, scheduled report generators.
- **Coding agents** (Claude Code, Cursor, Codex) — via a thin MCP adapter binary CALM ships.

Those are illustrative, not architectural categories. Any LLM application that speaks HTTP and authenticates with a namespace credential is a valid CALM workload.

## What changes for the reader

- **Token spend drops by an order of magnitude** on tool-heavy sessions.
- **The "max N tool calls" ceiling becomes unnecessary.** Pipelines throttle their own tool use today because raw output overruns context; controlling what enters context removes the ceiling.
- **Interactive sessions survive longer before compaction.** When it does fire, state is reconstructable from the captured event stream — the model remembers what it did and what was decided.
- **Context quality becomes observable.** When a session degrades today, teams can't tell if the model is wrong for the task or the context is polluted. CALM exposes the signals to tell them apart.

## Measurable quality

A context-management layer's worst failure mode is invisible to the workload: compression saves tokens while quietly degrading answer quality, and the team never notices because the session is faster and cheaper. CALM treats this as a first-class risk — the outcome-attribution loop above is the mechanism that catches it. Degradation gets surfaced and attributed, not buried inside an end-to-end pass rate. The value claim is verifiable, not asserted.

## What this isn't

The shape pattern-matches to several other things it deliberately is not:

- **Not RAG.** No document ingestion, no embedding pipeline, no persistent corpus. Content is indexed ephemerally during a session and expires when the session ends.
- **Not a proxy.** Doesn't sit on the LLM call path; doesn't make LLM calls; doesn't decide what the agent does next.
- **Not a prompt engineering tool.** Manages what data is available for context; doesn't touch prompts.
- **Not cross-session memory.** Session-scoped storage with TTL cleanup; long-term agent memory is a different system.
- **Not a replacement for compaction.** Compaction is the LLM platform's responsibility; CALM reduces how often it fires and how much state is lost when it does.
- **Not for solo install.** CALM is shared team infrastructure; the solo-developer case is well-served by simpler tools.

## The ask

Read the [HLD](HLD.md) if any of the above is surprising or unwelcome — the reasoning lives there, including the Decision Log (`DL01`–`DL15`) that captures the architectural forks and the alternatives rejected. The HLD is a living spec; pushback is welcome.

Concrete contributions of value, in rough order of usefulness:

- A candidate workload (internal LLM app, eval harness, coding-agent integration) willing to integrate and report what works and what doesn't.
- Visibility into existing token-spend dashboards so the compression claim can be validated against measured data rather than asserted.
- Review of the API contract at [`api/openapi.yaml`](api/openapi.yaml) and the architectural forks in HLD's Decision Log.
