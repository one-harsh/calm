# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Offline tests for the runner's pure helpers — no clones, no CLI spawn."""

from __future__ import annotations

import dataclasses
from pathlib import Path

import pytest

import arms
import extract
import runner
from calmclient import CalmError
from suite import Task, load_suite


def _task() -> Task:
    return Task(
        id="t1", quadrant="Q1", prompt="p", fixture="deadbeef",
        acceptance="checks/t1.sh", timeout_minutes=1, expected_cost_note="x",
    )


def _raise(exc: Exception):
    def _fn(*_a, **_k):
        raise exc
    return _fn


def test_cell_id() -> None:
    assert runner.cell_id("t1", "mcp", 2) == "t1-mcp-r2"


def test_scrub_env_removes_host_state_and_locks_git() -> None:
    parent = {
        "CLAUDECODE": "1",
        "CLAUDE_CODE_ENTRYPOINT": "cli",
        "CLAUDE_CODE_SESSION_ID": "abc",
        "CLAUDE_CODE_SSE_PORT": "9",
        "CLAUDE_PROJECT_DIR": "/x",
        "CLAUDE_CONFIG_DIR": "/host",
        "CALM_ADAPTER_CONFIG_FILE": "/leak",
        "CALM_CAPTURE_ACTIVE": "1",
        "CALM_HOME": "/host-calm",
        "GH_TOKEN": "ghp_x",
        "GITHUB_TOKEN": "ghp_y",
        "PATH": "/usr/bin",
    }
    env = runner.scrub_env(parent)
    for key in runner.SCRUB_VARS:
        assert key not in env
    assert env["GIT_TERMINAL_PROMPT"] == "0"
    assert env["PATH"] == "/usr/bin"
    # The OAuth token is never injected by scrub_env.
    assert runner.OAUTH_TOKEN_ENV not in env


def test_scrub_env_critical_config_file_var_removed() -> None:
    # CALM_ADAPTER_CONFIG_FILE overrides $CALM_HOME/adapter.yaml — a leak
    # silently redirects config/logging.
    env = runner.scrub_env({"CALM_ADAPTER_CONFIG_FILE": "/leak"})
    assert "CALM_ADAPTER_CONFIG_FILE" not in env


def test_encode_project_dir() -> None:
    assert runner.encode_project_dir("/private/tmp/work") == "-private-tmp-work"


def test_encode_project_dir_encodes_slash_and_dot() -> None:
    # Empirical rule confirmed on the live gate: BOTH '/' and '.' collapse to
    # '-', so a '/.' boundary yields a double dash.
    cwd = "/Users/harsh/.calm/bench/work/clone-t-smoke-raw-r1"
    assert runner.encode_project_dir(cwd) == "-Users-harsh--calm-bench-work-clone-t-smoke-raw-r1"


def test_transcript_path_layout() -> None:
    path = runner.transcript_path(Path("/home/x"), "/w/clone", "sess-uuid")
    assert path == Path("/home/x/projects/-w-clone/sess-uuid.jsonl")


def test_newest_transcript_finds_encoded_project_dir(tmp_path: Path) -> None:
    cwd = "/w/.calm/clone-x"
    proj = tmp_path / "projects" / runner.encode_project_dir(cwd)
    proj.mkdir(parents=True)
    (proj / "sess.jsonl").write_text("{}", encoding="utf-8")
    assert runner.newest_transcript(tmp_path, cwd) == proj / "sess.jsonl"


def test_newest_transcript_fallback_scans_all_project_dirs(tmp_path: Path) -> None:
    # The transcript lives under a project dir whose encoding we did NOT predict;
    # the per-cell config home means any *.jsonl in it is this cell's, so the
    # last-resort scan still finds it (encoding drift is non-fatal).
    other = tmp_path / "projects" / "-some-unexpected-encoding"
    other.mkdir(parents=True)
    found = other / "sess.jsonl"
    found.write_text("{}", encoding="utf-8")
    assert runner.newest_transcript(tmp_path, "/w/clone-that-encodes-elsewhere") == found


def test_newest_transcript_none_when_no_projects(tmp_path: Path) -> None:
    assert runner.newest_transcript(tmp_path, "/w/clone") is None


def test_build_manifest_has_no_secrets() -> None:
    config = _config()
    manifest = runner.build_manifest(config, "2.1.212")
    assert manifest["claude_cli_version"] == "2.1.212"
    assert manifest["model"] == "opus"
    assert manifest["benchmark_branch_commit"] == "abc123"
    assert manifest["adapter_commit"] == "adaptersha"
    # No token/key material in the manifest.
    blob = str(manifest).lower()
    assert "token" not in blob and "api_key" not in blob


def test_assert_manifest_matches_aborts_on_model_drift() -> None:
    base = {"claude_cli_version": "2.1.212", "model": "opus"}
    drift = {"claude_cli_version": "2.1.212", "model": "opus-next"}
    runner.assert_manifest_matches(base, dict(base))  # no raise
    with pytest.raises(runner.RunnerAbort):
        runner.assert_manifest_matches(base, drift)


def test_assert_manifest_matches_aborts_on_cli_drift() -> None:
    base = {"claude_cli_version": "2.1.212", "model": "opus"}
    drift = {"claude_cli_version": "2.1.300", "model": "opus"}
    with pytest.raises(runner.RunnerAbort):
        runner.assert_manifest_matches(base, drift)


def test_assert_manifest_matches_aborts_on_adapter_commit_drift() -> None:
    base = {"claude_cli_version": "2.1.212", "model": "opus", "adapter_commit": "a"}
    drift = {"claude_cli_version": "2.1.212", "model": "opus", "adapter_commit": "b"}
    runner.assert_manifest_matches(base, dict(base))  # no raise
    with pytest.raises(runner.RunnerAbort):
        runner.assert_manifest_matches(base, drift)


def test_benchmark_adapter_marker_is_under_work_root_and_distinct() -> None:
    config = _config()
    marker = runner.benchmark_adapter_bin(config)
    # The benchmark adapter lives under work_root and its name is a strict
    # superset of "calm-adapter", so pgrep on it never matches a dev's adapter.
    assert marker == "/work/calm-adapter-bench"
    assert marker.startswith(config.work_root)
    assert "calm-adapter-bench" not in "calm-adapter"


def test_flagged_tasks_from_report() -> None:
    report_dict = {
        "task_ratios": [
            {"task_id": "t1", "arm": "mcp", "needs_more_reps": True},
            {"task_id": "t1", "arm": "hook", "needs_more_reps": False},
            {"task_id": "t2", "arm": "mcp", "needs_more_reps": False},
            {"task_id": "t5", "arm": "hook", "needs_more_reps": True},
        ]
    }
    assert runner.flagged_tasks_from_report(report_dict) == ["t1", "t5"]


def test_existing_max_rep_and_next_reps(tmp_path: Path) -> None:
    (tmp_path / "t1-raw-r1.json").write_text("{}", encoding="utf-8")
    (tmp_path / "t1-mcp-r1.json").write_text("{}", encoding="utf-8")
    (tmp_path / "t1-hook-r3.json").write_text("{}", encoding="utf-8")
    (tmp_path / "t2-raw-r1.json").write_text("{}", encoding="utf-8")
    assert runner.existing_max_rep(tmp_path, "t1") == 3
    assert runner.existing_max_rep(tmp_path, "t2") == 1
    assert runner.existing_max_rep(tmp_path, "t9") == 0
    # +2 reps that never collide with existing traces.
    assert runner.next_reps(3) == [4, 5]
    assert runner.next_reps(0) == [1, 2]


# --- failure taxonomy — cell-scoped errors never abort the sweep ----------


def test_retry_contains_cell_scoped_error_and_marks_invalid(tmp_path: Path, monkeypatch) -> None:
    config = dataclasses.replace(_config(), output_dir=str(tmp_path))
    attempts = {"n": 0}

    def boom(*_a, **_k):
        attempts["n"] += 1
        raise CalmError("manage API returned HTTP 503")  # cell-scoped, not sweep-fatal.

    monkeypatch.setattr(runner, "run_cell", boom)
    trace = runner._run_cell_with_retry(
        config, _task(), arms.ARMS["mcp"], 1,
        baseline_manifest={"model": "opus"}, oauth_token="", manage_client=None, root=Path("."),
    )
    assert attempts["n"] == 2  # exactly one retry
    assert trace["status"] == runner.STATUS_INVALID
    assert (tmp_path / "t1-mcp-r1.json").exists()


def test_retry_contains_provisioning_runtime_error(tmp_path: Path, monkeypatch) -> None:
    config = dataclasses.replace(_config(), output_dir=str(tmp_path))
    monkeypatch.setattr(runner, "run_cell", _raise(RuntimeError("provision step failed")))
    trace = runner._run_cell_with_retry(
        config, _task(), arms.ARMS["hook"], 1,
        baseline_manifest={"model": "opus"}, oauth_token="", manage_client=None, root=Path("."),
    )
    assert trace["status"] == runner.STATUS_INVALID


def test_retry_contains_join_ambiguity(tmp_path: Path, monkeypatch) -> None:
    config = dataclasses.replace(_config(), output_dir=str(tmp_path))
    monkeypatch.setattr(runner, "run_cell", _raise(extract.TranscriptError("two new sessions")))
    trace = runner._run_cell_with_retry(
        config, _task(), arms.ARMS["mcp"], 1,
        baseline_manifest={"model": "opus"}, oauth_token="", manage_client=None, root=Path("."),
    )
    assert trace["status"] == runner.STATUS_INVALID


def test_runner_abort_propagates_out_of_cell(monkeypatch) -> None:
    monkeypatch.setattr(runner, "run_cell", _raise(runner.RunnerAbort("stray adapter")))
    with pytest.raises(runner.RunnerAbort):
        runner._run_cell_with_retry(
            _config(), _task(), arms.ARMS["mcp"], 1,
            baseline_manifest={}, oauth_token="", manage_client=None, root=Path("."),
        )


def test_format_drift_propagates_out_of_cell(monkeypatch) -> None:
    # True format drift is sweep-fatal — the version pin exists to catch it.
    monkeypatch.setattr(runner, "run_cell", _raise(extract.UnknownRecordShape("unknown type 'meteorite'")))
    with pytest.raises(extract.UnknownRecordShape):
        runner._run_cell_with_retry(
            _config(), _task(), arms.ARMS["mcp"], 1,
            baseline_manifest={}, oauth_token="", manage_client=None, root=Path("."),
        )


# --- a retry starts from a clean home (log does not accumulate) -----------


def test_reset_dir_wipes_stale_content(tmp_path: Path) -> None:
    home = tmp_path / "calm-t1-mcp-r1"
    logs = home / "logs"
    logs.mkdir(parents=True)
    (logs / "calm-capture.log").write_text("prior attempt's correlation ids\n", encoding="utf-8")
    runner._reset_dir(home)
    assert home.exists()
    assert list(home.iterdir()) == []  # nothing from the prior attempt survives


# --- a timeout kills the whole process group, not just the parent ---------


def test_kill_process_group_targets_the_group(monkeypatch) -> None:
    import signal

    class _FakeProc:
        pid = 4321

        def kill(self):  # pragma: no cover - only the fallback path.
            self.killed = True

    killed = {}
    monkeypatch.setattr(runner.os, "getpgid", lambda pid: 9999)
    monkeypatch.setattr(runner.os, "killpg", lambda pgid, sig: killed.update(pgid=pgid, sig=sig))
    runner._kill_process_group(_FakeProc())
    assert killed == {"pgid": 9999, "sig": signal.SIGKILL}


# --- acceptance runs in the clone, not the runner's cwd -------------------


def test_run_acceptance_runs_in_clone_dir(tmp_path: Path, monkeypatch) -> None:
    captured = {}

    class _Result:
        returncode = 0
        stdout = ""
        stderr = ""

    def fake_run(argv, **kwargs):
        captured["argv"] = argv
        captured["cwd"] = kwargs.get("cwd")
        return _Result()

    monkeypatch.setattr(runner.subprocess, "run", fake_run)
    clone = tmp_path / "clone-t1-mcp-r1"
    clone.mkdir()
    result = runner.run_acceptance(_task(), Path("/root"), clone, {})
    assert captured["cwd"] == str(clone)
    assert captured["argv"][0] == "bash"
    assert result["passed"] is True


def test_read_oauth_token_refuses_loose_permissions(tmp_path: Path) -> None:
    token = tmp_path / "oauth-token"
    token.write_text("secret-value", encoding="utf-8")
    token.chmod(0o644)
    with pytest.raises(runner.RunnerAbort):
        runner.read_oauth_token(str(token))


def test_read_oauth_token_reads_0600(tmp_path: Path) -> None:
    token = tmp_path / "oauth-token"
    token.write_text("secret-value\n", encoding="utf-8")
    token.chmod(0o600)
    assert runner.read_oauth_token(str(token)) == "secret-value"


def test_read_oauth_token_missing_aborts(tmp_path: Path) -> None:
    with pytest.raises(runner.RunnerAbort):
        runner.read_oauth_token(str(tmp_path / "nope"))


def test_build_claude_argv_appends_teaching_for_mcp_only() -> None:
    config = _config()
    raw_argv = runner.build_claude_argv(config, arms.ARMS["raw"], "do the task")
    mcp_argv = runner.build_claude_argv(config, arms.ARMS["mcp"], "do the task")
    assert "--append-system-prompt" not in raw_argv
    assert "--append-system-prompt" in mcp_argv
    assert "--dangerously-skip-permissions" not in mcp_argv
    assert mcp_argv[:3] == ["claude", "-p", "do the task"]
    assert "--model" in mcp_argv and "opus" in mcp_argv


def test_preflight_suite_blocks_until_fixtures_land(tmp_path: Path) -> None:
    blockers = runner.preflight_suite(load_suite(), tmp_path)
    # Every task is blocked here: t1/t2/t5 on their PENDING fixtures, and every
    # task because its checker resolves under the (empty) root passed in, where
    # no checks/ tree exists. Both blocking reasons are exercised.
    ids = {b.split(":")[0] for b in blockers}
    assert ids == {"t1", "t2", "t3", "t4", "t5", "t6", "t-smoke"}


def _config() -> "runner.RunnerConfig":
    return runner.RunnerConfig(
        primary_repo="/repo",
        pinned_sha="abc123",
        benchmark_branch="bench",
        calm_base_url="http://localhost:8080",
        calm_api_key_ref="[file:/k]",
        calm_api_key_value="resolved-key",
        namespace="bench",
        db_dsn="postgresql://postgres:postgres@localhost:5432/calm",
        model="opus",
        adapter_bin="/repo/bin/calm-adapter",
        capture_cli_bin="/repo/bin/calm-capture",
        adapter_commit="adaptersha",
        oauth_token_path="/home/u/.claude/benchmark-oauth-token",
        output_dir="/out",
        work_root="/work",
    )


def test_prepare_clone_checks_out_substrate_without_cherry_pick(tmp_path: Path, monkeypatch) -> None:
    # New model: the substrate sha is checked out directly (no cherry-pick), and
    # every other local branch is deleted so no pristine-code ref remains to diff.
    calls: list[list[str]] = []

    def fake_git(_clone_dir, args, _env):
        calls.append(args)
        out = "main\nharness\n" if args[:1] == ["for-each-ref"] else ""
        return runner.subprocess.CompletedProcess(args=args, returncode=0, stdout=out, stderr="")

    def fake_run(*_a, **_k):
        return runner.subprocess.CompletedProcess(args=[], returncode=0, stdout="", stderr="")

    monkeypatch.setattr(runner, "_git", fake_git)
    monkeypatch.setattr(runner.subprocess, "run", fake_run)

    config = dataclasses.replace(_config(), work_root=str(tmp_path))
    runner.prepare_clone(config, "t1-mcp-r1", "substratesha", {})

    assert ["checkout", "--quiet", "substratesha"] in calls
    assert not any(a[:1] == ["cherry-pick"] for a in calls)
    # Stray pristine branches are deleted; the cell branch is kept.
    assert ["branch", "-D", "main"] in calls
    assert ["branch", "-D", "harness"] in calls
    assert ["branch", "-D", "bench/t1-mcp-r1"] not in calls
