# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Cell executor for the paired-run benchmark.

One cell = one (task, arm, rep). Cells are serial; arms interleave round-robin
within a task. A cell:

  reap stray adapters  ->  CALM health check  ->  disposable originless clone
  at the pinned sha on branch bench/<cell-id>  ->  arm-provisioned config home
  + self-check  ->  pre-cell session snapshot  ->  headless `claude -p`  ->
  acceptance in the clone  ->  post-cell snapshot (assert exactly one new
  session)  ->  immediate extraction  ->  archive diff/branch  ->  reap  ->
  delete clone.

Git safety is topological: the primary repo is never a cell's working
directory; the clone has no `origin`; GH_TOKEN/GITHUB_TOKEN are cleared and
GIT_TERMINAL_PROMPT=0; the permission preseed never allowlists push/remote/gh.
The runner NEVER deletes a CALM session (extraction depends on it; TTL reclaims).

The OAuth token (from `claude setup-token`) is read from a user-held 0600 file
and injected into the child env only — never logged, never in a manifest,
argv, or trace artifact.
"""

from __future__ import annotations

import json
import os
import shutil
import signal
import subprocess  # nosec B404 - drives git and the claude CLI.
import sys
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import arms
import extract
from calmclient import CalmClient, CalmError
from suite import Task, load_suite

# Env vars that leak host Claude Code / CALM state into a cell and must be
# scrubbed. CALM_ADAPTER_CONFIG_FILE is CRITICAL: calm-capture
# consults it BEFORE $CALM_HOME/adapter.yaml, so a leaked value silently
# redirects config/logging.
SCRUB_VARS = (
    "CLAUDECODE",
    "CLAUDE_CODE_ENTRYPOINT",
    "CLAUDE_CODE_SESSION_ID",
    "CLAUDE_CODE_SSE_PORT",
    "CLAUDE_PROJECT_DIR",
    "CLAUDE_CONFIG_DIR",
    "CALM_ADAPTER_CONFIG_FILE",
    "CALM_CAPTURE_ACTIVE",
    "CALM_HOME",
    "GH_TOKEN",
    "GITHUB_TOKEN",
)

# The benchmark's own adapter is a distinct binary under work_root, so process
# matching never touches a developer's own `calm-adapter`. Its name is a
# strict superset of "calm-adapter", so pgrep on the bench name is one-directional.
BENCH_ADAPTER_NAME = "calm-adapter-bench"
OAUTH_TOKEN_ENV = "CLAUDE_CODE_OAUTH_TOKEN"  # nosec B105 - env var name, not a secret.

STATUS_ACCEPTED = "accepted"
STATUS_ACCEPTANCE_FAILED = "acceptance_failed"
STATUS_INVALID = "invalid"


class RunnerAbort(Exception):
    """Raised for a sweep-fatal condition (version drift, stray adapters, format drift)."""


class HarnessFailure(Exception):
    """Raised for a per-cell harness failure (spawn/parse/CALM down) — one retry."""


# Cell-scoped errors are contained by the retry path and never abort the sweep;
# format drift (UnknownRecordShape) and RunnerAbort are the only sweep-fatal ones.
CELL_RETRYABLE = (HarnessFailure, CalmError, extract.TranscriptError, RuntimeError)


# --- config ---------------------------------------------------------------


@dataclass
class RunnerConfig:
    primary_repo: str
    pinned_sha: str
    benchmark_branch: str
    calm_base_url: str
    calm_api_key_ref: str  # a `[file:...]`/`[env:...]` reference, resolved for env only.
    calm_api_key_value: str  # resolved namespace key for the manage-API client (never logged).
    namespace: str
    db_dsn: str
    model: str
    adapter_bin: str
    capture_cli_bin: str
    adapter_commit: str  # actual adapter/CALM build commit — pinned + asserted per cell.
    oauth_token_path: str
    output_dir: str
    work_root: str
    arms_to_run: list[str] = field(default_factory=lambda: ["raw", "mcp", "hook"])
    claude_bin: str = "claude"

    @staticmethod
    def from_file(path: Path) -> "RunnerConfig":
        raw = json.loads(Path(path).read_text(encoding="utf-8"))
        return RunnerConfig(**raw)


# --- pure helpers (unit-tested offline) -----------------------------------


def cell_id(task_id: str, arm: str, rep: int) -> str:
    return f"{task_id}-{arm}-r{rep}"


def scrub_env(parent_env: dict[str, str]) -> dict[str, str]:
    """Return a child env with host state scrubbed and git locked down.

    The OAuth token is NOT added here — it is injected per spawn so it never
    lands in a base env that could be logged.
    """
    env = {k: v for k, v in parent_env.items() if k not in SCRUB_VARS}
    env["GIT_TERMINAL_PROMPT"] = "0"
    return env


def encode_project_dir(cwd: str) -> str:
    """Claude Code encodes the transcript project dir as cwd with '/' -> '-'."""
    return cwd.replace("/", "-")


def transcript_path(config_dir: Path, cwd: str, session_id: str) -> Path:
    return Path(config_dir) / "projects" / encode_project_dir(cwd) / f"{session_id}.jsonl"


def newest_transcript(config_dir: Path, cwd: str) -> Path | None:
    project_dir = Path(config_dir) / "projects" / encode_project_dir(cwd)
    if not project_dir.exists():
        return None
    candidates = sorted(project_dir.glob("*.jsonl"), key=lambda p: p.stat().st_mtime, reverse=True)
    return candidates[0] if candidates else None


def build_manifest(config: RunnerConfig, claude_cli_version: str) -> dict[str, Any]:
    return {
        "claude_cli_version": claude_cli_version,
        "model": config.model,
        "benchmark_branch_commit": config.pinned_sha,
        "adapter_commit": config.adapter_commit,
        "namespace": config.namespace,
    }


def assert_manifest_matches(baseline: dict[str, Any], current: dict[str, Any]) -> None:
    """Abort the sweep on CLI / model / adapter-build drift (by-construction pin, risk 4)."""
    for key in ("claude_cli_version", "model", "adapter_commit"):
        if baseline.get(key) != current.get(key):
            raise RunnerAbort(
                f"manifest drift on {key}: baseline={baseline.get(key)!r} current={current.get(key)!r}"
            )


# --- staged reps (report-driven) ------------------------------------------


def flagged_tasks_from_report(report_dict: dict[str, Any]) -> list[str]:
    """Task ids a report flags for +2 reps (any CALM-arm ratio near a gate boundary)."""
    flagged: list[str] = []
    for row in report_dict.get("task_ratios", []):
        if row.get("needs_more_reps") and row.get("task_id") not in flagged:
            flagged.append(str(row["task_id"]))
    return flagged


def existing_max_rep(output_dir: Path, task_id: str) -> int:
    """Highest rep index already recorded for a task across all arms (0 if none)."""
    top = 0
    for path in Path(output_dir).glob(f"{task_id}-*-r*.json"):
        suffix = path.stem.rsplit("-r", 1)
        if len(suffix) == 2 and suffix[1].isdigit():
            top = max(top, int(suffix[1]))
    return top


def next_reps(current_max: int, count: int = 2) -> list[int]:
    return list(range(current_max + 1, current_max + 1 + count))


def read_oauth_token(path: str) -> str:
    token_path = Path(path)
    if not token_path.exists():
        raise RunnerAbort(f"OAuth token file not found: {path} (create it with `claude setup-token`)")
    mode = token_path.stat().st_mode & 0o777
    if mode & 0o077:
        raise RunnerAbort(f"OAuth token file {path} is not 0600 (mode {oct(mode)}) — refusing to read a loose secret")
    return token_path.read_text(encoding="utf-8").strip()


def preflight_suite(tasks: list[Task], root: Path) -> list[str]:
    """Return blocking reasons (empty => the suite is runnable)."""
    blockers: list[str] = []
    for task in tasks:
        runnable, reason = task.is_runnable(root)
        if not runnable:
            blockers.append(f"{task.id}: {reason}")
    return blockers


# --- adapter reaping & health ---------------------------------------------


def benchmark_adapter_bin(config: RunnerConfig) -> str:
    """The benchmark's own adapter binary — a copy of config.adapter_bin under
    work_root with a benchmark-unique name, so process matching only ever sees
    benchmark adapters, never a developer's live `calm-adapter`."""
    return str(Path(config.work_root) / BENCH_ADAPTER_NAME)


def ensure_benchmark_adapter(config: RunnerConfig) -> str:
    dest = Path(benchmark_adapter_bin(config))
    dest.parent.mkdir(parents=True, exist_ok=True)
    src = Path(config.adapter_bin)
    if not dest.exists() or dest.stat().st_mtime < src.stat().st_mtime:
        shutil.copy2(src, dest)
        dest.chmod(0o755)
    return str(dest)


def stray_adapter_pids(marker: str) -> list[int]:
    """PIDs whose command line contains marker (the benchmark adapter path)."""
    result = subprocess.run(  # nosec B603 B607 - fixed argv.
        ["pgrep", "-f", marker], capture_output=True, text=True, check=False
    )
    return [int(line) for line in result.stdout.split() if line.strip().isdigit()]


def reap_adapters(marker: str) -> int:
    pids = stray_adapter_pids(marker)
    for pid in pids:
        try:
            os.kill(pid, 15)
        except ProcessLookupError:  # pragma: no cover - race with natural exit.
            continue
    return len(pids)


def assert_no_stray_adapters(marker: str) -> None:
    pids = stray_adapter_pids(marker)
    if pids:
        raise RunnerAbort(f"stray benchmark adapters before cell: {pids} — a stale adapter's live session would contaminate the cell's corpus")


# --- clone setup ----------------------------------------------------------


def _git(clone_dir: Path, args: list[str], env: dict[str, str]) -> subprocess.CompletedProcess:
    result = subprocess.run(  # nosec B603 B607 - fixed git subcommands.
        ["git", "-C", str(clone_dir), *args], env=env, capture_output=True, text=True, check=False
    )
    if result.returncode != 0:
        raise HarnessFailure(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result


def prepare_clone(config: RunnerConfig, cid: str, fixture: str, base_env: dict[str, str]) -> Path:
    clone_dir = Path(config.work_root) / f"clone-{cid}"
    if clone_dir.exists():
        shutil.rmtree(clone_dir)
    clone_dir.parent.mkdir(parents=True, exist_ok=True)
    clone = subprocess.run(  # nosec B603 B607 - disposable local clone, no network.
        ["git", "clone", "--no-hardlinks", "--quiet", config.primary_repo, str(clone_dir)],
        env=base_env,
        capture_output=True,
        text=True,
        check=False,
    )
    if clone.returncode != 0:
        raise HarnessFailure(f"git clone failed: {clone.stderr.strip()}")
    _git(clone_dir, ["checkout", "--quiet", config.pinned_sha], base_env)
    _git(clone_dir, ["checkout", "--quiet", "-b", f"bench/{cid}"], base_env)
    # Push is physically impossible: remove origin (clone-local; primary untouched).
    _git(clone_dir, ["remote", "remove", "origin"], base_env)
    if fixture not in ("none", ""):
        # Commit the fixture onto the cell branch so HEAD is the seeded state;
        # acceptance and archival then diff the agent's work-tree against HEAD.
        _git(clone_dir, ["cherry-pick", fixture], base_env)
    return clone_dir


def archive_cell(clone_dir: Path, artifacts_dir: Path, base_env: dict[str, str]) -> None:
    artifacts_dir.mkdir(parents=True, exist_ok=True)
    diff = subprocess.run(  # nosec B603 B607 - read-only git diff.
        ["git", "-C", str(clone_dir), "diff", "HEAD"], env=base_env, capture_output=True, text=True, check=False
    )
    (artifacts_dir / "cell.diff").write_text(diff.stdout, encoding="utf-8")
    log = subprocess.run(  # nosec B603 B607 - read-only git log.
        ["git", "-C", str(clone_dir), "log", "--oneline", "-n", "50"],
        env=base_env,
        capture_output=True,
        text=True,
        check=False,
    )
    (artifacts_dir / "branch.log").write_text(log.stdout, encoding="utf-8")


# --- claude spawn ---------------------------------------------------------


def claude_version(config: RunnerConfig, base_env: dict[str, str]) -> str:
    result = subprocess.run(  # nosec B603 - fixed argv.
        [config.claude_bin, "--version"], env=base_env, capture_output=True, text=True, check=False
    )
    return result.stdout.strip()


def build_claude_argv(config: RunnerConfig, arm: arms.Arm, prompt: str) -> list[str]:
    argv = [config.claude_bin, "-p", prompt, "--model", config.model, "--output-format", "json"]
    if arm.teaching_system_prompt:
        argv.extend(["--append-system-prompt", arm.teaching_system_prompt])
    return argv


def spawn_claude(
    config: RunnerConfig,
    arm: arms.Arm,
    prompt: str,
    clone_dir: Path,
    config_dir: Path,
    scrubbed_env: dict[str, str],
    extra_env: dict[str, str],
    oauth_token: str,
    timeout_seconds: int,
) -> dict[str, Any]:
    env = dict(scrubbed_env)
    env["CLAUDE_CONFIG_DIR"] = str(config_dir)
    env.update(extra_env)
    env[OAUTH_TOKEN_ENV] = oauth_token  # injected only here; never logged.
    # start_new_session puts claude and its grandchildren (go test, task, …) in
    # their own process group so a timeout can kill the WHOLE tree — otherwise
    # orphaned grandchildren keep hammering the shared Postgres.
    proc = subprocess.Popen(  # nosec B603 - fixed argv, scrubbed env, no shell.
        build_claude_argv(config, arm, prompt),
        cwd=str(clone_dir),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    try:
        stdout, stderr = proc.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired as err:
        _kill_process_group(proc)
        proc.communicate()
        raise HarnessFailure(f"claude -p timed out after {timeout_seconds}s") from err
    if proc.returncode != 0:
        raise HarnessFailure(f"claude -p exited {proc.returncode}: {stderr.strip()[:500]}")
    try:
        payload = json.loads(stdout)
    except json.JSONDecodeError as err:
        raise HarnessFailure(f"claude -p produced non-JSON result output: {err}") from err
    return payload


def _kill_process_group(proc: subprocess.Popen) -> None:
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
    except (ProcessLookupError, PermissionError):  # pragma: no cover - race with natural exit.
        proc.kill()


# --- trace assembly -------------------------------------------------------


def build_trace(
    *,
    cid: str,
    task: Task,
    arm_name: str,
    rep: int,
    status: str,
    manifest: dict[str, Any],
    transcript_measures: extract.TranscriptMeasures | None,
    calm_measures: extract.CalmMeasures | None,
    acceptance: dict[str, Any],
    failure_reason: str = "",
    started_at: str = "",
    ended_at: str = "",
) -> dict[str, Any]:
    return {
        "cell_id": cid,
        "task_id": task.id,
        "quadrant": task.quadrant,
        "arm": arm_name,
        "rep": rep,
        "status": status,
        "failure_reason": failure_reason,
        "manifest": manifest,
        "acceptance": acceptance,
        "transcript": transcript_measures.as_dict() if transcript_measures else {},
        "calm": calm_measures.as_dict() if calm_measures else {},
        "started_at": started_at,
        "ended_at": ended_at,
    }


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


# --- cell orchestration ---------------------------------------------------


def run_acceptance(task: Task, root: Path, clone_dir: Path, base_env: dict[str, str]) -> dict[str, Any]:
    checker = task.checker_path(root)
    result = subprocess.run(  # nosec B603 - checker path from the committed suite.
        ["bash", str(checker), str(clone_dir)],
        cwd=str(clone_dir),  # the checker runs against the clone, not the runner's cwd.
        env=base_env,
        capture_output=True,
        text=True,
        check=False,
    )
    return {
        "passed": result.returncode == 0,
        "returncode": result.returncode,
        "stdout_tail": result.stdout[-2000:],
        "stderr_tail": result.stderr[-2000:],
    }


def run_cell(
    config: RunnerConfig,
    task: Task,
    arm: arms.Arm,
    rep: int,
    *,
    baseline_manifest: dict[str, Any],
    oauth_token: str,
    manage_client: CalmClient,
    root: Path,
) -> dict[str, Any]:
    cid = cell_id(task.id, arm.name, rep)
    started_at = _now_iso()
    base_env = scrub_env(dict(os.environ))
    artifacts_dir = Path(config.output_dir) / cid
    config_dir = Path(config.work_root) / f"home-{cid}"
    calm_home = Path(config.work_root) / f"calm-{cid}"
    adapter_marker = benchmark_adapter_bin(config)
    clone_dir: Path | None = None

    # A retry reuses this cid, so reset both homes first — otherwise the prior
    # attempt's calm-capture.log accumulates and correlation_ids_from_log
    # double-counts, and the config home carries stale plugin/MCP state.
    _reset_dir(config_dir)
    _reset_dir(calm_home)

    assert_no_stray_adapters(adapter_marker)
    if not manage_client.health():
        raise HarnessFailure("CALM health check failed before cell")

    try:
        clone_dir = prepare_clone(config, cid, task.fixture, base_env)

        # Arm provisioning + per-cell env.
        extra_env: dict[str, str] = {}
        (calm_home / "logs").mkdir(parents=True, exist_ok=True)
        log_file = str(calm_home / "logs" / "calm-capture.log")
        arms.write_settings(config_dir, arm)
        if arm.surface == arms.SURFACE_MCP:
            env_vars = arms.adapter_env(
                calm_url=config.calm_base_url,
                client=arm.client_tag,
                api_key_ref=config.calm_api_key_ref,
                log_file=log_file,
            )
            for step in arms.mcp_provision_steps(config_dir, ensure_benchmark_adapter(config), env_vars):
                arms.run_step(step, base_env)
        elif arm.surface == arms.SURFACE_PLUGIN:
            init_env = arms.adapter_env(
                calm_url=config.calm_base_url,
                client=arm.client_tag,
                api_key_ref=config.calm_api_key_ref,
                log_file=log_file,
                calm_home=str(calm_home),
            )
            for step in arms.hook_provision_steps(config_dir, calm_home, config.capture_cli_bin, init_env):
                arms.run_step(step, base_env)
            arms.merge_permissions_into_settings(config_dir, arm)
            extra_env["CALM_HOME"] = str(calm_home)
            extra_env["CALM_ADAPTER_LOG_FILE"] = log_file
            extra_env["CALM_ADAPTER_CALM_KEEP_SESSION"] = "true"

        self_check = arms.arm_self_check(config_dir, arm)
        if not self_check.passed:
            raise HarnessFailure(f"arm self-check failed: {self_check.reasons}")

        # Pre-cell session snapshot (CALM arms).
        is_calm_arm = arm.surface in (arms.SURFACE_MCP, arms.SURFACE_PLUGIN)
        before = manage_client.list_managed_sessions(client=arm.client_tag) if is_calm_arm else []

        payload = spawn_claude(
            config,
            arm,
            task.prompt,
            clone_dir,
            config_dir,
            base_env,
            extra_env,
            oauth_token,
            task.timeout_minutes * 60,
        )
        session_id = str(payload.get("session_id", ""))

        # Locate + parse transcript.
        cwd = str(clone_dir)
        tpath = transcript_path(config_dir, cwd, session_id)
        if not tpath.exists():
            fallback = newest_transcript(config_dir, cwd)
            if fallback is None:
                raise HarnessFailure(f"transcript not found for session {session_id}")
            tpath = fallback
        transcript_measures = extract.parse_transcript(extract.load_transcript(tpath))
        artifacts_dir.mkdir(parents=True, exist_ok=True)
        shutil.copy2(tpath, artifacts_dir / "transcript.jsonl")

        # Acceptance.
        acceptance = run_acceptance(task, root, clone_dir, base_env)

        # Post-cell snapshot + join integrity (CALM arms).
        calm_measures = extract.CalmMeasures()
        if is_calm_arm:
            after = manage_client.list_managed_sessions(client=arm.client_tag)
            session = extract.assert_one_new_session(before, after)
            calm_measures.session_snapshot = session
            calm_measures = _pull_correlations(config, arm, session, calm_home, calm_measures)

        # Archive the clone diff / branch before deletion.
        archive_cell(clone_dir, artifacts_dir, base_env)

        status = STATUS_ACCEPTED if acceptance["passed"] else STATUS_ACCEPTANCE_FAILED
        trace = build_trace(
            cid=cid,
            task=task,
            arm_name=arm.name,
            rep=rep,
            status=status,
            manifest=baseline_manifest,
            transcript_measures=transcript_measures,
            calm_measures=calm_measures,
            acceptance=acceptance,
            started_at=started_at,
            ended_at=_now_iso(),
        )
        _write_trace(config, cid, trace)
        return trace
    finally:
        reap_adapters(adapter_marker)
        if clone_dir and clone_dir.exists():
            shutil.rmtree(clone_dir, ignore_errors=True)
        # The CALM session is intentionally NOT deleted (extraction depends on it).


def _reset_dir(path: Path) -> None:
    if path.exists():
        shutil.rmtree(path, ignore_errors=True)
    path.mkdir(parents=True, exist_ok=True)


def _pull_correlations(
    config: RunnerConfig,
    arm: arms.Arm,
    session: dict[str, Any],
    calm_home: Path,
    measures: extract.CalmMeasures,
) -> extract.CalmMeasures:
    log_file = calm_home / "logs" / "calm-capture.log"
    ids = extract.correlation_ids_from_log(log_file)
    namespace = str(session.get("namespace", config.namespace))
    since = str(session.get("created_at", ""))
    try:
        if ids:
            rows = extract.fetch_correlations(config.db_dsn, correlation_ids=ids)
        else:
            rows = extract.fetch_correlations(
                config.db_dsn, namespace=namespace, client=arm.client_tag, since_iso=since
            )
    except Exception as err:  # noqa: BLE001 - a DB-pull miss must not kill the cell (never-worse).
        print(f"correlations pull failed for {arm.client_tag}: {err}", file=sys.stderr)
        return measures
    pulled = extract.parse_correlation_rows(rows)
    pulled.session_snapshot = measures.session_snapshot
    return pulled


def _write_trace(config: RunnerConfig, cid: str, trace: dict[str, Any]) -> None:
    out_dir = Path(config.output_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / f"{cid}.json").write_text(json.dumps(trace, indent=2, sort_keys=True), encoding="utf-8")


# --- CLI ------------------------------------------------------------------


def _manage_client(config: RunnerConfig) -> CalmClient:
    return CalmClient(config.calm_base_url, config.calm_api_key_value)


def cmd_selfcheck(config: RunnerConfig, root: Path) -> int:
    """Provision each requested arm into a throwaway home and self-check it."""
    base_env = scrub_env(dict(os.environ))
    ok = True
    for arm_name in config.arms_to_run:
        arm = arms.ARMS[arm_name]
        config_dir = Path(config.work_root) / f"selfcheck-home-{arm_name}"
        calm_home = Path(config.work_root) / f"selfcheck-calm-{arm_name}"
        arms.write_settings(config_dir, arm)
        if arm.surface == arms.SURFACE_MCP:
            env_vars = arms.adapter_env(
                calm_url=config.calm_base_url,
                client=arm.client_tag,
                api_key_ref=config.calm_api_key_ref,
                log_file=str(calm_home / "logs" / "calm-capture.log"),
            )
            for step in arms.mcp_provision_steps(config_dir, ensure_benchmark_adapter(config), env_vars):
                arms.run_step(step, base_env)
        elif arm.surface == arms.SURFACE_PLUGIN:
            (calm_home / "logs").mkdir(parents=True, exist_ok=True)
            init_env = arms.adapter_env(
                calm_url=config.calm_base_url,
                client=arm.client_tag,
                api_key_ref=config.calm_api_key_ref,
                log_file=str(calm_home / "logs" / "calm-capture.log"),
                calm_home=str(calm_home),
            )
            for step in arms.hook_provision_steps(config_dir, calm_home, config.capture_cli_bin, init_env):
                arms.run_step(step, base_env)
            arms.merge_permissions_into_settings(config_dir, arm)
        result = arms.arm_self_check(config_dir, arm)
        status = "PASS" if result.passed else "FAIL"
        print(f"[{status}] arm {arm_name}: evidence={result.evidence} reasons={result.reasons}")
        ok = ok and result.passed
    return 0 if ok else 1


def cmd_preflight(config: RunnerConfig, root: Path) -> int:
    tasks = load_suite()
    blockers = preflight_suite(tasks, root)
    if blockers:
        print("Suite is NOT runnable — blockers:")
        for blocker in blockers:
            print(f"  - {blocker}")
        return 1
    print("Suite is runnable: all fixtures resolved and checkers present.")
    return 0


def cmd_sweep(config: RunnerConfig, root: Path, dry_run_task: str | None) -> int:
    tasks = load_suite()
    if dry_run_task:
        tasks = [t for t in tasks if t.id == dry_run_task]
        if not tasks:
            print(f"no such task: {dry_run_task}", file=sys.stderr)
            return 2
    # sweep-1 = every task at rep 1, arms interleaved round-robin.
    grid = [(task, 1) for task in tasks]
    return _execute_grid(config, root, grid)


def cmd_reps(config: RunnerConfig, root: Path, report_path: Path) -> int:
    """Run +2 reps for every task the report flags near a gate boundary, all arms
    paired, with rep indices that never overwrite existing traces."""
    report_dict = json.loads(Path(report_path).read_text(encoding="utf-8"))
    flagged = flagged_tasks_from_report(report_dict)
    if not flagged:
        print("no tasks flagged for additional reps.")
        return 0
    suite_by_id = {t.id: t for t in load_suite()}
    grid: list[tuple[Task, int]] = []
    for task_id in flagged:
        task = suite_by_id.get(task_id)
        if task is None:
            print(f"flagged task {task_id} not in suite — skipping", file=sys.stderr)
            continue
        base = existing_max_rep(Path(config.output_dir), task_id)
        for rep in next_reps(base):
            grid.append((task, rep))
    return _execute_grid(config, root, grid)


def cmd_run_cell(config: RunnerConfig, root: Path, task_id: str, arm_name: str, rep: int) -> int:
    suite_by_id = {t.id: t for t in load_suite()}
    task = suite_by_id.get(task_id)
    if task is None:
        print(f"no such task: {task_id}", file=sys.stderr)
        return 2
    if arm_name not in config.arms_to_run:
        print(f"arm {arm_name} not in arms_to_run {config.arms_to_run}", file=sys.stderr)
        return 2
    return _execute_grid(config, root, [(task, rep)], arms_override=[arm_name])


def _execute_grid(
    config: RunnerConfig,
    root: Path,
    grid: list[tuple[Task, int]],
    arms_override: list[str] | None = None,
) -> int:
    """Run a set of (task, rep) pairs across the configured arms, round-robin
    within each pair. Cell-scoped failures are contained; only RunnerAbort (drift,
    stray adapters, format drift) stops the sweep."""
    arm_names = arms_override or config.arms_to_run
    unique_tasks = {t.id: t for t, _ in grid}.values()
    blockers = preflight_suite(list(unique_tasks), root)
    if blockers:
        print("Refusing to run — suite not ready:", file=sys.stderr)
        for blocker in blockers:
            print(f"  - {blocker}", file=sys.stderr)
        return 1

    try:
        oauth_token = read_oauth_token(config.oauth_token_path)
        base_env = scrub_env(dict(os.environ))
        ensure_benchmark_adapter(config)
        baseline_manifest = build_manifest(config, claude_version(config, base_env))
        with _manage_client(config) as manage_client:
            for task, rep in grid:
                for arm_name in arm_names:
                    arm = arms.ARMS[arm_name]
                    assert_manifest_matches(baseline_manifest, build_manifest(config, claude_version(config, base_env)))
                    trace = _run_cell_with_retry(
                        config, task, arm, rep,
                        baseline_manifest=baseline_manifest,
                        oauth_token=oauth_token,
                        manage_client=manage_client,
                        root=root,
                    )
                    print(f"cell {trace['cell_id']}: {trace['status']}")
    except RunnerAbort as abort:
        print(f"SWEEP ABORTED (sweep-fatal): {abort}", file=sys.stderr)
        return 3
    return 0


def _run_cell_with_retry(config: RunnerConfig, task: Task, arm: arms.Arm, rep: int, **kwargs: Any) -> dict[str, Any]:
    """Harness failure → one retry → STATUS_INVALID. Cell-scoped errors
    (provisioning, manage-API, join ambiguity, malformed transcript line) are
    contained here and never escape the cell loop. RunnerAbort and format drift
    (UnknownRecordShape) propagate — they are sweep-fatal by design."""
    last_err: Exception | None = None
    for attempt in (1, 2):
        try:
            return run_cell(config, task, arm, rep, **kwargs)
        except (RunnerAbort, extract.UnknownRecordShape):
            raise
        except CELL_RETRYABLE as err:
            last_err = err
            print(
                f"harness failure (attempt {attempt}/2) for {cell_id(task.id, arm.name, rep)}: {err}",
                file=sys.stderr,
            )
    cid = cell_id(task.id, arm.name, rep)
    trace = build_trace(
        cid=cid, task=task, arm_name=arm.name, rep=rep, status=STATUS_INVALID,
        manifest=kwargs["baseline_manifest"], transcript_measures=None, calm_measures=None,
        acceptance={"passed": False}, failure_reason=str(last_err),
    )
    _write_trace(config, cid, trace)
    return trace


def main(argv: list[str] | None = None) -> int:
    import argparse  # noqa: PLC0415 - CLI entry only.

    parser = argparse.ArgumentParser(description="Paired-run benchmark cell executor.")
    parser.add_argument("command", choices=("selfcheck", "preflight", "dry-run", "sweep", "reps", "run-cell"))
    parser.add_argument("--config", type=Path, required=True, help="Runner config JSON.")
    parser.add_argument("--dry-run-task", default="t-smoke", help="Task id for the dry-run gate.")
    parser.add_argument("--from-report", type=Path, help="report.json driving `reps` (needs_more_reps flags).")
    parser.add_argument("--task", help="Task id for `run-cell`.")
    parser.add_argument("--arm", help="Arm name for `run-cell`.")
    parser.add_argument("--rep", type=int, default=1, help="Rep index for `run-cell`.")
    args = parser.parse_args(argv)

    config = RunnerConfig.from_file(args.config)
    root = Path(__file__).resolve().parent
    if args.command == "selfcheck":
        return cmd_selfcheck(config, root)
    if args.command == "preflight":
        return cmd_preflight(config, root)
    if args.command == "dry-run":
        return cmd_sweep(config, root, dry_run_task=args.dry_run_task)
    if args.command == "reps":
        if not args.from_report:
            parser.error("reps requires --from-report <report.json>")
        return cmd_reps(config, root, args.from_report)
    if args.command == "run-cell":
        if not args.task or not args.arm:
            parser.error("run-cell requires --task and --arm")
        return cmd_run_cell(config, root, args.task, args.arm, args.rep)
    return cmd_sweep(config, root, dry_run_task=None)


if __name__ == "__main__":
    raise SystemExit(main())
