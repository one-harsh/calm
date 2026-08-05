# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Offline report/gate tests — synthetic traces, no network or DB."""

from __future__ import annotations

import report


def _trace(task: str, quadrant: str, arm: str, rep: int, output: int, status: str = "accepted", **extra) -> dict:
    transcript = {
        "output_tokens": output,
        "call_count": extra.get("call_count", 0),
        "bytes_served_total": extra.get("bytes_served_total", 0),
        "read_before_edit_pairs": extra.get("read_before_edit_pairs", 0),
        "capture_probe_loops": extra.get("capture_probe_loops", 0),
        "post_compaction_rereads": extra.get("post_compaction_rereads", 0),
    }
    calm = {
        "searches_attempted": extra.get("searches_attempted", 0),
        "search_intent_zero_match_total": extra.get("intent_zero_match", 0),
        "match_layer_counts": extra.get("match_layer_counts", {}),
    }
    return {
        "cell_id": f"{task}-{arm}-r{rep}",
        "task_id": task,
        "quadrant": quadrant,
        "arm": arm,
        "rep": rep,
        "status": status,
        "failure_reason": extra.get("failure_reason", ""),
        "transcript": transcript,
        "calm": calm,
    }


def test_verdict_bands() -> None:
    assert report.verdict_for_ratio(None) == report.VERDICT_INCONCLUSIVE
    assert report.verdict_for_ratio(1.2) == report.VERDICT_FAILING
    assert report.verdict_for_ratio(1.0) == report.VERDICT_NO_GAIN
    assert report.verdict_for_ratio(0.9) == report.VERDICT_NO_GAIN
    assert report.verdict_for_ratio(0.75) == report.VERDICT_VIABLE
    assert report.verdict_for_ratio(0.6) == report.VERDICT_VIABLE
    assert report.verdict_for_ratio(0.50) == report.VERDICT_SUCCESS
    assert report.verdict_for_ratio(0.25) == report.VERDICT_SUCCESS


def test_near_gate_boundary_band() -> None:
    assert report.near_gate_boundary(0.62) is True  # within 0.15 of 0.75 and 0.50
    assert report.near_gate_boundary(0.90) is True  # within 0.15 of 0.75 and 1.0
    assert report.near_gate_boundary(0.30) is False
    assert report.near_gate_boundary(None) is False


def test_ratio_and_quadrant_verdict() -> None:
    traces = [
        _trace("t1", "Q1", "raw", 1, 1000),
        _trace("t1", "Q1", "mcp", 1, 500),  # ratio 0.5 -> success
        _trace("t1", "Q1", "hook", 1, 900),  # ratio 0.9 -> no_gain
    ]
    rep = report.build_report(traces)
    ratios = {(tr.task_id, tr.arm): tr.ratio for tr in rep.task_ratios}
    assert ratios[("t1", "mcp")] == 0.5
    assert ratios[("t1", "hook")] == 0.9
    verdicts = {(qv.quadrant, qv.arm): qv.verdict for qv in rep.quadrant_verdicts}
    assert verdicts[("Q1", "mcp")] == report.VERDICT_SUCCESS
    assert verdicts[("Q1", "hook")] == report.VERDICT_NO_GAIN


def test_quadrant_verdict_is_median_of_task_ratios() -> None:
    traces = [
        _trace("t1", "Q1", "raw", 1, 1000),
        _trace("t1", "Q1", "mcp", 1, 400),  # 0.40
        _trace("t2", "Q1", "raw", 1, 1000),
        _trace("t2", "Q1", "mcp", 1, 600),  # 0.60
        _trace("t9", "Q1", "raw", 1, 1000),
        _trace("t9", "Q1", "mcp", 1, 800),  # 0.80
    ]
    rep = report.build_report(traces)
    q1_mcp = next(qv for qv in rep.quadrant_verdicts if qv.quadrant == "Q1" and qv.arm == "mcp")
    assert q1_mcp.median_ratio == 0.60
    assert q1_mcp.verdict == report.VERDICT_VIABLE


def test_failed_acceptance_excluded_from_ratios() -> None:
    traces = [
        _trace("t1", "Q1", "raw", 1, 1000),
        _trace("t1", "Q1", "mcp", 1, 200, status="acceptance_failed"),
        _trace("t1", "Q1", "mcp", 2, 500),
    ]
    rep = report.build_report(traces)
    mcp = next(tr for tr in rep.task_ratios if tr.task_id == "t1" and tr.arm == "mcp")
    assert mcp.accepted_reps == 1
    assert mcp.arm_mean_output_tokens == 500
    assert mcp.ratio == 0.5
    assert rep.excluded_cells == 1


def test_missing_raw_baseline_is_inconclusive() -> None:
    traces = [_trace("t1", "Q1", "mcp", 1, 500)]
    rep = report.build_report(traces)
    mcp = next(tr for tr in rep.task_ratios if tr.task_id == "t1" and tr.arm == "mcp")
    assert mcp.ratio is None
    assert mcp.accepted_reps == 1
    # A CALM arm with no raw baseline yields an explicit INCONCLUSIVE verdict,
    # not a silently dropped row.
    q1_mcp = next(qv for qv in rep.quadrant_verdicts if qv.quadrant == "Q1" and qv.arm == "mcp")
    assert q1_mcp.median_ratio is None
    assert q1_mcp.verdict == report.VERDICT_INCONCLUSIVE


def test_zero_accepted_cell_reported_inconclusive_not_dropped() -> None:
    # A (task, arm) that ran but was entirely excluded still appears — reported
    # inconclusive, never averaged around.
    traces = [
        _trace("t1", "Q1", "raw", 1, 1000),
        _trace("t1", "Q1", "mcp", 1, 500, status="acceptance_failed"),
    ]
    rep = report.build_report(traces)
    mcp = next(tr for tr in rep.task_ratios if tr.task_id == "t1" and tr.arm == "mcp")
    assert mcp.accepted_reps == 0
    assert mcp.arm_mean_output_tokens is None
    assert mcp.ratio is None
    q1_mcp = next(qv for qv in rep.quadrant_verdicts if qv.quadrant == "Q1" and qv.arm == "mcp")
    assert q1_mcp.verdict == report.VERDICT_INCONCLUSIVE


def test_staged_rep_trigger_flag() -> None:
    traces = [
        _trace("t1", "Q1", "raw", 1, 1000),
        _trace("t1", "Q1", "mcp", 1, 780),  # ratio 0.78 -> within 0.15 of 0.75
    ]
    rep = report.build_report(traces)
    mcp = next(tr for tr in rep.task_ratios if tr.task_id == "t1" and tr.arm == "mcp")
    assert mcp.needs_more_reps is True


def test_decomposition_and_retrieval_tables() -> None:
    traces = [
        _trace("t1", "Q1", "raw", 1, 1000, call_count=10, bytes_served_total=2000, read_before_edit_pairs=4),
        _trace(
            "t1", "Q1", "mcp", 1, 500, call_count=8, bytes_served_total=800,
            read_before_edit_pairs=0, searches_attempted=3, intent_zero_match=1,
            match_layer_counts={"primary": 5},
        ),
    ]
    rep = report.build_report(traces)
    raw_dc = next(dc for dc in rep.decomposition if dc.arm == "raw")
    assert raw_dc.mean_call_count == 10
    assert raw_dc.mean_bytes_per_call == 200
    assert raw_dc.mean_read_before_edit_pairs == 4
    mcp_ru = next(ru for ru in rep.retrieval_usage if ru.arm == "mcp")
    assert mcp_ru.searches_attempted == 3
    assert mcp_ru.intent_zero_match == 1
    assert mcp_ru.match_layer_counts == {"primary": 5}


def test_markdown_and_json_render() -> None:
    traces = [
        _trace("t1", "Q1", "raw", 1, 1000),
        _trace("t1", "Q1", "mcp", 1, 500),
    ]
    rep = report.build_report(traces)
    md = report.render_markdown(rep)
    assert "Gate verdicts" in md
    assert "Decomposition" in md
    assert "Retrieval usage" in md
    # Data-derived content, not just headers: the computed ratio + verdict render.
    assert "0.500" in md
    assert report.VERDICT_SUCCESS in md
    assert "| t1 | Q1 | mcp |" in md
    payload = rep.as_json()
    assert payload["accepted_cells"] == 2
    q1_mcp = next(qv for qv in payload["quadrant_verdicts"] if qv["arm"] == "mcp")
    assert q1_mcp["median_ratio"] == 0.5
    assert q1_mcp["verdict"] == report.VERDICT_SUCCESS
