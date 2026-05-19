// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package search implements ranked retrieval.
//
// Three-layer fallback: porter-stemmed BM25 → trigram substring → fuzzy
// (Levenshtein over the indexed vocabulary). Returns exact indexed text
// with smart snippet extraction; never summaries or paraphrases.
package search
