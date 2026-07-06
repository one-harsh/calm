<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# CALM MCP Adapter

The MCP adapter that turns local coding-agent actions into CALM-managed context. An MCP host calls one of the adapter's tools; the adapter runs the underlying local action (file read, shell command, git operation, edit), captures the output to a CALM session, and returns task-facing text. The agent later searches the captured material through the same surface.

This is one CALM workload, not the universal shape of CALM integration. The adapter solves the hardest case: a coding-agent host where CALM cannot see the host's native tools, so the adapter has to expose its own.

## Canonical contracts

Read these before contributing to the adapter:

- [`docs/DESIGN.md`](docs/DESIGN.md) — adapter design contract: tool surface, capture & presentation, lifecycle, observability, Adapter Decision Log.
- [`docs/LABELING.md`](docs/LABELING.md) — source-label grammar (fused `<base>[#<seq>]@<token>` form, validation rules) and event-extraction contract.
- [`docs/archives/PROTOTYPE-LEARNINGS.md`](docs/archives/PROTOTYPE-LEARNINGS.md) — origin painpoints that shaped the contracts; historical record only.

## Package layout

| Path | Purpose |
|---|---|
| `calm/` | CALM HTTP client port (Client interface, genapi wrapper, transport logging, mockery mock). |
| `mcp/` | MCP stdio protocol layer — server lifecycle, tool registry, handlers (`run_command`, `search`), result formatting. |
| `extract/` | Shell-command parsing → source label + event derivation per LABELING.md (registry of `{matcher, builder}` rules, normalization, dual-write planning). |
| `exec/` | Local process execution wrapper used by the shell-substrate tool. |
| `config/` | Adapter-side configuration loader (separate from CALM-server config under `cmd/calm/config/`). |
| `obs/` | Structured-log field keys + closed enums (degraded_reason, presentation mode), per-call measurement constants, context-bound per-call identity. |
| `docs/` | The canonical adapter contracts (see above). |

The binary entry point is [`cmd/calm-adapter/main.go`](../../cmd/calm-adapter/main.go). The adapter ships as a standalone static binary; an MCP host (Claude Code, Cursor, …) launches it as an `stdio` server.

## Boundary

The adapter lives under `internal/adapter` because only `cmd/calm-adapter` consumes it — CALM's public surface is the OpenAPI spec, not a client SDK (see DL09 in `docs/HLD.md`). A boundary test pins its server-package imports to the extraction-portable set so a future carve-out is a lift, not a refactor.

The adapter owns the MCP protocol surface, local action execution, capture identity, response presentation, degraded-state signaling, and event emission. It does not own CALM's namespace/session security model, indexing semantics, feedback model, or storage lifecycle — those live in [`docs/HLD.md`](../../docs/HLD.md). It does not sandbox local execution either; commands run on the developer's machine with that process's ordinary permissions.

## Build, run, test

```sh
task build:adapter       # produce bin/calm-adapter
task test:unit           # adapter unit tests (with mockery mocks)
task test:integration    # adapter integration tests against a real CALM server
task smoke:adapter       # offline MCP-protocol smoke
```

Operator-facing deployment instructions (binary install, MCP-host registration, environment variables) live in the top-level [`README.md`](../../README.md). Project-wide engineering disciplines (logging, transactions, comment policy, …) live in [`CLAUDE.md`](../../CLAUDE.md).

## Platform notes

The module compile-checks on linux/darwin/windows × amd64/arm64 (`task build:all`, packages and all test files). Every tool lands on something that ships with the OS: `calm_run_command` uses the platform shell (`sh -c` on Unix, `cmd /c` on Windows — named in its description) and labels cmd-native idioms (`type`, `dir`, `findstr`) with the same stable identities as their Unix equivalents; `calm_grep` selects its engine at startup (ripgrep when installed, else grep, else findstr on Windows) and names it — findstr's limited regex dialect is stated in the description. The native file tools and `calm_search` are platform-independent. Only the git tools require an external install (`git` — which a git workspace implies). Timeout kills use process groups on Unix; on Windows only the direct child is killed, with the wait still bounded.

## Related

- [`docs/HLD.md`](../../docs/HLD.md) — CALM's High-Level Design (workload-agnostic, no MCP-specific surface). The adapter is one of CALM's workloads.
- [`CONTRIBUTING.md`](../../CONTRIBUTING.md) — license, DCO sign-off, dependency policy.
