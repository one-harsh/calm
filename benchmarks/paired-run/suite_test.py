# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Offline suite-loader and runnability-gate tests."""

from __future__ import annotations

from pathlib import Path

import pytest

import suite


def test_loads_tasks_with_quadrants() -> None:
    tasks = suite.load_suite()
    assert [t.id for t in tasks] == ["t1", "t2", "t3", "t4", "t5", "t7", "t8", "t-smoke"]
    quadrants = {t.id: t.quadrant for t in tasks}
    assert quadrants == {
        "t1": "Q1",
        "t2": "Q1",
        "t3": "Q2",
        "t4": "Q2",
        "t5": "Q3",
        "t7": "Q2",
        "t8": "Q3",
        "t-smoke": "Q4",
    }


def test_prompts_are_verbatim() -> None:
    # Prompts are the plan's verbatim text (identical across all three arms —
    # one prompt field, so parity is by construction). Q2 tasks legitimately
    # name the calm artifact under modification (calm_edit_file, calm-capture);
    # that is subject matter, not tool-choice steering.
    tasks = {t.id: t for t in suite.load_suite()}
    assert tasks["t1"].prompt.startswith("The integration suite is failing")
    assert tasks["t2"].prompt.startswith("`task ci` fails its coverage gate")
    assert "replace_all" in tasks["t3"].prompt
    assert "--json" in tasks["t4"].prompt
    assert tasks["t5"].prompt.startswith("Operator bug report")
    assert "management API endpoints" in tasks["t7"].prompt
    assert "extract pipeline" in tasks["t8"].prompt
    for task in tasks.values():
        # No arm-specific steering: prompts never instruct preferring calm_* tools.
        assert "Prefer the calm" not in task.prompt
        assert "use the calm_* tools" not in task.prompt.lower()


def test_fixture_is_uniformly_a_substrate_sha_or_pending() -> None:
    # New model: every task's fixture is the substrate tip sha to check out (or
    # the PENDING placeholder before wiring). There is no 'none'/fixtureless
    # marker — fixture-less tasks share bench/base's tip once wired.
    import re

    for task in suite.load_suite():
        assert task.fixture == suite.PENDING_FIXTURE or re.fullmatch(r"[0-9a-f]{7,40}", task.fixture), (
            f"{task.id} fixture={task.fixture!r} is neither PENDING nor a sha"
        )


def test_pending_fixture_task_is_not_runnable(tmp_path: Path) -> None:
    # The runner refuses a still-unpinned fixture even when its checker exists —
    # the PENDING placeholder is a hard gate independent of checker presence.
    checks = tmp_path / "checks"
    checks.mkdir()
    (checks / "tp.sh").write_text("exit 0\n", encoding="utf-8")
    pending = suite.Task(
        id="tp", quadrant="Q1", prompt="p", fixture=suite.PENDING_FIXTURE,
        acceptance="checks/tp.sh", timeout_minutes=1, expected_cost_note="x",
    )
    runnable, reason = pending.is_runnable(tmp_path)
    assert not runnable
    assert suite.PENDING_FIXTURE in reason


def test_resolved_fixture_task_blocks_on_missing_checker(tmp_path: Path) -> None:
    # A wired substrate sha is not enough: an absent checker still blocks.
    task = suite.Task(
        id="tx", quadrant="Q2", prompt="p", fixture="deadbeef",
        acceptance="checks/tx.sh", timeout_minutes=1, expected_cost_note="x",
    )
    runnable, reason = task.is_runnable(tmp_path)
    assert not runnable
    assert "checker missing" in reason


def test_resolved_fixture_task_runnable_once_checker_present(tmp_path: Path) -> None:
    checks = tmp_path / "checks"
    checks.mkdir()
    (checks / "tx.sh").write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
    task = suite.Task(
        id="tx", quadrant="Q2", prompt="p", fixture="deadbeef",
        acceptance="checks/tx.sh", timeout_minutes=1, expected_cost_note="x",
    )
    runnable, reason = task.is_runnable(tmp_path)
    assert runnable, reason


def test_malformed_suite_rejected(tmp_path: Path) -> None:
    bad = tmp_path / "suite.yaml"
    bad.write_text("tasks: []\n", encoding="utf-8")
    with pytest.raises(suite.SuiteError):
        suite.load_suite(bad)


def test_group_by_quadrant() -> None:
    grouped = suite.group_by_quadrant(suite.load_suite())
    assert set(grouped) == {"Q1", "Q2", "Q3", "Q4"}
    assert [t.id for t in grouped["Q1"]] == ["t1", "t2"]
    assert [t.id for t in grouped["Q4"]] == ["t-smoke"]
