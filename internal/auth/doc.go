// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package auth resolves API keys to namespaces per HLD §6 / §13.
//
// The registry is loaded from a keys file at startup (LoadRegistry). Missing
// or malformed file causes the service to refuse to start. There is no
// runtime local-mode bypass; namespace enforcement is a hard invariant.
package auth
