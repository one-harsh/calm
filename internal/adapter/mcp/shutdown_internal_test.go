// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func TestShutdown_DeletesSessionByDefault(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().DeleteSession(mock.Anything, "tok-1").Return(nil).Once()
	s := NewServer(Config{Calm: m, Logger: logging.Nop(), SessionTTLMinutes: 60})
	s.mu.Lock()
	s.session = "tok-1"
	s.mu.Unlock()

	s.shutdown()
}

func TestShutdown_KeepSessionSkipsDelete(t *testing.T) {
	m := calm.NewMockClient(t)
	s := NewServer(Config{Calm: m, Logger: logging.Nop(), SessionTTLMinutes: 60, KeepSession: true})
	s.mu.Lock()
	s.session = "tok-1"
	s.mu.Unlock()

	s.shutdown()
}
