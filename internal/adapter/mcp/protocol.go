// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import "encoding/json"

const jsonRPCVersion = "2.0"

const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// A request with no id member is a notification (gets no response); id:null is a
// request, answered with a null id.
func (r *request) isNotification() bool { return len(r.ID) == 0 }

// A response always carries id: the request's id, or null when it couldn't be read
// (parse/invalid-request). A nil ID marshals to null.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
