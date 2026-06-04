// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
)

// ToolHandler runs a tool/call. A returned error is surfaced as an isError result.
type ToolHandler func(ctx context.Context, args json.RawMessage) (ToolResult, error)

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     ToolHandler
}

type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func TextResult(text string, isError bool) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: text}}, IsError: isError}
}
