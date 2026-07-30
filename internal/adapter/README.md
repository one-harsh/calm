<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# CALM Adapter

The adapter turns local coding-agent actions into CALM-managed context. At its core is one **capture engine**: it runs (or receives) a local action, captures the full output to a CALM session, and returns task-facing text; the agent later retrieves the captured material through the same surface. The engine is shell-agnostic and drives two shells:

- **MCP shell** (`mcp/`) — an `stdio` MCP server whose tools the host invokes directly (file read, shell command, git operation, edit). Coverage is what the host routes through the adapter's own tools, since CALM cannot see the host's native ones.
- **Capture CLI** (`capturecli/`) — the `calm-capture` binary a harness-native hook rewrites shell tool calls onto (`exec`), the agent-facing retrieval (`search`) and outcome (`feedback`) commands, the harness-facing `hook`, and the operator's `init`. Utilization is structural: the hook fires on every native shell execution.

This is one CALM workload, not the universal shape of CALM integration. The adapter solves the hardest case: a coding-agent host where CALM cannot see the host's native tools, so the adapter has to run alongside them.

## Canonical contracts

Read these before contributing to the adapter:

- [`docs/DESIGN.md`](docs/DESIGN.md) — adapter design contract: the capture engine (Part I), the shells and their command/hook/distribution surface (Part III), capture & presentation, lifecycle, observability.
- [`docs/DECISIONS.md`](docs/DECISIONS.md) — the append-only Adapter Decision Log (stable `ADnn` anchors), cited by name from DESIGN.md.
- [`docs/LABELING.md`](docs/LABELING.md) — source-label grammar (fused `<base>[#<seq>]@<token>` form, validation rules) and event-extraction contract.
- [`docs/archives/PROTOTYPE-LEARNINGS.md`](docs/archives/PROTOTYPE-LEARNINGS.md) — origin painpoints that shaped the contracts; historical record only.

## Package layout

| Path | Purpose |
|---|---|
| `capture/` | The shell-agnostic capture engine — the `Session`/`EventSink` seam, the capture pipeline, response presentation, the staleness registry, and the discovery / session-start cards. |
| `calm/` | CALM HTTP client port (Client interface, genapi wrapper, transport logging, mockery mock). |
| `mcp/` | MCP stdio protocol layer — server lifecycle, tool registry, the tool handlers (shell, retrieval, file, git), result formatting. |
| `capturecli/` | The `calm-capture` CLI shell — `exec`/`search`/`feedback`/`hook`/`init` dispatch, the PreToolUse rewrite and source-shaped session-start card injection. |
| `session/` | The CLI's on-disk session-state strategy — one directory per harness conversation under `$CALM_HOME`, crash-released advisory lock, event spool, opportunistic reclamation with no daemon (AD05). |
| `extract/` | Shell-command parsing → source label + event derivation per LABELING.md (registry of `{matcher, builder}` rules, normalization, dual-write planning). |
| `exec/` | Local process execution wrapper used by the shell-substrate tool and the CLI's `exec`. |
| `config/` | Adapter-side configuration loader (separate from CALM-server config under `cmd/calm/config/`); resolves `$CALM_HOME/adapter.yaml` when no config-file env is set. |
| `obs/` | Structured-log field keys + closed enums (degraded_reason, presentation mode), per-call measurement constants, context-bound per-call identity. |
| `docs/` | The canonical adapter contracts (see above). |

Two binary entry points over the one engine: [`cmd/calm-adapter/main.go`](../../cmd/calm-adapter/main.go) is the MCP `stdio` server an MCP host (Claude Code, Cursor, …) launches; [`cmd/calm-capture/main.go`](../../cmd/calm-capture/main.go) is the `calm-capture` CLI a harness hook and the operator invoke. Both are standalone static binaries.

## Boundary

The adapter lives under `internal/adapter` because only this repository's binaries (`cmd/calm-adapter`, `cmd/calm-capture`) consume it — CALM's public surface is the OpenAPI spec, not a client SDK (see DL09 in `docs/HLD.md`). A boundary test pins its server-package imports to the extraction-portable set so a future carve-out is a lift, not a refactor.

The adapter owns its host-facing surfaces (the MCP protocol; the hook rewrite and CLI), local action execution, capture identity, response presentation, degraded-state signaling, and event emission. It does not own CALM's namespace/session security model, indexing semantics, feedback model, or storage lifecycle — those live in [`docs/HLD.md`](../../docs/HLD.md). It does not sandbox local execution either; commands run on the developer's machine with that process's ordinary permissions.

## Build, run, test

```sh
task build:adapter       # produce bin/calm-adapter (MCP shell)
task build:capture       # produce bin/calm-capture (CLI shell)
task test:unit           # adapter unit tests (with mockery mocks)
task test:integration    # adapter integration tests against a real CALM server
task smoke:adapter       # offline MCP-protocol smoke
task closeout            # rebuild bin/calm-adapter for the next conversation
```

Operator-facing deployment instructions (binary install, MCP-host registration, environment variables) live in the top-level [`README.md`](../../README.md). Project-wide engineering disciplines (logging, transactions, comment policy, …) live in [`CLAUDE.md`](../../CLAUDE.md).

## Platform notes

The module compile-checks on linux/darwin/windows × amd64/arm64 (`task build:all`, packages and all test files). Every tool lands on something that ships with the OS: `calm_run_command` uses the platform shell (`sh -c` on Unix, `cmd /c` on Windows — named in its description) and labels cmd-native idioms (`type`, `dir`, `findstr`) with the same stable identities as their Unix equivalents; `calm_grep` selects its engine at startup (ripgrep when installed, else grep, else findstr on Windows) and names it — findstr's limited regex dialect is stated in the description. The native file tools and `calm_search` are platform-independent. Only the git tools require an external install (`git` — which a git workspace implies). Timeout kills use process groups on Unix; on Windows only the direct child is killed, with the wait still bounded.

## Related

- [`docs/HLD.md`](../../docs/HLD.md) — CALM's High-Level Design (workload-agnostic, no MCP-specific surface). The adapter is one of CALM's workloads.
- [`CONTRIBUTING.md`](../../CONTRIBUTING.md) — license, DCO sign-off, dependency policy.
