# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Task-suite loader and readiness gate for the paired-run benchmark.

``suite.yaml`` is the source of truth for the tasks. Each task's ``fixture`` is
the substrate tip sha to check out — fixture-bearing (t1/t2/t5) and fixture-less
(t3/t4/t6/t-smoke) tasks alike run against an orphan substrate branch, so the
field is uniformly "the sha to check out". A ``PENDING`` fixture has not been
wired yet and the runner refuses to run it. A task is runnable only when its
fixture is a resolved sha AND its acceptance checker exists on disk.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import yaml

PENDING_FIXTURE = "PENDING"
QUADRANTS = ("Q1", "Q2", "Q3", "Q4")

HERE = Path(__file__).resolve().parent
DEFAULT_SUITE = HERE / "suite.yaml"


class SuiteError(Exception):
    """Raised for a malformed or incomplete suite definition."""


@dataclass(frozen=True)
class Task:
    id: str
    quadrant: str
    prompt: str
    fixture: str
    acceptance: str
    timeout_minutes: int
    expected_cost_note: str

    @property
    def fixture_pending(self) -> bool:
        return self.fixture == PENDING_FIXTURE

    def checker_path(self, root: Path) -> Path:
        return (root / self.acceptance).resolve()

    def is_runnable(self, root: Path) -> tuple[bool, str]:
        """Return (runnable, reason). Reason is empty when runnable."""
        if self.fixture_pending:
            return False, f"fixture is {PENDING_FIXTURE} (not yet authored)"
        checker = self.checker_path(root)
        if not checker.exists():
            return False, f"acceptance checker missing: {self.acceptance}"
        return True, ""


def load_suite(path: Path = DEFAULT_SUITE) -> list[Task]:
    raw = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(raw, dict) or "tasks" not in raw:
        raise SuiteError(f"{path}: expected a mapping with a 'tasks' key")
    entries = raw["tasks"]
    if not isinstance(entries, list) or not entries:
        raise SuiteError(f"{path}: 'tasks' must be a non-empty list")
    tasks = [_task_from_object(path, index, item) for index, item in enumerate(entries, start=1)]
    ids = [task.id for task in tasks]
    if len(set(ids)) != len(ids):
        raise SuiteError(f"{path}: duplicate task ids in {ids}")
    return tasks


def _task_from_object(path: Path, index: int, value: object) -> Task:
    if not isinstance(value, dict):
        raise SuiteError(f"{path}: task #{index} must be a mapping")
    missing = [
        key
        for key in ("id", "quadrant", "prompt", "clone_setup", "acceptance", "timeout_minutes", "expected_cost_note")
        if key not in value
    ]
    if missing:
        raise SuiteError(f"{path}: task #{index} missing keys {missing}")
    quadrant = str(value["quadrant"])
    if quadrant not in QUADRANTS:
        raise SuiteError(f"{path}: task #{index} quadrant {quadrant!r} not in {QUADRANTS}")
    clone_setup = value["clone_setup"]
    if not isinstance(clone_setup, dict) or "fixture" not in clone_setup:
        raise SuiteError(f"{path}: task #{index} clone_setup must carry a 'fixture'")
    prompt = str(value["prompt"]).strip()
    if not prompt:
        raise SuiteError(f"{path}: task #{index} has an empty prompt")
    try:
        timeout_minutes = int(value["timeout_minutes"])
    except (TypeError, ValueError) as err:
        raise SuiteError(f"{path}: task #{index} timeout_minutes must be an integer") from err
    return Task(
        id=str(value["id"]),
        quadrant=quadrant,
        prompt=prompt,
        fixture=str(clone_setup["fixture"]),
        acceptance=str(value["acceptance"]),
        timeout_minutes=timeout_minutes,
        expected_cost_note=str(value["expected_cost_note"]),
    )


def group_by_quadrant(tasks: list[Task]) -> dict[str, list[Task]]:
    grouped: dict[str, list[Task]] = {}
    for task in tasks:
        grouped.setdefault(task.quadrant, []).append(task)
    return grouped
