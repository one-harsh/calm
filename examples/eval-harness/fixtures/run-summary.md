<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Eval run summary: checkout assistant staging

## Run metadata

- run id: `checkout-eval-2026-06-14T2315Z`
- prompt candidate: `checkout-assistant-v17`
- baseline prompt: `checkout-assistant-v16`
- model family: internal chat model under test
- fixture set: 184 synthetic shopper conversations
- judge: deterministic rubric plus human spot-check notes

## Aggregate result

The pass rate moved from 91.3 percent on v16 to 82.1 percent on v17. The largest
drop is concentrated in tool selection and JSON argument formation. Latency also
rose in the fulfillment path because the retry budget was consumed by repeated
`fulfillment_quote` calls after avoidable timeouts.

| Cluster | v16 failures | v17 failures | Notes |
|---|---:|---:|---|
| wrong tool selected | 3 | 19 | mostly `catalog_search` before `inventory_lookup` |
| malformed arguments | 4 | 17 | `update_cart` payload omits `quantity_delta` |
| timeout or retry behavior | 5 | 14 | fulfillment path retries without changed inputs |
| hallucinated tool output | 2 | 9 | answer cites delivery ETA not returned by tool |
| schema-following failure | 4 | 13 | JSON includes additional properties |
| overbroad retrieval | 3 | 11 | policy snippets mixed into product questions |

## Triage priority

The first fix should be the tool choice instruction around exact SKUs. The
second fix should restore explicit `quantity_delta` wording in the cart tool
contract. The retry and hallucinated output clusters are downstream effects in
many traces, but not all of them.

## Noise and distractors

Several passing cases include long policy snippets about restricted items,
return windows, and regional shipping rules. They are intentionally left in the
fixture corpus because real eval artifacts include unrelated retrieved context,
not just clean failure labels.
