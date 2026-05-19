// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/secrets"
)

const (
	maxNamespaceLength = 64
	minRatePerSecond   = 1
	maxRatePerSecond   = 100000

	bomBytes = "\ufeff"
)

var namespaceRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// LoadRegistry reads the keys file at path and returns a Registry.
// Fatal-on-failure by design — registry loading is bootstrap-time, no
// useful degraded mode. loadRegistry is the testable core.
//
// File format (one row per line, namespace-first):
//
//	<namespace>:<secret_ref>[:<rate_per_sec>]
//
// `<secret_ref>` is a bracketed reference parsed by `internal/secrets`:
// `[text:...]`, `[env:VAR]`, `[file:/path]`.
func LoadRegistry(ctx context.Context, path string, logger *logging.Logger) Registry {
	reg, err := loadRegistry(ctx, path, logger)
	if err != nil {
		logger.WithContext(ctx).Fatal("load registry", logging.ErrorField(err))
	}
	return reg
}

func loadRegistry(ctx context.Context, path string, logger *logging.Logger) (Registry, error) {
	if path == "" {
		return nil, errors.New("CALM_API_KEYS_FILE is required")
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("keys file not found: %s", path)
		}
		return nil, fmt.Errorf("stat keys file %s: %w", path, err)
	}
	if isPermissiveMode(info.Mode()) {
		logger.Background().Warn(
			"keys file has permissive mode; consider chmod 600",
			logging.StringField("path", path),
			logging.StringField("mode", info.Mode().Perm().String()),
		)
	}

	f, err := os.Open(path) //nolint:gosec // operator-controlled path
	if err != nil {
		return nil, fmt.Errorf("open keys file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	reader := secrets.New(logger)
	keys, rates, err := parseKeysFile(ctx, f, reader)
	if err != nil {
		return nil, fmt.Errorf("parse keys file %s: %w", path, err)
	}

	return NewMemoryRegistry(keys, rates), nil
}

func isPermissiveMode(mode os.FileMode) bool {
	return mode.Perm()&0o077 != 0
}

func parseKeysFile(ctx context.Context, r io.Reader, reader secrets.SecretReader) (map[string]string, map[string]int, error) {
	keys := map[string]string{}
	rates := map[string]int{}
	keyOriginLine := map[string]int{}
	rateOriginLine := map[string]int{}

	scanner := bufio.NewScanner(r)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		if lineNum == 1 {
			raw = strings.TrimPrefix(raw, bomBytes)
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		namespace, secretRef, rateStr, err := splitRow(line)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", lineNum, err)
		}

		if namespace == "" {
			return nil, nil, fmt.Errorf("line %d: empty namespace", lineNum)
		}
		if !namespaceRegex.MatchString(namespace) {
			return nil, nil, fmt.Errorf("line %d: invalid namespace %q (allowed pattern: [a-zA-Z0-9_-], 1-%d chars)", lineNum, namespace, maxNamespaceLength)
		}

		if !strings.HasPrefix(secretRef, "[") || !strings.HasSuffix(secretRef, "]") {
			return nil, nil, fmt.Errorf("line %d: api_key must be a bracketed secret reference [scheme:payload]; got %q", lineNum, secretRef)
		}

		apiKey := reader.ReadSecret(ctx, secrets.Secret(secretRef))
		if apiKey == "" {
			// Empty bearer key would authenticate any `Authorization: Bearer ` request.
			return nil, nil, fmt.Errorf("line %d: api_key resolved to empty value (secret %q)", lineNum, secretRef)
		}

		if existingNS, ok := keys[apiKey]; ok {
			return nil, nil, fmt.Errorf("line %d: duplicate api_key (also seen on line %d, mapped to namespace %q)", lineNum, keyOriginLine[apiKey], existingNS)
		}

		if rateStr != "" {
			rate, err := strconv.Atoi(rateStr)
			if err != nil {
				return nil, nil, fmt.Errorf("line %d: rate %q is not a valid integer", lineNum, rateStr)
			}
			if rate < minRatePerSecond {
				return nil, nil, fmt.Errorf("line %d: rate %d below minimum (%d)", lineNum, rate, minRatePerSecond)
			}
			if rate > maxRatePerSecond {
				return nil, nil, fmt.Errorf("line %d: rate %d above maximum (%d)", lineNum, rate, maxRatePerSecond)
			}
			if existing, ok := rates[namespace]; ok && existing != rate {
				return nil, nil, fmt.Errorf("line %d: conflicting rate %d for namespace %q (existing rate %d set on line %d)", lineNum, rate, namespace, existing, rateOriginLine[namespace])
			}
			rates[namespace] = rate
			if _, ok := rateOriginLine[namespace]; !ok {
				rateOriginLine[namespace] = lineNum
			}
		}

		keys[apiKey] = namespace
		keyOriginLine[apiKey] = lineNum
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read keys file: %w", err)
	}

	return keys, rates, nil
}

// splitRow is bracket-aware: a secret reference may contain `:` in its
// payload (e.g., `[file:/etc/calm/keys]`), so strings.Split on `:` would
// mis-tokenize. First `:` separates namespace; secret ref is `[...]`;
// optional rate follows a `:` after the closing bracket.
func splitRow(line string) (namespace, secretRef, rateStr string, err error) {
	firstColon := strings.Index(line, ":")
	if firstColon == -1 {
		return "", "", "", errors.New("missing field separator (expected <namespace>:<secret_ref>[:<rate>])")
	}
	namespace = strings.TrimSpace(line[:firstColon])
	rest := strings.TrimSpace(line[firstColon+1:])

	if !strings.HasPrefix(rest, "[") {
		return "", "", "", fmt.Errorf("api_key must be a bracketed secret reference [scheme:payload]; got %q", rest)
	}
	closeBracket := strings.Index(rest, "]")
	if closeBracket == -1 {
		return "", "", "", errors.New("unterminated secret reference (missing closing ']')")
	}
	secretRef = rest[:closeBracket+1]
	afterBracket := strings.TrimSpace(rest[closeBracket+1:])

	if afterBracket == "" {
		return namespace, secretRef, "", nil
	}
	if !strings.HasPrefix(afterBracket, ":") {
		return "", "", "", fmt.Errorf("unexpected content after secret reference: %q (expected ':<rate>' or end of line)", afterBracket)
	}
	rateStr = strings.TrimSpace(afterBracket[1:])
	return namespace, secretRef, rateStr, nil
}
