// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package auth resolves API keys to namespaces. See DL10.
//
// The registry is built from the operator's config.yaml at startup via
// BuildRegistry, which resolves each namespace's bracketed api_key
// reference through internal/secrets. A missing config file, malformed
// secret reference, or duplicate post-resolution key value causes the
// service to refuse to start. There is no runtime local-mode bypass;
// namespace enforcement is a hard invariant.
package auth
