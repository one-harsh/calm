<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# CALM eval harness example

This example shows CALM as infrastructure for an LLM evaluation workflow rather
than a coding agent. A Python harness ingests realistic synthetic eval artifacts,
runs triage-style search queries, reports exact byte reduction, and posts
feedback for each verified retrieval outcome.

The implementation talks to CALM through the public HTTP API. It does not import
Go packages or generated internal clients.

## Prerequisites

- Python 3.11+
- A running CALM server. Use the root README's local-dev Quickstart for the
  canonical setup.
- Python dependencies for the harness:

```bash
task example:eval:deps
```

## Run

From the repo root:

```bash
python3 examples/eval-harness/eval_harness.py demo
python3 examples/eval-harness/eval_harness.py bench
python3 examples/eval-harness/eval_harness.py verify
```

Equivalent task targets are available:

```bash
task example:eval:demo
task example:eval:bench
task example:eval:verify
task example:eval:deps
task example:eval:check
task example:eval:test
```

The harness reads `CALM_EVAL_API_KEY`, then `CALM_DEFAULT_KEY`, for the namespace
API key. `CALM_EVAL_BASE_URL` overrides the default `http://localhost:8080`.

The HTTP client uses `httpx.Client` for connection reuse and request timeouts.
Each CALM call prints a short stderr line with method, path, status, duration,
and CALM's `X-CALM-Correlation-Id` when present.

## Production workload notes

This example keeps the CALM wiring visible rather than wrapping it in production
infrastructure. Production workloads should keep connection reuse, session
creation with `Idempotency-Key`, correlation-id-aware logs, and feedback
submission, then add their own retry/backoff policy for retryable statuses
(`408`, `429`, `5xx`) and transport errors. Credentialed namespaces should also
send `Authorization: Bearer <client-token>` on session-touching requests.

Use `--json` for a machine-readable report:

```bash
python3 examples/eval-harness/eval_harness.py bench --json
```

## Workload shape

The fixture corpus models an eval regression investigation for a checkout
assistant. It includes:

- a prompt diff from `v16` to `v17`
- an aggregate eval run summary
- per-case traces with tool calls, JSON arguments, model answers, timeout/retry
  behavior, and judge rationales
- failure clusters for wrong tool selection, malformed arguments, schema
  following, hallucinated tool output, timeout/retry behavior, and overbroad
  retrieval

The data is synthetic and contains no real customer, provider, or production
model output. It is still written to resemble real eval artifacts: noisy,
specific, cross-referenced, and not limited to clean assertion strings.

Repository SPDX headers are stripped from fixture files before ingestion so the
indexed content represents the synthetic workload artifact, not repository
metadata.

## Golden queries

`golden_queries.json` stores LLM-style retrieval prompts plus the short search
probes sent to CALM. The prompt is what the operator would ask during triage;
`search_queries` is the concrete `/v1/search` query array used for retrieval.
That mirrors how an adapter can decompose one investigation question into a few
lexical probes without hiding the human-readable intent:

```json
{
  "query": "Where did prompt v17 route exact SKU availability through the wrong inventory tool path?",
  "search_queries": ["wrong tool", "exact SKU", "prompt v17"],
  "expected_sources": ["traces/tool-selection-regression.md"],
  "evidence_terms": ["catalog_search", "inventory_lookup", "wrong tool"],
  "required": true
}
```

`verify` classifies each search result:

- `success`: expected source and evidence are present
- `degraded`: CALM returned related hits, but the expected source or evidence was
  missing
- `retry`: a required query returned no useful hits

The harness posts that outcome to `/v1/feedback` using the search response's
`X-CALM-Correlation-Id`.

## Byte reports

Reports use exact UTF-8 byte counts:

- raw fixture bytes ingested
- ingest compact bytes assembled from summaries and distinctive terms
- search compact bytes assembled from returned exact snippets
- compact context bytes used for the benchmark comparison
- raw minus compact bytes
- raw/compact ratio

For this eval workflow, compact context means the search snippets the harness
would hand to the model for triage. Ingest summaries are still measured and
reported, but they are not added to the benchmark comparison.

The harness intentionally does not estimate model tokens. Token impact depends
on the tokenizer and model family, so the example keeps the benchmark grounded in
bytes.
