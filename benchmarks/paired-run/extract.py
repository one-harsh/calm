# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Per-cell trace extraction for the paired-run benchmark.

Two sources feed one trace JSON per cell:

1. The Claude Code transcript JSONL — tokens, calls, bytes-served, and the
   derived behavioural measures (read-before-edit pairs, post-compaction
   re-reads, capture-probe loops).
2. CALM — the management-API session snapshot plus a read-only correlations
   pull over the dev-compose Postgres.

Transcript contract facts (verified live 2026-08-03, CLI 2.1.212; see
spikes/NOTES.md), encoded here:

* One API message emits MULTIPLE assistant records (a thinking-block record and
  a text/tool_use-block record), each repeating the SAME full ``usage`` object.
  Usage is therefore aggregated per unique ``message.id`` — summing raw records
  multiple-counts output tokens.
* ``tool_use`` blocks live in assistant record content (name + input);
  ``tool_result`` lives in user records' content list (a string, with
  ``is_error`` present-or-absent).
* Unknown top-level record types or content-block types FAIL LOUD — a silent
  skip would miscount, and the format is version-pinned in the run manifest.
"""

from __future__ import annotations

import json
import os
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

# --- transcript record contract -------------------------------------------

KNOWN_RECORD_TYPES = frozenset(
    {
        "user",
        "assistant",
        "attachment",
        "queue-operation",
        "ai-title",
        "last-prompt",
        # Compaction / lifecycle records. Kept in the known set so a real run
        # does not fail loud on them; treated as potential compaction boundaries.
        "summary",
        "system",
    }
)
KNOWN_ASSISTANT_BLOCKS = frozenset({"thinking", "redacted_thinking", "text", "tool_use", "server_tool_use"})
KNOWN_USER_BLOCKS = frozenset({"tool_result", "text", "image"})

# --- tool-shape classification --------------------------------------------

READ_TOOLS = frozenset({"Read", "mcp__calm__calm_read_file"})
EDIT_TOOLS = frozenset({"Edit", "NotebookEdit", "mcp__calm__calm_edit_file"})
WRITE_TOOLS = frozenset({"Write", "mcp__calm__calm_write_file"})
GREP_TOOLS = frozenset({"Grep", "mcp__calm__calm_grep"})
LIST_TOOLS = frozenset({"LS", "Glob", "mcp__calm__calm_list_dir"})
COMMAND_TOOLS = frozenset({"Bash", "mcp__calm__calm_run_command"})
SEARCH_TOOLS = frozenset({"mcp__calm__calm_search"})

SHAPE_READ = "read"
SHAPE_EDIT = "edit"
SHAPE_WRITE = "write"
SHAPE_GREP = "grep"
SHAPE_LIST = "list"
SHAPE_COMMAND = "command"
SHAPE_SEARCH = "search"
SHAPE_OTHER = "other"

# SessionStart hook fires under -p but leaves NO capture-log line: its only
# transcript footprint is a hook attachment record. Detection keys on that.
CALM_TOOL_PREFIX = "mcp__calm__"

# A denied tool is distinguished by an explicit denial signal — never by
# is_error alone (a plain failing command must not read as "denied", S4).
DENIAL_MARKERS = (
    "was blocked. For security",
    "haven't granted it yet",
    "requested permissions to",
)


class UnknownRecordShape(Exception):
    """Raised when a transcript record carries an unrecognised shape (fail loud)."""


class TranscriptError(Exception):
    """Raised for a structurally invalid transcript."""


@dataclass
class ToolCall:
    order: int
    message_id: str
    tool_use_id: str
    name: str
    shape: str
    identity: str
    input: dict[str, Any]
    grep_paths: tuple[str, ...] = ()
    result_bytes: int = 0
    is_error: bool = False
    denied: bool = False
    post_compaction: bool = False
    segment: int = 0  # number of compaction boundaries preceding this call.


@dataclass
class TeachingState:
    sessionstart_hook_fired: bool = False
    calm_tool_used: bool = False
    retrieval_card_seen: bool = False


@dataclass
class TranscriptMeasures:
    session_id: str = ""
    cwd: str = ""
    model: str = ""
    cli_version: str = ""
    # Gate metric and context-pressure diagnostics (usage deduped by message id).
    output_tokens: int = 0
    input_tokens: int = 0
    cache_creation_tokens: int = 0
    cache_read_tokens: int = 0
    assistant_messages: int = 0
    # Calls and bytes.
    call_count: int = 0
    calls_by_shape: dict[str, int] = field(default_factory=dict)
    bytes_served_total: int = 0
    bytes_served_by_shape: dict[str, int] = field(default_factory=dict)
    tool_error_count: int = 0
    denied_count: int = 0
    # Derived behavioural measures.
    read_before_edit_pairs: int = 0
    post_compaction_rereads: int = 0
    capture_probe_loops: int = 0
    compaction_boundaries: int = 0
    teaching: TeachingState = field(default_factory=TeachingState)

    def as_dict(self) -> dict[str, Any]:
        data = asdict(self)
        return data


def load_transcript(path: str | Path) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    with open(path, "r", encoding="utf-8") as handle:
        for line_no, line in enumerate(handle, start=1):
            line = line.strip()
            if not line:
                continue
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError as err:
                raise TranscriptError(f"{path}:{line_no}: invalid JSON: {err}") from err
    return records


def parse_transcript(records: list[dict[str, Any]]) -> TranscriptMeasures:
    _validate_record_shapes(records)

    measures = TranscriptMeasures()
    _fill_run_metadata(records, measures)

    # 1) Usage, deduped by message.id.
    seen_usage: set[str] = set()
    for record in records:
        if record.get("type") != "assistant":
            continue
        # message.id is guaranteed present by _validate_record_shapes.
        message = record.get("message") or {}
        message_id = str(message.get("id"))
        if message_id in seen_usage:
            continue
        seen_usage.add(message_id)
        usage = message.get("usage") or {}
        measures.output_tokens += int(usage.get("output_tokens", 0) or 0)
        measures.input_tokens += int(usage.get("input_tokens", 0) or 0)
        measures.cache_creation_tokens += int(usage.get("cache_creation_input_tokens", 0) or 0)
        measures.cache_read_tokens += int(usage.get("cache_read_input_tokens", 0) or 0)
    measures.assistant_messages = len(seen_usage)

    # 2) Calls (tool_use blocks), in document order, with compaction segmenting.
    calls = _extract_calls(records)
    _attach_tool_results(records, calls)

    measures.call_count = len(calls)
    for call in calls:
        measures.calls_by_shape[call.shape] = measures.calls_by_shape.get(call.shape, 0) + 1
        measures.bytes_served_total += call.result_bytes
        measures.bytes_served_by_shape[call.shape] = (
            measures.bytes_served_by_shape.get(call.shape, 0) + call.result_bytes
        )
        if call.is_error:
            measures.tool_error_count += 1
        if call.denied:
            measures.denied_count += 1

    measures.compaction_boundaries = sum(1 for r in records if _is_compaction_boundary(r))
    measures.read_before_edit_pairs = _count_read_before_edit(calls)
    measures.capture_probe_loops = _count_capture_probe_loops(calls)
    measures.post_compaction_rereads = _count_post_compaction_rereads(calls)

    # 3) Teaching state (arm-diagnostic; recorded, never assumed).
    measures.teaching = _teaching_state(records, calls)
    return measures


def _validate_record_shapes(records: list[dict[str, Any]]) -> None:
    for index, record in enumerate(records):
        rtype = record.get("type")
        if rtype not in KNOWN_RECORD_TYPES:
            raise UnknownRecordShape(f"record #{index}: unknown record type {rtype!r}")
        message = record.get("message")
        if rtype == "assistant":
            if not isinstance(message, dict) or not message.get("id"):
                # The gate metric is Σ output_tokens deduped by message.id. A
                # missing id would force a per-record fallback that double-counts
                # (each assistant API message emits several records). Fail loud —
                # this is CLI-format drift the version pin exists to catch.
                raise UnknownRecordShape(f"record #{index}: assistant record missing message.id")
            for block in message.get("content", []) or []:
                if isinstance(block, dict) and block.get("type") not in KNOWN_ASSISTANT_BLOCKS:
                    raise UnknownRecordShape(
                        f"record #{index}: unknown assistant content block {block.get('type')!r}"
                    )
        if rtype == "user" and isinstance(message, dict):
            content = message.get("content")
            if isinstance(content, list):
                for block in content:
                    if isinstance(block, dict) and block.get("type") not in KNOWN_USER_BLOCKS:
                        raise UnknownRecordShape(
                            f"record #{index}: unknown user content block {block.get('type')!r}"
                        )


def _fill_run_metadata(records: list[dict[str, Any]], measures: TranscriptMeasures) -> None:
    for record in records:
        if not measures.cwd and record.get("cwd"):
            measures.cwd = str(record["cwd"])
        if not measures.cli_version and record.get("version"):
            measures.cli_version = str(record["version"])
        if not measures.session_id and record.get("sessionId"):
            measures.session_id = str(record["sessionId"])
        message = record.get("message")
        if not measures.model and isinstance(message, dict) and message.get("model"):
            measures.model = str(message["model"])


def _is_compaction_boundary(record: dict[str, Any]) -> bool:
    if record.get("isCompactSummary") is True:
        return True
    rtype = record.get("type")
    if rtype == "summary":
        return True
    if rtype == "system" and "compact" in str(record.get("subtype", "")).lower():
        return True
    attachment = record.get("attachment")
    if isinstance(attachment, dict) and "compact" in str(attachment.get("type", "")).lower():
        return True
    return False


def _classify_tool(name: str, tool_input: dict[str, Any]) -> tuple[str, str, tuple[str, ...]]:
    """Return (shape, identity, grep_paths)."""
    path_identity = _path_from_input(tool_input)
    if name in READ_TOOLS:
        return SHAPE_READ, path_identity, ()
    if name in EDIT_TOOLS:
        return SHAPE_EDIT, path_identity, ()
    if name in WRITE_TOOLS:
        return SHAPE_WRITE, path_identity, ()
    if name in GREP_TOOLS:
        # Native Grep uses "path" (singular); calm_grep uses "paths" (list).
        # Read both so grep-shaped capture-probe loops are counted for every arm.
        raw_paths = list(tool_input.get("paths", []) or [])
        if tool_input.get("path"):
            raw_paths.append(tool_input["path"])
        paths = tuple(_normalize_path(str(p)) for p in raw_paths)
        return SHAPE_GREP, str(tool_input.get("pattern", "")), paths
    if name in LIST_TOOLS:
        return SHAPE_LIST, path_identity, ()
    if name in COMMAND_TOOLS:
        command = str(tool_input.get("command", ""))
        return SHAPE_COMMAND, " ".join(command.split()), ()
    if name in SEARCH_TOOLS:
        identity = str(tool_input.get("source") or ";".join(tool_input.get("queries", []) or []))
        return SHAPE_SEARCH, identity, ()
    return SHAPE_OTHER, name, ()


def _path_from_input(tool_input: dict[str, Any]) -> str:
    for key in ("file_path", "path", "notebook_path"):
        if tool_input.get(key):
            return _normalize_path(str(tool_input[key]))
    return ""


def _normalize_path(path: str) -> str:
    if not path:
        return ""
    return os.path.normpath(path)


def _extract_calls(records: list[dict[str, Any]]) -> list[ToolCall]:
    calls: list[ToolCall] = []
    order = 0
    boundary_count = 0
    for record in records:
        if _is_compaction_boundary(record):
            boundary_count += 1
        if record.get("type") != "assistant":
            continue
        message = record.get("message") or {}
        message_id = str(message.get("id"))
        for block in message.get("content", []) or []:
            if not isinstance(block, dict) or block.get("type") != "tool_use":
                continue
            name = str(block.get("name", ""))
            tool_input = block.get("input") or {}
            if not isinstance(tool_input, dict):
                tool_input = {}
            shape, identity, grep_paths = _classify_tool(name, tool_input)
            calls.append(
                ToolCall(
                    order=order,
                    message_id=message_id,
                    tool_use_id=str(block.get("id", "")),
                    name=name,
                    shape=shape,
                    identity=identity,
                    input=tool_input,
                    grep_paths=grep_paths,
                    post_compaction=boundary_count > 0,
                    segment=boundary_count,
                )
            )
            order += 1
    return calls


def _attach_tool_results(records: list[dict[str, Any]], calls: list[ToolCall]) -> None:
    by_id = {call.tool_use_id: call for call in calls if call.tool_use_id}
    for record in records:
        if record.get("type") != "user":
            continue
        message = record.get("message") or {}
        content = message.get("content")
        if not isinstance(content, list):
            continue
        denial_kind = record.get("toolDenialKind")
        for block in content:
            if not isinstance(block, dict) or block.get("type") != "tool_result":
                continue
            call = by_id.get(block.get("tool_use_id"))
            if call is None:
                continue
            block_content = block.get("content")
            call.result_bytes = _result_bytes(block_content)
            call.is_error = bool(block.get("is_error", False))
            call.denied = call.denied or _is_denial(denial_kind, block_content)


def _is_denial(denial_kind: Any, content: Any) -> bool:
    """A permission denial — not any failing tool call. Requires the explicit
    denial signal (toolDenialKind) or the documented denial-text shape; is_error
    alone (e.g. a failing command) does NOT count."""
    if denial_kind:
        return True
    return isinstance(content, str) and any(marker in content for marker in DENIAL_MARKERS)


def _result_bytes(content: Any) -> int:
    """Bytes-served-per-call = UTF-8 length of the tool_result content string.

    For the hook arm this is post-replacement text (AD08: replacement
    precedes storage). Small outputs may render visible>raw (discovery card /
    trailer appended) — that is not an error; the count is exactly what entered
    the context window.
    """
    if content is None:
        return 0
    if isinstance(content, str):
        return len(content.encode("utf-8"))
    # Structured content (list of blocks) — sum the text payloads.
    total = 0
    if isinstance(content, list):
        for block in content:
            if isinstance(block, dict) and isinstance(block.get("text"), str):
                total += len(block["text"].encode("utf-8"))
            elif isinstance(block, str):
                total += len(block.encode("utf-8"))
    return total


def _count_read_before_edit(calls: list[ToolCall]) -> int:
    """One pair per edit whose nearest prior touch of the path is a read.

    Native Edit's mandated prior Read scores a pair; calm_edit_file without a
    prior read scores none — that asymmetry is the measurement's point.
    """
    pairs = 0
    last_touch: dict[str, str] = {}
    for call in calls:
        if call.shape == SHAPE_EDIT and call.identity:
            if last_touch.get(call.identity) == SHAPE_READ:
                pairs += 1
        if call.shape in (SHAPE_READ, SHAPE_EDIT, SHAPE_WRITE) and call.identity:
            last_touch[call.identity] = call.shape
    return pairs


def _count_capture_probe_loops(calls: list[ToolCall]) -> int:
    """One loop per read/grep-shaped call targeting a path edited in the prior 3 calls."""
    loops = 0
    for i, call in enumerate(calls):
        if call.shape not in (SHAPE_READ, SHAPE_GREP):
            continue
        window = calls[max(0, i - 3):i]
        edited = {
            c.identity
            for c in window
            if c.shape in (SHAPE_EDIT, SHAPE_WRITE) and c.identity
        }
        if not edited:
            continue
        if call.shape == SHAPE_READ and call.identity in edited:
            loops += 1
        elif call.shape == SHAPE_GREP and edited.intersection(call.grep_paths):
            loops += 1
    return loops


def _count_post_compaction_rereads(calls: list[ToolCall]) -> int:
    """One re-read per read/command-shaped call after a compaction boundary whose
    identity was already read/run at or before the MOST RECENT boundary.

    ToolCall.segment counts boundaries preceding a call, so the snapshot advances
    at every boundary — a run with several compactions measures each re-read
    against the boundary immediately before it, not just the first one.
    """
    rereads = 0
    seen: set[str] = set()
    seen_at_last_boundary: set[str] = set()
    current_segment = 0
    for call in calls:
        if call.segment > current_segment:
            seen_at_last_boundary = set(seen)
            current_segment = call.segment
        if call.shape not in (SHAPE_READ, SHAPE_COMMAND) or not call.identity:
            continue
        if call.segment > 0 and call.identity in seen_at_last_boundary:
            rereads += 1
        seen.add(call.identity)
    return rereads


def _teaching_state(records: list[dict[str, Any]], calls: list[ToolCall]) -> TeachingState:
    state = TeachingState()
    for record in records:
        if record.get("type") != "attachment":
            continue
        attachment = record.get("attachment")
        if not isinstance(attachment, dict):
            continue
        hook_event = str(attachment.get("hookEvent", ""))
        atype = str(attachment.get("type", ""))
        if hook_event == "SessionStart" or "hook" in atype.lower():
            state.sessionstart_hook_fired = True
        content = attachment.get("content")
        if isinstance(content, str) and "calm_search" in content:
            state.retrieval_card_seen = True
    state.calm_tool_used = any(call.name.startswith(CALM_TOOL_PREFIX) for call in calls)
    return state


# --- CALM side: manage snapshot delta -------------------------------------


def session_key(session: dict[str, Any]) -> tuple[str, str]:
    return (str(session.get("client", "")), str(session.get("created_at", "")))


def new_sessions(before: list[dict[str, Any]], after: list[dict[str, Any]]) -> list[dict[str, Any]]:
    before_keys = {session_key(s) for s in before}
    return [s for s in after if session_key(s) not in before_keys]


def assert_one_new_session(before: list[dict[str, Any]], after: list[dict[str, Any]]) -> dict[str, Any]:
    fresh = new_sessions(before, after)
    if len(fresh) != 1:
        raise TranscriptError(
            f"expected exactly one new session for this cell, found {len(fresh)} "
            "(client+serial join is ambiguous — cell invalid)"
        )
    return fresh[0]


# --- CALM side: correlations pull (read-only) ------------------------------

CORRELATIONS_BY_IDS_SQL = (
    "SELECT correlation_id, request_type, request_meta, outcome, created_at "
    "FROM correlations WHERE correlation_id = ANY(%(ids)s)"
)


def correlation_id_to_bytea(uuid_text: str) -> bytes:
    """Canonical UUID text (as the adapter logs it) to the raw 16-byte key.

    Mirrors the probe-verified join ``correlation_id = decode(replace(id,'-',''),'hex')``.
    """
    raw = bytes.fromhex(uuid_text.replace("-", ""))
    if len(raw) != 16:
        raise ValueError(f"correlation id {uuid_text!r} is not a 16-byte UUID")
    return raw


def build_correlations_by_ids(correlation_ids: list[str]) -> tuple[str, dict[str, Any]]:
    return CORRELATIONS_BY_IDS_SQL, {"ids": [correlation_id_to_bytea(cid) for cid in correlation_ids]}


def build_correlations_by_client_window(
    namespace: str,
    client: str,
    since_iso: str,
) -> tuple[str, dict[str, Any]]:
    sql = (
        "SELECT c.correlation_id, c.request_type, c.request_meta, c.outcome, c.created_at "
        "FROM correlations c JOIN sessions s ON s.id = c.session_id "
        "WHERE s.namespace = %(namespace)s AND s.client = %(client)s AND c.created_at >= %(since)s "
        "ORDER BY c.created_at"
    )
    return sql, {"namespace": namespace, "client": client, "since": since_iso}


@dataclass
class CalmMeasures:
    correlations_total: int = 0
    by_request_type: dict[str, int] = field(default_factory=dict)
    outcome_counts: dict[str, int] = field(default_factory=dict)
    searches_attempted: int = 0
    search_intent_zero_match_total: int = 0
    match_layer_counts: dict[str, int] = field(default_factory=dict)
    allocator_counts: dict[str, int] = field(default_factory=dict)
    correlation_ids: list[str] = field(default_factory=list)
    session_snapshot: dict[str, Any] = field(default_factory=dict)

    def as_dict(self) -> dict[str, Any]:
        return asdict(self)


def normalize_correlation_row(row: Any) -> dict[str, Any]:
    """Normalize a DB row (tuple or dict) to a driver-agnostic dict.

    correlation_id is hex-encoded to canonical text so downstream code (and the
    tests) never touch the raw bytea.
    """
    if isinstance(row, dict):
        cid = row.get("correlation_id")
        request_type = row.get("request_type")
        request_meta = row.get("request_meta")
        outcome = row.get("outcome")
        created_at = row.get("created_at")
    else:
        cid, request_type, request_meta, outcome, created_at = row
    if isinstance(cid, (bytes, bytearray, memoryview)):
        cid = bytes(cid).hex()
    if isinstance(request_meta, str):
        request_meta = json.loads(request_meta)
    return {
        "correlation_id": str(cid),
        "request_type": str(request_type),
        "request_meta": request_meta or {},
        "outcome": str(outcome),
        "created_at": str(created_at),
    }


def parse_correlation_rows(rows: list[Any]) -> CalmMeasures:
    measures = CalmMeasures()
    for raw in rows:
        row = normalize_correlation_row(raw)
        measures.correlations_total += 1
        rtype = row["request_type"]
        measures.by_request_type[rtype] = measures.by_request_type.get(rtype, 0) + 1
        outcome = row["outcome"]
        measures.outcome_counts[outcome] = measures.outcome_counts.get(outcome, 0) + 1
        measures.correlation_ids.append(row["correlation_id"])
        meta = row["request_meta"]
        if rtype == "search":
            measures.searches_attempted += 1
            measures.search_intent_zero_match_total += int(meta.get("intent_zero_match", 0) or 0)
            for layer, count in (meta.get("match_layer") or {}).items():
                measures.match_layer_counts[str(layer)] = (
                    measures.match_layer_counts.get(str(layer), 0) + int(count or 0)
                )
            allocator = meta.get("allocator")
            if allocator:
                measures.allocator_counts[str(allocator)] = measures.allocator_counts.get(str(allocator), 0) + 1
    return measures


def fetch_correlations(
    dsn: str,
    *,
    correlation_ids: list[str] | None = None,
    namespace: str | None = None,
    client: str | None = None,
    since_iso: str | None = None,
) -> list[dict[str, Any]]:
    """Read-only correlations pull. psycopg is imported lazily so the offline
    specimen tests run with no driver installed."""
    import psycopg  # noqa: PLC0415 - deliberate lazy import (offline tests have no driver).

    if correlation_ids:
        sql, params = build_correlations_by_ids(correlation_ids)
    elif namespace and client and since_iso:
        sql, params = build_correlations_by_client_window(namespace, client, since_iso)
    else:
        raise ValueError("fetch_correlations needs correlation_ids or (namespace, client, since_iso)")

    with psycopg.connect(dsn) as conn:
        conn.read_only = True
        with conn.cursor() as cur:
            cur.execute(sql, params)
            rows = cur.fetchall()
    return [normalize_correlation_row(r) for r in rows]


# --- correlation ids from the adapter capture log --------------------------


def correlation_ids_from_log(log_path: str | Path) -> list[str]:
    """Harvest canonical correlation-id UUIDs from an adapter capture log.

    The per-call summary flush binds ``correlation_id=<uuid>``; harvesting them
    lets the DB pull use the probe-verified hex-join point lookup. Absent a log
    (or an arm with none) the caller falls back to the client+window pull.
    """
    import re  # noqa: PLC0415 - local, used only here.

    pattern = re.compile(r"correlation_id[=\":\s]+([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})")
    ids: list[str] = []
    seen: set[str] = set()
    path = Path(log_path)
    if not path.exists():
        return ids
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        for match in pattern.findall(line):
            cid = match.lower()
            if cid not in seen:
                seen.add(cid)
                ids.append(cid)
    return ids
