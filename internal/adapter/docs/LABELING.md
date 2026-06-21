<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Source labeling & event extraction contract

This is the canonical reference for how the MCP adapter translates a captured
command into CALM source labels and events. It is the first worked example of a
CALM workload constructing labels, so its conventions are de-facto practice for
future integrations.

The broader MCP adapter contract lives in [`DESIGN.md`](DESIGN.md). This document
owns only the source-label grammar, capture policy, and event-extraction rules.

## 1. Why this exists — the idempotent-indexing footgun

CALM's `idempotent-indexing` invariant makes a **source label both the identity
of content and the dedup key**: ingesting twice under the same label replaces the
first with the second. A naive scheme silently destroys history — two
`git diff HEAD` runs against different working-tree states, both labeled
`git diff HEAD`, collide, and the second overwrites the first. CALM cannot detect
the collision; the LLM cannot recover from it.

The fix is to stop overloading one label with three competing concepts:

- **Semantic identity** — *what information* this is (the file, the diff range).
- **Latest state** — the *newest* output for that identity (the dedup/replace target).
- **Invocation history** — the output of *one particular execution*.

CALM already separates these if used correctly: **sources** hold searchable
content snapshots (semantic identity + latest), and the capture policy decides
whether a re-run replaces the snapshot, preserves the prior one as history, or
both. **Events** hold execution chronology and cross-link the snapshots.

## 2. The structured label grammar

```
calm:v1:<domain>:<verb>:<context…>:<identity>[#<seq>][@<token>]
```

- `calm:v1` — product + grammar version. Bump the version when normalization
  changes, so old and new labels never silently alias.
- `domain` ∈ `{file, vcs, search, shell}`.
- `verb` — the operation (`read`, `list`, `git`, `grep`, `find`).
- `context…` — zero or more scoping segments (a git subcommand, a grep pattern, and
  — only when a session spans more than one workspace — a `WorkspaceID`).
- `identity` — the semantic identity (a workspace-relative path, a ref range).
- `#<seq>` — the **invocation id** (a session-local sequence allocated at
  invocation start). Present only on history/coexist sources. It is deliberately
  **not** the event ordinal: a failed or deduped event write would make that link
  inaccurate.
- `@<token>` — the **per-call staleness suffix** (6 characters of lowercase
  base32 per RFC 4648, alphabet `[a-z2-7]`, e.g. `a3f2k6`). Session-scoped
  local-validation marker; the adapter validates it without a CALM round-trip.
  The adapter always emits the fused form in recall hints; the suffix is
  optional on input — base-only labels remain valid references without
  staleness checking (shell-substrate references and programmatic clients still
  work).

The **base** label (without `#<seq>` and `@<token>`) is the latest/semantic source;
`base#<seq>` is the per-invocation history source; `@<token>` appears alongside
either form on adapter-emitted labels.

Reserved characters within a segment (`:`, `#`, `@`, space, `%`) and control bytes
are percent-encoded; multibyte UTF-8 is left intact. Over-length identities collapse
to a trailing content hash so labels stay bounded.

### Token validation rules

The adapter maintains a session-scoped registry mapping each emitted source
identity to the token currently canonical for it. Validation cases:

- **Latest source** (`<base>@<token>`): only the **current** token for `<base>`
  in this session validates. A prior token whose content has been replaced
  returns a `session_lost` degradation signal — the agent's reference is to a
  capture moment that no longer represents `<base>`.
- **History source** (`<base>#<seq>@<token>`): the token recorded at the
  invocation that created `<base>#<seq>` validates. History content is immutable
  per invocation, so the token remains stable for the session lifetime.
- **Cross-session**: any token from a different session epoch (after session
  replacement per `DESIGN.md` AD03) rejects with `session_lost`.
- **Token absent**: base-only labels forward to CALM without staleness checking.

Token collisions (two distinct identities sharing the same token) are tolerated:
validation lookup is keyed by `(base, seq if any)` first, then matched against
the token, so cross-identity collisions never alias references.

### Labeling & mode table

Examples elide the `@<token>` suffix for readability; adapter-emitted labels
always carry it (e.g., `calm:v1:file:read:foo.py@a3f2k6`).

| Tool invocation | Mode | Latest source | History source |
|---|---|---|---|
| `cat ./foo.py` | replace | `calm:v1:file:read:foo.py` | — |
| `ls src` | replace | `calm:v1:file:list:src` | — |
| `git diff main..feat` / `git diff HEAD` / `git diff` | dual | `calm:v1:vcs:git:diff:<refs>` | `…:diff:<refs>#<seq>` |
| `git status` | dual | `calm:v1:vcs:git:status` | `…:status#<seq>` |
| `git show HEAD:file` | dual | `calm:v1:vcs:git:show:HEAD%3Afile` | `…:show:HEAD%3Afile#<seq>` |
| `grep TODO src` | replace | `calm:v1:search:grep:TODO:src` | — |
| `calm_edit_file(foo.py)` | dual | `calm:v1:file:read:foo.py` | `…:edit:foo.py#<seq>` |
| `calm_write_file(foo.py)` | dual | `calm:v1:file:read:foo.py` | `…:edit:foo.py#<seq>` |
| `go test ./...` / `pytest` / `make` (runners) | coexist | — | `calm:v1:shell:<program>#<seq>` |
| unknown / pipeline | coexist | — | `calm:v1:shell:<program>#<seq>` (`sh` for pipelines) |

The structured tools above (`calm_edit_file`, `calm_write_file`, and the structured-inspection set referenced by their `cat` / `ls` / `git` / `grep` equivalents) are defined in [`DESIGN.md`](DESIGN.md)'s Tool Surface; this document owns how their captures are labeled and what events they emit.

Both `calm_edit_file` and `calm_write_file` use the same history verb (`file:edit:#<seq>`). The history source represents "post-modification snapshot of this file"; the operation type (edit / write / create) is carried in the accompanying `file_touched` event payload, not in the label.

The `WorkspaceID` context segment is omitted in the common one-workspace-per-session
case shown above. When a session binds multiple workspaces, the segment is
implementer-chosen at workspace registration (basename of root, operator-configured
name, or short hash of the root path) and stays stable for the workspace's lifetime
within the session. Collision-free identifiers across the registered set are
required; the adapter MUST refuse to bind two workspaces with conflicting IDs at
startup.

## 3. Capture policy — three modes

| Mode | Writes | Default for |
|---|---|---|
| `replace` | latest source only | file reads, directory listings, search |
| `dual` | latest source **and** an invocation-history source | all git (refs are mutable, so output isn't replace-safe); structured edit/write tools (each invocation produces semantically distinct file content) |
| `coexist` | invocation-history source only | build/test runners, arbitrary / unrecognized commands, pipelines, escaping paths, output-affecting flags |

`replace` is for stable identities whose newest output is the only one worth
keeping. `dual` is for identities whose freshness matters *and* whose individual
runs are worth preserving — it is the piece that lets stable freshness and
preserved history coexist without one clobbering the other. `coexist` forgoes
dedup entirely when no stable identity can be extracted: losing dedup on some
content is recoverable; silently overwriting semantically distinct content is not.

**Build/test runners are coexist, not a recognized category.** `go test`, `pytest`,
`make`, `cargo`, … have no content identity derivable from the command without
hardcoding per-tool knowledge (each has a different subcommand/target/operand shape),
so recognizing them by name doesn't generalize. Their output is invocation history,
which coexist captures exactly. A dedup'd "latest output" for tools is a deferred,
configurable enhancement — a generic build-tool allowlist designed once two or three
real tools confirm the operand model — not per-tool code.

Recognized commands are content-addressed (identity from file/dir/ref/pattern
operands); each is one declarative registry row, with an irregular command (git)
as a code escape hatch.

## 4. Normalization (before identity construction)

Two commands referring to the same entity must produce the same label, so
normalization happens *before* the grammar is built:

- **Workspace-relative paths.** A path is resolved against `Cwd`, then made
  relative to `WorkspaceRoot`. `cat foo.py` and `cat ./foo.py` collapse to one
  label. A path that **escapes** the root (`..`, an absolute path outside it) is
  never emitted as a stable label — it would leak host paths and collide across
  workspaces — so it falls back to `coexist`. `Cwd` alone is insufficient (it
  cannot tell a subdir path from another repo or an escape), which is why
  `WorkspaceRoot` is an explicit input. This boundary is for **labeling only,
  not access control**: an escaping command still runs and its output is still
  captured (under `coexist`). The adapter does not sandbox execution or confine
  commands to `WorkspaceRoot` — local shell access is full, by design (DL02).
- **Cosmetic flags** (`--color`, `--no-pager`) — which don't change output — are stripped so cosmetic variants share one label. **Output-affecting flags** (`-q`, `--stat`, `-l`, …) are *not* stripped: a recognized command carrying one has an ambiguous output identity (e.g. `grep -q` emits nothing), so it falls back to `coexist` rather than risk overwriting the flag-free label.
- **Whitespace** is collapsed by tokenization.
- **All operands** form the identity — `cat a b` → `calm:v1:file:read:a:b`, never just
  `read:a` — so a multi-operand command can't alias onto a single-operand label. If any
  operand can't be resolved, the whole command falls back to `coexist`.
- **Bare listings** — `ls` / `find` with no path operand list the cwd, so the cwd
  (workspace-relative) becomes the identity; otherwise the same command from two
  directories would collide on one label.
- **Glob patterns** (`*.go`) resolve to an unstable identity (the expansion is not
  fixed), so they fall back to `coexist`.

## 5. Event derivation (HLD-aligned)

Events are built from the same single parse as the labels, then finalized once the
ingest write outcomes are known. The shell substrate exposes only what a command
exit reveals; structured edit/write tools surface their own intent directly. The
adapter emits this subset of the HLD's example taxonomy:

| Event | Priority | When | Data |
|---|---|---|---|
| `tool_invocation` | 3 | always | `tool_name`, `command`, `exit_code`, `invocation_id`, `latest_source?`, `history_source?` |
| `error_observed` | 2 | non-zero exit / timeout | `message`, `source`, `exit_code`, `trace_snippet`, `invocation_id` |
| `git_operation` | 2 | git command | `command`, `subcommand`, `invocation_id` |
| `file_touched` | 1 | edit/write via `calm_edit_file` or `calm_write_file` | `path` (workspace-relative), `operation` (`edit` / `write` / `create`), `diff` (unified diff, sanitized, length-capped), `invocation_id`, `latest_source?`, `history_source?` |

`latest_source` / `history_source` cross-links are populated **only** for sources
that actually persisted, so an event never points at a write that failed.

`task_in_progress` and `delegated_work` from the HLD taxonomy are **deferred** —
they need host-native-tool visibility (subagent invocations, structured task lists)
that the current adapter surface does not expose.

## 6. Failure isolation

Per the `never-worse` invariant, no translation fault may break the agent's command:

- Derivation returns an explicit error for a genuinely untranslatable command
  (blank input); every other input yields at least a `coexist` plan and never
  panics. The error is the command handler's signal to skip CALM and return the
  raw captured output.
- The recovery boundary lives in the command handler, **after** execution, while
  the raw output is still in scope — so a derivation error, a panic, or an ingest
  failure all fall through to raw output.
- **Dual write ordering is preservation-first:** (1) history ingest, (2) latest
  ingest, (3) events. History-ok/latest-fails still leaves the output recoverable;
  history-fails/latest-ok leaves current state available; both-fail still returns
  raw output. Events are **best-effort and off the critical path** — emitted after the
  response is determined, so a slow or failed `/v1/events` never delays or breaks the
  command; cross-links point only at sources that persisted.

## 7. Persistence-safety, retention, and search tradeoffs

Labels and events are persisted and audit-logged, so three data classes are
treated distinctly:

- **Label metadata** — normalized, workspace-relative, percent-encoded, length-capped
  (overflow → trailing hash). No raw absolute paths; escaping/unstable identities
  fall back to `coexist`.
- **Event metadata** — bounded *and* sanitized. The persisted `command` is a
  summary (program + subcommand only, never the raw arg string), so an argument
  carrying a secret is never stored. `trace_snippet` is the stderr tail,
  length-capped, stripped of control bytes / invalid UTF-8, and redacted of known
  secret-bearing forms (`--password=…`, `--token …`, `Authorization: Bearer …`).
  `file_touched.diff` follows the same treatment — length-capped (truncate over
  budget, set `diff_truncated: true`), control-byte / invalid-UTF-8 stripped,
  secret-form redacted. The post-modification file content captured under
  `file:read:` and `file:edit:#<seq>` remains raw per the captured-output stance
  below; consumers needing the unredacted change retrieve it via source-scoped
  `calm_search`.
- **Captured tool output** — stored raw by design (`content-fidelity`). It can
  contain secrets, but that is the workload intentionally capturing tool output,
  which is CALM's purpose. Scrubbing it is out of scope.

**Retention.** `dual`/`coexist` history sources live until session deletion (CALM
evicts events but not sources). Acceptable because adapter sessions are ephemeral:
*invocation-history sources are retained for the session lifetime.*

**Duplicate / stale search.** Under `dual`, the newest output exists under both the
latest and a history label, so an unscoped search may return current and historical
hits. The stance: accept it, and advertise source-scoped search (`source=<base>`
targets the latest identity).

## 8. Extensibility & capture model

Rules are a registry of `{matcher, builder}`: each recognized command family pairs a
matcher with an explicit builder that produces its identity and capture mode. **Adding a
recognized command is a small builder plus a registry row** — each builder is
self-contained, so a command's full labeling behavior reads in one place rather than
being spread across shared interpreter flags.

Capture runs through the adapter's explicit shell-command tool — the substrate
both target agents share (Claude Code's `Bash`, Codex's `shell`/`exec`).
Intercepting a host's *native* tools needs per-host hooks (which an MCP server
cannot see and which Codex does not expose); that is an optional, host-specific
enhancement, out of scope here. If a hook front-end ever lands, it feeds the same
derivation additively.

## 9. Advertising to the LLM

The `calm_run_command` tool description tells the model how to retrieve captured
output: the recall hint emits a fused source label (`<base>[#<seq>]@<token>`),
which the model passes back as `source=<fused-label>` to scope a search. The
suffix gives the adapter per-call staleness validation; a stale reference returns
a clear degradation signal rather than empty results from the current session.

Base-only references (without `@<token>`) remain valid as inputs and forward to
CALM without staleness checking — useful for shell-substrate references and
programmatic clients that don't track per-call tokens.
