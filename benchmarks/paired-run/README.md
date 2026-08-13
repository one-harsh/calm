<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Paired-run benchmark

One question: **does CALM reduce overall agent token spend on real coding work,
without changing the outcome?**

Start with the verdicts: [`results/`](results/) — one file per sweep, highest
number is latest. Each shows per-task ratios of *arm tokens ÷ raw-native
tokens*. Lower is better; under 1.0, CALM saved tokens on that task.

| Ratio | Verdict |
|---|---|
| > 1.0 | failing |
| 0.75 – 1.0 | no gain |
| 0.50 – 0.75 | viable |
| ≤ 0.50 | success |

## How it works, in 60 seconds

The same task prompt runs three times — once per **arm** — on identical
repository state:

| Arm | Surface |
|---|---|
| `raw` | native tools only, no CALM anywhere |
| `mcp` | CALM's MCP tools, routing mandated |
| `hook` | CALM capture hooks on native tools, no MCP |

Each run is a sealed **cell**: fresh clone, no remotes, no ambient
credentials, fresh agent process. Afterwards a hidden **oracle** — an
acceptance test the agent never saw — grades the work, and the transcript's
output tokens are summed. Score = tokens vs the raw arm, same sweep.

Prompts never mention CALM. Tasks are real backlog items (multi-file, a
design constraint, hours of human scale), tagged by shape — diagnosis,
feature, exploration — because task shape, not averages, decides whether
context management pays.

## Pieces

| Piece | What it is | Where |
|---|---|---|
| suite | task prompts + quadrants + substrate pins | `suite.yaml` |
| substrate | orphan-branch repo snapshot cells work on; seeded defects hide in one import commit; benchmark harness stripped out | `fixtures/` |
| oracle | post-run acceptance, injected by the checker | `checks/` |
| trace | per-cell tokens, transcript, diff | runner output dir |
| report | ratios, verdicts, behavioral decomposition | `report.py` |

## Running a sweep

```
python3 runner.py selfcheck --config <cfg>   # arms expose exactly their surface
python3 runner.py preflight --config <cfg>   # fixtures + checkers present
python3 runner.py dry-run   --config <cfg>   # smoke task through every arm
python3 runner.py sweep     --config <cfg>   # the grid, serial cells
python3 report.py <traces-dir>               # ratios + verdicts
```

Before `selfcheck`, cut substrates: set base sha + branch prefix in
`fixtures/create-benchmark-branch.sh`, run it, wire the printed tips into
`suite.yaml` and your config (`config.example.json` shows the shape; secrets
stay file/env-referenced, agent token minted per run and revoked after).
Then prove every checker **fails on the unmodified substrate, for the reason
its prompt pins** — a checker failing by timeout or compile error will also
fail correct solutions later.

Record the sweep as `results/sweep-N.md` with provenance and every caveat the
run earned, and archive the traces. Results files are historical records —
numbers are never edited after the fact.

## Rules that keep it honest

- **Oracles assert only what prompts pin.** Every assertion traces to a
  prompt sentence; what the prompt leaves open is the agent's freedom.
- **Substrates carry no answer keys** — no harness, no oracle fixtures, no
  benchmark vocabulary in history, identical commit shape across tasks.
- **Prompts freeze** when the first real cell starts.
- **Compare ratios across sweeps, never raw token counts.** Task ids are
  never reused, so a retired task keeps its record.
- **Any bounded coverage is disclosed** — shed cells, skipped reps, all of it.

## Re-judging

Measurement and grading are separable on purpose. Tokens are extracted at
cell time and immutable; acceptance is a pure function of the archived cell
state plus the checker. A checker found defective after a sweep gets fixed,
and affected cells are re-judged from their archived diffs — verdicts update,
marked and disclosed; measurements never move.
