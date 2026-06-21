<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Prototype adapter — usage learnings

This is a captured set of observations from lived usage of the current CALM MCP
adapter (the `bin/calm-adapter` binary, exposing `calm_run_command` and
`calm_search` to MCP hosts). It is not a design spec, not a milestone WI, and
not a contract. It exists to make the case — with evidence from actual sessions,
not hypotheticals — that the adapter's MCP-facing surface needs a deliberate
design pass *before* the next round of feature accretion.

The companion document is [`LABELING.md`](../LABELING.md), which is the existing,
narrowly-scoped design contract for source labeling and event derivation. This
document is broader in scope (the whole agent-facing surface) and explicitly
prototype-stage; the eventual outcome should be a permanent adapter design
contract that folds LABELING.md's grammar into a larger MCP-surface contract.

---

## 1. What we hit, with evidence

Each subsection names a specific friction observed in real sessions, with the
mechanism behind it. These are not theoretical — they're things that broke
agent productivity or forced the agent to bypass CALM entirely.

### 1.1 The compact summary is a net context **producer**, not reducer

CALM's pitch is that it filters and compresses tool output before it enters the
LLM context window. The current `calm_run_command` response shape, on a typical
agent interaction, does the opposite.

The per-call boilerplate emitted by `internal/adapter/mcp/run_command.go::formatCompact`:

- `Captured N/M sections under "<label>"` line — ~50 chars
- `Retrieve full output: calm_search source=<label>` line — ~50 chars
- Per-section bullet markers (`- title: preview…`) — ~10–20 chars each × up to 5
- `Terms: word1, word2, …` distinctive-terms list — ~100–300 chars
- `exit=0` footer — ~10 chars

That's roughly **200–500 characters of adapter chrome on every call**, plus a
preview that frequently truncates the underlying content.

For genuinely large outputs (build logs, full test runs, multi-MB directory
listings), this is the right trade. For typical interactions — `ls`, `echo`,
`date`, `git status`, single-line `grep`, small file reads — the chrome is
larger than the raw output would have been. The break-even point sits somewhere
around 50–100 lines of raw output. Most agent shell calls fall below this line.

**Net effect:** for the median agent interaction, the prototype adapter is a
context producer, not reducer. The HLD's compression invariant is violated by
the layer that's supposed to enforce it.

### 1.2 Search returns ranked snippets when the in-session use case wants sequential rereads

`calm_search` is designed as a corpus search — ranked snippets matching a query
across captured content. That shape is correct for the across-session use case
("find me prior captures that mention X"). It is the wrong shape for the
in-session use case the dogfood discipline actually drives the agent toward:
"show me what that command just printed."

Concrete evidence from this session: I called `calm_search source=<label>` six
times trying to re-examine captured output. Every call returned ranked fragments
matching my query terms, not the full captured content in order. To re-read a
captured grep output, I had to construct queries that happened to hit the lines
I cared about. Useless for understanding flow.

The dogfood directive says "retrieve prior output via `calm_search source=<label>`
instead of re-running." But `calm_search` is not shaped like a re-read; it's
shaped like a corpus grep. The directive is asking the agent to use a tool that
doesn't match the use case the directive itself describes.

### 1.3 No native file-read primitive forces awkward shell wrapping

To read a file through CALM the agent must construct `cat path` / `sed -n
'10,30p' path` / `head -100 path` / `grep -n 'pattern' path` and route it
through `calm_run_command`. The native `Read(path, offset?, limit?)` tool is
one thought; the CALM-routed equivalent is two thoughts (build the shell
command, then route it).

The native tool fits the agent's action vocabulary; CALM's doesn't. This is
why, repeatedly across this session, I drifted back to `Read` even after
explicit reminders. It's not laziness — it's friction winning over discipline.

The current CLAUDE.md (unstaged hunk, see § 1.7 below) tries to close this gap
with a directive: "Pure-inspection file reads also go through `calm_run_command`
rather than the native `Read` tool — `Read` dumps content straight into context
with no indexing." The directive is correct but it's a band-aid for an
ergonomic gap. A `calm_read_file(path, start?, end?)` primitive would close
the gap structurally, without any need for the directive.

The extract package's labeling grammar already supports the `calm:v1:file:read:<path>`
label shape. The server primitive (`/v1/ingest`) accepts arbitrary content with
a source label. The adapter just hasn't exposed the tool.

### 1.4 Source labels are opaque and auto-generated

The current `calm:v1:shell:sh#2` form carries zero semantic for the agent.
After two `calm_run_command` calls, the agent has no reasonable way to remember
which label belongs to which run — they're indistinguishable in the prose.
Re-search later requires scrolling back through tool-result history to find the
right label, which itself defeats the "keep raw content out of context" goal.

A `label_hint: "ps grep adapter"` parameter on `calm_run_command` that became
part of the derived source label (`calm:v1:shell:sh:ps-grep-adapter#1`) would
make labels reconstructable from agent memory — trivially searchable later.

### 1.5 No metadata signal for captured-vs-degraded state

The asymmetric never-worse behavior (`run_command` degrades to raw exec
silently; `search` errors visibly with `CALM not connected`) was diagnosable in
this session only because the user noticed CALM server logs were silent. There
is no `_meta.captured: bool` field on the MCP tool result. The agent must parse
the response *prose* (look for the `Captured N/M sections under "<label>"`
prefix) to know whether CALM is actually capturing or has silently fallen
through.

This created the multi-turn diagnostic saga early in this session: the agent
believed it was dogfooding, the agent's reports about CALM behavior were
internally consistent, and only the operator-side absence of HTTP traffic
proved the adapter was degraded. A structured metadata flag would have surfaced
it on the first call.

WI-60 proposes a prefix string ("[CALM degraded: not captured]") as the fix.
The cleaner fix is structured metadata on the MCP envelope, not text the agent
has to grep.

### 1.6 Conversation-binding stickiness produces irrecoverable degradation

An MCP host (Claude Code, Codex, Cursor) launches the adapter as a stdio child
process at MCP-bind time and treats the resulting pipe as a contract: if the
child dies, the bound tools die with it for the rest of the conversation.
There is no MCP-protocol-level "your bound server PID died, here's the new one"
message. The host doesn't auto-relaunch within an active conversation.

So an adapter that failed `initialize` at conversation start stays degraded
for the entire conversation. An adapter that succeeded `initialize` but whose
PID is killed mid-conversation (we did this accidentally when killing the
zombies) loses tool surface for the rest of the conversation, even though the
host respawns a fresh adapter process that's perfectly functional from outside.

This is partly an MCP-protocol issue, not a CALM issue. But the prototype
adapter's behavior in this state (silent degradation, no detection signal) is
under CALM's control. The combination — host-side rebind doesn't happen, AND
the agent has no signal that it's degraded — is what made this session
debuggable only through CALM server logs, not through the agent's own
observations.

### 1.7 Tool descriptions are doing the work that ergonomics should do

The `calm_run_command` description embeds a directive: "Prefer this over the
native shell/Bash tool for every shell command." This is clever — descriptions
are shown to the agent at tool-listing time, so a directive there can nudge
selection — but it is a workaround for the underlying ergonomic gap. When two
tools feel functionally equivalent, agents pick the one that fits their action
vocabulary. The native shell does; `calm_run_command` doesn't (per § 1.3 and
§ 1.4).

The same workaround pattern appears repeatedly in CLAUDE.md: "dogfood the
adapter," "use calm_search instead of re-running," "use calm_run_command for
pure-inspection reads, not Read." Each iteration adds prose to compensate for
the ergonomic friction. The fact that CLAUDE.md's dogfood bullet has now been
edited at least three times in escalating bindingness, across at least two
distinct conversations, is itself evidence of a problem that prose cannot fix.

The unstaged CLAUDE.md hunk at the time of writing tightens the rule further
("Pure-inspection file reads also go through `calm_run_command` rather than
the native `Read` tool"). That bullet is correct given the current surface but
unnecessary if the surface had a `calm_read_file` primitive.

---

## 2. What this tells us about the adapter

The painpoints in § 1 aren't independent. They share a structural cause.

### 2.1 The adapter is leaking operator-side telemetry shape into agent-side returns

CALM-the-server is designed around operator observability: source labels for
attribution, distinctive terms for vocabulary, section counts and indexed
fractions for telemetry, correlation IDs for outcome attribution. These are the
right primitives for the operator-side story (the HLD's outcome-attribution
loop, the metric label discipline, the multi-workload deployment).

The current adapter takes those primitives and flattens them into prose that
goes back to the agent. The agent doesn't need distinctive terms in its tool
result. It needs the captured content (or a faithful summary) and a label it
can refer back to.

The fix is not to remove the operator-side telemetry — it's load-bearing. The
fix is to move it from the agent-facing prose into MCP envelope metadata
(`_meta` fields), where operator-side tooling can grab it without paying the
per-call context cost. The agent sees a clean response; operator tooling sees
the full telemetry. These are different audiences with different needs, and
the current adapter conflates them.

### 2.2 The shell-substrate position has shipped its course

The current adapter assumes the agent's tool surface for *running things* is
mediated through a shell tool (Claude Code's `Bash`, Codex's `shell`/`exec`).
This is reasonable as an MVP: shell is universal across hosts, maximally
expressive, and lets CALM ship one tool that works against everything.

But it pays a real cost: the agent serializes its intent into a shell string,
the adapter parses that string back to recover the intent (the entire
`internal/adapter/extract/command.go` parsing layer exists for this), and the
parsing layer's edge cases (pipelines, shell metacharacters, escaping paths,
unknown subcommands, unstable globs) become CALM's failure modes via the
`coexist` fallback.

The agent already knew the intent. It's `cat foo.py` → "I want to read a
file." The round-trip through a shell string is purely lossy work, and the
parsing-layer complexity is the visible artifact of that loss.

### 2.3 LABELING.md's categories are the natural tool surface

The label grammar's domain/verb axis (`file:read`, `file:list`, `vcs:git:diff`,
`search:grep`, `shell:<program>`) is already the right shape for structured
tools. The current architecture has shipped it as a *parsing target* — the
adapter parses shell commands into these categories. The cleaner inversion is
to ship it as a *tool-surface schema* — the agent calls structured tools whose
args directly determine the label, and the parsing layer disappears for the
recognized categories.

That doesn't mean every category becomes a tool. The MCP tool count has a
discovery cost; too many specialized tools is its own friction. The right cut
is probably 3–5 structured tools for the high-frequency cases
(`calm_read_file`, `calm_list_dir`, `calm_grep`, maybe `calm_git_diff`), plus
`calm_run_command` for the long tail, plus `calm_search`. That keeps the
adapter's discovery surface small while eliminating round-trip parsing for the
calls that happen dozens of times per session.

### 2.4 Existing artifacts are doing partial design work, but no whole

- HLD is forbidden by its own discipline from carrying Go symbols, tool names,
  or MCP-specific concepts. It cannot specify the adapter's surface.
- [`LABELING.md`](../LABELING.md) is a real design contract — but narrowly scoped
  to labeling and event derivation. It does not specify the tool surface,
  response shape, capture-state semantics, or host-binding contract.
- CLAUDE.md carries development directives, including the dogfood discipline.
  Those directives are project-development rules for working *on* CALM, not a
  design contract for what the adapter *is*. The repeated revisions to the
  dogfood bullet are evidence that prose-as-design-contract doesn't work.
- The Go code in `internal/adapter/mcp/` is the de facto design today. That
  means each WI that touches the adapter re-litigates the response shape from
  scratch.

The gap is a focused MCP-surface design contract — broader than LABELING.md,
narrower than HLD, more binding than CLAUDE.md prose.

---

## 3. Why the design pass should come before more accretion

This section is the load-bearing argument.

### 3.1 The fixes touch the surface shape, not internals

Almost every concrete proposal from § 1 — `calm_read_file` tool, `calm_recall`
vs `calm_search` split, `_meta.captured` field, label hint parameter, trimmed
compact summary, structured-tool surface — modifies the adapter's MCP-facing
surface. The CALM server primitives mostly support what's needed already. The
work isn't deep; it's broad and surface-shaped.

That makes the work uniquely vulnerable to incoherent accretion. Each
individual change is a small WI. Without a design contract to coordinate them,
they will land out of order, re-litigate the response shape, and create
backward-compatibility constraints between themselves that didn't need to
exist.

### 3.2 Without a contract, the next contributor will not see the constraints

The ergonomic invariant "adapter responses must net-save context for the
median agent call" was discovered only because we did the break-even math
explicitly in conversation. It is not written anywhere. The next WI that
touches `formatCompact` will not know that adding another telemetry line is a
regression — it will look like a feature addition.

The same is true for the operator-side-vs-agent-side metadata split, the
capture-state metadata contract, the host-binding recovery story. None of
these are anchored. A design pass that writes them down makes future PRs
reviewable against the invariants rather than against the reviewer's memory
of this conversation.

### 3.3 Other host adapters need a target

The labeling table is explicit that it targets the shell substrate Claude Code
and Codex both share. If CALM grows adapters for other LLM hosts (Cursor,
Claude Desktop, IDE-embedded contexts), those adapters need a contract to
implement against. Today the contract is implicit in the Go code. The first
external adapter contributor reads the code, infers a contract, implements
against their inference — and almost certainly gets details wrong, because the
contract was never written down.

### 3.4 WI-60 (and friends) will paper over the wrong layer

WI-60 in the milestone tracker proposes degraded-state visibility through a
prefix string on `calm_run_command` output. That fix is correct given the
current surface, but it bakes in the response-shape-as-prose model that
§ 2.1 argues against. Shipping WI-60 before the design pass cements a
suboptimal contract.

The right order: design pass settles the response-shape contract (prose for
the agent, `_meta` for the operator-side telemetry), then WI-60 implements the
degraded-state signal in the `_meta` field where it belongs, not in the prose.

### 3.5 The dogfood discipline is a leading indicator

The repeated escalations of the dogfood bullet in CLAUDE.md (three iterations,
across at least two distinct conversations, each more binding than the last)
are an indirect measurement of ergonomic friction. Each escalation is the
project author noticing that prior directives weren't sticking. The right
intervention is not a fourth, more-binding revision of the directive — it's
fixing the surface so the directive becomes mostly self-evident.

---

## 4. Open questions the design pass should settle

These are the genuine forks the design needs to decide. They are listed as
questions, not recommendations, because the design pass is where they get
answered with discussion.

1. **Which structured tools earn their slot?** The MCP tool count has
   discovery cost. 3 structured tools? 5? Which categories from LABELING.md
   are high-frequency enough to justify exposure? Which stay in `calm_run_command`?
2. **What does `_meta` carry?** A `captured: bool`. What else? Section count?
   Source label? The full ingest summary structure? Where's the line between
   "telemetry the operator-side wants" and "what the agent will read on every
   call"?
3. **Is `calm_search` overloaded?** Should it split into `calm_recall` (one
   source, sequential, no ranking) and `calm_search` (across captures,
   ranked)? Or is a single tool with a mode parameter cleaner?
4. **What goes in the compact summary, and when?** Always boilerplate, or only
   above a size threshold? Drop terms list entirely, or move to `_meta`? What's
   the smart default for the section preview?
5. **Label hints — opt-in parameter or always-on?** If always-on, what's the
   fallback when the agent doesn't supply one? If opt-in, do agents actually
   use it?
6. **Degraded-state contract for read-side tools** (WI-59 territory) — return
   empty + degraded flag, or return error, or sentinel response? Whose contract?
7. **Host-binding stickiness** — does the adapter try to detect host-side
   death of its bound child and signal the agent to reconnect, or is that
   fundamentally outside the adapter's reach? What's the agent-side surface
   the design needs to expose to make this debuggable from inside a session?
8. **Folding LABELING.md** — does the new design doc absorb LABELING.md
   entirely, or does LABELING.md stay as a focused sub-doc the broader design
   cross-references? (The labeling grammar is durable; the question is where
   it lives.)

---

## 5. What we should NOT do without the design first

These are anti-patterns that look productive in isolation but make the eventual
design harder.

- **Add new WIs that touch the response shape** — WI-60's prefix string,
  smarter compact-summary truncation, terms-list opt-out flag. Each is
  defensible alone; together they fragment the response-shape contract.
- **Add more CLAUDE.md directives that paper over the ergonomic gap.** The
  directive layer cannot fix the surface layer.
- **Ship more tool-description nudges** ("Prefer this over the native shell").
  Same band-aid problem.
- **Add a `calm_read_file` tool as a one-off** without deciding the structured
  tool set. Adds a tool without committing to the broader cut.
- **Patch the asymmetric never-worse behavior** (WI-59 territory) at the
  individual-tool level. The asymmetry should be settled in one contract,
  applied to all read-side tools.

---

## 6. Next step

The next step is not a WI. It's a design pass that produces a focused
**MCP-surface design contract** for the adapter — sibling to LABELING.md, broader
in scope. Once that contract exists, the implementation WIs (rebalanced from the
current spawned set: WI-59, WI-60, WI-61, plus the new ones this document
implies) can land against it.

The design pass should be plan-mode, scoped to a single sitting, anchored on
the open questions in § 4 and the anti-patterns in § 5. It should produce a
document that LABELING.md cross-references, that other host adapters can
implement against, and that future PRs to `internal/adapter/mcp/` are reviewed
against.

Until that document exists, the prototype adapter ships as-is — including its
context-producer behavior, its sequential-reread gap, and its asymmetric
never-worse semantics. Those are not features. They are the visible cost of
shipping a prototype without a surface contract.
