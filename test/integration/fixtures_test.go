// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func uniqueSessionToken(t *testing.T) string {
	t.Helper()
	suffix := uuid.NewString()[:8]
	name := strings.ReplaceAll(t.Name(), "/", "-")
	return "it-" + name + "-" + suffix
}
