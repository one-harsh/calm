# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

"""CALM eval-triage showcase.

The useful thing to study is the run loop:
register client -> create session -> ingest eval artifacts -> search golden
queries -> classify outcomes -> post feedback -> report exact byte counts.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.parse
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any

import httpx

from fixtures import FixtureDoc, GoldenQuery, UsageError, load_fixtures, load_golden_queries


DEFAULT_BASE_URL = "http://localhost:8080"
DEFAULT_CLIENT = "eval-harness"
DEFAULT_TIMEOUT_SECONDS = 10.0
DEFAULT_LIMIT = 2

API_KEY_HEADER = "X-CALM-API-Key"
SESSION_TOKEN_HEADER = "X-CALM-Session-Token"  # nosec B105 - HTTP header name, not a secret.
CORRELATION_ID_HEADER = "X-CALM-Correlation-Id"  # nosec B105 - HTTP header name, not a secret.
WORKLOAD_REQUEST_ID_HEADER = "X-Workload-Request-Id"
IDEMPOTENCY_KEY_HEADER = "Idempotency-Key"

HERE = Path(__file__).resolve().parent
DEFAULT_FIXTURES = HERE / "fixtures"
DEFAULT_GOLDEN_QUERIES = HERE / "golden_queries.json"

INGEST_INTENTS = (
    "tool selection regression",
    "schema following failures",
    "timeouts retries hallucinated tool output",
)


class CalmError(Exception):
    """Raised when the example cannot complete a CALM call."""


@dataclass(frozen=True)
class APIResult:
    status: int
    body: dict[str, Any]
    correlation_id: str
    latency_ms: float


@dataclass(frozen=True)
class IngestRecord:
    source: str
    raw_bytes: int
    compact_bytes: int
    latency_ms: float
    sections_indexed: int
    sections_total: int
    summary_truncated: bool
    correlation_id: str
    distinctive_terms: tuple[str, ...]


@dataclass(frozen=True)
class SearchRecord:
    query: str
    search_queries: tuple[str, ...]
    compact_bytes: int
    latency_ms: float
    hit_count: int
    match_layers: tuple[str, ...]
    hits: tuple[dict[str, Any], ...]
    correlation_id: str
    outcome: str = ""
    feedback_status: str = ""


@dataclass(frozen=True)
class RunReport:
    mode: str
    base_url: str
    client: str
    sources_ingested: int
    queries_run: int
    raw_fixture_bytes: int
    ingest_compact_bytes: int
    search_compact_bytes: int
    compact_context_bytes: int
    raw_minus_compact_bytes: int
    compression_ratio: float | None
    ingest_latency_ms: float
    search_latency_ms: float
    match_layer_counts: dict[str, int] = field(default_factory=dict)
    feedback_counts: dict[str, int] = field(default_factory=dict)
    ingest_records: tuple[IngestRecord, ...] = ()
    search_records: tuple[SearchRecord, ...] = ()
    cleanup_error: str = ""

    def as_json(self) -> dict[str, Any]:
        return asdict(self)


class CalmClient:
    def __init__(self, base_url: str, api_key: str, timeout: float):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
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

    def register_client(self, name: str) -> None:
        path = "/v1/clients/" + urllib.parse.quote(name, safe="")
        self.request("POST", path, allowed_statuses={409})

    def create_session(self, client_name: str, idempotency_key: str) -> str:
        result = self.request(
            "POST",
            "/v1/sessions",
            body={
                "client": client_name,
                "labels": {"example": "eval-harness", "workload": "llm-eval-triage"},
            },
            idempotency_key=idempotency_key,
        )
        return result.body["session_token"]

    def delete_session(self, session_token: str) -> None:
        self.request("DELETE", "/v1/sessions", session_token=session_token)

    def ingest(self, session_token: str, fixture: FixtureDoc, workload_request_id: str) -> APIResult:
        return self.request(
            "POST",
            "/v1/ingest",
            body={
                "content": fixture.content,
                "source": fixture.source,
                "format": fixture.format_hint,
                "content_type": fixture.content_type,
                "intents": list(INGEST_INTENTS),
            },
            session_token=session_token,
            workload_request_id=workload_request_id,
        )

    def search(self, session_token: str, queries: tuple[str, ...], workload_request_id: str) -> APIResult:
        return self.request(
            "POST",
            "/v1/search",
            body={"queries": list(queries), "limit": DEFAULT_LIMIT},
            session_token=session_token,
            workload_request_id=workload_request_id,
        )

    def feedback(self, session_token: str, correlation_id: str, outcome: str, workload_request_id: str) -> APIResult:
        return self.request(
            "POST",
            "/v1/feedback",
            body={"correlation_id": correlation_id, "outcome": outcome},
            session_token=session_token,
            workload_request_id=workload_request_id,
            allowed_statuses={409},
        )

    def request(
        self,
        method: str,
        path: str,
        *,
        body: dict[str, Any] | None = None,
        session_token: str = "",
        workload_request_id: str = "",
        idempotency_key: str = "",
        allowed_statuses: set[int] | None = None,
    ) -> APIResult:
        allowed_statuses = allowed_statuses or set()
        headers = {API_KEY_HEADER: self.api_key}
        if session_token:
            headers[SESSION_TOKEN_HEADER] = session_token
        if workload_request_id:
            headers[WORKLOAD_REQUEST_ID_HEADER] = workload_request_id
        if idempotency_key:
            headers[IDEMPOTENCY_KEY_HEADER] = idempotency_key

        started = time.perf_counter()
        try:
            # Production integrations should retry transient 408/429/5xx
            # responses and transport errors with exponential backoff + jitter.
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
        if parsed is None:
            parsed = {}
        if not isinstance(parsed, dict):
            raise CalmError(f"{method} {path} returned {type(parsed).__name__}, expected JSON object")
        return APIResult(response.status_code, parsed, correlation_id, latency_ms)


def parse_body(response: httpx.Response) -> Any:
    if response.status_code == 204 or not response.content:
        return {}
    try:
        return response.json()
    except json.JSONDecodeError as err:
        raise CalmError(f"{response.request.method} {response.request.url} returned invalid JSON") from err


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        report = run(args)
    except UsageError as err:
        parser.error(str(err))
    except Exception as err:  # noqa: BLE001 - CLI should print a clean one-line failure.
        print(f"eval harness failed: {err}", file=sys.stderr)
        return 1

    if args.json:
        print(json.dumps(report.as_json(), indent=2, sort_keys=True))
    else:
        print(render_markdown(report))

    if args.command == "verify" and report.feedback_counts.get("retry", 0) > 0:
        return 2
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Run the CALM eval-harness showcase.")
    parser.add_argument("command", choices=("demo", "bench", "verify"))
    parser.add_argument(
        "--base-url",
        default=os.environ.get("CALM_EVAL_BASE_URL", DEFAULT_BASE_URL),
        help=f"CALM base URL (default: CALM_EVAL_BASE_URL or {DEFAULT_BASE_URL})",
    )
    parser.add_argument(
        "--api-key",
        default=os.environ.get("CALM_EVAL_API_KEY") or os.environ.get("CALM_DEFAULT_KEY"),
        help="Namespace API key (default: CALM_EVAL_API_KEY or CALM_DEFAULT_KEY)",
    )
    parser.add_argument(
        "--client",
        default=DEFAULT_CLIENT,
        help=f"CALM client name to register/use (default: {DEFAULT_CLIENT})",
    )
    parser.add_argument(
        "--fixtures",
        type=Path,
        default=DEFAULT_FIXTURES,
        help=f"Fixture directory (default: {DEFAULT_FIXTURES})",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"HTTP timeout in seconds (default: {DEFAULT_TIMEOUT_SECONDS:g})",
    )
    parser.add_argument("--json", action="store_true", help="Print JSON instead of markdown.")
    return parser


def run(args: argparse.Namespace) -> RunReport:
    if not args.api_key:
        raise UsageError("set --api-key, CALM_EVAL_API_KEY, or CALM_DEFAULT_KEY")

    fixtures = load_fixtures(args.fixtures)
    golden_queries = load_golden_queries(DEFAULT_GOLDEN_QUERIES)
    selected_queries = golden_queries if args.command in {"bench", "verify"} else golden_queries[:3]
    run_id = str(int(time.time() * 1000))
    idempotency_key = f"eval-harness-{run_id}"

    with CalmClient(args.base_url, args.api_key, args.timeout) as client:
        client.register_client(args.client)
        session_token = client.create_session(args.client, idempotency_key)
        return run_session(
            mode=args.command,
            base_url=args.base_url,
            client_name=args.client,
            calm=client,
            session_token=session_token,
            fixtures=fixtures,
            queries=selected_queries,
            run_id=run_id,
        )


def run_session(
    *,
    mode: str,
    base_url: str,
    client_name: str,
    calm: CalmClient,
    session_token: str,
    fixtures: list[FixtureDoc],
    queries: list[GoldenQuery],
    run_id: str,
) -> RunReport:
    ingest_records: list[IngestRecord] = []
    search_records: list[SearchRecord] = []
    feedback_counts = {"success": 0, "degraded": 0, "retry": 0, "feedback_error": 0}
    cleanup_error = ""

    try:
        for index, fixture in enumerate(fixtures, start=1):
            result = calm.ingest(
                session_token,
                fixture,
                workload_request_id=f"eval-harness/{run_id}/ingest/{index}/{fixture.source}",
            )
            ingest_records.append(ingest_record_from_result(fixture, result))

        for index, golden in enumerate(queries, start=1):
            result = calm.search(
                session_token,
                golden.search_queries,
                workload_request_id=f"eval-harness/{run_id}/search/{index}",
            )
            hits = extract_hits(result.body)
            outcome = ""
            feedback_status = ""
            if mode == "verify":
                outcome = classify_outcome(golden, hits)
                feedback_counts[outcome] += 1
                try:
                    feedback = calm.feedback(
                        session_token,
                        result.correlation_id,
                        outcome,
                        workload_request_id=f"eval-harness/{run_id}/feedback/{index}",
                    )
                    feedback_status = "already_submitted" if feedback.status == 409 else "submitted"
                except Exception as err:  # noqa: BLE001 - feedback is reported, not hidden.
                    feedback_counts["feedback_error"] += 1
                    feedback_status = f"error: {err}"
            search_records.append(search_record_from_result(golden, hits, result, outcome, feedback_status))
    finally:
        try:
            calm.delete_session(session_token)
        except Exception as err:  # noqa: BLE001 - cleanup failure belongs in the report.
            cleanup_error = str(err)

    return build_report(
        mode=mode,
        base_url=base_url,
        client_name=client_name,
        ingest_records=tuple(ingest_records),
        search_records=tuple(search_records),
        feedback_counts=feedback_counts if mode == "verify" else {},
        cleanup_error=cleanup_error,
    )


def ingest_record_from_result(fixture: FixtureDoc, result: APIResult) -> IngestRecord:
    compact = render_ingest_compact(fixture.source, result.body)
    return IngestRecord(
        source=fixture.source,
        raw_bytes=fixture.raw_bytes,
        compact_bytes=utf8_len(compact),
        latency_ms=result.latency_ms,
        sections_indexed=int(result.body.get("sections_indexed", 0)),
        sections_total=int(result.body.get("sections_total", 0)),
        summary_truncated=bool(result.body.get("summary_truncated", False)),
        correlation_id=result.correlation_id,
        distinctive_terms=tuple(str(term) for term in result.body.get("distinctive_terms", [])),
    )


def search_record_from_result(
    golden: GoldenQuery,
    hits: tuple[dict[str, Any], ...],
    result: APIResult,
    outcome: str = "",
    feedback_status: str = "",
) -> SearchRecord:
    compact = render_search_compact(golden.query, golden.search_queries, hits)
    layers = sorted({str(hit.get("match_layer", "")) for hit in hits if hit.get("match_layer")})
    return SearchRecord(
        query=golden.query,
        search_queries=golden.search_queries,
        compact_bytes=utf8_len(compact),
        latency_ms=result.latency_ms,
        hit_count=len(hits),
        match_layers=tuple(layers),
        hits=hits,
        correlation_id=result.correlation_id,
        outcome=outcome,
        feedback_status=feedback_status,
    )


def build_report(
    *,
    mode: str,
    base_url: str,
    client_name: str,
    ingest_records: tuple[IngestRecord, ...],
    search_records: tuple[SearchRecord, ...],
    feedback_counts: dict[str, int],
    cleanup_error: str,
) -> RunReport:
    raw_fixture_bytes = sum(record.raw_bytes for record in ingest_records)
    ingest_compact_bytes = sum(record.compact_bytes for record in ingest_records)
    search_compact_bytes = sum(record.compact_bytes for record in search_records)
    compact_context_bytes = search_compact_bytes
    match_layer_counts: dict[str, int] = {}
    for record in search_records:
        for hit in record.hits:
            layer = str(hit.get("match_layer", ""))
            if layer:
                match_layer_counts[layer] = match_layer_counts.get(layer, 0) + 1
    return RunReport(
        mode=mode,
        base_url=base_url,
        client=client_name,
        sources_ingested=len(ingest_records),
        queries_run=len(search_records),
        raw_fixture_bytes=raw_fixture_bytes,
        ingest_compact_bytes=ingest_compact_bytes,
        search_compact_bytes=search_compact_bytes,
        compact_context_bytes=compact_context_bytes,
        raw_minus_compact_bytes=raw_fixture_bytes - compact_context_bytes,
        compression_ratio=round(raw_fixture_bytes / compact_context_bytes, 2) if compact_context_bytes else None,
        ingest_latency_ms=round(sum(record.latency_ms for record in ingest_records), 2),
        search_latency_ms=round(sum(record.latency_ms for record in search_records), 2),
        match_layer_counts=match_layer_counts,
        feedback_counts=feedback_counts,
        ingest_records=ingest_records,
        search_records=search_records,
        cleanup_error=cleanup_error,
    )


def extract_hits(search_body: dict[str, Any]) -> tuple[dict[str, Any], ...]:
    hits: list[dict[str, Any]] = []
    for result in search_body["results"]:
        hits.extend(result["hits"])
    return tuple(hits)


def classify_outcome(golden: GoldenQuery, hits: tuple[dict[str, Any], ...]) -> str:
    if not hits:
        return "retry" if golden.required else "degraded"
    source_ok = not golden.expected_sources or any(str(hit.get("source", "")) in golden.expected_sources for hit in hits)
    haystack = "\n".join(" ".join(str(hit.get(field, "")) for field in ("source", "title", "snippet")) for hit in hits)
    evidence_ok = not golden.evidence_terms or any(term.lower() in haystack.lower() for term in golden.evidence_terms)
    return "success" if source_ok and evidence_ok else "degraded"


def render_ingest_compact(source: str, body: dict[str, Any]) -> str:
    lines = [f"source: {source}"]
    terms = body.get("distinctive_terms", [])
    if terms:
        lines.append("distinctive_terms: " + ", ".join(str(term) for term in terms))
    for section in body.get("summary", []):
        lines.append(f"title: {section.get('title', '')}")
        if section.get("preview"):
            lines.append(f"preview: {section['preview']}")
        if section.get("matches"):
            lines.append("matches: " + ", ".join(str(match) for match in section["matches"]))
    return "\n".join(lines)


def render_search_compact(query: str, search_queries: tuple[str, ...], hits: tuple[dict[str, Any], ...]) -> str:
    lines = [f"query: {query}"]
    if search_queries != (query,):
        lines.append("search_queries: " + ", ".join(search_queries))
    for hit in hits:
        lines.extend(
            [
                f"source: {hit.get('source', '')}",
                f"title: {hit.get('title', '')}",
                f"match_layer: {hit.get('match_layer', '')}",
                f"snippet: {hit.get('snippet', '')}",
            ],
        )
    return "\n".join(lines)


def render_markdown(report: RunReport) -> str:
    lines = [
        f"# CALM eval-harness {report.mode} report",
        "",
        "This report uses exact UTF-8 byte counts. Model-token impact is tokenizer/model-specific and is not estimated.",
        "",
        "## Run",
        "",
        "| Metric | Value |",
        "|---|---:|",
        f"| CALM base URL | `{report.base_url}` |",
        f"| client | `{report.client}` |",
        f"| sources ingested | {report.sources_ingested} |",
        f"| queries run | {report.queries_run} |",
        f"| raw fixture bytes | {report.raw_fixture_bytes} |",
        f"| ingest compact bytes | {report.ingest_compact_bytes} |",
        f"| search compact bytes | {report.search_compact_bytes} |",
        f"| compact context bytes | {report.compact_context_bytes} |",
        f"| raw minus compact bytes | {report.raw_minus_compact_bytes} |",
        f"| raw / compact ratio | {report.compression_ratio if report.compression_ratio is not None else 'n/a'} |",
        f"| ingest latency ms | {report.ingest_latency_ms:.2f} |",
        f"| search latency ms | {report.search_latency_ms:.2f} |",
        "",
        "## Ingest",
        "",
        "| Source | Raw bytes | Compact bytes | Sections | Latency ms |",
        "|---|---:|---:|---:|---:|",
    ]
    for record in report.ingest_records:
        sections = f"{record.sections_indexed}/{record.sections_total}"
        if record.summary_truncated:
            sections += " truncated"
        lines.append(
            f"| `{record.source}` | {record.raw_bytes} | {record.compact_bytes} | "
            f"{sections} | {record.latency_ms:.2f} |",
        )

    lines.extend(["", "## Search", ""])
    for index, record in enumerate(report.search_records, start=1):
        outcome = f" outcome={record.outcome}" if record.outcome else ""
        feedback = f" feedback={record.feedback_status}" if record.feedback_status else ""
        layers = ", ".join(record.match_layers) if record.match_layers else "none"
        lines.extend(
            [
                f"### {index}. {record.query}",
                "",
                f"- search probes: {', '.join(f'`{query}`' for query in record.search_queries)}",
                f"- hits: {record.hit_count}",
                f"- match layers: {layers}",
                f"- compact bytes: {record.compact_bytes}",
                f"- latency ms: {record.latency_ms:.2f}",
                f"- correlation id: `{record.correlation_id}`{outcome}{feedback}",
                "",
            ],
        )
        if not record.hits:
            lines.extend(["No hits returned.", ""])
            continue
        for hit in record.hits:
            snippet = single_line(str(hit.get("snippet", "")), max_len=280)
            lines.append(
                f"- `{hit.get('source', '')}` — {hit.get('title', '')} "
                f"({hit.get('match_layer', '')})",
            )
            if snippet:
                lines.append(f"  `{snippet}`")
        lines.append("")

    if report.match_layer_counts:
        lines.extend(["## Match-layer distribution", "", "| Layer | Hits |", "|---|---:|"])
        for layer in ("primary", "trigram"):
            count = report.match_layer_counts.get(layer, 0)
            if count or layer in report.match_layer_counts:
                lines.append(f"| {layer} | {count} |")
        lines.append("")

    if report.feedback_counts:
        lines.extend(["## Feedback", "", "| Outcome | Count |", "|---|---:|"])
        for key in ("success", "degraded", "retry", "feedback_error"):
            lines.append(f"| {key} | {report.feedback_counts.get(key, 0)} |")
        lines.append("")

    if report.cleanup_error:
        lines.extend(["## Cleanup", "", f"Session cleanup failed: `{report.cleanup_error}`", ""])
    return "\n".join(lines)


def utf8_len(text: str) -> int:
    return len(text.encode("utf-8"))


def single_line(text: str, *, max_len: int) -> str:
    collapsed = " ".join(text.split())
    if len(collapsed) <= max_len:
        return collapsed
    return collapsed[: max_len - 1] + "..."


if __name__ == "__main__":
    raise SystemExit(main())
