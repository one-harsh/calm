<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Trace cluster: schema-following problems with JSON arguments

## Case SCH-006

Tool schema excerpt:

```json
{
  "name": "policy_search",
  "additionalProperties": false,
  "required": ["topic"],
  "properties": {
    "topic": {"type": "string"},
    "region": {"type": "string"}
  }
}
```

Actual arguments:

```json
{"topic":"return window","region":"CA","customer_tier":"gold"}
```

Judge rationale: schema-following failure. The JSON includes
`customer_tier`, but the schema sets `additionalProperties: false`. This is not
an unknown tool; it is an invalid argument object.

## Case SCH-011

Tool schema excerpt:

```json
{"name":"inventory_lookup","required":["sku"],"additionalProperties":false}
```

Actual arguments:

```json
{"sku":"A19-BLACK-8","includeAlternates":true}
```

Judge rationale: schema-following failure. The model supplied an extra
`includeAlternates` property and treated the inventory tool like a catalog
search. This overlaps with the tool choice regression but is counted separately
because the selected tool was valid and the JSON object was not.

## Pattern

The v17 prompt asks the model to "include helpful context in tool arguments".
That phrase conflicts with strict JSON schemas that reject extra fields under
`additionalProperties: false`.
