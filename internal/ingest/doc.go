// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package ingest implements content ingestion.
//
// Responsibilities: chunking by auto-detected (JSON, Markdown, plain text)
// or hint-driven format (log, stacktrace, csv, tsv, metrics) via the chunk
// subpackage; enforcing the per-source chunk cap — the first 500 sections
// index, in document order; producing the compact representation returned
// to the workload.
package ingest
