<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Adapter Decision Log

Settled adapter decisions and the reasoning behind them, captured so the design isn't relitigated. AD numbers are append-only, stable anchors — cite them directly (`AD03`); entries never renumber. The design contract lives in `DESIGN.md`; the labeling and event grammar in `LABELING.md`.

### AD01

**No Dedicated Recall Tool**

`calm_search` is the single retrieval primitive. No dedicated recall tool. Historical lookup of captured content goes through source-scoped `calm_search`; code-state historical lookup goes through the existing git inspection tools (`calm_git_diff` and related).

Three reasons no recall tool is needed.

Shell-command output, build logs, test results, and other non-locally-re-readable captures — the strongest case for a recall-like primitive — are served by source-scoped `calm_search` in two modes: ranked retrieval when the agent has a query, document-order chunks for sequential reread. One tool, two access patterns; pre-CALM, large captured output was take-it-or-lose-it at capture time.

Code-state historical lookup is already a dedicated affordance via `calm_git_diff` and related git inspection; agents reach for git for historical code state, not a generic "what did I see earlier" primitive.

Generic historical lookup — "show me the full content I captured at turn 3, exactly as it was at turn 3" — doesn't exist as a workflow primitive in current coding-agent implementations; a general-purpose recall tool would be a new affordance agents aren't trained to reach for.

Removing the recall primitive also makes the `Net context savings` invariant structural: every primitive is bounded — action/capture operations by the invariant directly, `calm_search` at the API layer by `limit` and `budget_bytes` — so no primitive can return unbounded content by design.

Two alternatives were considered and rejected. Blind recall — local re-read of the source on demand with opportunistic re-ingest — can't serve shell-substrate captures (commands may be unsafe or impossible to re-run), produces silent semantic drift when the source changes between capture and re-read, and for idempotent sources duplicates the structured-read primitives. Full-content recall via a new CALM-side endpoint carries substantial CALM-side cost (new endpoint, raw-storage vs lossless-reconstruction decision, read-after-write consistency guarantee) for a use case agents have not been observed to need; chunked source-scoped `calm_search` covers the documented painpoints — the right fix was teaching the search-with-scoping affordance, not a new primitive.

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

Coding-agent conversations are long-lived and need capture continuity across operational disruptions that drop or invalidate the underlying CALM session — transient connectivity loss, session TTL expiry, or other lifecycle events; without replacement, capture dies at the first mid-conversation disruption. TTL expiry is the rarest of these in steady operation — every CALM-touching call refreshes `last_activity` — so loss most commonly means a connectivity blip or operator-side cleanup (`DELETE /v1/sessions`).

When an established CALM session is lost, expired, or deleted, the adapter creates a replacement session and resumes capture for future work. Replacement doesn't preserve logical continuity with prior captures: source labels minted against the prior session become stale, and references to them fail clearly rather than resolving against the new session.

The trigger is 404 on a session-touching call. CALM returns 404 when the presented session token references a session that no longer exists (deleted, TTL-expired, or never issued). 404 is also CALM's response to cross-namespace mismatch — invisibility, not denial — so a stale or rotated namespace API key surfaces the same status code as a lost session. The adapter can't distinguish the two failure modes from the response alone.

A direct 401/403 on a session-touching call is not ambiguous and gets no recovery attempt: CALM rejects credentials before resolving the session, so a recreate would prove nothing. The adapter maps it to `auth_failed` directly, with the same terminal semantics as a rejected recovery create. CALM itself emits only 401; 403 is accepted defensively for edge-gated deployments.

The recovery path resolves the 404 ambiguity without a dedicated validation call. On 404 against a session token this integration minted, the adapter attempts `POST /v1/sessions` with its current namespace API key. If the create succeeds, credentials are good and the new session is the replacement; the prior session's captures are declared stale. If the create is rejected (4xx), the credentials are the problem — the adapter surfaces `auth_failed` as the degradation reason, stops all CALM traffic for the remainder of the shell's latch scope — the process lifetime for the MCP shell, the harness session for the capture shell — and does NOT loop; recovery from rejected credentials is operator action plus a fresh latch scope. If the create fails transiently (network failure, 5xx, timeout), nothing is yet learned about the credentials — the original call surfaces `calm_unreachable` and the next 404 re-attempts recovery. The recovery attempt itself is the credential validation.

The cost is one extra round-trip per session-loss event, paid once by the first call after the trigger. Other failure responses do NOT trigger replacement: 5xx and timeouts are transient, recovered via retry or fall-through to the local result per the `Never worse for local actions` invariant — recreation handles one failure mode and doesn't paper over the others.

This chooses useful recovery with honest discontinuity: capture resumes for new work, and prior-session captures are genuinely unrecoverable — non-locally-re-readable content is gone until the producing action is re-run, where it can be. The cost is bounded by what CALM uniquely provides: session loss removes a capability CALM created — pre-CALM this material was never searchable at all — and content with independent durability (files, git history, the workspace) is unaffected. The benefit: a long-lived integration can recover from CALM lifecycle loss without silently mixing old and new capture worlds.

### AD04

**Mandate Adapter Write Surface**

The adapter exposes write tools (`calm_edit_file`, `calm_write_file`) as a hard requirement, not a nice-to-have. The forcing function: when the adapter is the canonical read surface, host-native write tools that depend on a host-native read precondition become unusable.

**Concrete evidence.** This design contract was authored through the prototype adapter: with reads routed through the adapter, a host-native edit rejected with "File has not been read yet" — its read precondition was unmet by construction. The surface that makes the adapter coherent for reads breaks writes unless the adapter owns writes too.

**Two tools.**

- **`calm_edit_file`** — partial-file edits via `old_string`/`new_string` exact-match. Self-documenting (the old_string is the location reference, no line numbers to drift), prevents hallucinated patches (mismatch fails clearly), matches the most ergonomically-familiar shape across current coding agents.
- **`calm_write_file`** — full-file write for new files and total rewrites. Ground-floor primitive.

**Capture model.** Both tools follow `LABELING.md`'s **dual mode** capture policy: re-ingest the post-edit file under `calm:v1:file:read:<path>` (latest, replace) so subsequent reads see the current content, AND ingest under `calm:v1:file:edit:<path>#<seq>` per invocation (history, coexist) so each edit's resulting content state remains searchable. Ingest is full-content per edit — the post-edit bytes are in hand from applying it. A `file_touched` event with operation + diff payload is emitted alongside, cross-linking to both source labels per `LABELING.md`'s event-derivation contract. File creation runs the same sequence uniformly — the replace-mode ingest is vacuous (no prior version) and the first history source is minted as for any later edit; no per-operation special case.

**Storage cost.** Dual mode grows per-file storage linearly with edit count × file size (a 20KB file edited 10 times ≈ 220KB session-scoped), bounded by session lifecycle — close or TTL clears it; operators size TTL against peak concurrency. Bounded-history mechanisms and partial/delta ingest are deliberately deferred: the former is premature without workload data, the latter needs CALM-side partial-update semantics (HLD-touching) and its per-delta metadata overhead may exceed the bytes saved on small edits.

**Why no `apply_patch` / unified-diff tool.** Unified diff is token-efficient for multi-hunk changes but fragile (line numbers drift between read and patch; hallucinated patches parse but don't apply). Doesn't earn a slot under the structured-tool earn-its-slot test today; deferred until multi-hunk workflows demonstrate the need.

**Compete-on-ergonomics, not exclusivity.** The agent always has host-native fallback; if these tools are clunkier, agents route around them and the `file_touched` coverage gap returns — ergonomics is the load-bearing property, not existence. Hook-based capture of host-native edits (per `LABELING.md`'s extensibility section) remains a host-specific supplement, not a replacement.

### AD05

**Per-Conversation State File, No Daemon**

The capture shell persists the engine's session state in an on-disk file keyed by the harness's conversation identity, guarded by an exclusive advisory lock. No resident process.

The hook lifecycle forces persistence: every invocation is a fresh process, while the CALM session must span the conversation — including kill-and-resume cycles, which reattach by conversation identity and find the same state. The state file is authoritative for the session token. CALM's create idempotency is a bounded per-pod cache: it collapses racing creates; it cannot serve as identity.

Alternatives rejected: a resident daemon holding state in memory — a second lifecycle to install, supervise, and crash-recover, a new failure domain purchased to avoid a lock file; identity via idempotency key alone — the cache's bounded window and per-pod scope make replayed creates mint fresh sessions, splitting the capture world silently; deriving state from harness internals (transcript paths, process environment) — neither portable across harnesses nor contract-stable within one.

The daemon rejection is structural, not merely operational. Never-worse forces the daemonless floor to exist regardless: capture must keep working — or degrade invisibly — when any resident process is dead, so a daemon can only ever be an optional accelerator layered on the mandatory floor, never a replacement for it. If per-invocation cost (process spawn, establishment overhead) proves dominant in measurement, the sanctioned evolution is that accelerator atop the floor, not a lifecycle rewrite. The MCP shell is the resident-process lifecycle in this same codebase; the boundary between the two shapes is deliberate, not accidental.

### AD06

**Spool-First, At-Most-Once Event Delivery**

Each capture appends its events to a per-conversation spool under the post-ingest lock; delivery is an immediate bounded flush after the response, plus opportunistic drain by later invocations. Claims are by rename; stale claims are deleted, never re-delivered.

Synchronous in-call delivery was rejected for bias, not latency: delivery failures under load correlate with capture size — the heaviest captures hold the longest ingests against the busiest CALM — so sync-drop losses concentrate on the highest-value calls and skew the attribution record on its wins. At-least-once replay was rejected while event ingestion has no server-side deduplication: silently duplicated events corrupt the attribution record, which is worse than losing a tail; replay becomes the upgrade path once ingestion deduplicates. Accepted residual: a conversation's final events are lost iff the final invocation's flush fails.

The spool is also the compatibility floor for servers without a compound ingest. When events ride the capture's own ingest request as a declared server floor, the spool deletes with the fan-out path: events then share fate with their capture — the size-correlated loss bias becomes unconstructible rather than mitigated — and the loss-vs-duplication question moves to the server's event-dedup contract, where at-least-once replay becomes the upgrade path.

### AD07

**Single Hook Layer, Guarded Re-Entrancy**

Harnesses stack hook layers — user, project, and plugin configurations may all fire on one tool call — and a stacked wrap corrupts capture identity: the outer wrap captures the inner wrapper's presentation instead of the command's output. The installer targets exactly one layer per harness and warns when it detects another capture layer; the wrap is guarded twice at runtime — the hook passes through inputs already wrapped or invoking `calm-capture` itself, and the executor sets an environment sentinel so nested shell executions inside a wrapped command pass through. The runtime guards are load-bearing, not redundancy: install-time discipline cannot see layers added after install.

Install-time detection is bounded by machine visibility: a second layer added afterward at another scope (a project-committed plugin, a user-scope install on another machine) fires no warning where the effect lands, and parallel hook application means both layers receive the unwrapped input — the wrap guard cannot catch a sibling that has not wrapped yet. The remedy is the same scan at session start: the session-start hook performs init's cross-layer scan once per session and surfaces a warning in the injected card on the affected machine. When a project layer and a user layer coexist, the project layer wins — it carries the team's pairing intent — and the surfaced guidance says to remove the user layer.

### AD08

**Observe-And-Replace Over Rewrite, Capability-Matched Per Harness**

The capture shell has two ways to put the engine on a harness's native shell path: rewrite the tool call before execution so it runs through `calm-capture exec` (wrap), or let the native call run untouched and substitute its result with the engine's presentation from a post-execution hook (observation). Observation is preferred wherever the harness can substitute a tool result after execution; wrap is the floor where it cannot.

The choice is an authority argument, not an ergonomics one. The rewrite collapses every command to one program identity — `calm-capture exec` with a quoted argument — so a harness's per-command permission model (each program evaluated, compound operators visible) can no longer discriminate, and the only durable allow rule is a blanket rule on the wrapper: a one-click universal bypass, the exact rule the hook-integration contract forbids shipping. Wrap mode is context-airtight by construction — the shell consumes the raw output inside its own subprocess, so raw bytes never touch the model's context — but it converts a granular permission surface into re-approve-forever or approve-everything. Observation inverts the trade where its precondition holds: permission evaluation, allow rules, and workspace confinement all bind the original command; the hook executes nothing and holds no standing authority; and the substitution timing is verified per harness — the harness stores the replacement, so raw output stays out of context (only the command string, riding the tool call's own input, remains). Where substitution is absent, observation is byte-neutral at best — post-hoc annotation cannot un-spend context — so wrap remains the only mechanism that saves the bytes there.

Alternatives rejected. Wrap everywhere: pays the permission collapse on harnesses that offer a clean alternative, and normalizes the blanket-bypass rule the contract forbids. Observation everywhere: result substitution is not a universal harness capability, so it strands the harnesses that can only rewrite input. A transparent interposer between harness and execution (daemon or sandbox): avoids the rewrite without needing substitution, but places a resident process on the execution path — capture availability becomes a precondition of execution, violating `never-worse` and the sidecar posture. Accepted residual: on wrap-mode harnesses the permission cost stands and is disclosed at install time rather than mitigated; mode assignments are per-harness facts verified live, so a harness gaining substitution later moves modes by reconfiguration, not redesign.
