<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# CALM Adapter — Design Contract

This is the design contract for CALM's adapter — one capture engine and its shells. Part I specifies the engine: the capture semantics every integration consumes whole. Part II specifies the MCP shell (`calm-adapter`); Part III the capture shell (`calm-capture`). The contract sits between `LABELING.md` (source-label grammar and event extraction) and the HLD (workload-agnostic, no adapter-specific surface); settled decisions live in `DECISIONS.md` under stable `ADnn` anchors. Read it after the HLD when implementing or evolving any part of the adapter.

# Part I — Capture Engine

# 1. Purpose & Boundary

The adapter turns local coding-agent actions into CALM-managed context. An agent's action — a shell command, a file read, an edit — runs locally; the full output is captured into a CALM session under a stable source label; the agent receives compact task-facing text in place of the raw output; the captured material stays retrievable on demand. "The adapter" names this whole subsystem — one engine and its shells.

This is one CALM workload, not the universal shape of CALM integration. Server-side workloads — Slackbots, debugging agents, eval harnesses — sit closer to their own tool-call boundary and integrate more thinly through CALM's HTTP API directly. The adapter solves the coding-agent case: a harness whose tool traffic CALM does not otherwise see.

**The engine is the product surface.** The capture engine owns the semantics that make capture worth having, independent of how any agent reaches them: shell-command extraction and the labeling handoff (`LABELING.md`), local execution, ingest orchestration, presentation policy, source-registry and staleness semantics, session-lifecycle semantics, degraded-reason classification, and event derivation. Every integration consumes the engine whole; no integration forks it.

**A shell is a delivery mechanism.** A shell puts the engine on an agent's tool path and owns exactly three things: its transport, its session-state strategy, and its degradation surface — how the engine's degraded-reason classification renders to its consumer. Two shells exist:

- The **MCP shell** (`calm-adapter`) is a stdio MCP server exposing structured `calm_*` tools. Utilization is discretionary — the agent must choose these tools over the harness's native ones. In exchange, the structured surface captures action classes the native execution path doesn't expose to interception: file reads, searches, listings, edits, git inspection, each with typed arguments that feed labeling without shell-string parsing.

- The **capture shell** (`calm-capture`) is a CLI invoked through harness-native hooks that rewrite native tool calls to pass through it. Utilization is structural — the rewrite fires on every native shell execution, with no agent cooperation. Coverage is bounded by what the harness's hooks expose to interception.

**The tool surface is contingent; the engine is not.** Every agent-initiated operation the MCP tools carry — retrieval, outcome feedback — is equally expressible as a `calm-capture` invocation through the harness's own shell tool; the hook recognizes the binary and passes it through unwrapped. What cannot be removed is what the tools route to: the labeling grammar, capture and presentation policy, and the CALM call orchestration. A dedicated tool therefore earns its slot on measured ergonomics and utilization, never on necessity. Outcome feedback is the strongest candidate for a retained tool — it is inherently agent-discretionary, so a tool description that teaches the affordance may outperform a CLI hint — but that is an ergonomics call, not an architectural dependency. Which shell earns continued investment is decided by measured context economics, not by architecture.

**Dependency rule.** Shells depend on the engine; the engine depends on no shell; shells never depend on each other. The rule is enforced mechanically. Its consequence is deliberate: retiring a shell is a deletion, not a refactor.

The adapter does not own CALM's namespace/session security model, indexing semantics, feedback model, or storage lifecycle — those live in the HLD. It does not sandbox local execution either; commands run on the developer's machine with that process's ordinary permissions (see DL02).

# 2. Design Invariants

Seven rules. A new tool or command, response-shape change, lifecycle change, or shell may pick its own mechanics — these properties hold across them.

**1. Never worse for local actions** (`never-worse`). CALM unavailable? The local result still returns. CALM slow? Same. CALM rejecting? Same. Worst case is lost capture and higher context cost, never a blocked action.

**2. Stable capture identity** (`idempotent-indexing`). Captured outputs need identities that avoid silent collision between semantically distinct content. Two captures of distinct content under the same source label silently destroy history; `LABELING.md` owns the grammar that prevents it.

**3. Session and namespace isolation, in process and at rest** (`namespace-isolation`, `session-isolation`). Every CALM session's credentials and capture state are isolated within the integration process, and at rest wherever a shell persists them — state files are owner-only. Session tokens never appear in logs, visible text, capture labels, event payloads, or world-readable paths; a shell's owner-only state file is the token's only at-rest home. Capture handles, derived events, and search results never cross session or namespace boundaries.

**4. Honest capture continuity.** The adapter does not imply that prior captures remain searchable after their CALM session boundary is gone. Continuity breaks surface reactively — when the agent reaches for content from the prior session, it gets a clear staleness signal, not empty results from the new session.

**5. Honest mutation surfacing.** The tool surface honestly conveys each tool's mutation intent: tools that make a non-mutating promise signal as such; tools that may mutate signal explicitly; tools whose mutation status depends entirely on agent-supplied inputs make no read/mutate promise. This is a declarative consumer-trust contract about adapter INTENT, not a sandboxing claim — local execution remains unsandboxed, and developer-configured hooks, aliases, or extensions that mutate the workspace through nominally-inspection commands are outside what the adapter can detect or enforce against. The MCP shell expresses this through tool names, descriptions, and annotations; the capture shell inherits its mutation posture from the harness — the command it wraps was already presented to the harness's own permission surface, so the shell re-declares nothing.

**6. Response-first events** (`never-worse`). Event derivation and emission are best-effort. They never determine or delay the user-visible tool result.

**7. Net context savings.** When the adapter returns a local action's output to the agent, the response net-reduces context cost relative to the raw output on the median call. Telemetry-class additions never belong in visible text — they belong in OTel emission.

# 3. Capture & Presentation

## Identities

The adapter uses two identities. Don't blur them.

**Source label.** CALM's server-side source identity, fused with a per-call staleness suffix. Format `<base>[#<seq>]@<token>` per `LABELING.md` — e.g., `calm:v1:file:read:foo.go@a3f2k6`. The base portion (`calm:v1:file:read:foo.go`) is the CALM-side addressing key, operator-visible. The `@<token>` suffix is a session-scoped local-validation marker minted per invocation. Stale or cross-session tokens return a clear staleness signal rather than empty results from the current session. See AD02.

**Feedback ref.** The outcome-reporting handle; each shell exposes its own reporting affordance that accepts it (the MCP shell's `calm_report_outcome` tool). Opaque to the agent — relayed, never interpreted — and subject to the feedback window enforced by CALM. A capture fans out to one or more source ingests; its feedback ref is the **primary source's** ingest correlation id, 1:1 with the capture by construction once a compound ingest lands. A retrieval's feedback ref is that search call's correlation id.

## Presentation

Two modes:

**Inline mode** returns the captured content itself in visible text, with minimal framing. Used when the output is small enough that summary + scoped-search would be a net cost.

**Summary mode** returns a task-facing summary plus the fused source label in visible text. Used when the output is large enough that summary + scoped-search beats raw output. The source label is mandatory — without it, the agent has no addressable way back to the underlying captured content.

A ranged file read (scoped by an explicit line range) is a summary-mode call that presents the requested slice verbatim instead of a task-facing summary — the agent already narrowed the view, so summarizing it would defeat the scoping; the slice is byte-capped past a ceiling with a truncation marker naming both recoveries (re-scope the range, or reread the full capture in document order), and the fused source label is always present.

Mode-selection thresholds are implementer policy, tunable via dogfooding and benchmarks without changing the contract. The `Net context savings` invariant binds the implementer to net-saving on the median call.

# 4. Session Lifecycle & Failure Model

Two lifecycles run in parallel: the **integration lifetime** — shell-owned: the stdio process for the MCP shell, the harness conversation for the capture shell — and the **CALM session** used for capture, search, events, and correlation.

**Session state.** The engine defines five items of per-session state; every shell holds them durably for its integration lifetime, in a store of its choosing: the session token (the credential — secret, never logged), the monotonic capture sequence (allocates history labels; never reused, continues across session replacement), the token registry (validates label suffixes per AD02), the auth latch (set on credential rejection; terminal for the latch scope), and the epoch (increments on each session replacement; tags deferred work so output belonging to a replaced session is discardable, never delivered as current). What increments, latches, or resets is engine semantics, identical across shells; only the storage differs — the MCP shell holds these in process memory, the capture shell persists them (Part III).

A session is established on demand by the first capture. Capture is the only establishment trigger — retrieval and feedback have nothing to find before the first capture, so they fail with their unavailability signal rather than creating sessions. Establishment is throttled so a down CALM taxes at most one call per interval with the create timeout. Client registration is re-attempted first whenever it has not yet landed — a create for an unregistered client is a guaranteed rejection, which must not read as a credential verdict. Any local outputs produced before establishment were not captured; the establishing call is captured normally, so the transition into capture-active state surfaces visibly. A create rejected by CALM latches the credential failure exactly as AD03's recovery create does.

If an established CALM session is lost, expired, or deleted, the adapter creates a replacement session — see AD03. Prior captures are not searchable from the new session unless explicitly re-captured. Retrieval against a source label from the prior session fails clearly, never returning empty results from the current session — the per-call validation suffix per `LABELING.md` is what makes this detection local.

## Failure Behavior

Failure shape depends on operation class.

**Action/capture operations** return the local result when local work succeeds but CALM capture fails. Visible text states the degraded capture state and reason in agent-readable phrasing; OTel emission records the same facts for operator slicing.

**Retrieval-only operations** cannot produce correct search results without CALM state. They return a visible degraded error when the CALM backend is unavailable. Stale-source-label behavior is governed by AD02; session loss by AD03.

**Event emission** is best-effort and off the response path.

## Workspace Binding

Workspaces are discovered at capture time since the non-working directories that agentic sessions touch cannot be known at the time of start of the session. A path's workspace is its **project anchor**: the deepest ancestor directory carrying a version-control marker, or — only when no VCS ancestor exists, which is the dependency-store case — the deepest ancestor carrying a recognized project manifest. A VCS marker directly at the user's home directory or the filesystem root is ignored as an anchor (a home-level repository is a dotfiles setup, not a project boundary). Paths with no anchor fall back to `coexist` mode per `LABELING.md`'s escape-path rule.

The **primary workspace** is the anchor of the directory the adapter was launched in (or that directory itself when unmarked). Its captures label bare — no WorkspaceID segment — so the common single-repository session stays clean. Every other workspace, whenever discovered, labels its captures with its WorkspaceID segment. Discovery is monotonic within a session: new workspaces are added on first touch and existing labels never change meaning; the workspace set only grows.

Tool calls select a workspace explicitly by its ID (structured tools), implicitly by the path's or cwd's own project anchor — which registers the workspace on first touch — or default to the primary. Anchor resolution is per-path, so a nested anchor (a submodule inside an already-known repository) is its own workspace regardless of which was touched first.

# 5. Labeling & Events

`LABELING.md` is the canonical source-label and event-extraction contract. This document owns the broader adapter surface; `LABELING.md` owns the grammar that maps adapter actions to CALM sources and event records.

The broader adapter surface changes how labels are produced, not why they exist. Structured tools feed labeling from typed arguments — file path, directory path, grep pattern and scope, Git operation and ref selection. They don't round-trip through shell-string parsing to recover intent the tool already knows.

Shell-substrate execution — `calm_run_command` in the MCP shell, `calm-capture exec` in the capture shell — uses best-effort shell-command extraction for the long tail. When extraction finds a stable semantic identity, it captures latest or latest-plus-history per the labeling contract. When it can't, it preserves invocation history rather than overwriting a misleading latest source.

Events derive from the same action facts as labels, finalized once ingest outcomes are known. Cross-links point only at sources that actually persisted. Event emission is best-effort and response-first — failed or slow event writes don't change the tool result.

Keeping labeling separate from any shell is what makes the engine portable: the durable parts — stable source labels, latest/history policy, event cross-links — live in the engine; shell mechanics — stdio lifecycle, tool descriptions, hook payload parsing — live outside it.

# 6. Observability & Context Health

Context health is an operational fact, not an autonomous judgement about whether the model has enough context. The adapter reports what it can know: whether capture was active, whether a response was degraded, whether a retrieval result came from the current session, whether events were derived or queued, whether a result can accept feedback.

## Output Surface Structure

The adapter's output splits into two surfaces — visible text the agent reads (and the harness renders in its UI), and OTel emission the operator consumes (harness-independent, zero context cost).

**Visible text.** What the agent reads and the harness renders. Always in model context. Carries:

- Task-facing summaries (summary mode) or captured content (inline mode).
- The fused source label for captured output, in the recall hint — addressable by the agent in a follow-up retrieval call.
- `feedback_ref` when the call backed a CALM feedback-eligible operation — addressable by the agent in a follow-up outcome report.
- Degradation phrasing when the call ran in a degraded state. Phrasing is stable and reason-specific so the agent can branch on the reason:
  - `CALM degraded — calm_unreachable. Capture and search may fail; local result is shown.`
  - `CALM degraded — auth_failed. CALM credentials rejected; capture and feedback are disabled for this conversation.`
  - `CALM degraded — session_lost. The prior session expired or was replaced; references to prior captures will fail.`
  - `CALM degraded — capture_failed. Local action ran; CALM did not index the output.`
  - `CALM degraded — capture_partial. Some captured sources were indexed; others were not.`
  - `CALM degraded — feedback_window_expired. The feedback window for this reference has closed.`

  Each phrasing keeps the cost bounded (one short sentence per call) while giving the agent enough specificity to choose a next move that differs by reason. The reason enum and the canonical phrasing are engine-owned; a shell chooses only where the sentence renders — tool-result text for the MCP shell, terminal output for the capture shell — so agent-visible degradation is identical across shells. New degradation reasons get added as additional modes are characterized; each addition is a deliberate change, not a silent extension.

The `Net context savings` invariant binds visible-text framing tight: telemetry-class facts (per-call byte counters, timings, mode-decision distribution) never appear here.

**OTel emission.** Adapter-resident metrics and structured logs emitted alongside CALM's OTel surface. Never reaches visible text — zero context cost by construction, harness-independent. Carries:

- Per-call measurement: `adapter.response.visible_bytes`, `adapter.response.raw_bytes`, `adapter.call.duration_ms`, `adapter.presentation.mode` (inline / summary / ranged distribution). Metric names follow the dotted-schema convention; the exporter converts `.` → `_` at emission.
- Structured forms of the same per-call facts that surface in visible-text degradation phrasing — `captured` (boolean), `degraded` (boolean), `degraded_reason` (closed enum matching the visible-text values), source identity, and CALM's `correlation_id` for joining adapter output to CALM-side logs.

Operator slicing keys on the `client` identifier the adapter registers at startup per HLD's integration contract. Granularity — per harness, per developer, per team installation — is operator policy.

This is the surface that makes the `Net context savings` invariant operationally checkable. Without OTel emission, the invariant would either be unenforceable (no measurement) or self-defeating (measurement riding in visible text adds to the cost it's measuring).

## Feedback & Outcome Reporting

CALM owns the feedback API and the long-term interpretation of those signals. The adapter owns the integration surface: when a CALM value-producing call has a correlation id, the adapter exposes an opaque `feedback_ref` the agent can later pass to its shell's reporting affordance. The affordance accepts only bounded outcomes — `success`, `retry`, or `degraded`. No free-form explanation field. The agent decides whether and when to submit feedback against any exposed `feedback_ref`; the adapter doesn't steer or gate.

The adapter doesn't infer human intent from the mere fact that outcome reporting was invoked. Richer provenance modeling — distinguishing whether a feedback signal is model-declared, user-authored, user-approved, harness-verified, or externally-verified — is a CALM-side concern. Without CALM accepting, persisting, and interpreting provenance, the adapter can't meaningfully expose it; it records only the bounded outcome against a known result handle.

# Part II — MCP Shell

# 7. Tool Surface

The MCP shell exposes tools at the level of agent intent, not local implementation mechanics. A tool earns its slot when the action is common in coding-agent work, has a stable enough capture identity, benefits from search later, and would lose useful intent if forced through a generic shell command.

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

# 8. Process Lifecycle & Degradation Surface

The MCP shell's integration lifetime is the stdio child process the host binds as an MCP server. MCP initialize succeeds whenever the adapter can serve local tool semantics. CALM registration and session creation are attempted during initialize, but CALM availability does not decide whether the host can bind the adapter's local tools.

**Session-state strategy.** The engine's per-session state (Part I) lives in process memory: it costs nothing while the process lives and dies with it. A host that respawns the process starts a fresh CALM session; continuity across process death is the capture shell's territory, not this shell's.

**Degradation surface.** The engine's canonical degradation phrasing renders as one sentence inside the tool-result text, where it reaches both the agent and the host UI.

**Capability discovery.** Capability discovery starts at the tool boundary. Tool names, descriptions, and schemas make mutation intent, capture behavior, retrieval behavior, and feedback support clear enough for a host, model, or user to reason about allow-listing and approval.

Host-side process death is outside the adapter's full control. If the stdio MCP child dies, most hosts give that dead process no protocol-level way to rebind tools inside the already-running conversation. The adapter is responsible for making live-process CALM degradation visible and recoverable; host process rebinding is not portable unless the host exposes that lifecycle.

Cross-process detection — distinguishing a freshly-bound adapter from a continuation of a prior one — is not surfaced as a first-class signal. The per-call degradation phrasing plus stale-source-label errors on source-scoped `calm_search` cover the actionable cases: a fresh process with no prior context degrades retrieval cleanly; new captures succeed normally.

# Part III — Capture Shell (`calm-capture`)

# 9. Purpose & Command Surface

`calm-capture` is a single-invocation CLI: a harness-native hook rewrites the harness's shell tool call to run through `calm-capture exec`, which executes the command, captures the full output into the CALM session, and prints the engine's presentation to stdout in place of the raw output. Utilization is structural — the hook fires on every native shell execution, steering nothing about the agent's behavior. Coverage is equally structural: what the harness's hooks intercept is captured; actions they don't expose (native file reads, searches, edits) stay native and uncaptured. Each invocation is a fresh process — all cross-invocation continuity lives in the on-disk session state, keyed by the harness's conversation identity.

Only `search` and `feedback` are model-facing — the inherently discretionary operations; `exec` is invoked by the rewrite, `hook` by the harness, `init` by the operator.

| Command | Role | Contract |
|---|---|---|
| `exec -- <argv>` | The capture path; the only form hooks rewrite to. | Runs the command, captures, prints the engine presentation to stdout. The wrapped command's own execution is untouched: its exit code propagates verbatim, and pipes or substitutions inside its argv see its raw streams — the wrap exists only at the harness tool-call boundary. On capture failure the presented stdout is the raw output verbatim (`never-worse`). |
| `search` | Retrieval — the same primitive as the MCP shell's `calm_search` (queries, source scope, document-order reread). | Results to stdout; exit 0. Nonzero with the engine's degradation phrasing on stderr when retrieval cannot be served. |
| `feedback <ref> <outcome>` | Outcome reporting against a feedback ref. | CALM's bounded outcomes only. Exit 0 on acceptance; nonzero plus phrasing otherwise. |
| `init` | Install and registration: writes the hook configuration for a harness (`--harness=…`), validates connectivity and credentials; `--reset` clears a persisted auth latch. | Idempotent — re-running converges and never installs a second hook layer (AD07). |
| `hook` | The harness-facing adapter: reads a hook payload on stdin, emits the rewrite response on stdout. | Parses every supported harness payload shape. On any parse or rewrite failure it emits the pass-through response and exits 0 — the native call proceeds unwrapped, and the hook binary never signals failure to the harness (`never-worse`). |

The engine's recall hint is shell-parameterized: each shell supplies its retrieval affordance's name, so a capture presented by this shell points at `calm-capture search source=<label>` exactly where the MCP shell's points at `calm_search`. Every non-degraded capture presentation carries a compact trailer — one line pairing the fused source label with the feedback ref — so source-scoped recall (of even a small inline capture, which would otherwise show no label) and outcome reporting are both discoverable from the result; search results carry the feedback ref alongside. Degradation renders inside the presented stdout for `exec` — the surface the agent reads — and on stderr for the agent-initiated commands; a degraded presentation carries the canonical degradation sentence and no trailer. Neither addition alters the wrapped command's exit code.

**Retrieval discovery.** Capture utilization is structural; retrieval remains agent-discretionary in every shell — what varies is how the affordance is taught. This shell teaches it in three layers: the per-capture recall hint (a copy-pasteable command, no prior knowledge needed); a one-time capability card appended to the presentation of the session's first capture — the persisted sequence makes "first" knowable — covering query discovery, source-scoped reread, and the label's verbatim-only handling; and, where the harness supports session-start context injection, the same card injected before any capture exists. Whether these channels match a tool description's effectiveness is a measured question, not an assumed one.

**Configuration.** `calm-capture` reads the same adapter configuration as the MCP shell — endpoint, namespace credential via secret refs (`[env:…]`, `[file:…]`), client identity. One pairing rule is contractual: the credential source this shell presents and the source the operator's CALM resolves must be one durable location; `init` validates the pairing and reports a mismatch as a credential failure at install time, before any hook fires.

# 10. Session State on Disk

State lives under `$CALM_HOME` (default `~/.calm`), one directory per harness conversation, holding the engine's session state (Part I), this shell's establishment bookkeeping, and the event spool. Directories and files are owner-only — this state is the session token's only at-rest home. Writes are crash-safe: an interrupted write leaves the prior state intact, and the worst case — a lost registry record for the last capture — surfaces later as an honest staleness signal, never as corruption.

**Local state is authoritative for the session token.** CALM's create idempotency is a bounded per-pod cache — it collapses racing creates; it is not identity. Establishment idempotency keys derive deterministically from the conversation identity, so same-conversation races collapse to one session; recovery creates key distinctly from the establishment they replace, durably across process deaths, so a replacement can never collide with the original.

**Concurrency.** Invocations of one conversation may overlap freely; mutual exclusion over the shared state is crash-released and local-filesystem-scoped (network filesystems unsupported; Windows in scope with equivalent semantics). The contractual properties: concurrent first invocations single-flight establishment — one create, the rest reuse it; a capture's sequence number is allocated once and never reused; ingest and every other CALM call run outside mutual exclusion — a slow or down CALM never serializes the conversation's other invocations; bookkeeping is recorded only for the generation that captured it — a replaced generation's results are discarded, since its labels died with the session; session loss triggers at most one recovery create per failing call (AD03), across which the registry resets, the epoch increments, and the sequence continues; a credential rejection sets the latch. The latch is persisted per harness conversation; it clears only with a new conversation or `init --reset`.

**Reclamation.** State idle beyond a multiple of the longest session TTL is reaped, along with orphaned intermediate files; reclamation runs opportunistically from ordinary invocations — no daemon (AD05) — and can never remove state a live invocation holds.

# 11. Event Spool

Events are response-first and the process is gone milliseconds after responding, so deferred delivery must survive the invocation — an on-disk spool holds each capture's event batch until delivered. Synchronous delivery inside the call would tie loss probability to capture size — the heaviest captures run the longest ingests against the busiest CALM, so drops would cluster on exactly the calls attribution most needs. The invocation attempts an immediate bounded flush after its response is emitted; any later invocation drains leftovers. Delivery is at-most-once (AD06): a batch is delivered or dropped, never delivered twice — stalled deliveries are abandoned, batches from a superseded epoch are skipped, and a 404 on delivery drops the batch without triggering session recovery. The accepted residual is tail loss — a conversation's final events are lost iff that invocation's flush fails.

# 12. Hook Integration & Distribution

The rewrite is the integration: a pre-execution hook receives the harness's shell tool call and rewrites its input to `calm-capture exec -- <original command>`; the harness executes the rewritten form natively. The hook's response rewrites input only — it never supplies a permission decision. What the permission surface then sees — the original command or the rewritten form — is harness-specific, so permission-outcome equivalence is verified per harness, never assumed. One gate is contractual: the rewrite must not weaken the permission surface — if a harness's rewrite semantics implicitly approve the call, or rules that bound the original command no longer bind the rewritten form, that harness does not ship the rewrite. Two guards make the wrap idempotent under stacked hook layers (AD07): the hook passes through inputs already wrapped or invoking `calm-capture` itself, and `exec` sets an environment sentinel so nested shell executions inside a wrapped command pass through untouched. `init` installs exactly one hook layer per harness and warns when it detects another capture layer.

Per harness: **Claude Code** — a plugin carrying the hook set; the session-start hook injects the retrieval capability card and triggers reclamation. **Codex** — a configuration-layer install; the harness requires a one-time interactive review to trust installed hooks, which the installer documents rather than works around. **Cursor** — a plugin; its rewrite and session-start behavior are verified live before its installer ships. Each installer ships allow-rule guidance matched to its harness's verified permission behavior.

# Adapter Decision Log

Settled adapter decisions live in `DECISIONS.md` — append-only entries with stable `ADnn` anchors (`AD01`, `AD03`, …), cited directly from this document.
