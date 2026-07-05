<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# MCP Adapter - Design Contract

This is CALM's MCP adapter contract. It sits between `LABELING.md` (source-label grammar and event extraction) and the HLD (workload-agnostic, no MCP-specific surface). Read it after the HLD when implementing or evolving the MCP adapter.

# 1. Purpose & Boundary

The adapter turns local coding-agent actions into CALM-managed context. An MCP host calls one of the adapter's tools; the adapter runs the underlying local action (file read, shell command, git operation, edit), captures the output to a CALM session, and returns task-facing text. The agent later searches the captured material through the same surface.

This is one CALM workload, not the universal shape of CALM integration. Server-side workloads — Slackbots, debugging agents, eval harnesses — sit closer to their own tool-call boundary and integrate more thinly through CALM's HTTP API directly. The adapter solves the hardest case: a coding-agent host where CALM cannot see the host's native tools, so the adapter has to expose its own.

The adapter owns the MCP protocol surface, local action execution, capture identity, response presentation, degraded-state signaling, and event emission for coding-agent sessions.

It does not own CALM's namespace/session security model, indexing semantics, feedback model, or storage lifecycle — those live in the HLD. It does not sandbox local execution either; commands run on the developer's machine with that process's ordinary permissions (see DL02).

Shell command capture is the long-tail substrate, not the core surface. High-frequency coding-agent actions get structured tools; shell wrapping is the fallback for everything else.

# 2. Design Invariants

Seven rules. A new tool, response-shape change, lifecycle change, or host integration may pick its own mechanics — these properties hold across them.

**1. Never worse for local actions** (`never-worse`). CALM unavailable? The local result still returns. CALM slow? Same. CALM rejecting? Same. Worst case is lost capture and higher context cost, never a blocked tool call.

**2. Stable capture identity** (`idempotent-indexing`). Captured outputs need identities that avoid silent collision between semantically distinct content. Two captures of distinct content under the same source label silently destroy history; `LABELING.md` owns the grammar that prevents it.

**3. Session and namespace isolation in-process** (`namespace-isolation`, `session-isolation`). Every CALM session's credentials and capture state are isolated within the MCP process. Session tokens never appear in logs, tool text, or metadata. Capture handles, derived events, and search results never cross session or namespace boundaries.

**4. Honest capture continuity.** The adapter does not imply that prior captures remain searchable after their CALM session boundary is gone. Continuity breaks surface reactively — when the agent reaches for content from the prior session, it gets a clear staleness signal, not empty results from the new session.

**5. Honest mutation surfacing.** The tool surface honestly conveys each tool's mutation intent: tools that make a non-mutating promise signal as such; tools that may mutate signal explicitly; tools whose mutation status depends entirely on agent-supplied inputs make no read/mutate promise. This is a declarative consumer-trust contract about adapter INTENT, not a sandboxing claim — local execution remains unsandboxed, and developer-configured hooks, aliases, or extensions that mutate the workspace through nominally-inspection commands are outside what the adapter can detect or enforce against.

**6. Response-first events** (`never-worse`). Event derivation and emission are best-effort. They never determine or delay the user-visible tool result.

**7. Net context savings.** When the adapter returns a local action's output to the agent, the response net-reduces context cost relative to the raw output on the median call. Telemetry-class additions never belong in visible text — they belong in OTel emission.

# 3. Tool Surface

The adapter exposes tools at the level of agent intent, not local implementation mechanics. A tool earns its slot when the action is common in coding-agent work, has a stable enough capture identity, benefits from search later, and would lose useful intent if forced through a generic shell command.

Five groups in the durable surface. Mutation intent reaches hosts and agents through MCP tool descriptions and host-supported annotations (e.g., `readOnlyHint`). The adapter signals; hosts and users own the trust decisions that follow.

## Structured Inspection

Workspace-read-only by contract. These tools use local subprocesses internally, but their exposed behavior is inspection — no intentional mutation of the working directory.

| Tool | Purpose | Capture shape |
|---|---|---|
| `calm_read_file` | Read file content by path and optional range. The range shapes presentation only; capture is always the full file. | Stable latest source for the path; history only when policy requires it. |
| `calm_list_dir` | List directory entries. | Stable latest source for the directory. |
| `calm_grep` | Search local files and capture matching lines. | Stable latest source for pattern and path scope when the identity is safe. |
| `calm_git_status` | Inspect working tree and index state. | Latest plus history — status changes over time and snapshots matter. |
| `calm_git_diff` | Inspect patch content for review or continuation. | Latest plus history — diffs are mutable and individual review snapshots matter. |

Coding-agent sessions repeatedly perform file, search, and Git inspection. Routing those actions through a shell string forces the adapter to parse intent back out of lossy text and makes the agent choose between native ergonomics and CALM capture.

The structured-git surface covers `diff` and `status` only. Both have constrained input shapes (refs for diff, no operands for status), natural single-identity-per-invocation, and high-frequency use in coding-agent work. Other git read operations — `blame`, `log`, `show` — fall to `calm_run_command` because their richer parameter space (line ranges, time / author / path filters, ref selection variants) means many semantically-distinct invocations that don't map cleanly to one structured tool's typed-args surface. They remain capturable through shell-substrate labeling per `LABELING.md`. New structured-git tools earn their slot when their operand model is demonstrated, not by anticipation.

## Structured Editing

Explicit workspace mutation. Mutation intent signaled in both name and description.

| Tool | Purpose | Capture shape |
|---|---|---|
| `calm_edit_file` | Apply an `old_string` → `new_string` exact-match edit. | Dual mode: latest under `calm:v1:file:read:<path>` (replace), history under `calm:v1:file:edit:<path>#<seq>` (coexist), `file_touched` event with diff. |
| `calm_write_file` | Write full content to a file (new file or total replacement). | Same dual + event shape as `calm_edit_file`. |

The adapter's canonical read surface makes host-native write tools that depend on host-native reads unusable; see AD04. The adapter's write surface keeps the read/write loop coherent and ensures every edit lands in CALM as a captured artifact and a `file_touched` event — closing the snapshot-reconstruction gap that host-native edits leave open.

## Arbitrary Execution

`calm_run_command` is the long-tail local execution surface — build/test runners, pipelines, ad-hoc debugging commands, anything the adapter has not learned to model.

Because `calm_run_command` may mutate local state depending on what the agent runs, hosts and users treat it as a shell surface. Its value is never-worse capture around arbitrary execution, not a read-only trust posture.

Source labels follow `LABELING.md`'s grammar; opaque labels in the shell-substrate long tail are inherent. A `label_hint` parameter is not part of its surface — the structured-tool surface is the design-level answer.

## Retrieval

`calm_search` is the single retrieval primitive. With queries, it returns ranked matches across captured outputs. With no queries and a source scope, it returns the source's chunks in document order — for sequential reread of captured content. Optionally scoped to a source label.

Scoped search covers three patterns: discovery ("find anything matching X across the session"), revisit ("find Y in the captured output of source Z"), and reread (walk the captured output of source Z in original order). For shell output, build logs, and other captured-but-not-locally-re-readable content, the agent chooses the pattern by what it supplies. No dedicated recall tool; see AD01.

The adapter takes CALM's per-namespace default allocator (per HLD) and does not expose an allocator-variant knob to the agent — workload-side ranking-strategy choice would add tool-surface complexity without a clear agent-side use case.

## Context Health

`calm_report_outcome` lets a host report a bounded outcome for a prior CALM-backed tool result. Input is intentionally closed: an adapter-issued feedback ref plus one of CALM's outcome values (`success`, `retry`, or `degraded`). No free-form explanation field.

Any adapter result backed by a CALM value-producing call may expose a feedback ref; the agent chooses whether to submit feedback for any such call.

# 4. Capture & Presentation

## Identities

The adapter uses two identities. Don't blur them.

**Source label.** CALM's server-side source identity, fused with a per-call staleness suffix. Format `<base>[#<seq>]@<token>` per `LABELING.md` — e.g., `calm:v1:file:read:foo.go@a3f2k6`. The base portion (`calm:v1:file:read:foo.go`) is the CALM-side addressing key, operator-visible. The `@<token>` suffix is a session-scoped local-validation marker minted per invocation. Stale or cross-session tokens return a clear staleness signal rather than empty results from the current session. See AD02.

**Feedback ref.** The adapter-facing outcome handle for `calm_report_outcome`. Resolves to a CALM correlation ID. Opaque to the agent; subject to the feedback window enforced by CALM.

## Presentation

Two modes:

**Inline mode** returns the captured content itself in visible text, with minimal framing. Used when the output is small enough that summary + scoped-search would be a net cost.

**Summary mode** returns a task-facing summary plus the fused source label in visible text. Used when the output is large enough that summary + scoped-search beats raw output. The source label is mandatory — without it, the agent has no addressable way back to the underlying captured content.

Mode-selection thresholds are implementer policy, tunable via dogfooding and benchmarks without changing the contract. The `Net context savings` invariant binds the implementer to net-saving on the median call.

# 5. Lifecycle & Failure Model

Two lifecycles run in parallel:

**MCP process lifecycle.** The stdio child process the host binds as an MCP server.

**CALM session lifecycle.** The logical CALM session used for capture, search, events, and correlation.

MCP initialize succeeds whenever the adapter can serve local tool semantics. CALM registration and session creation are attempted during initialize, but CALM availability does not decide whether the host can bind the adapter's local tools.

If no CALM session has ever existed in the live process, the adapter may create one later and begin capture. Any local outputs produced before that point were not captured; the first successful transition into capture-active state surfaces visibly enough to diagnose.

If an established CALM session is lost, expired, or deleted, the adapter creates a replacement session — see AD03. Prior captures are not searchable from the new session unless explicitly re-captured. `calm_search` against a source label from the prior session fails clearly, never returning empty results from the current session — the per-call validation suffix per `LABELING.md` is what makes this detection local.

## Failure Behavior

Failure shape depends on tool class.

**Action/capture tools** return the local result when local work succeeds but CALM capture fails. Visible text states the degraded capture state and reason in agent-readable phrasing; OTel emission records the same facts for operator slicing.

**Retrieval-only tools** cannot produce correct search results without CALM state. They return a visible degraded error when the CALM backend is unavailable. Stale-source-label behavior is governed by AD02; session loss by AD03.

**Event emission** is best-effort and off the response path.

Host-side process death is outside the adapter's full control. If the stdio MCP child dies, most hosts give that dead process no protocol-level way to rebind tools inside the already-running conversation. The adapter is responsible for making live-process CALM degradation visible and recoverable; host process rebinding is not portable unless the host exposes that lifecycle.

Cross-process detection — distinguishing a freshly-bound adapter from a continuation of a prior one — is not surfaced as a first-class signal. The per-call degradation phrasing plus stale-source-label errors on source-scoped `calm_search` cover the actionable cases: a fresh process with no prior context degrades retrieval cleanly; new captures succeed normally.

## Workspace Binding

Configured at adapter startup — explicit configuration (one or more workspace roots) or discovery from the MCP host when the host exposes its workspace surface. When the adapter binds a single workspace, source labels omit the WorkspaceID segment per `LABELING.md` grammar; the common case stays clean.

When the adapter binds multiple workspaces — a workflow observed in practice for monorepo-adjacent sessions and agents that switch repos within one conversation — source labels include the WorkspaceID segment to disambiguate collisions between same-workspace-relative-path captures across distinct roots. Paths outside every registered workspace root fall back to `coexist` mode per `LABELING.md`'s escape-path rule.

Workspaces are fixed at session start; mid-session workspace addition is not supported in this contract.

# 6. Labeling & Events

`LABELING.md` is the canonical source-label and event-extraction contract. This document owns the broader MCP surface; `LABELING.md` owns the grammar that maps adapter actions to CALM sources and event records.

The broader adapter surface changes how labels are produced, not why they exist. Structured tools feed labeling from typed arguments — file path, directory path, grep pattern and scope, Git operation and ref selection. They don't round-trip through shell-string parsing to recover intent the tool already knows.

`calm_run_command` uses best-effort shell-command extraction for the long tail. When extraction finds a stable semantic identity, it captures latest or latest-plus-history per the labeling contract. When it can't, it preserves invocation history rather than overwriting a misleading latest source.

Events derive from the same action facts as labels, finalized once ingest outcomes are known. Cross-links point only at sources that actually persisted. Event emission is best-effort and response-first — failed or slow event writes don't change the tool result.

Keeping labeling separate from the MCP surface lets another integration reuse the durable parts (stable source labels, latest/history policy, event cross-links) without copying MCP-specific mechanics (stdio lifecycle, tool descriptions, shell-command parsing).

# 7. Observability & Context Health

Context health is an operational fact, not an autonomous judgement about whether the model has enough context. The adapter reports what it can know: whether capture was active, whether a response was degraded, whether a retrieval result came from the current session, whether events were derived or queued, whether a result can accept feedback.

## Output Surface Structure

The adapter's output splits into two surfaces — visible text the agent reads (and the host renders in UI), and OTel emission the operator consumes (host-independent, zero context cost).

**Visible text.** What the agent reads and the host renders. Always in model context. Carries:

- Task-facing summaries (summary mode) or captured content (inline mode).
- The fused source label for captured output, in the recall hint — addressable by the agent in a follow-up `calm_search`.
- `feedback_ref` when the call backed a CALM feedback-eligible operation — addressable by the agent in a follow-up `calm_report_outcome`.
- Degradation phrasing when the call ran in a degraded state. Phrasing is stable and reason-specific so the agent can branch on the reason:
  - `CALM degraded — calm_unreachable. Capture and search may fail; local result is shown.`
  - `CALM degraded — auth_failed. CALM credentials rejected; capture and feedback are disabled for this conversation.`
  - `CALM degraded — session_lost. The prior session expired or was replaced; references to prior captures will fail.`
  - `CALM degraded — capture_failed. Local action ran; CALM did not index the output.`
  - `CALM degraded — capture_partial. Some captured sources were indexed; others were not.`
  - `CALM degraded — feedback_window_expired. The feedback window for this reference has closed.`

  Each phrasing keeps the cost bounded (one short sentence per call) while giving the agent enough specificity to choose a next move that differs by reason. New degradation reasons get added as additional modes are characterized; each addition is a deliberate change, not a silent extension.

The `Net context savings` invariant binds visible-text framing tight: telemetry-class facts (per-call byte counters, timings, mode-decision distribution) never appear here.

**OTel emission.** Adapter-resident metrics and structured logs emitted alongside CALM's OTel surface. Never reaches the MCP wire — zero context cost by construction, host-independent. Carries:

- Per-call measurement: `adapter.response.visible_bytes`, `adapter.response.raw_bytes`, `adapter.call.duration_ms`, `adapter.presentation.mode` (inline vs summary distribution). Metric names follow the dotted-schema convention; the exporter converts `.` → `_` at emission.
- Structured forms of the same per-call facts that surface in visible-text degradation phrasing — `captured` (boolean), `degraded` (boolean), `degraded_reason` (closed enum matching the visible-text values), source identity, and CALM's `correlation_id` for joining adapter output to CALM-side logs.

Operator slicing keys on the `client` identifier the adapter registers at startup per HLD's integration contract. Granularity — per agent host, per developer, per team installation — is operator policy.

This is the surface that makes the `Net context savings` invariant operationally checkable. Without OTel emission, the invariant would either be unenforceable (no measurement) or self-defeating (measurement riding in visible text adds to the cost it's measuring).

## Capability Discovery

Capability discovery starts at the tool boundary. Tool names, descriptions, and schemas make mutation intent, capture behavior, retrieval behavior, and feedback support clear enough for a host, model, or user to reason about allow-listing and approval. Visible-text degradation phrasing carries per-call diagnosis the agent acts on; OTel emission carries the structured forms operators slice on.

## Feedback & Outcome Reporting

CALM owns the feedback API and the long-term interpretation of those signals. The adapter owns the integration surface: when a CALM value-producing call has a correlation id, the adapter exposes an opaque `feedback_ref` the host can later pass to `calm_report_outcome`. The report tool accepts only bounded outcomes — `success`, `retry`, or `degraded`. No free-form explanation field. The agent decides whether and when to submit feedback against any exposed `feedback_ref`; the adapter doesn't steer or gate.

The adapter doesn't infer human intent from the mere fact that `calm_report_outcome` was invoked. Richer provenance modeling — distinguishing whether a feedback signal is model-declared, user-authored, user-approved, host-verified, or externally-verified — is a CALM-side concern. Without CALM accepting, persisting, and interpreting provenance, the adapter can't meaningfully expose it; it records only the bounded outcome against a known result handle.

# 8. Adapter Decision Log

Settled adapter decisions and the reasoning behind them. Captured here so the design isn't relitigated.

### AD01

**No Dedicated Recall Tool**

`calm_search` is the single retrieval primitive. No dedicated recall tool. Historical lookup of captured content goes through source-scoped `calm_search`; code-state historical lookup goes through the existing git inspection tools (`calm_git_diff` and related).

Three reasons no recall tool is needed.

Shell-command output, build logs, test results, and other non-locally-re-readable captures — the strongest case for a recall-like primitive — are served by source-scoped `calm_search` in two modes: ranked retrieval when the agent has a query (specific errors, values, lines), and document-order chunks when the agent wants sequential reread (the failure context around line 200 of a 500-line log, in flow). One tool, two access patterns. This is a new capability CALM enables; pre-CALM, large captured output was take-it-or-lose-it at capture time.

Code-state historical lookup — comparing a file's current state to an earlier version — is already a dedicated affordance in the coding-agent surface via `calm_git_diff` and related git inspection tools. Agents reach for git when they need historical code state, not for a generic "what did I see earlier" primitive.

Generic historical lookup — "show me the full content I captured at turn 3, exactly as it was at turn 3" — doesn't exist as a workflow primitive in current coding-agent implementations. Agents rely on their context window for recent observations, git for code history, re-execution for idempotent sources. A general-purpose historical-recall tool would be a new affordance agents aren't trained to reach for.

Removing the recall primitive also makes the `Net context savings` invariant structurally enforced across the tool surface. Action and capture tools are bounded by the invariant directly; `calm_search` is bounded at the API layer by `limit` and `budget_bytes`. No primitive in the surface can return unbounded content by design — the invariant becomes a structural property, not a per-tool discipline.

Two alternatives were considered and rejected.

Blind recall — local re-read of the source on demand, with opportunistic re-ingest — can't serve shell-substrate captures (commands may have side effects, may not be safely re-runnable, may be impossible to re-run) and produces silent semantic drift when the source changes between capture and re-read (agent reasoning gets invalidated without any tool-level signal). For idempotent sources, it collapses into a duplicate of the structured-read primitives (`calm_read_file` and others), violating the tool-surface earn-its-slot discipline.

Full-content recall via a new CALM-side endpoint carries substantial CALM-side cost (new endpoint, storage-model decision between raw-storage and lossless-reconstruction from chunks, read-after-write consistency guarantee) for a use case agents have not been observed to need. Chunked source-scoped `calm_search` covers the documented painpoints — the right fix was to teach the search-with-scoping affordance, not add a new primitive.

### AD02

**Source Labels Carry Per-Call Staleness Suffixes**

Source labels are CALM's server-side identity inside one CALM session and serve as the addressing key for source-scoped `calm_search`. The adapter additionally fuses a per-call validation suffix into each emitted label: format `<base>[#<seq>]@<token>` per `LABELING.md` — e.g., `calm:v1:file:read:foo.go@a3f2k6`. The token is a session-scoped local-validation marker; the adapter validates it without a CALM round-trip. Mismatch returns a clear staleness signal rather than empty results from the current session.

This gives the agent a per-call staleness mechanism without inventing a second handle. LLM state-tracking across many turns is unreliable; a per-call validation that fires on stale references is a more direct signal than expecting the agent to maintain "session was lost N turns ago" in working memory.

One fused handle, two semantic axes:

- The **base** portion (`<base>[#<seq>]`) addresses content identity. For a latest source, the base follows the newest content as later captures replace earlier states. For a history source (`base#<seq>`), the base addresses a specific preserved invocation.
- The **suffix** (`@<token>`) addresses the specific capture moment. For a latest source, only the current token validates — a stale token signals that later captures have replaced the content under that base. For a history source, any token once-current within the session validates, because the invocation's content is immutable. Cross-session tokens reject in both cases.

Agents asking "current foo.go" use the latest-source form; agents asking "foo.go as I saw it earlier" use a history-source form. Both fail clearly across session replacements; the latest form additionally fails clearly when later captures have replaced the content under that base.

A non-fused base-only label (`<base>` without `@<token>`) remains valid as an input — it forwards to CALM without staleness checking. This keeps shell-substrate references and programmatic clients working, while the recall-hint path (which always emits the fused form) gives the agent staleness protection by default.

A two-handle alternative — separate `source_label` and opaque `capture_ref` — was considered and rejected. Cost: two distinct identities in every recall hint, doubled visible-text footprint, and the agent has to choose which to pass. Fused form collapses into one canonical identity with the same semantic axes available as parse-time properties of one string.

### AD03

**Replace The CALM Session On Loss**

Coding-agent conversations are long-lived and need capture continuity across operational disruptions that drop or invalidate the underlying CALM session — transient connectivity loss between CALM and the adapter, session TTL expiry, or other lifecycle events. Without replacement, capture would die on the first such disruption mid-conversation, breaking the workload the adapter exists to serve.

In steady operation, TTL expiry is the rarest of these. Every adapter tool call hits CALM (capture, search, events), refreshing `last_activity` and pushing the TTL forward. Session loss most commonly triggers on connectivity disruption (network blip between adapter and CALM) or explicit operator-side cleanup (`DELETE /v1/sessions` via management API). TTL fires only when the agent does extended local-only reasoning between tool calls — possible but uncommon.

When an established CALM session is lost, expired, or deleted, the adapter creates a replacement session and resumes capture for future work. Replacement doesn't preserve logical continuity with prior captures: source labels minted against the prior session become stale, and references to them fail clearly rather than resolving against the new session.

The trigger is 404 on a session-touching call. CALM returns 404 when the presented session token references a session that no longer exists (deleted, TTL-expired, or never issued). 404 is also CALM's response to cross-namespace mismatch — invisibility, not denial — so a stale or rotated namespace API key surfaces the same status code as a lost session. The adapter can't distinguish the two failure modes from the response alone.

A direct 401/403 on a session-touching call is not ambiguous and gets no recovery attempt: CALM rejects credentials before resolving the session, so a recreate would prove nothing. The adapter maps it to `auth_failed` directly, with the same terminal semantics as a rejected recovery create. CALM itself emits only 401; 403 is accepted defensively for edge-gated deployments.

The recovery path resolves the 404 ambiguity without a dedicated validation call. On 404 against a session token this process minted, the adapter attempts `POST /v1/sessions` with its current namespace API key. If the create succeeds, credentials are good and the new session is the replacement; the prior session's captures are declared stale. If the create is rejected (4xx), the credentials are the problem — the adapter surfaces `auth_failed` as the degradation reason, stops all CALM traffic for the remainder of the process, and does NOT loop; recovery from rejected credentials is operator action plus an adapter restart. If the create fails transiently (network failure, 5xx, timeout), nothing is yet learned about the credentials — the original call surfaces `calm_unreachable` and the next 404 re-attempts recovery. The recovery attempt itself is the credential validation.

The cost is one extra round-trip per session-loss event before recovery is confirmed. Bounded — one per session loss, not per request — and the adapter's first call after a session-lost trigger pays this latency once. Other failure responses do NOT trigger replacement: 5xx and timeouts are transient and recovered via retry or fall-through to the local result per the `Never worse for local actions` invariant. Recreation handles one specific failure mode and explicitly doesn't paper over the others.

This chooses useful recovery with honest discontinuity. The adapter keeps local tools available and resumes capturing new work, but it doesn't pretend old session data remains searchable from the new session. The cost is real: captures from the prior session are unrecoverable, not merely re-categorized. Shell command output, build logs, test results, and other non-locally-re-readable content are gone from the moment of session loss until the agent re-runs the producing action (where possible).

This cost is bounded by what CALM uniquely provides. Pre-CALM, no agent had cross-turn access to historical shell output, build logs, or test results at all — that material entered the LLM context once and was either retained or evicted, never searchable. Session loss removes a capability CALM created; it doesn't lose data any other toolset could have recovered. Content with independent durability — files (re-readable), git history (`calm_git_diff` and related), the workspace itself — is unaffected.

The benefit: a long-running MCP process can recover from CALM lifecycle loss without silently mixing old and new capture worlds.

### AD04

**Mandate Adapter Write Surface**

The adapter exposes write tools (`calm_edit_file`, `calm_write_file`) as a hard requirement, not a nice-to-have. The forcing function: when the adapter is the canonical read surface (per host-side dogfood discipline encoded in CLAUDE.md), host-native write tools that depend on a host-native read precondition become unusable.

**Concrete evidence.** This design contract itself was authored through the prototype adapter. Reading files for design verification went through `calm_run_command`'s shell pipes (grep/sed). When edits to milestone tracking and design files were applied via Claude Code's native Edit tool, the tool rejected with "File has not been read yet" because no native Read of the target file existed in the conversation — the precondition was unmet because reads had been routed through the adapter. The same workflow surface that makes the adapter coherent for reads breaks coherence for writes unless the adapter also owns writes.

**Two tools.**

- **`calm_edit_file`** — partial-file edits via `old_string`/`new_string` exact-match. Self-documenting (the old_string is the location reference, no line numbers to drift), prevents hallucinated patches (mismatch fails clearly), matches the most ergonomically-familiar shape across current coding agents.
- **`calm_write_file`** — full-file write for new files and total rewrites. Ground-floor primitive.

**Capture model.** Both tools follow `LABELING.md`'s **dual mode** capture policy: re-ingest the post-edit file under `calm:v1:file:read:<path>` (latest, replace) so subsequent reads see the current content, AND ingest under `calm:v1:file:edit:<path>#<seq>` per invocation (history, coexist) so each edit's resulting content state remains searchable. Ingest is full-content per edit — the adapter has the post-edit bytes in hand from applying the edit itself, no separate re-read needed. A `file_touched` event with operation + diff payload is emitted alongside, cross-linking to both source labels per `LABELING.md`'s event-derivation contract.

The same model handles file creation (`calm_write_file` to a path that didn't previously exist) uniformly: replace-mode ingest under `calm:v1:file:read:<path>` establishes the latest source — a vacuous replace, since there's no prior version — and the history source is created on the first invocation just as it would be for any later edit. No special-case code path per operation type; create / write / edit all execute the same capture sequence.

**Storage cost.** Dual mode means per-file DB usage grows linearly with edit count × file size during a session. A 20KB file edited 10 times costs ~220KB of session-scoped storage — one 20KB latest source (replaced each edit, retains only the final state) plus ten 20KB history sources (one per invocation, preserved). Long sessions with many edits scale accordingly. Bounded by session lifecycle — explicit close or TTL clears the session and its history sources. Operator controls per-session TTL to size this against their workload's peak concurrency. Per-file bounded-history mechanisms (keep last N states, time-bounded eviction) are deliberately deferred — premature optimization without real workload data. Partial / delta ingest is also deferred — it would require CALM-side partial-update semantics (HLD-touching) with significant complexity, and its own per-delta metadata overhead may exceed the bytes saved for small edits.

**Why no `apply_patch` / unified-diff tool.** Unified diff is token-efficient for multi-hunk changes but fragile (line numbers drift between read and patch; hallucinated patches parse but don't apply). Doesn't earn a slot under the structured-tool earn-its-slot test today; deferred until multi-hunk workflows demonstrate the need.

**Compete-on-ergonomics, not exclusivity.** The adapter ships these tools knowing the agent has fallback to host-native Edit/Write. If the adapter's tools are clunkier than native, agents will route around them and the `file_touched` coverage problem returns. Ergonomics is the load-bearing property here, not the existence of the tool. Hook-based capture of host-native edits (per `LABELING.md`'s extensibility section) remains an optional, host-specific enhancement — supplements but doesn't replace the structured tools.
