<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Paired-run benchmark — pre-sweep spike record (S1–S4)

All four spikes ran live on **2026-08-03**, **claude CLI 2.1.212**, **macOS**
(Darwin 24.6). Specimen transcripts live beside this file under
`specimens/*.jsonl` and are the extractor's testdata (`extract_test.py`). Every
operational fact below is baked into the harness code; this file is the durable
record of how it was established.

The spikes existed to de-risk five unknowns the runner depends on: auth in an
isolated config home, what `CLAUDE_CONFIG_DIR` actually isolates, the per-cell
environment scrub, the transcript JSONL contract, and headless permission
behaviour.

---

## AUTH — isolated homes are not logged in; use an OAuth token env var

A fresh `CLAUDE_CONFIG_DIR` home is **not** authenticated. On macOS the
credential lives in the login keychain, which an isolated home cannot read;
copying `.claude.json` across is insufficient.

**Sanctioned path:** `CLAUDE_CODE_OAUTH_TOKEN` (produced by
`claude setup-token`). The token is kept in a user-held file
(default `~/.claude/benchmark-oauth-token`, mode `0600`). The runner takes the
**path** via config, reads it at spawn time, and injects it into the child
process env **only** — the value never appears in logs, manifests, argv, or a
trace artifact. `runner.read_oauth_token` refuses a file that is not `0600`.

## S2 — what `CLAUDE_CONFIG_DIR` isolates (proven)

`CLAUDE_CONFIG_DIR` isolates, per home:

- MCP registrations (`claude mcp add` writes to that home's `.claude.json`),
- `settings.json`,
- plugins (marketplace + install state),
- transcripts (`<config_dir>/projects/…`).

`.claude.json` lives **inside** the config dir. Repo-level bleed was checked:
the calm repo carries no tracked `.mcp.json` or `.claude/settings*` files — but
the arm self-check still asserts none appear in the pinned clone.

Consequence in code: each arm is one `CLAUDE_CONFIG_DIR`; `arms.arm_self_check`
inspects `<config_dir>/.claude.json` + `<config_dir>/settings.json` to prove
arm-1 exposes no calm surface, arm-2 is MCP-only, arm-3 is plugin-only.

## Per-cell environment scrub (proven recipe)

Unset before every cell (host Claude Code / CALM state that would leak in):

    CLAUDECODE  CLAUDE_CODE_ENTRYPOINT  CLAUDE_CODE_SESSION_ID
    CLAUDE_CODE_SSE_PORT  CLAUDE_PROJECT_DIR  CALM_ADAPTER_CONFIG_FILE
    CALM_CAPTURE_ACTIVE

**`CALM_ADAPTER_CONFIG_FILE` is critical:** calm-capture consults it **before**
`$CALM_HOME/adapter.yaml`, so a leaked value silently redirects config and
logging. The runner also clears `GH_TOKEN`/`GITHUB_TOKEN`, sets
`GIT_TERMINAL_PROMPT=0`, and drops the host `CLAUDE_CONFIG_DIR`/`CALM_HOME`
(set fresh per cell). See `runner.SCRUB_VARS` / `runner.scrub_env`.

Hook-arm cells additionally set `CALM_HOME=<cell calm home>` and
`CALM_ADAPTER_LOG_FILE=<cell calm home>/logs/calm-capture.log` **explicitly** —
default-path log creation can silently degrade to discard under sandboxes.

## S3 — transcript JSONL contract (proven; specimens are the testdata)

Location: `$CLAUDE_CONFIG_DIR/projects/<cwd with '/' → '-'>/<session-uuid>.jsonl`
(`runner.transcript_path`; `runner.newest_transcript` is the fallback).

Record `type` values seen: `user`, `assistant`, `attachment`,
`queue-operation`, `ai-title`, `last-prompt`. The extractor's
`KNOWN_RECORD_TYPES` also admits `summary`/`system` (compaction/lifecycle) so a
real run does not fail loud on them; anything else **fails loud**
(`extract.UnknownRecordShape`, `extract_test.test_unknown_record_type_fails_loud`).

Load-bearing detail — **usage must be aggregated per unique `message.id`:**
one API message emits MULTIPLE assistant records (a thinking-block record and a
text/tool_use-block record), each repeating the SAME full `usage` object.
`specimens/s4-write-allowed.jsonl` proves it: three records share
`msg_…4oDbe97H9tuiXWzt`, each carrying `output_tokens=313` — counted once yields
408 total, summing raw records yields 721. `usage` fields consumed:
`input_tokens`, `output_tokens`, `cache_creation_input_tokens`,
`cache_read_input_tokens`, `cache_creation{ephemeral_1h,5m}`, `iterations[]`.

- `tool_use` blocks live in assistant record content (`name` + `input`); one
  block = one call (three parallel blocks = three calls).
- `tool_result` lives in user records' content list (`content` is a string;
  `is_error` present-or-absent). Bytes-served-per-call = UTF-8 length of that
  string (post-replacement for the hook arm).

Specimen-pinned totals (`extract_test.py`):

| specimen | Σ output_tokens | calls | bytes served | denied |
|---|---:|---:|---:|---:|
| s2-trivial-ok | 41 | 0 | 0 | 0 |
| s4-bash-autoallow | 277 | 1 | 102 | 0 |
| s4-write-allowed | 408 | 2 | 248 | 0 |
| s4-write-denied | 755 | 2 | 565 | 2 |

## S4 — headless permissions (proven)

Under `claude -p`:

- read-only Bash (e.g. the classifier `ls -la`) **auto-allows** — no prompt.
- write-Bash (`touch …`) and the `Write` tool **gracefully DENY** without an
  allowlist: no hang, denial text is returned to the model, process exits 0.
  In the transcript the denied `tool_result` carries `is_error: true` and the
  user record carries `toolDenialKind: "user-rejected"`
  (`specimens/s4-write-denied.jsonl`).

Preseed surface that works: `<config_dir>/settings.json`
`{"permissions": {"allow": [...]}}`. All arms get an **identical** posture
modulo their own tool surface (arm-2 adds `mcp__calm__*`). `git push` /
`git remote` / `gh` are never allowlisted and are additionally denied
(`arms.BASE_DENY`). **No skip-permission flags** — they would change agent
behaviour vs. real usage.

## S1 — hook arm: SessionStart under `-p`, provisioning, detection

- **SessionStart DOES fire under `-p`** (verified), injecting the retrieval
  card. It leaves **no** capture-log line and creates **no** CALM session
  (capture is lazy on first tool call). Its only transcript footprint is a hook
  attachment record (`hook_success`, `hookEvent=SessionStart`) — the extractor's
  teaching-state detection keys on that (`extract._teaching_state`).
- **Provisioning order** (proven): (1) `calm-capture init --harness=claude` with
  `CALM_HOME=<cell calm home>` + `CALM_ADAPTER_CALM_URL/CLIENT/API_KEY` env
  writes `adapter.yaml`, `0600` credentials, and the plugin bundle at
  `$CALM_HOME/plugins/claude`; (2) `claude plugin marketplace add
  $CALM_HOME/plugins/claude` under the arm `CLAUDE_CONFIG_DIR`; (3)
  `claude plugin install calm-capture`; (4) verify via `claude plugin list`,
  enabling with `claude plugin enable calm-capture@calm-capture --scope user`
  if needed; (5) merge permissions into `settings.json` **after** the plugin
  steps via read-modify-write — the plugin CLI writes `enabledPlugins` /
  `extraKnownMarketplaces` into the same file; **never blind-overwrite it**
  (`arms.hook_provision_steps` + `arms.merge_permissions_into_settings`).
- **Small-output nuance:** outputs ≤512 B are presented inline — raw text
  retained, discovery card / trailer appended — so `visible_bytes` can EXCEED
  `raw_bytes` on small calls (914 vs 14 observed). Decomposition must not treat
  visible>raw as an error; bytes-served remains the UTF-8 length of the
  post-replacement `tool_result`
  (`extract_test.test_small_output_visible_exceeds_raw_is_not_an_error`).

## Correlations pull (probe-verified)

`correlations.correlation_id` is `BYTEA` (raw 16-byte UUIDv7); the adapter logs
the canonical UUID text. Join:

    WHERE correlation_id = decode(replace(<uuid>, '-', ''), 'hex')

encoded as `extract.correlation_id_to_bytea` + `build_correlations_by_ids`.
`request_type` is `CHECK IN ('ingest','search','snapshot')`. The pull is
**read-only SELECTs** against the dev-compose Postgres (`postgres/postgres`, db
`calm`, `localhost:5432`); `psycopg` is imported lazily so the offline tests run
with no driver installed. A `client + created_at`-window join through `sessions`
is the aggregate fallback when no correlation ids were harvested from the log.

## MCP arm session survival (probe-verified)

Adapter env `CALM_ADAPTER_CALM_KEEP_SESSION=true` skips the shutdown
`DeleteSession`, so the session + its correlation rows survive a clean
`claude -p` exit — required because extraction runs post-cell. The runner
**never** deletes a CALM session; TTL (≥1440 min for the bench namespace)
reclaims it. Join integrity is guarded by asserting exactly one new session
appears in the post-cell snapshot (`extract.assert_one_new_session`).

The snapshot is a read-only DB query scoped to the dedicated **benchmark
namespace** (`/v1/manage/sessions` is a 501 stub), not the client tag: the MCP
arm's sessions land with `client='claude-code'` because the adapter prefers the
MCP handshake's `clientInfo.name` over the configured tag, and every Claude Code
instance self-identifies identically. Cells are strictly serial, so within the
bench namespace the before/after set difference is unambiguous; the correlations
pull then keys off the surfaced `session_id`.

## Operational notes for the sweep operator

- **Benchmark adapters are a distinct binary.** The runner copies `adapter_bin`
  to `<work_root>/calm-adapter-bench` and registers that; the stray-adapter
  assert/reap only ever match that path, so a developer's own live `calm-adapter`
  (and its CALM session) is never touched.
- **A retry reuses the cell id**, so the runner clears `home-<cid>` and
  `calm-<cid>` at the start of every attempt — otherwise the failed attempt's
  `calm-capture.log` would accumulate and the correlation-id harvest would
  double-count. Terminal status after one retry is `invalid`.
- **`work_root` accumulates across cells.** `home-<cid>` (transcripts) and
  `calm-<cid>` (adapter config + logs) persist per cell for post-hoc debugging;
  the archived trace already copies the transcript into the output dir. Over a
  full sweep (18+ cells × reps) these add up — periodically prune `work_root`
  between sweeps, keeping only the trace `output_dir`.
- **Timeouts kill the whole process tree.** `claude -p` runs in its own session
  (`start_new_session=True`); on timeout the runner SIGKILLs the process group,
  so grandchildren (`go test`, `task`) can't orphan and keep hammering Postgres.
- **Staged reps are report-driven.** `runner.py reps --from-report <report.json>`
  runs +2 reps for every task the report flags `needs_more_reps` (a CALM-arm
  ratio within ±0.15 of a gate boundary), all arms paired, at rep indices past
  the existing maximum so nothing is overwritten.
