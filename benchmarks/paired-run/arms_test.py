# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Offline arm-construction and self-check tests — file IO only, no CLI spawn."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

import arms


# --- settings.json posture ------------------------------------------------


def test_raw_arm_has_no_calm_surface_in_settings() -> None:
    doc = arms.settings_document(arms.ARMS["raw"])
    allow = doc["permissions"]["allow"]
    assert not any(entry.startswith("mcp__calm") for entry in allow)
    assert "Bash" in allow and "Edit" in allow and "Write" in allow


def test_mcp_arm_adds_mcp_allowlist() -> None:
    doc = arms.settings_document(arms.ARMS["mcp"])
    allow = doc["permissions"]["allow"]
    assert "mcp__calm__calm_read_file" in allow
    assert "mcp__calm__calm_run_command" in allow


def test_hook_arm_settings_have_no_mcp_allow() -> None:
    doc = arms.settings_document(arms.ARMS["hook"])
    allow = doc["permissions"]["allow"]
    assert not any(entry.startswith("mcp__calm") for entry in allow)


def test_git_push_remote_gh_are_denied_every_arm() -> None:
    for arm in arms.ARMS.values():
        deny = arms.base_permissions(arm)["deny"]
        assert "Bash(git push:*)" in deny
        assert "Bash(git remote:*)" in deny
        assert "Bash(gh:*)" in deny


# --- adapter env ----------------------------------------------------------


def test_adapter_env_uses_key_reference_not_value() -> None:
    env = arms.adapter_env(
        calm_url="http://localhost:8080",
        client="bench-mcp",
        api_key_ref="[file:/home/u/.calm/key]",
        log_file="/tmp/c/logs/calm-capture.log",
    )
    assert env["CALM_ADAPTER_CALM_API_KEY"] == "[file:/home/u/.calm/key]"
    assert env["CALM_ADAPTER_CALM_KEEP_SESSION"] == "true"
    assert env["CALM_ADAPTER_CALM_CLIENT"] == "bench-mcp"
    # A session that expires mid-sweep cascades its correlations away.
    assert int(env["CALM_ADAPTER_CALM_SESSION_TTL_MINUTES"]) >= 1440
    # No raw secret material anywhere in the env values.
    assert all("secret" not in v.lower() for v in env.values())


def test_validate_key_ref_accepts_file_and_env_refs() -> None:
    arms.validate_key_ref("[file:/home/u/.calm/key]")
    arms.validate_key_ref("[env:CALM_BENCH_KEY]")


def test_adapter_env_rejects_raw_pasted_key() -> None:
    # A raw key would land in argv (`claude mcp add --env`) and the persisted
    # .claude.json — refuse it before it can leak.
    with pytest.raises(ValueError):
        arms.adapter_env(
            calm_url="http://localhost:8080",
            client="bench-mcp",
            api_key_ref="sk-live-abcdef123456",
            log_file="/tmp/c/logs/calm-capture.log",
        )
    with pytest.raises(ValueError):
        arms.validate_key_ref("[file:/k")  # unterminated reference


def test_mcp_add_command_carries_env_and_binary_no_raw_key() -> None:
    step = arms.mcp_add_command(
        Path("/w/home-x"),
        "/repo/bin/calm-adapter",
        arms.adapter_env(
            calm_url="http://localhost:8080",
            client="bench-mcp",
            api_key_ref="[file:/k]",
            log_file="/l",
        ),
    )
    assert step.argv[:4] == ["claude", "mcp", "add", "calm"]
    assert step.argv[-1] == "/repo/bin/calm-adapter"
    assert "--env" in step.argv
    joined = " ".join(step.argv)
    assert "CALM_ADAPTER_CALM_API_KEY=[file:/k]" in joined
    assert step.env["CLAUDE_CONFIG_DIR"] == "/w/home-x"


def test_hook_provision_steps_order_and_marketplace_path() -> None:
    steps = arms.hook_provision_steps(
        Path("/w/home-hook"),
        Path("/w/calm-hook"),
        "/repo/bin/calm-capture",
        {"CALM_ADAPTER_CALM_URL": "http://localhost:8080"},
    )
    descriptions = [s.description for s in steps]
    assert "write adapter.yaml + credentials + plugin bundle" == descriptions[0]
    # init runs first, then marketplace add, then install.
    assert steps[0].argv[:2] == ["/repo/bin/calm-capture", "init"]
    add = next(s for s in steps if s.argv[:3] == ["claude", "plugin", "marketplace"] and "add" in s.argv)
    assert str(Path("/w/calm-hook/plugins/claude")) in add.argv
    install = next(s for s in steps if s.argv[:3] == ["claude", "plugin", "install"])
    assert install.argv[-1] == arms.PLUGIN_NAME


# --- self-check against fabricated homes ----------------------------------


def _write(config_dir: Path, claude_json: dict | None = None, settings: dict | None = None) -> None:
    config_dir.mkdir(parents=True, exist_ok=True)
    if claude_json is not None:
        (config_dir / ".claude.json").write_text(json.dumps(claude_json), encoding="utf-8")
    if settings is not None:
        (config_dir / "settings.json").write_text(json.dumps(settings), encoding="utf-8")


def test_raw_self_check_passes_when_clean(tmp_path: Path) -> None:
    _write(tmp_path, claude_json={}, settings=arms.settings_document(arms.ARMS["raw"]))
    result = arms.arm_self_check(tmp_path, arms.ARMS["raw"])
    assert result.passed, result.reasons


def test_raw_self_check_fails_if_mcp_leaks(tmp_path: Path) -> None:
    _write(tmp_path, claude_json={"mcpServers": {"calm": {"command": "x"}}}, settings={})
    result = arms.arm_self_check(tmp_path, arms.ARMS["raw"])
    assert not result.passed
    assert any("calm MCP server" in r for r in result.reasons)


def test_mcp_self_check_requires_server_and_allow(tmp_path: Path) -> None:
    _write(
        tmp_path,
        claude_json={"mcpServers": {"calm": {"command": "x"}}},
        settings=arms.settings_document(arms.ARMS["mcp"]),
    )
    result = arms.arm_self_check(tmp_path, arms.ARMS["mcp"])
    assert result.passed, result.reasons


def test_mcp_self_check_fails_without_server(tmp_path: Path) -> None:
    _write(tmp_path, claude_json={}, settings=arms.settings_document(arms.ARMS["mcp"]))
    result = arms.arm_self_check(tmp_path, arms.ARMS["mcp"])
    assert not result.passed


def test_hook_self_check_requires_plugin_only(tmp_path: Path) -> None:
    settings = arms.settings_document(arms.ARMS["hook"])
    settings["enabledPlugins"] = {"calm-capture@calm-capture": True}
    _write(tmp_path, claude_json={}, settings=settings)
    result = arms.arm_self_check(tmp_path, arms.ARMS["hook"])
    assert result.passed, result.reasons


def test_hook_self_check_fails_if_mcp_also_present(tmp_path: Path) -> None:
    settings = arms.settings_document(arms.ARMS["hook"])
    settings["enabledPlugins"] = {"calm-capture@calm-capture": True}
    _write(tmp_path, claude_json={"mcpServers": {"calm": {}}}, settings=settings)
    result = arms.arm_self_check(tmp_path, arms.ARMS["hook"])
    assert not result.passed
    assert any("MCP server" in r for r in result.reasons)


def test_merge_permissions_preserves_plugin_keys(tmp_path: Path) -> None:
    settings_path = tmp_path / "settings.json"
    tmp_path.mkdir(parents=True, exist_ok=True)
    settings_path.write_text(
        json.dumps({"enabledPlugins": {"calm-capture@calm-capture": True}, "extraKnownMarketplaces": {"m": {}}}),
        encoding="utf-8",
    )
    arms.merge_permissions_into_settings(tmp_path, arms.ARMS["hook"])
    merged = json.loads(settings_path.read_text(encoding="utf-8"))
    assert merged["enabledPlugins"] == {"calm-capture@calm-capture": True}
    assert merged["extraKnownMarketplaces"] == {"m": {}}
    assert "permissions" in merged
