// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package auth resolves API keys to namespaces per HLD §6 / §13.
//
// The registry is loaded from configuration (file or env) at startup. Local
// mode is signaled by an empty registry: namespace enforcement is skipped.
package auth
