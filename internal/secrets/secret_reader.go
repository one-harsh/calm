// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	logging "github.com/one-harsh/context-logging"
)

type SecretReaderType string

const (
	FileSecretReader SecretReaderType = "file"
	TextSecretReader SecretReaderType = "text"
	EnvSecretReader  SecretReaderType = "env"
)

const secretReaderPattern = `^\[(file|text|env):([^\]]+)\]$` //nolint:gosec // regex pattern, not a credential

var secretReaderRegex = regexp.MustCompile(secretReaderPattern)

// Secret represents a bracketed secret reference of the form `[scheme:payload]`
// where scheme is one of `text`, `env`, or `file`. Pass it to a SecretReader
// to obtain the concrete value.
//
// Supported secret patterns:
//  1. `[file:<absolute_path>]`  — value is the file's contents, whitespace-trimmed
//  2. `[text:<literal>]`        — value is the literal payload
//  3. `[env:<VAR_NAME>]`        — value is os.LookupEnv(VAR_NAME); fails when unset/empty
type Secret string

func (s Secret) String() string {
	return string(s)
}

// SecretReader resolves Secret references at startup. By design the contract
// is fail-fast: any resolution failure (malformed reference, unknown scheme,
// unset/empty env var, missing/unreadable/empty file) logs Fatal and exits
// the process. This keeps the contract simple -> read gives secret directly.
type SecretReader interface {
	ReadSecret(ctx context.Context, secret Secret) string
}

type secretReader struct {
	logger *logging.Logger
}

func New(logger *logging.Logger) SecretReader {
	return &secretReader{
		logger: logger.WithCallerSkip(1),
	}
}

func (s *secretReader) ReadSecret(ctx context.Context, secret Secret) string {
	val, err := resolve(string(secret))
	if err != nil {
		s.logger.WithContext(ctx).Fatal("resolve secret", logging.ErrorField(err))
	}
	return val
}

func resolve(ref string) (string, error) {
	match := secretReaderRegex.FindStringSubmatch(ref)
	if match == nil {
		return "", fmt.Errorf("malformed secret reference %q (expected [text:..], [env:..], or [file:..])", ref)
	}
	scheme, payload := match[1], match[2]
	switch scheme {
	case string(TextSecretReader):
		return payload, nil
	case string(EnvSecretReader):
		v, ok := os.LookupEnv(payload)
		if !ok {
			return "", fmt.Errorf("env var %q referenced by secret is not set", payload)
		}
		return v, nil
	case string(FileSecretReader):
		data, err := os.ReadFile(payload) //nolint:gosec // operator-controlled path
		if err != nil {
			return "", fmt.Errorf("read file %q referenced by secret: %w", payload, err)
		}
		trimmed := strings.TrimSpace(string(data))
		if trimmed == "" {
			return "", fmt.Errorf("file %q referenced by secret is empty", payload)
		}
		return trimmed, nil
	default:
		return "", fmt.Errorf("unknown secret scheme %q", scheme)
	}
}
