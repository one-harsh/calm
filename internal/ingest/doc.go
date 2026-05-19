// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package ingest implements content ingestion.
//
// Responsibilities: chunking by auto-detected (JSON, Markdown, plain text)
// or hint-driven format (log, stacktrace, csv, metrics); optional intent
// ordering via RRF when intents are provided and content exceeds a size
// threshold; producing the compact representation returned to the workload.
package ingest
