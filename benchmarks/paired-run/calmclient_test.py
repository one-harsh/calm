# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Offline CalmClient tests using the eval-harness HTTPXRecorder pattern.

The recorder is copied (not imported) from examples/eval-harness/, matching the
harness's no-cross-example-boundary rule.
"""

from __future__ import annotations

import contextlib
import io
import json
from typing import Any
from unittest import mock

import httpx
import pytest

import calmclient


class HTTPXRecorder:
    def __init__(self, responses: list[httpx.Response]):
        self.responses = responses
        self.requests: list[dict[str, Any]] = []

    def __call__(self, method: str, url: str, **kwargs: Any) -> httpx.Response:
        self.requests.append(
            {"method": method, "url": url, "headers": dict(kwargs.get("headers", {})), "json": kwargs.get("json")}
        )
        response = self.responses.pop(0)
        request = httpx.Request(method, "http://calm.test" + url)
        return httpx.Response(
            status_code=response.status_code,
            headers=response.headers,
            content=response.content,
            request=request,
        )


def _response(status: int, body: Any, headers: dict[str, str] | None = None) -> httpx.Response:
    content = b"" if status == 204 else json.dumps(body).encode("utf-8")
    return httpx.Response(status_code=status, headers=headers or {}, content=content)


def test_health_true_on_200() -> None:
    recorder = HTTPXRecorder([_response(200, {"status": "ok"})])
    with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
        with calmclient.CalmClient("http://calm.test", "api-key") as client:
            assert client.health() is True
    assert recorder.requests[0]["headers"]["X-CALM-API-Key"] == "api-key"


def test_health_false_on_503() -> None:
    recorder = HTTPXRecorder([_response(503, {"status": "degraded"})])
    with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
        with calmclient.CalmClient("http://calm.test", "api-key") as client:
            assert client.health() is False


def test_list_managed_sessions_sends_client_filter() -> None:
    body = {"sessions": [{"namespace": "bench", "client": "bench-mcp", "created_at": "t1", "event_count": 3}]}
    recorder = HTTPXRecorder([_response(200, body)])
    with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
        with calmclient.CalmClient("http://calm.test", "api-key") as client:
            sessions = client.list_managed_sessions(client="bench-mcp")
    assert sessions[0]["client"] == "bench-mcp"
    assert "client=bench-mcp" in recorder.requests[0]["url"]


def test_error_status_raises_calm_error() -> None:
    recorder = HTTPXRecorder([_response(500, {"error": "boom"})])
    with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
        with calmclient.CalmClient("http://calm.test", "api-key") as client:
            with pytest.raises(calmclient.CalmError):
                client.list_managed_sessions()
