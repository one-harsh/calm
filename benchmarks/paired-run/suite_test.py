# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Offline suite-loader and runnability-gate tests."""

from __future__ import annotations

from pathlib import Path

import pytest

import suite


def test_loads_six_tasks_with_quadrants() -> None:
    tasks = suite.load_suite()
    assert [t.id for t in tasks] == ["t1", "t2", "t3", "t4", "t5", "t6"]
    quadrants = {t.id: t.quadrant for t in tasks}
    assert quadrants == {"t1": "Q1", "t2": "Q1", "t3": "Q2", "t4": "Q2", "t5": "Q3", "t6": "Q3"}


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
    assert "document-order reread path" in tasks["t6"].prompt
    for task in tasks.values():
        # No arm-specific steering: prompts never instruct preferring calm_* tools.
        assert "Prefer the calm" not in task.prompt
        assert "use the calm_* tools" not in task.prompt.lower()


def test_fixture_tasks_are_pending_pr2() -> None:
    tasks = {t.id: t for t in suite.load_suite()}
    assert tasks["t1"].fixture == suite.PENDING_FIXTURE
    assert tasks["t2"].fixture == suite.PENDING_FIXTURE
    assert tasks["t5"].fixture == suite.PENDING_FIXTURE
    # T3/T4/T6 need no seeded fixture.
    assert tasks["t3"].fixture == suite.NO_FIXTURE
    assert tasks["t4"].fixture == suite.NO_FIXTURE
    assert tasks["t6"].fixture == suite.NO_FIXTURE


def test_pending_fixture_task_is_not_runnable(tmp_path: Path) -> None:
    tasks = {t.id: t for t in suite.load_suite()}
    runnable, reason = tasks["t1"].is_runnable(tmp_path)
    assert not runnable
    assert suite.PENDING_FIXTURE in reason


def test_missing_checker_blocks_even_fixtureless_task(tmp_path: Path) -> None:
    tasks = {t.id: t for t in suite.load_suite()}
    # t3 has fixture=none but its checker does not exist yet.
    runnable, reason = tasks["t3"].is_runnable(tmp_path)
    assert not runnable
    assert "checker missing" in reason


def test_fixtureless_task_runnable_once_checker_present(tmp_path: Path) -> None:
    tasks = {t.id: t for t in suite.load_suite()}
    checks = tmp_path / "checks"
    checks.mkdir()
    (checks / "t3.sh").write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
    runnable, reason = tasks["t3"].is_runnable(tmp_path)
    assert runnable, reason


def test_malformed_suite_rejected(tmp_path: Path) -> None:
    bad = tmp_path / "suite.yaml"
    bad.write_text("tasks: []\n", encoding="utf-8")
    with pytest.raises(suite.SuiteError):
        suite.load_suite(bad)


def test_group_by_quadrant() -> None:
    grouped = suite.group_by_quadrant(suite.load_suite())
    assert set(grouped) == {"Q1", "Q2", "Q3"}
    assert [t.id for t in grouped["Q1"]] == ["t1", "t2"]
