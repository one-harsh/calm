<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# CALM Adapter

A coding agent's tools spill a lot into its context window — a build
log, a whole file, a `git diff` — and every line rides the input on
every following turn: discounted when cached, never free, always in
the way of the model's attention. The adapter is what puts CALM in front
of that: it runs (or observes) the agent's action, captures the **full**
output into a CALM session, and hands the agent back a task-facing version
plus a label to retrieve the rest on demand — verbatim when the output is
small enough to read whole (failures included), compact when it isn't. The
raw output stays searchable in CALM; the agent's context stays lean.

Concretely: the agent runs a 5,000-line test suite. Without the adapter,
all 5,000 lines land in context. With it, the agent gets back a short
presentation and a `source=<label>`; later, if it needs the three
failing assertions, it searches that label and pulls back just those.
The same goes for file reads, greps, and git inspection.

At its core is **one capture engine**, with two front-ends over it (we
call them *shells* — not OS shells; think "entry points"). The engine
owns the capture semantics; each shell owns how a particular host reaches
it:

- **MCP server** (`mcp/`) — CALM tools a coding-agent host (Claude Code,
  Cursor, Codex, …) calls directly over the Model Context Protocol (MCP,
  how such hosts invoke external tools). What gets captured is whatever
  the host routes through *these* tools — CALM can't see the host's own
  native ones.
- **Capture CLI** (`capturecli/`) — the `calm-capture` binary that
  harness-native hooks put on *every* shell command, so capture is
  automatic — no directive, no agent cooperation. The hook uses the
  strongest mode the harness supports:

  | Harness | How capture happens |
  |---|---|
  | Claude Code | After each command runs, the hook swaps the raw output for the compact version. Permission prompts, allow rules, and approvals stay exactly as native — the hook only observes. |
  | Cursor, Codex | Their hooks can't substitute a tool's output, so the hook rewrites the command to run through `calm-capture exec`, which runs it and captures. |

  The same binary carries the full command surface:

  - `calm-capture search [source=<label>] <terms>` — retrieve captured
    output (the same primitive as the MCP shell's `calm_search`); with
    `source=` and no terms, rereads a capture in document order.
  - `calm-capture feedback <ref> <outcome>` — report an outcome
    (`success` / `retry` / `degraded`) against a captured call.
  - `calm-capture exec -- '<command>'` — run a command and capture its
    output (the rewrite target for hooks that can't substitute output).
  - `calm-capture hook` — the harness-facing entry point: reads a hook
    payload on stdin, replies on stdout. The harness invokes this, not you.
  - `calm-capture init --harness=claude` — installs the Claude Code hook
    (deployment steps in the top-level README).

  Every non-degraded capture names its source label in the presentation
  body itself (`Captured 1/1 sections under "calm:v1:shell:sh#1@ab12cd"`),
  at any output size, so recall is always discoverable from the result.
  The trailer carries the one thing the body doesn't — the outcome handle:

  ```text
  ↳ feedback: calm-capture feedback <ref>
  ```

**This is one CALM workload, not the universal shape of CALM
integration.** The adapter solves the *hardest* case — a coding-agent
host where CALM can't see the native tools, so the adapter has to run
alongside them. Other CALM workloads (say, an eval pipeline) just call
the HTTP contract directly and need none of this machinery.

**Two reader paths.** *Just want to understand what this does?* The intro
above plus [`docs/DESIGN.md`](docs/DESIGN.md) Part I (the capture engine)
is the whole orientation. *Contributing to the adapter?* Read the four
contracts below, then the package layout.

## Canonical contracts

The design lives in docs, not in this README — read these before changing
the adapter:

- [`docs/DESIGN.md`](docs/DESIGN.md) — adapter design contract: the capture engine (Part I), the shells and their command/hook/distribution surface (Part III), capture & presentation, lifecycle, observability.
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — the append-only Adapter Decision Log (stable `ADnn` anchors), cited by name from DESIGN.md.
- [`docs/LABELING.md`](docs/LABELING.md) — source-label grammar (the fused `<base>[#<seq>]@<token>` form that makes a capture retrievable, plus validation rules) and event-extraction contract.
- [`docs/archives/PROTOTYPE-LEARNINGS.md`](docs/archives/PROTOTYPE-LEARNINGS.md) — origin painpoints that shaped the contracts; historical record only.

## Package layout

*For contributors — a map of the source, not required reading to
understand what the adapter does.*

| Path | Purpose |
|---|---|
| `capture/` | The shell-agnostic capture engine — the `Session`/`EventSink` seam, the capture pipeline, response presentation, the staleness registry, and the discovery / session-start cards. |
| `calm/` | CALM HTTP client port (Client interface, genapi wrapper, transport logging, mockery mock). |
| `mcp/` | MCP stdio protocol layer — server lifecycle, tool registry, the tool handlers (shell, retrieval, file, git), result formatting. |
| `capturecli/` | The `calm-capture` CLI shell — `exec`/`search`/`feedback`/`hook`/`init` dispatch, the post-execution observation arm and pre-execution rewrite, source-shaped session-start card injection. |
| `capturecli/harness/` | The mode-shaped seam between a harness's wire format and the capture flow — neutral observation/wrap/session-start events, with per-harness parse/render (Claude today). |
| `session/` | The CLI's on-disk session-state strategy — one directory per harness conversation under `$CALM_HOME`, crash-released advisory lock, event spool, opportunistic reclamation with no daemon (AD05). |
| `extract/` | Shell-command parsing → source label + event derivation per LABELING.md (registry of `{matcher, builder}` rules, normalization, dual-write planning). |
| `exec/` | Local process execution wrapper used by the shell-substrate tool and the CLI's `exec`. |
| `config/` | Adapter-side configuration loader (separate from CALM-server config under `cmd/calm/config/`); resolves `$CALM_HOME/adapter.yaml` when no config-file env is set. |
| `obs/` | Structured-log field keys + closed enums (degraded_reason, presentation mode), per-call measurement constants, context-bound per-call identity. |
| `docs/` | The canonical adapter contracts (see above). |

Two binary entry points over the one engine:
[`cmd/calm-adapter/main.go`](../../cmd/calm-adapter/main.go) is the MCP
`stdio` server an MCP host launches;
[`cmd/calm-capture/main.go`](../../cmd/calm-capture/main.go) is the
`calm-capture` CLI a harness hook and the operator invoke. Both are
standalone static binaries.

## Boundary

The adapter lives under `internal/adapter` because only this repository's
binaries (`cmd/calm-adapter`, `cmd/calm-capture`) consume it — CALM's
public surface is the OpenAPI spec, not a client SDK. (A boundary test
keeps its imports carve-out-ready, so the engine could later ship as its
own library without a rewrite.)

The adapter **owns** its host-facing surfaces (the MCP protocol; the hook
rewrite and CLI), local action execution, capture identity, response
presentation, degraded-state signaling, and event emission. It does
**not** own CALM's namespace/session security model, indexing semantics,
feedback model, or storage lifecycle — those live in
[`docs/HLD.md`](../../docs/HLD.md). It does not sandbox local execution
either; commands run on the developer's machine with that process's
ordinary permissions.

## Build, run, test

```sh
task build:adapter       # produce bin/calm-adapter (MCP shell)
task build:capture       # produce bin/calm-capture (CLI shell)
task test:unit           # adapter unit tests (with mockery mocks)
task test:integration    # adapter integration tests against a real CALM server
task smoke:adapter       # offline MCP-protocol smoke
task closeout            # rebuild bin/calm-adapter for the next conversation
```

Operator-facing deployment (binary install, MCP-host registration,
environment variables) lives in the top-level
[`README.md`](../../README.md). Project-wide engineering disciplines
(logging, transactions, comment policy, …) live in
[`CLAUDE.md`](../../CLAUDE.md).

## Platform notes

The adapter runs cross-platform — it compile-checks on
linux/darwin/windows × amd64/arm64 (`task build:all`), and every tool
lands on something that ships with the OS (only the git tools need an
external `git`). The per-tool specifics — the shell each `exec` uses,
`calm_grep`'s engine selection, cmd-native idiom labeling, and the bounded
Windows timeout — are stated in each tool's own description, which is
where an integrator actually reads them.

## Related

- [`docs/HLD.md`](../../docs/HLD.md) — CALM's High-Level Design (workload-agnostic, no MCP-specific surface). The adapter is one of CALM's workloads.
- [`CONTRIBUTING.md`](../../CONTRIBUTING.md) — license, DCO sign-off, dependency policy.
