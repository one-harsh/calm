# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Offline extractor tests — specimen-driven, no network or DB.

The specimens under spikes/specimens/ are real `claude -p` transcripts recorded
live (2026-08-03, CLI 2.1.212; see spikes/NOTES.md). They pin the load-bearing
contract: usage deduped per message id, calls per tool_use block, bytes-served
as UTF-8 length of tool_result content, and the denied-tool shape.
"""

from __future__ import annotations

from pathlib import Path

import pytest

import extract

HERE = Path(__file__).resolve().parent
SPECIMENS = HERE / "spikes" / "specimens"


def _measures(name: str) -> extract.TranscriptMeasures:
    return extract.parse_transcript(extract.load_transcript(SPECIMENS / name))


# --- usage dedup + calls + bytes (specimen-pinned) ------------------------


def test_trivial_ok_single_message_usage_counted_once() -> None:
    m = _measures("s2-trivial-ok.jsonl")
    assert m.output_tokens == 41
    assert m.input_tokens == 10
    assert m.cache_creation_tokens == 6762
    assert m.cache_read_tokens == 17418
    assert m.assistant_messages == 1
    assert m.call_count == 0
    assert m.bytes_served_total == 0
    assert m.model == "claude-haiku-4-5-20251001"
    assert m.cli_version == "2.1.212"


def test_bash_autoallow_one_command_call() -> None:
    m = _measures("s4-bash-autoallow.jsonl")
    # Two distinct message ids: 112 (thinking+tool_use) + 165 (thinking+text).
    assert m.output_tokens == 277
    assert m.input_tokens == 18
    assert m.assistant_messages == 2
    assert m.call_count == 1
    assert m.calls_by_shape == {"command": 1}
    assert m.bytes_served_total == 102
    assert m.tool_error_count == 0
    assert m.denied_count == 0


def test_write_allowed_dedupes_three_records_sharing_one_id() -> None:
    m = _measures("s4-write-allowed.jsonl")
    # msg ...4oDbe97H9tuiXWzt spans 3 records (thinking + Bash + Write), each
    # repeating output_tokens=313 — it must be counted ONCE. Plus 95 for the
    # closing message.
    assert m.output_tokens == 408
    assert m.assistant_messages == 2
    assert m.call_count == 2
    assert m.calls_by_shape == {"command": 1, "write": 1}
    assert m.bytes_served_total == 248
    assert m.tool_error_count == 0
    # A Write with no prior Read of the path scores no read-before-edit pair.
    assert m.read_before_edit_pairs == 0


def test_write_denied_flags_denied_tools() -> None:
    m = _measures("s4-write-denied.jsonl")
    assert m.output_tokens == 755
    assert m.call_count == 2
    assert m.calls_by_shape == {"command": 1, "write": 1}
    assert m.bytes_served_total == 565
    assert m.tool_error_count == 2
    assert m.denied_count == 2


def test_failing_command_is_error_but_not_denied() -> None:
    # A plain failing command (is_error, no denial signal) must NOT count as a
    # permission denial — denied != is_error.
    records = [
        _assistant("m1", [_tool_use("t1", "Bash", {"command": "go test ./..."})]),
        _tool_result("t1", "FAIL: build failed\nexit status 1", is_error=True),
    ]
    m = extract.parse_transcript(records)
    assert m.tool_error_count == 1
    assert m.denied_count == 0


def test_denial_by_text_marker_without_denial_kind() -> None:
    records = [
        _assistant("m1", [_tool_use("t1", "Write", {"file_path": "/x/n.txt", "content": "hi"})]),
        _tool_result("t1", "Claude requested permissions to write to /x/n.txt, but you haven't granted it yet.", is_error=True),
    ]
    m = extract.parse_transcript(records)
    assert m.denied_count == 1


# --- fail loud on unknown shapes ------------------------------------------


def test_unknown_record_type_fails_loud() -> None:
    records = [{"type": "user", "message": {"role": "user", "content": "hi"}}, {"type": "meteorite"}]
    with pytest.raises(extract.UnknownRecordShape):
        extract.parse_transcript(records)


def test_unknown_assistant_block_fails_loud() -> None:
    records = [
        {
            "type": "assistant",
            "uuid": "u1",
            "message": {"id": "m1", "role": "assistant", "content": [{"type": "hologram"}], "usage": {}},
        }
    ]
    with pytest.raises(extract.UnknownRecordShape):
        extract.parse_transcript(records)


def test_missing_message_id_fails_loud() -> None:
    # The gate metric is Σ output_tokens deduped by message.id; a missing id is
    # CLI-format drift and must fail loud, never fall back to per-record counting
    # that double-counts output tokens.
    records = [
        {"type": "assistant", "uuid": "u1", "message": {"role": "assistant", "content": [], "usage": {"output_tokens": 50}}},
    ]
    with pytest.raises(extract.UnknownRecordShape):
        extract.parse_transcript(records)


# --- derived measures (synthetic) -----------------------------------------


def _assistant(mid: str, blocks: list[dict], output: int = 10) -> dict:
    return {
        "type": "assistant",
        "uuid": mid + "-u",
        "message": {"id": mid, "role": "assistant", "content": blocks, "usage": {"output_tokens": output}},
    }


def _tool_use(tuid: str, name: str, tool_input: dict) -> dict:
    return {"type": "tool_use", "id": tuid, "name": name, "input": tool_input}


def _tool_result(tuid: str, content: str, is_error: bool = False) -> dict:
    block = {"type": "tool_result", "tool_use_id": tuid, "content": content}
    if is_error:
        block["is_error"] = True
    return {"type": "user", "uuid": tuid + "-r", "message": {"role": "user", "content": [block]}}


def test_read_before_edit_counts_native_pair_not_calm_edit() -> None:
    records = [
        _assistant("m1", [_tool_use("t1", "Read", {"file_path": "/repo/a.go"})]),
        _tool_result("t1", "contents"),
        _assistant("m2", [_tool_use("t2", "Edit", {"file_path": "/repo/a.go"})]),
        _tool_result("t2", "edited"),
        # calm_edit_file with no prior read — scores no pair.
        _assistant("m3", [_tool_use("t3", "mcp__calm__calm_edit_file", {"path": "b.go"})]),
        _tool_result("t3", "edited"),
    ]
    m = extract.parse_transcript(records)
    assert m.read_before_edit_pairs == 1


def test_capture_probe_loop_flags_read_after_recent_edit() -> None:
    records = [
        _assistant("m1", [_tool_use("t1", "Edit", {"file_path": "/repo/a.go"})]),
        _tool_result("t1", "edited"),
        _assistant("m2", [_tool_use("t2", "Read", {"file_path": "/repo/a.go"})]),
        _tool_result("t2", "contents"),
    ]
    m = extract.parse_transcript(records)
    assert m.capture_probe_loops == 1


def test_capture_probe_loop_flags_native_grep_singular_path() -> None:
    # Native Grep uses "path" (singular); reading only "paths" would zero out
    # grep-shaped probes for the raw arm and bias the decomposition.
    records = [
        _assistant("m1", [_tool_use("t1", "Edit", {"file_path": "/repo/a.go"})]),
        _tool_result("t1", "edited"),
        _assistant("m2", [_tool_use("t2", "Grep", {"pattern": "func", "path": "/repo/a.go"})]),
        _tool_result("t2", "match"),
    ]
    m = extract.parse_transcript(records)
    assert m.capture_probe_loops == 1


def test_capture_probe_window_is_exactly_three_calls() -> None:
    # An edit exactly 3 calls before a read is IN the window; 4 before is OUT.
    def read_pair(mid: str, tid: str, path: str, name: str = "Read", inp: dict | None = None):
        return [_assistant(mid, [_tool_use(tid, name, inp or {"file_path": path})]), _tool_result(tid, "x")]

    in_window = (
        read_pair("m1", "t1", "/repo/a.go", name="Edit")
        + read_pair("m2", "t2", "/repo/b.go")
        + read_pair("m3", "t3", "/repo/c.go")
        + read_pair("m4", "t4", "/repo/a.go")  # read is 3 calls after the edit -> in window
    )
    assert extract.parse_transcript(in_window).capture_probe_loops == 1

    out_of_window = (
        read_pair("m1", "t1", "/repo/a.go", name="Edit")
        + read_pair("m2", "t2", "/repo/b.go")
        + read_pair("m3", "t3", "/repo/c.go")
        + read_pair("m4", "t4", "/repo/d.go")
        + read_pair("m5", "t5", "/repo/a.go")  # read is 4 calls after the edit -> out of window
    )
    assert extract.parse_transcript(out_of_window).capture_probe_loops == 0


def test_post_compaction_reread_uses_most_recent_boundary() -> None:
    # C is first read in the segment AFTER boundary1, then re-read after
    # boundary2. Measuring against the most-recent boundary counts it;
    # a first-boundary-only snapshot would miss it.
    records = [
        _assistant("m1", [_tool_use("t1", "Read", {"file_path": "/repo/a.go"})]),
        _tool_result("t1", "a"),
        {"type": "summary", "isCompactSummary": True, "summary": "c1"},
        _assistant("m2", [_tool_use("t2", "Read", {"file_path": "/repo/c.go"})]),
        _tool_result("t2", "c"),
        {"type": "summary", "isCompactSummary": True, "summary": "c2"},
        _assistant("m3", [_tool_use("t3", "Read", {"file_path": "/repo/c.go"})]),
        _tool_result("t3", "c-again"),
    ]
    m = extract.parse_transcript(records)
    assert m.compaction_boundaries == 2
    assert m.post_compaction_rereads == 1


def test_post_compaction_reread_flagged_across_boundary() -> None:
    records = [
        _assistant("m1", [_tool_use("t1", "Read", {"file_path": "/repo/a.go"})]),
        _tool_result("t1", "contents"),
        {"type": "summary", "isCompactSummary": True, "summary": "compacted"},
        _assistant("m2", [_tool_use("t2", "Read", {"file_path": "/repo/a.go"})]),
        _tool_result("t2", "contents-again"),
    ]
    m = extract.parse_transcript(records)
    assert m.compaction_boundaries == 1
    assert m.post_compaction_rereads == 1


def test_teaching_state_detects_sessionstart_hook() -> None:
    records = [
        {"type": "attachment", "uuid": "a1", "attachment": {"type": "hook_success", "hookEvent": "SessionStart"}},
        _assistant("m1", [_tool_use("t1", "mcp__calm__calm_read_file", {"path": "a.go"})]),
        _tool_result("t1", "contents"),
    ]
    m = extract.parse_transcript(records)
    assert m.teaching.sessionstart_hook_fired is True
    assert m.teaching.calm_tool_used is True


def test_small_output_visible_exceeds_raw_is_not_an_error() -> None:
    # S1 nuance: a hook-replaced small output can carry a discovery card, so the
    # tool_result served may exceed the raw command output. bytes_served is just
    # the UTF-8 length of what entered context — no anomaly.
    served = "x" * 900
    records = [
        _assistant("m1", [_tool_use("t1", "Bash", {"command": "echo hi"})]),
        _tool_result("t1", served),
    ]
    m = extract.parse_transcript(records)
    assert m.bytes_served_total == 900


# --- CALM side: correlations join + parsing --------------------------------


def test_correlation_id_hex_roundtrip() -> None:
    cid = "0190abcd-1234-7abc-8def-0123456789ab"
    raw = extract.correlation_id_to_bytea(cid)
    assert len(raw) == 16
    assert raw.hex() == cid.replace("-", "")


def test_build_correlations_by_ids_decodes_to_bytea() -> None:
    sql, params = extract.build_correlations_by_ids(["0190abcd-1234-7abc-8def-0123456789ab"])
    assert "correlation_id = ANY(%(ids)s)" in sql
    assert params["ids"][0] == bytes.fromhex("0190abcd12347abc8def0123456789ab")


def test_build_correlations_by_client_window_params() -> None:
    sql, params = extract.build_correlations_by_client_window("bench", "bench-mcp", "2026-08-03T00:00:00Z")
    assert "JOIN sessions s ON s.id = c.session_id" in sql
    assert params == {"namespace": "bench", "client": "bench-mcp", "since": "2026-08-03T00:00:00Z"}


def test_build_correlations_by_session_params() -> None:
    # The snapshot hands us the session id, so the fallback scopes by it.
    sql, params = extract.build_correlations_by_session(4242)
    assert "WHERE session_id = %(session_id)s" in sql
    assert params == {"session_id": 4242}


def test_normalize_correlation_row_hex_encodes_and_parses_meta() -> None:
    row = (
        bytes.fromhex("0190abcd12347abc8def0123456789ab"),
        "search",
        '{"intent_zero_match": 2}',
        "success",
        "2026-08-03T01:02:03Z",
    )
    norm = extract.normalize_correlation_row(row)
    assert norm["correlation_id"] == "0190abcd12347abc8def0123456789ab"
    assert norm["request_meta"] == {"intent_zero_match": 2}


def test_parse_correlation_rows_aggregates_search_signal() -> None:
    rows = [
        {"correlation_id": "aa", "request_type": "ingest", "request_meta": {}, "outcome": "unset", "created_at": "t"},
        {
            "correlation_id": "bb",
            "request_type": "search",
            "request_meta": {"intent_zero_match": 2, "match_layer": {"primary": 3, "trigram": 1}, "allocator": "mmr"},
            "outcome": "success",
            "created_at": "t",
        },
    ]
    m = extract.parse_correlation_rows(rows)
    assert m.correlations_total == 2
    assert m.by_request_type == {"ingest": 1, "search": 1}
    assert m.searches_attempted == 1
    assert m.search_intent_zero_match_total == 2
    assert m.match_layer_counts == {"primary": 3, "trigram": 1}
    assert m.allocator_counts == {"mmr": 1}
    assert m.outcome_counts == {"unset": 1, "success": 1}


# --- CALM side: session snapshot delta ------------------------------------


def test_build_sessions_by_namespace_is_parameterized_single_statement() -> None:
    sql, params = extract.build_sessions_by_namespace("bench")
    assert sql == "SELECT id, namespace, client, created_at FROM sessions WHERE namespace = %(namespace)s"
    assert params == {"namespace": "bench"}


def test_build_sessions_by_client_is_parameterized_single_statement() -> None:
    sql, params = extract.build_sessions_by_client("bench-mcp")
    assert sql == "SELECT id, namespace, client, created_at FROM sessions WHERE client = %(client)s"
    assert params == {"client": "bench-mcp"}


def test_normalize_session_row_from_db_tuple() -> None:
    # DB rows arrive as (id, namespace, client, created_at); created_at is a
    # datetime that must stringify stably for the set-difference key.
    from datetime import datetime, timezone

    row = (77, "bench", "bench-mcp", datetime(2026, 8, 6, 1, 2, 3, tzinfo=timezone.utc))
    norm = extract.normalize_session_row(row)
    assert norm["id"] == 77
    assert norm["namespace"] == "bench"
    assert norm["client"] == "bench-mcp"
    assert norm["created_at"] == "2026-08-06 01:02:03+00:00"


def test_assert_one_new_session_ok_carries_db_session_id() -> None:
    before = [extract.normalize_session_row((1, "bench", "bench-mcp", "t0"))]
    after = [
        extract.normalize_session_row((1, "bench", "bench-mcp", "t0")),
        extract.normalize_session_row((2, "bench", "bench-mcp", "t1")),
    ]
    session = extract.assert_one_new_session(before, after)
    assert session["created_at"] == "t1"
    assert session["id"] == 2  # the snapshot delta surfaces the session id for the correlations pull


def test_assert_one_new_session_survives_clientinfo_override() -> None:
    # The MCP arm's session lands with client='claude-code' (the adapter prefers
    # the MCP handshake clientInfo.name over the configured 'bench-mcp' tag). The
    # namespace-scoped snapshot still identifies the new session because cells are
    # serial and (client, created_at) stays unique on created_at.
    before = [extract.normalize_session_row((1, "bench", "claude-code", "t0"))]
    after = [
        extract.normalize_session_row((1, "bench", "claude-code", "t0")),
        extract.normalize_session_row((2, "bench", "claude-code", "t1")),
    ]
    session = extract.assert_one_new_session(before, after)
    assert session["id"] == 2
    assert session["client"] == "claude-code"  # not the arm tag — the join survives the override


def test_assert_one_new_session_ambiguous_raises() -> None:
    before: list[dict] = []
    after = [
        extract.normalize_session_row((2, "bench", "bench-mcp", "t1")),
        extract.normalize_session_row((3, "bench", "bench-mcp", "t2")),
    ]
    with pytest.raises(extract.TranscriptError):
        extract.assert_one_new_session(before, after)


def test_correlation_ids_from_log_harvests_uuids(tmp_path: Path) -> None:
    log = tmp_path / "calm-capture.log"
    log.write_text(
        'ts=1 level=info event="call completed" correlation_id=0190abcd-1234-7abc-8def-0123456789ab\n'
        'ts=2 level=info event="call completed" correlation_id=0190abcd-1234-7abc-8def-0123456789ab\n'
        'ts=3 no id here\n',
        encoding="utf-8",
    )
    ids = extract.correlation_ids_from_log(log)
    assert ids == ["0190abcd-1234-7abc-8def-0123456789ab"]
