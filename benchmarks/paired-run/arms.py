# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Arm construction and isolation self-checks for the paired-run benchmark.

Three arms, each an isolated ``CLAUDE_CONFIG_DIR`` (the config dir isolates
MCP registrations, settings.json, plugins, and transcripts; ``.claude.json``
lives inside it):

* raw  — no CALM surface at all.
* mcp  — the ``calm_*`` MCP server registered; teaching relocated to a
         runner-supplied appended system prompt.
* hook — the calm-capture plugin installed (PostToolUse observation, AD08);
         teaching is structural (SessionStart card).

Each arm's permission posture is IDENTICAL modulo its own tool surface:
read-only Bash auto-allows headless; write-Bash and Write need the allowlist.
No skip-permission flags. ``git push`` / ``git remote`` / ``gh`` are never
allowlisted and are additionally denied (git-safety belt-and-suspenders; the
disposable clone also has no origin).

Provisioning shell-outs are returned as ``ProvisionStep`` specs (pure,
testable) and executed by ``provision_*``; the JSON/settings assembly is pure.
"""

from __future__ import annotations

import json
import subprocess  # nosec B404 - drives the claude CLI for arm provisioning.
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

SURFACE_NONE = "none"
SURFACE_MCP = "mcp"
SURFACE_PLUGIN = "plugin"

PLUGIN_NAME = "calm-capture"
PLUGIN_QUALIFIED = "calm-capture@calm-capture"

# Tool families the six tasks legitimately need. Kept minimal; broad `Bash` is
# allowed for build/test/git-inspection but push/remote/gh are denied below.
BASE_ALLOW = (
    "Bash",
    "Edit",
    "MultiEdit",
    "Write",
    "Read",
    "Grep",
    "Glob",
    "LS",
    "NotebookEdit",
    "TodoWrite",
    "Task",
)
BASE_DENY = (
    "Bash(git push:*)",
    "Bash(git remote:*)",
    "Bash(gh:*)",
)
MCP_ALLOW = (
    "mcp__calm__calm_read_file",
    "mcp__calm__calm_run_command",
    "mcp__calm__calm_grep",
    "mcp__calm__calm_list_dir",
    "mcp__calm__calm_write_file",
    "mcp__calm__calm_edit_file",
    "mcp__calm__calm_search",
    "mcp__calm__calm_git_status",
    "mcp__calm__calm_git_diff",
)

MCP_TEACHING_SYSTEM_PROMPT = (
    "Prefer the calm_* tools over native command, file, grep, and git tools. "
    "Use calm_search to retrieve earlier captured output instead of rerunning a "
    "command when possible."
)


@dataclass(frozen=True)
class Arm:
    name: str
    client_tag: str
    surface: str
    teaching_system_prompt: str = ""


ARMS: dict[str, Arm] = {
    "raw": Arm(name="raw", client_tag="bench-raw", surface=SURFACE_NONE),
    "mcp": Arm(
        name="mcp",
        client_tag="bench-mcp",
        surface=SURFACE_MCP,
        teaching_system_prompt=MCP_TEACHING_SYSTEM_PROMPT,
    ),
    "hook": Arm(name="hook", client_tag="bench-hook", surface=SURFACE_PLUGIN),
}


@dataclass(frozen=True)
class ProvisionStep:
    argv: list[str]
    env: dict[str, str] = field(default_factory=dict)
    description: str = ""
    allow_failure: bool = False


@dataclass
class SelfCheckResult:
    arm: str
    passed: bool
    evidence: dict[str, Any] = field(default_factory=dict)
    reasons: list[str] = field(default_factory=list)


# --- settings.json --------------------------------------------------------


def base_permissions(arm: Arm) -> dict[str, Any]:
    allow = list(BASE_ALLOW)
    if arm.surface == SURFACE_MCP:
        allow.extend(MCP_ALLOW)
    return {"allow": allow, "deny": list(BASE_DENY)}


def settings_document(arm: Arm) -> dict[str, Any]:
    return {"permissions": base_permissions(arm)}


def write_settings(config_dir: Path, arm: Arm) -> Path:
    config_dir.mkdir(parents=True, exist_ok=True)
    path = config_dir / "settings.json"
    path.write_text(json.dumps(settings_document(arm), indent=2) + "\n", encoding="utf-8")
    return path


def merge_permissions_into_settings(config_dir: Path, arm: Arm) -> Path:
    """Read-modify-write settings.json, preserving keys the plugin CLI wrote
    (enabledPlugins / extraKnownMarketplaces). NEVER blind-overwrite it."""
    path = config_dir / "settings.json"
    current: dict[str, Any] = {}
    if path.exists():
        current = json.loads(path.read_text(encoding="utf-8"))
    current["permissions"] = base_permissions(arm)
    path.write_text(json.dumps(current, indent=2) + "\n", encoding="utf-8")
    return path


# --- adapter env ----------------------------------------------------------

# A safe api-key reference exposes only a lookup, never the secret, when it lands
# in argv (`claude mcp add --env …`) and the persisted `.claude.json`.
KEY_REF_PREFIXES = ("[file:", "[env:")


def validate_key_ref(api_key_ref: str) -> None:
    """Refuse a raw pasted key. Only `[file:...]`/`[env:...]` references may be
    passed as env: they keep the secret out of argv and the persisted config."""
    if not any(api_key_ref.startswith(prefix) for prefix in KEY_REF_PREFIXES) or not api_key_ref.endswith("]"):
        raise ValueError(
            "calm_api_key_ref must be a [file:...] or [env:...] reference, not a raw key "
            "(a raw key would leak into argv and the persisted .claude.json)"
        )


def adapter_env(
    *,
    calm_url: str,
    client: str,
    api_key_ref: str,
    log_file: str,
    calm_home: str | None = None,
    keep_session: bool = True,
    session_ttl_minutes: int = 1440,
) -> dict[str, str]:
    """CALM_ADAPTER_* env for a cell. api_key_ref is a `[file:...]`/`[env:...]`
    reference — the raw key value never enters env, argv, or a trace artifact."""
    validate_key_ref(api_key_ref)
    env = {
        "CALM_ADAPTER_CALM_URL": calm_url,
        "CALM_ADAPTER_CALM_CLIENT": client,
        "CALM_ADAPTER_CALM_API_KEY": api_key_ref,
        "CALM_ADAPTER_CALM_KEEP_SESSION": "true" if keep_session else "false",
        # Sessions must outlive the sweep: the TTL scanner's session delete
        # FK-cascades the correlations rows extraction and audits read.
        "CALM_ADAPTER_CALM_SESSION_TTL_MINUTES": str(session_ttl_minutes),
        "CALM_ADAPTER_LOG_FILE": log_file,
    }
    if calm_home:
        env["CALM_HOME"] = calm_home
    return env


# --- arm-2 (mcp) provisioning ---------------------------------------------


def mcp_add_command(config_dir: Path, adapter_bin: str, adapter_env_vars: dict[str, str]) -> ProvisionStep:
    argv = ["claude", "mcp", "add", "calm", "--scope", "user"]
    for key, value in adapter_env_vars.items():
        argv.extend(["--env", f"{key}={value}"])
    argv.extend(["--", adapter_bin])
    return ProvisionStep(
        argv=argv,
        env={"CLAUDE_CONFIG_DIR": str(config_dir)},
        description="register calm MCP server (arm-2)",
    )


def mcp_remove_command(config_dir: Path) -> ProvisionStep:
    return ProvisionStep(
        argv=["claude", "mcp", "remove", "calm", "--scope", "user"],
        env={"CLAUDE_CONFIG_DIR": str(config_dir)},
        description="drop any prior calm MCP registration",
        allow_failure=True,
    )


def mcp_provision_steps(config_dir: Path, adapter_bin: str, adapter_env_vars: dict[str, str]) -> list[ProvisionStep]:
    return [mcp_remove_command(config_dir), mcp_add_command(config_dir, adapter_bin, adapter_env_vars)]


# --- arm-3 (hook) provisioning --------------------------------------------


def hook_provision_steps(
    config_dir: Path,
    calm_home: Path,
    capture_cli_bin: str,
    calm_init_env: dict[str, str],
) -> list[ProvisionStep]:
    """S1 order: init writes the config home + plugin bundle; then marketplace
    add / install / enable under the arm config dir. Permission merge happens
    AFTER these steps (caller invokes merge_permissions_into_settings)."""
    plugin_dir = str(calm_home / "plugins" / "claude")
    config_env = {"CLAUDE_CONFIG_DIR": str(config_dir)}
    init_env = dict(calm_init_env)
    init_env["CALM_HOME"] = str(calm_home)
    return [
        ProvisionStep(
            argv=[capture_cli_bin, "init", "--harness=claude"],
            env=init_env,
            description="write adapter.yaml + credentials + plugin bundle",
        ),
        ProvisionStep(
            argv=["claude", "plugin", "marketplace", "remove", PLUGIN_NAME],
            env=config_env,
            description="drop any prior calm-capture marketplace",
            allow_failure=True,
        ),
        ProvisionStep(
            argv=["claude", "plugin", "marketplace", "add", plugin_dir],
            env=config_env,
            description="register the calm-capture marketplace",
        ),
        ProvisionStep(
            argv=["claude", "plugin", "install", PLUGIN_NAME],
            env=config_env,
            description="install the calm-capture plugin",
        ),
        ProvisionStep(
            argv=["claude", "plugin", "enable", PLUGIN_QUALIFIED, "--scope", "user"],
            env=config_env,
            description="ensure the plugin is enabled",
            allow_failure=True,
        ),
    ]


# --- executor -------------------------------------------------------------


def run_step(step: ProvisionStep, base_env: dict[str, str], dry_run: bool = False) -> subprocess.CompletedProcess | None:
    if dry_run:
        return None
    env = dict(base_env)
    env.update(step.env)
    result = subprocess.run(  # nosec B603 - fixed argv, no shell.
        step.argv,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0 and not step.allow_failure:
        raise RuntimeError(
            f"provision step failed ({step.description}): {' '.join(step.argv)}\n{result.stderr.strip()}"
        )
    return result


# --- self-check -----------------------------------------------------------


def _read_json(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return {}
    return data if isinstance(data, dict) else {}


def _has_calm_mcp(config_dir: Path) -> bool:
    claude_json = _read_json(config_dir / ".claude.json")
    settings = _read_json(config_dir / "settings.json")
    for source in (claude_json.get("mcpServers"), settings.get("mcpServers")):
        if isinstance(source, dict) and "calm" in source:
            return True
    return False


def _has_mcp_allow(config_dir: Path) -> bool:
    settings = _read_json(config_dir / "settings.json")
    allow = (settings.get("permissions") or {}).get("allow", [])
    return any(str(entry).startswith("mcp__calm") for entry in allow)


def _has_calm_plugin(config_dir: Path) -> bool:
    settings = _read_json(config_dir / "settings.json")
    enabled = settings.get("enabledPlugins")
    if isinstance(enabled, dict):
        if any(PLUGIN_NAME in str(key) and value for key, value in enabled.items()):
            return True
    if isinstance(enabled, list):
        if any(PLUGIN_NAME in str(item) for item in enabled):
            return True
    markets = settings.get("extraKnownMarketplaces")
    if isinstance(markets, dict) and any(PLUGIN_NAME in str(key) for key in markets):
        return True
    if isinstance(markets, list) and any(PLUGIN_NAME in str(item) for item in markets):
        return True
    return False


def arm_self_check(config_dir: Path, arm: Arm) -> SelfCheckResult:
    evidence = {
        "has_calm_mcp": _has_calm_mcp(config_dir),
        "has_mcp_allow": _has_mcp_allow(config_dir),
        "has_calm_plugin": _has_calm_plugin(config_dir),
    }
    reasons: list[str] = []
    if arm.surface == SURFACE_NONE:
        if evidence["has_calm_mcp"]:
            reasons.append("raw arm exposes a calm MCP server")
        if evidence["has_mcp_allow"]:
            reasons.append("raw arm allowlists mcp__calm tools")
        if evidence["has_calm_plugin"]:
            reasons.append("raw arm has the calm-capture plugin")
    elif arm.surface == SURFACE_MCP:
        if not evidence["has_calm_mcp"]:
            reasons.append("mcp arm is missing the calm MCP server")
        if not evidence["has_mcp_allow"]:
            reasons.append("mcp arm is missing the mcp__calm allowlist")
        if evidence["has_calm_plugin"]:
            reasons.append("mcp arm leaks the calm-capture plugin")
    elif arm.surface == SURFACE_PLUGIN:
        if not evidence["has_calm_plugin"]:
            reasons.append("hook arm is missing the calm-capture plugin")
        if evidence["has_calm_mcp"]:
            reasons.append("hook arm leaks a calm MCP server")
        if evidence["has_mcp_allow"]:
            reasons.append("hook arm leaks the mcp__calm allowlist")
    else:  # pragma: no cover - guarded by ARMS construction.
        reasons.append(f"unknown surface {arm.surface!r}")
    return SelfCheckResult(arm=arm.name, passed=not reasons, evidence=evidence, reasons=reasons)
