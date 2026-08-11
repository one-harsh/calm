# CALM paired-run benchmark report — sweep 1 (baseline)

Run 2026-08-06: 21 cells (7 tasks × raw/mcp/hook), one rep each, serial, fresh
isolated process per cell. Substrate: orphan-import branches at the recorded
manifest pin; claude CLI 2.1.212, model claude-opus-4-8.

Caveats this record carries deliberately: single-rep cells (directional, not
tight); t4 excluded — its acceptance oracle over-specified a JSON schema the
prompt never pinned, so all three arms failed the checker (an oracle defect,
not three failed runs); t2-hook/t3-mcp figures reflect genuine agent outcomes.
Ratios are within-run normalized — compare ratios across sweeps, never raw
token counts.

Gate metric is per-run Σ `usage.output_tokens` (deduped by message id). Ratio = arm mean / raw-native mean per task; quadrant verdict = median of its task ratios.

Bands: `>1.0` failing · `(0.75,1.0]` no_gain · `(0.50,0.75]` viable · `≤0.50` success.

Quadrants stratify task shape: Q1 = diagnosis (integration-failure root-cause,
coverage restoration; t1–t2) · Q2 = feature addition (t3–t4) · Q3 =
exploration-then-bounded-change (t5–t6) · Q4 = trivial smoke (t-smoke).

## Run

| Metric | Value |
|---|---:|
| total cells | 21 |
| accepted cells | 17 |
| excluded cells | 4 |

## Gate verdicts (per quadrant × arm)

| Quadrant | Arm | Median ratio | Verdict | More reps? |
|---|---|---:|---|---|
| Q1 | hook | 1.175 | failing | yes |
| Q1 | mcp | 1.303 | failing | no |
| Q2 | hook | 0.932 | no_gain | yes |
| Q2 | mcp | n/a | inconclusive | no |
| Q3 | hook | 0.953 | no_gain | yes |
| Q3 | mcp | 1.167 | failing | yes |
| Q4 | hook | 0.804 | no_gain | yes |
| Q4 | mcp | 1.452 | failing | no |

## Per-task ratios

| Task | Quadrant | Arm | Reps | Arm Σout | Raw Σout | Ratio | More reps? |
|---|---|---|---:|---:|---:|---:|---|
| t-smoke | Q4 | raw | 1 | 387 | 387 | 1.000 | no |
| t-smoke | Q4 | mcp | 1 | 562 | 387 | 1.452 | no |
| t-smoke | Q4 | hook | 1 | 311 | 387 | 0.804 | yes |
| t1 | Q1 | raw | 1 | 4989 | 4989 | 1.000 | no |
| t1 | Q1 | mcp | 1 | 6125 | 4989 | 1.228 | no |
| t1 | Q1 | hook | 1 | 5653 | 4989 | 1.133 | yes |
| t2 | Q1 | raw | 1 | 35470 | 35470 | 1.000 | no |
| t2 | Q1 | mcp | 1 | 48865 | 35470 | 1.378 | no |
| t2 | Q1 | hook | 1 | 43190 | 35470 | 1.218 | no |
| t3 | Q2 | raw | 1 | 28216 | 28216 | 1.000 | no |
| t3 | Q2 | mcp | 0 | n/a | 28216 | n/a | no |
| t3 | Q2 | hook | 1 | 26293 | 28216 | 0.932 | yes |
| t4 | Q2 | raw | 0 | n/a | n/a | n/a | no |
| t4 | Q2 | mcp | 0 | n/a | n/a | n/a | no |
| t4 | Q2 | hook | 0 | n/a | n/a | n/a | no |
| t5 | Q3 | raw | 1 | 10514 | 10514 | 1.000 | no |
| t5 | Q3 | mcp | 1 | 17039 | 10514 | 1.621 | no |
| t5 | Q3 | hook | 1 | 11811 | 10514 | 1.123 | yes |
| t6 | Q3 | raw | 1 | 96181 | 96181 | 1.000 | no |
| t6 | Q3 | mcp | 1 | 68678 | 96181 | 0.714 | yes |
| t6 | Q3 | hook | 1 | 75284 | 96181 | 0.783 | yes |

## Decomposition (per quadrant × arm, mean over accepted cells)

| Quadrant | Arm | Cells | Calls | Bytes/call | Read→edit pairs | Capture-probe loops | Post-compaction re-reads |
|---|---|---:|---:|---:|---:|---:|---:|
| Q1 | hook | 2 | 36.0 | 1774 | 1.0 | 0.0 | 0.0 |
| Q1 | mcp | 2 | 37.5 | 1325 | 0.5 | 0.0 | 0.0 |
| Q1 | raw | 2 | 20.0 | 1922 | 0.5 | 0.0 | 0.0 |
| Q2 | hook | 1 | 37.0 | 1875 | 3.0 | 0.0 | 0.0 |
| Q2 | raw | 1 | 45.0 | 1652 | 3.0 | 0.0 | 0.0 |
| Q3 | hook | 2 | 60.5 | 2010 | 5.0 | 0.0 | 0.0 |
| Q3 | mcp | 2 | 70.0 | 1432 | 4.0 | 0.5 | 0.0 |
| Q3 | raw | 2 | 63.0 | 2566 | 5.0 | 0.0 | 0.0 |
| Q4 | hook | 1 | 2.0 | 640 | 0.0 | 0.0 | 0.0 |
| Q4 | mcp | 1 | 4.0 | 79 | 0.0 | 0.0 | 0.0 |
| Q4 | raw | 1 | 2.0 | 190 | 0.0 | 0.0 | 0.0 |

## Retrieval usage (per arm)

| Arm | Cells | Searches | intent_zero_match | Match layers | Stale-signal re-reads |
|---|---:|---:|---:|---|---:|
| raw | 6 | 0 | 0 | none | 0 |
| mcp | 5 | 28 | 7 | none | 0 |
| hook | 6 | 21 | 0 | none | 0 |

## Excluded cells

| Cell | Status | Reason |
|---|---|---|
| t3-mcp-r1 | acceptance_failed | agent's change did not pass the acceptance oracle (genuine outcome) |
| t4-hook-r1 | acceptance_failed | oracle defect: checker pinned a JSON schema the prompt never specified |
| t4-mcp-r1 | acceptance_failed | oracle defect: checker pinned a JSON schema the prompt never specified |
| t4-raw-r1 | acceptance_failed | oracle defect: checker pinned a JSON schema the prompt never specified |

