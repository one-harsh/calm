# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Aggregation and gate evaluation for the paired-run benchmark.

Reads one trace JSON per cell (see extract.py / runner.py) and produces, per
quadrant x arm: a gate verdict, a decomposition table, and a retrieval-usage
table, in markdown and JSON.

Gate bands (ratio = arm mean / raw-native mean, per task; quadrant verdict =
median of its task ratios):

    ratio > 1.0    failing   (CALM arm costs MORE than raw-native)
    0.75 < ratio <= 1.0   no_gain
    0.50 < ratio <= 0.75  viable
    ratio <= 0.50   success

A CALM-arm ratio within +/-0.15 of a boundary (0.50, 0.75, 1.0) flags the cell
for staged extra reps. Cells with zero accepted runs are inconclusive — never
averaged around.
"""

from __future__ import annotations

import json
import statistics
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

RAW_ARM = "raw"
ARMS = ("raw", "mcp", "hook")
CALM_ARMS = ("mcp", "hook")
STATUS_ACCEPTED = "accepted"
GATE_BOUNDARIES = (0.50, 0.75, 1.0)
REP_TRIGGER_BAND = 0.15

VERDICT_INCONCLUSIVE = "inconclusive"
VERDICT_FAILING = "failing"
VERDICT_NO_GAIN = "no_gain"
VERDICT_VIABLE = "viable"
VERDICT_SUCCESS = "success"


def verdict_for_ratio(ratio: float | None) -> str:
    if ratio is None:
        return VERDICT_INCONCLUSIVE
    if ratio > 1.0:
        return VERDICT_FAILING
    if ratio > 0.75:
        return VERDICT_NO_GAIN
    if ratio > 0.50:
        return VERDICT_VIABLE
    return VERDICT_SUCCESS


def near_gate_boundary(ratio: float | None, band: float = REP_TRIGGER_BAND) -> bool:
    if ratio is None:
        return False
    return any(abs(ratio - boundary) <= band for boundary in GATE_BOUNDARIES)


@dataclass
class TaskRatio:
    task_id: str
    quadrant: str
    arm: str
    accepted_reps: int
    arm_mean_output_tokens: float | None
    raw_mean_output_tokens: float | None
    ratio: float | None
    needs_more_reps: bool


@dataclass
class QuadrantVerdict:
    quadrant: str
    arm: str
    median_ratio: float | None
    verdict: str
    task_ratios: list[float] = field(default_factory=list)
    needs_more_reps: bool = False


@dataclass
class Decomposition:
    quadrant: str
    arm: str
    accepted_cells: int
    mean_call_count: float
    mean_bytes_per_call: float
    mean_read_before_edit_pairs: float
    mean_capture_probe_loops: float
    mean_post_compaction_rereads: float


@dataclass
class RetrievalUsage:
    arm: str
    accepted_cells: int
    searches_attempted: int
    intent_zero_match: int
    match_layer_counts: dict[str, int]
    stale_signal_encounters: int  # post-compaction re-reads (teaching-attributable waste)


@dataclass
class PairedRunReport:
    total_cells: int
    accepted_cells: int
    excluded_cells: int
    task_ratios: list[TaskRatio]
    quadrant_verdicts: list[QuadrantVerdict]
    decomposition: list[Decomposition]
    retrieval_usage: list[RetrievalUsage]
    excluded: list[dict[str, Any]] = field(default_factory=list)

    def as_json(self) -> dict[str, Any]:
        return asdict(self)


def load_traces(directory: Path) -> list[dict[str, Any]]:
    traces: list[dict[str, Any]] = []
    for path in sorted(Path(directory).glob("*.json")):
        traces.append(json.loads(path.read_text(encoding="utf-8")))
    return traces


def _output_tokens(trace: dict[str, Any]) -> int:
    return int((trace.get("transcript") or {}).get("output_tokens", 0) or 0)


def _accepted(traces: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [t for t in traces if t.get("status") == STATUS_ACCEPTED]


def _mean(values: list[float]) -> float | None:
    return statistics.fmean(values) if values else None


def build_report(traces: list[dict[str, Any]]) -> PairedRunReport:
    accepted = _accepted(traces)
    excluded = [
        {"cell_id": t.get("cell_id"), "status": t.get("status"), "reason": t.get("failure_reason", "")}
        for t in traces
        if t.get("status") != STATUS_ACCEPTED
    ]

    quadrant_of = {t["task_id"]: t.get("quadrant", "") for t in traces if t.get("task_id")}

    # arm means per (task, arm), from accepted cells only.
    means: dict[tuple[str, str], list[int]] = {}
    for trace in accepted:
        key = (str(trace.get("task_id")), str(trace.get("arm")))
        means.setdefault(key, []).append(_output_tokens(trace))

    # The (task, arm) universe includes cells that ran but were all excluded, so
    # a zero-accepted cell is reported inconclusive, never silently dropped.
    universe: set[tuple[str, str]] = {
        (str(t.get("task_id")), str(t.get("arm"))) for t in traces if t.get("task_id")
    }
    task_ids = sorted({task for task, _ in universe})

    task_ratios: list[TaskRatio] = []
    ratio_by_quadrant_arm: dict[tuple[str, str], list[float]] = {}
    quadrant_arms_seen: set[tuple[str, str]] = set()
    for task_id in task_ids:
        raw_values = means.get((task_id, RAW_ARM), [])
        raw_mean = _mean([float(v) for v in raw_values])
        for arm in ARMS:
            if (task_id, arm) not in universe:
                continue
            quadrant = quadrant_of.get(task_id, "")
            if arm in CALM_ARMS:
                quadrant_arms_seen.add((quadrant, arm))
            arm_values = means.get((task_id, arm), [])
            arm_mean = _mean([float(v) for v in arm_values]) if arm_values else None
            ratio = (arm_mean / raw_mean) if (arm_mean is not None and raw_mean) else None
            needs = arm in CALM_ARMS and near_gate_boundary(ratio)
            task_ratios.append(
                TaskRatio(
                    task_id=task_id,
                    quadrant=quadrant,
                    arm=arm,
                    accepted_reps=len(arm_values),
                    arm_mean_output_tokens=arm_mean,
                    raw_mean_output_tokens=raw_mean,
                    ratio=ratio,
                    needs_more_reps=needs,
                )
            )
            if arm in CALM_ARMS and ratio is not None:
                ratio_by_quadrant_arm.setdefault((quadrant, arm), []).append(ratio)

    quadrant_verdicts: list[QuadrantVerdict] = []
    for quadrant, arm in sorted(quadrant_arms_seen):
        ratios = ratio_by_quadrant_arm.get((quadrant, arm), [])
        median_ratio = statistics.median(ratios) if ratios else None
        quadrant_verdicts.append(
            QuadrantVerdict(
                quadrant=quadrant,
                arm=arm,
                median_ratio=median_ratio,
                verdict=verdict_for_ratio(median_ratio),
                task_ratios=ratios,
                needs_more_reps=any(near_gate_boundary(r) for r in ratios),
            )
        )

    decomposition = _build_decomposition(accepted)
    retrieval_usage = _build_retrieval_usage(accepted)

    return PairedRunReport(
        total_cells=len(traces),
        accepted_cells=len(accepted),
        excluded_cells=len(excluded),
        task_ratios=task_ratios,
        quadrant_verdicts=quadrant_verdicts,
        decomposition=decomposition,
        retrieval_usage=retrieval_usage,
        excluded=excluded,
    )


def _build_decomposition(accepted: list[dict[str, Any]]) -> list[Decomposition]:
    groups: dict[tuple[str, str], list[dict[str, Any]]] = {}
    for trace in accepted:
        groups.setdefault((str(trace.get("quadrant")), str(trace.get("arm"))), []).append(trace)
    out: list[Decomposition] = []
    for (quadrant, arm), cells in sorted(groups.items()):
        call_counts = []
        bytes_per_call = []
        rbe = []
        probes = []
        rereads = []
        for cell in cells:
            tm = cell.get("transcript") or {}
            calls = int(tm.get("call_count", 0) or 0)
            served = int(tm.get("bytes_served_total", 0) or 0)
            call_counts.append(float(calls))
            bytes_per_call.append(float(served) / calls if calls else 0.0)
            rbe.append(float(tm.get("read_before_edit_pairs", 0) or 0))
            probes.append(float(tm.get("capture_probe_loops", 0) or 0))
            rereads.append(float(tm.get("post_compaction_rereads", 0) or 0))
        out.append(
            Decomposition(
                quadrant=quadrant,
                arm=arm,
                accepted_cells=len(cells),
                mean_call_count=_mean(call_counts) or 0.0,
                mean_bytes_per_call=_mean(bytes_per_call) or 0.0,
                mean_read_before_edit_pairs=_mean(rbe) or 0.0,
                mean_capture_probe_loops=_mean(probes) or 0.0,
                mean_post_compaction_rereads=_mean(rereads) or 0.0,
            )
        )
    return out


def _build_retrieval_usage(accepted: list[dict[str, Any]]) -> list[RetrievalUsage]:
    groups: dict[str, list[dict[str, Any]]] = {}
    for trace in accepted:
        groups.setdefault(str(trace.get("arm")), []).append(trace)
    out: list[RetrievalUsage] = []
    for arm in ARMS:
        cells = groups.get(arm, [])
        if not cells:
            continue
        searches = 0
        zero_match = 0
        stale = 0
        layers: dict[str, int] = {}
        for cell in cells:
            calm = cell.get("calm") or {}
            searches += int(calm.get("searches_attempted", 0) or 0)
            zero_match += int(calm.get("search_intent_zero_match_total", 0) or 0)
            for layer, count in (calm.get("match_layer_counts") or {}).items():
                layers[layer] = layers.get(layer, 0) + int(count or 0)
            tm = cell.get("transcript") or {}
            stale += int(tm.get("post_compaction_rereads", 0) or 0)
        out.append(
            RetrievalUsage(
                arm=arm,
                accepted_cells=len(cells),
                searches_attempted=searches,
                intent_zero_match=zero_match,
                match_layer_counts=layers,
                stale_signal_encounters=stale,
            )
        )
    return out


def _fmt_ratio(ratio: float | None) -> str:
    return f"{ratio:.3f}" if ratio is not None else "n/a"


def render_markdown(report: PairedRunReport) -> str:
    lines = [
        "# CALM paired-run benchmark report",
        "",
        "Gate metric is per-run Σ `usage.output_tokens` (deduped by message id). "
        "Ratio = arm mean / raw-native mean per task; quadrant verdict = median of its task ratios.",
        "",
        "Bands: `>1.0` failing · `(0.75,1.0]` no_gain · `(0.50,0.75]` viable · `≤0.50` success.",
        "",
        "## Run",
        "",
        "| Metric | Value |",
        "|---|---:|",
        f"| total cells | {report.total_cells} |",
        f"| accepted cells | {report.accepted_cells} |",
        f"| excluded cells | {report.excluded_cells} |",
        "",
        "## Gate verdicts (per quadrant × arm)",
        "",
        "| Quadrant | Arm | Median ratio | Verdict | More reps? |",
        "|---|---|---:|---|---|",
    ]
    for gv in report.quadrant_verdicts:
        lines.append(
            f"| {gv.quadrant} | {gv.arm} | {_fmt_ratio(gv.median_ratio)} | "
            f"{gv.verdict} | {'yes' if gv.needs_more_reps else 'no'} |"
        )

    lines.extend(["", "## Per-task ratios", "", "| Task | Quadrant | Arm | Reps | Arm Σout | Raw Σout | Ratio | More reps? |", "|---|---|---|---:|---:|---:|---:|---|"])
    for tr in report.task_ratios:
        arm_mean = f"{tr.arm_mean_output_tokens:.0f}" if tr.arm_mean_output_tokens is not None else "n/a"
        raw_mean = f"{tr.raw_mean_output_tokens:.0f}" if tr.raw_mean_output_tokens is not None else "n/a"
        lines.append(
            f"| {tr.task_id} | {tr.quadrant} | {tr.arm} | {tr.accepted_reps} | "
            f"{arm_mean} | {raw_mean} | {_fmt_ratio(tr.ratio)} | {'yes' if tr.needs_more_reps else 'no'} |"
        )

    lines.extend(
        [
            "",
            "## Decomposition (per quadrant × arm, mean over accepted cells)",
            "",
            "| Quadrant | Arm | Cells | Calls | Bytes/call | Read→edit pairs | Capture-probe loops | Post-compaction re-reads |",
            "|---|---|---:|---:|---:|---:|---:|---:|",
        ]
    )
    for dc in report.decomposition:
        lines.append(
            f"| {dc.quadrant} | {dc.arm} | {dc.accepted_cells} | {dc.mean_call_count:.1f} | "
            f"{dc.mean_bytes_per_call:.0f} | {dc.mean_read_before_edit_pairs:.1f} | "
            f"{dc.mean_capture_probe_loops:.1f} | {dc.mean_post_compaction_rereads:.1f} |"
        )

    lines.extend(
        [
            "",
            "## Retrieval usage (per arm)",
            "",
            "| Arm | Cells | Searches | intent_zero_match | Match layers | Stale-signal re-reads |",
            "|---|---:|---:|---:|---|---:|",
        ]
    )
    for ru in report.retrieval_usage:
        layers = ", ".join(f"{k}={v}" for k, v in sorted(ru.match_layer_counts.items())) or "none"
        lines.append(
            f"| {ru.arm} | {ru.accepted_cells} | {ru.searches_attempted} | "
            f"{ru.intent_zero_match} | {layers} | {ru.stale_signal_encounters} |"
        )

    if report.excluded:
        lines.extend(["", "## Excluded cells", "", "| Cell | Status | Reason |", "|---|---|---|"])
        for cell in report.excluded:
            lines.append(f"| {cell.get('cell_id')} | {cell.get('status')} | {cell.get('reason', '')} |")

    lines.append("")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    import argparse  # noqa: PLC0415 - CLI entry only.

    parser = argparse.ArgumentParser(description="Aggregate paired-run benchmark traces.")
    parser.add_argument("traces_dir", type=Path, help="Directory of per-cell trace JSON files.")
    parser.add_argument("--json", action="store_true", help="Emit JSON instead of markdown.")
    args = parser.parse_args(argv)

    report = build_report(load_traces(args.traces_dir))
    if args.json:
        print(json.dumps(report.as_json(), indent=2, sort_keys=True))
    else:
        print(render_markdown(report))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
