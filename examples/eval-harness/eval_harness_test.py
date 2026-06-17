# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from typing import Any
from unittest import mock

import httpx

import eval_harness
from fixtures import GoldenQuery, load_fixtures, load_golden_queries


class EvalHarnessTest(unittest.TestCase):
    def test_fixture_loading_strips_license_and_sets_source(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fixture = root / "traces" / "sample.md"
            fixture.parent.mkdir()
            fixture.write_text(
                "<!--\nCopyright 2026 The CALM Authors\n"
                "SPDX-License-Identifier: Apache-2.0\n-->\n\n"
                "# Real artifact\n\ninventory_lookup trace\n",
                encoding="utf-8",
            )

            docs = load_fixtures(root)

        self.assertEqual(len(docs), 1)
        self.assertEqual(docs[0].source, "traces/sample.md")
        self.assertEqual(docs[0].format_hint, "markdown")
        self.assertNotIn("SPDX-License-Identifier", docs[0].content)
        self.assertIn("inventory_lookup trace", docs[0].content)

    def test_classify_outcome_success_degraded_and_retry(self) -> None:
        golden = GoldenQuery(
            query="where did catalog_search replace inventory_lookup?",
            search_queries=("wrong tool", "exact SKU"),
            expected_sources=("traces/tool-selection-regression.md",),
            evidence_terms=("wrong tool",),
        )
        success_hits = (
            {
                "source": "traces/tool-selection-regression.md",
                "title": "Case INV-042",
                "snippet": "wrong tool: catalog_search before inventory_lookup",
                "match_layer": "primary",
            },
        )
        degraded_hits = (
            {
                "source": "run-summary.md",
                "title": "Aggregate result",
                "snippet": "wrong tool failures increased",
                "match_layer": "primary",
            },
        )

        self.assertEqual(eval_harness.classify_outcome(golden, success_hits), "success")
        self.assertEqual(eval_harness.classify_outcome(golden, degraded_hits), "degraded")
        self.assertEqual(eval_harness.classify_outcome(golden, ()), "retry")

    def test_report_byte_metrics_are_exact_utf8_counts(self) -> None:
        ingest = (
            eval_harness.IngestRecord(
                source="a.md",
                raw_bytes=20,
                compact_bytes=7,
                latency_ms=2.5,
                sections_indexed=1,
                sections_total=1,
                summary_truncated=False,
                correlation_id="ingest-corr",
                distinctive_terms=("inventory",),
            ),
        )
        search = (
            eval_harness.SearchRecord(
                query="show inventory",
                search_queries=("inventory",),
                compact_bytes=3,
                latency_ms=1.25,
                hit_count=1,
                match_layers=("primary",),
                hits=(),
                correlation_id="search-corr",
            ),
        )

        report = eval_harness.build_report(
            mode="bench",
            base_url="http://localhost:8080",
            client_name="eval-harness",
            ingest_records=ingest,
            search_records=search,
            feedback_counts={},
            cleanup_error="",
        )

        self.assertEqual(report.raw_fixture_bytes, 20)
        self.assertEqual(report.compact_context_bytes, 3)
        self.assertEqual(report.raw_minus_compact_bytes, 17)
        self.assertEqual(report.compression_ratio, 6.67)

    def test_search_sends_multi_query_request_with_session_and_workload_headers(self) -> None:
        recorder = HTTPXRecorder(
            responses=[
                httpx_response(
                    200,
                    {"results": [{"query": "inventory lookup", "hits": []}, {"query": "wrong tool", "hits": []}]},
                    {"X-CALM-Correlation-Id": "search-corr"},
                ),
            ],
        )
        with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
            with eval_harness.CalmClient("http://calm.test", "api-key", 3) as client:
                result = client.search("session-token", ("inventory lookup", "wrong tool"), "req-1")

        self.assertEqual(result.correlation_id, "search-corr")
        request = recorder.requests[0]
        self.assertEqual(request["headers"]["X-CALM-API-Key"], "api-key")
        self.assertEqual(request["headers"]["X-CALM-Session-Token"], "session-token")
        self.assertEqual(request["headers"]["X-Workload-Request-Id"], "req-1")
        self.assertEqual(request["json"], {"limit": 2, "queries": ["inventory lookup", "wrong tool"]})

    def test_create_session_sends_idempotency_key(self) -> None:
        recorder = HTTPXRecorder(
            responses=[
                httpx_response(201, {"session_token": "session-token"}, {"X-CALM-Correlation-Id": "session-corr"}),
            ],
        )
        with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
            with eval_harness.CalmClient("http://calm.test", "api-key", 3) as client:
                token = client.create_session("eval-harness", "eval-harness-123")

        self.assertEqual(token, "session-token")
        request = recorder.requests[0]
        self.assertEqual(request["headers"]["Idempotency-Key"], "eval-harness-123")
        self.assertEqual(request["json"]["client"], "eval-harness")

    def test_feedback_payload_accepts_already_submitted_409(self) -> None:
        recorder = HTTPXRecorder(
            responses=[
                httpx_response(409, {"error": "feedback already submitted"}, {"X-CALM-Correlation-Id": "feedback-corr"}),
            ],
        )
        with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
            with eval_harness.CalmClient("http://calm.test", "api-key", 3) as client:
                result = client.feedback("session-token", "search-corr", "success", "req-feedback")

        self.assertEqual(result.status, 409)
        request = recorder.requests[0]
        self.assertEqual(request["json"], {"correlation_id": "search-corr", "outcome": "success"})
        self.assertEqual(request["headers"]["X-CALM-Session-Token"], "session-token")

    def test_malformed_success_response_raises_calm_error(self) -> None:
        recorder = HTTPXRecorder(responses=[httpx_response(200, ["not", "an", "object"], {})])
        with mock.patch.object(httpx.Client, "request", recorder), contextlib.redirect_stderr(io.StringIO()):
            with eval_harness.CalmClient("http://calm.test", "api-key", 3) as client:
                with self.assertRaises(eval_harness.CalmError):
                    client.search("session-token", ("inventory lookup",), "req-1")

    def test_load_golden_queries_keeps_prompt_and_search_probes_separate(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "golden_queries.json"
            path.write_text(
                json.dumps(
                    [
                        {
                            "query": "where did the prompt start picking the wrong inventory tool?",
                            "search_queries": ["wrong tool", "exact SKU"],
                            "expected_sources": ["traces/tool-selection-regression.md"],
                            "evidence_terms": ["catalog_search"],
                            "required": True,
                        },
                    ],
                ),
                encoding="utf-8",
            )

            queries = load_golden_queries(path)

        self.assertEqual(queries[0].query, "where did the prompt start picking the wrong inventory tool?")
        self.assertEqual(queries[0].search_queries, ("wrong tool", "exact SKU"))
        self.assertEqual(queries[0].expected_sources, ("traces/tool-selection-regression.md",))

    def test_markdown_and_json_report_shapes(self) -> None:
        report = eval_harness.RunReport(
            mode="verify",
            base_url="http://localhost:8080",
            client="eval-harness",
            sources_ingested=1,
            queries_run=1,
            raw_fixture_bytes=100,
            ingest_compact_bytes=20,
            search_compact_bytes=10,
            compact_context_bytes=30,
            raw_minus_compact_bytes=70,
            compression_ratio=3.33,
            ingest_latency_ms=4.0,
            search_latency_ms=2.0,
            feedback_counts={"success": 1, "degraded": 0, "retry": 0, "feedback_error": 0},
            ingest_records=(),
            search_records=(),
        )

        markdown = eval_harness.render_markdown(report)
        rendered_json = report.as_json()

        self.assertIn("exact UTF-8 byte counts", markdown)
        self.assertIn("Model-token impact", markdown)
        self.assertEqual(rendered_json["raw_minus_compact_bytes"], 70)
        self.assertEqual(rendered_json["feedback_counts"]["success"], 1)


class HTTPXRecorder:
    def __init__(self, responses: list[httpx.Response]):
        self.responses = responses
        self.requests: list[dict[str, Any]] = []

    def __call__(self, method: str, url: str, **kwargs: Any) -> httpx.Response:
        self.requests.append(
            {
                "method": method,
                "url": url,
                "headers": dict(kwargs.get("headers", {})),
                "json": kwargs.get("json"),
            },
        )
        response = self.responses.pop(0)
        request = httpx.Request(method, "http://calm.test" + url)
        return httpx.Response(
            status_code=response.status_code,
            headers=response.headers,
            content=response.content,
            request=request,
        )


def httpx_response(status: int, body: Any, headers: dict[str, str]) -> httpx.Response:
    content = b"" if status == 204 else json.dumps(body).encode("utf-8")
    return httpx.Response(status_code=status, headers=headers, content=content)


if __name__ == "__main__":
    unittest.main()
