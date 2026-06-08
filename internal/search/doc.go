// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package search implements ranked retrieval.
//
// Two-layer fallback: porter-stemmed BM25 → trigram substring. Returns
// exact indexed text with smart snippet extraction; never summaries or
// paraphrases.
package search
