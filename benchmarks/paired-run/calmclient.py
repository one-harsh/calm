# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""Minimal CALM HTTP client for the paired-run benchmark harness.

Adapted from ``examples/eval-harness/eval_harness.py`` (``CalmClient`` /
``APIResult``). It is COPIED, not imported: the benchmark harness must not
depend on the ``examples/`` package boundary. Only the request core, client
registration, the management read surface (session snapshot), and the health
probe are retained here — ingest/search/feedback are absent because the
workload under test is a headless ``claude -p`` process driving CALM through
the adapter, never this client.
"""

from __future__ import annotations

import json
import sys
import time
import urllib.parse
from dataclasses import dataclass
from typing import Any

import httpx

DEFAULT_BASE_URL = "http://localhost:8080"
DEFAULT_TIMEOUT_SECONDS = 10.0

API_KEY_HEADER = "X-CALM-API-Key"
CORRELATION_ID_HEADER = "X-CALM-Correlation-Id"  # nosec B105 - header name, not a secret.


class CalmError(Exception):
    """Raised when the harness cannot complete a CALM call."""


@dataclass(frozen=True)
class APIResult:
    status: int
    body: Any
    correlation_id: str
    latency_ms: float


class CalmClient:
    """Thin JSON client scoped to one namespace API key.

    The api_key is an already-resolved secret. It is only ever placed in the
    ``X-CALM-API-Key`` request header — never logged, printed, or echoed into a
    trace artifact. Callers must resolve any ``[file:...]``/``[env:...]``
    reference before construction.
    """

    def __init__(self, base_url: str, api_key: str, timeout: float = DEFAULT_TIMEOUT_SECONDS):
        self.base_url = base_url.rstrip("/")
        self._api_key = api_key
        self.http = httpx.Client(
            base_url=self.base_url,
            timeout=httpx.Timeout(timeout),
            headers={"Accept": "application/json"},
        )

    def close(self) -> None:
        self.http.close()

    def __enter__(self) -> "CalmClient":
        return self

    def __exit__(self, exc_type: object, exc: object, tb: object) -> None:
        self.close()

    def health(self) -> bool:
        result = self.request("GET", "/v1/health", allowed_statuses={503})
        return result.status == 200

    def register_client(self, name: str) -> None:
        path = "/v1/clients/" + urllib.parse.quote(name, safe="")
        self.request("POST", path, allowed_statuses={409})

    def list_managed_sessions(
        self,
        client: str | None = None,
        labels: dict[str, str] | None = None,
    ) -> list[dict[str, Any]]:
        params: list[tuple[str, str]] = []
        if client:
            params.append(("client", client))
        for key, value in (labels or {}).items():
            params.append((f"labels[{key}]", value))
        query = ("?" + urllib.parse.urlencode(params)) if params else ""
        result = self.request("GET", "/v1/manage/sessions" + query)
        if not isinstance(result.body, dict):
            raise CalmError("GET /v1/manage/sessions returned a non-object body")
        sessions = result.body.get("sessions", [])
        if not isinstance(sessions, list):
            raise CalmError("GET /v1/manage/sessions: sessions is not a list")
        return sessions

    def request(
        self,
        method: str,
        path: str,
        *,
        body: dict[str, Any] | None = None,
        allowed_statuses: set[int] | None = None,
    ) -> APIResult:
        allowed_statuses = allowed_statuses or set()
        headers = {API_KEY_HEADER: self._api_key}

        started = time.perf_counter()
        try:
            response = self.http.request(method, path, json=body, headers=headers)
        except httpx.HTTPError as err:
            raise CalmError(f"{method} {path} failed: {err}") from err

        latency_ms = round((time.perf_counter() - started) * 1000, 2)
        correlation_id = response.headers.get(CORRELATION_ID_HEADER, "")
        print(
            f"{method} {path} -> {response.status_code} "
            f"correlation_id={correlation_id or '-'} duration_ms={latency_ms:.2f}",
            file=sys.stderr,
        )

        parsed = parse_body(response)
        if not response.is_success and response.status_code not in allowed_statuses:
            raise CalmError(f"{method} {path} returned HTTP {response.status_code}: {parsed}")
        return APIResult(response.status_code, parsed, correlation_id, latency_ms)


def parse_body(response: httpx.Response) -> Any:
    if response.status_code == 204 or not response.content:
        return {}
    try:
        return response.json()
    except json.JSONDecodeError as err:
        raise CalmError(f"{response.request.method} {response.request.url} returned invalid JSON") from err
