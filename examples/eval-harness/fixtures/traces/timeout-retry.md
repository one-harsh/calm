<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Trace cluster: fulfillment timeout and retry budget

## Case FUL-009

User request: "Can this arrive by Friday if I ship to 98109?"

Tool path:

1. `fulfillment_quote({"sku":"A19-BLACK-8","postal_code":"98109","speed":"standard"})`
2. timeout after 1800 ms
3. `fulfillment_quote({"sku":"A19-BLACK-8","postal_code":"98109","speed":"standard"})`
4. timeout after 1800 ms
5. answer: "It should arrive by Friday."

Judge rationale: retry budget exhausted. The model retried the identical
`fulfillment_quote` call twice, then answered without a tool result. The answer
is unsupported because the timeout path produced no delivery estimate.

## Case FUL-022

User request: "If I upgrade shipping on SKU M88-WHITE-11, does it arrive before
June 20?"

Tool path:

1. `fulfillment_quote({"sku":"M88-WHITE-11","postal_code":"10003","speed":"expedited"})`
2. timeout after 1800 ms
3. `fulfillment_quote({"sku":"M88-WHITE-11","postal_code":"10003","speed":"expedited"})`
4. timeout after 1800 ms

Judge rationale: timeout handling degraded. The prompt did not tell the model to
stop after the retry budget or ask for permission to continue. Latency grew but
answer quality did not improve.

## Pattern

Timeout cases increased after v17 because the retry budget language moved below
the tool list and became less visible. The issue is not service availability in
the fixture; it is repeated identical retry behavior after the budget was already
spent.
