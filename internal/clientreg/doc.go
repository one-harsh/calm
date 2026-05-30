// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package clientreg owns the Client domain-entity lifecycle: registration,
// credential issuance and rotation, token-based lookup, listing, session
// counting, and deletion. The package name reflects the package's purpose
// (client registration / credential registry) rather than the entity name,
// to avoid the collision with the generic "client = network code" reading.
package clientreg
