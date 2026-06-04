// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestServerInternalImportsAreExtractionPortable enforces the decoupling seam: the
// adapter may import the server's internal packages only when those packages are
// extraction-portable — internal/api/genapi (codegen from the OpenAPI spec; confined
// to genapi_client.go so the codegen swap is one file) and internal/secrets (a slim,
// dependency-free package copied alongside at extraction; allowed anywhere).
// Intra-adapter imports (internal/adapter/...) are always fine; every other internal
// package is off-limits, keeping a future module carve-out a lift, not a rewrite.
func TestServerInternalImportsAreExtractionPortable(t *testing.T) {
	const serverInternal = `"github.com/one-harsh/calm/internal/`
	const adapterInternal = `"github.com/one-harsh/calm/internal/adapter/`
	const secretsPkg = `"github.com/one-harsh/calm/internal/secrets"`
	const genapiPkg = `"github.com/one-harsh/calm/internal/api/genapi"`
	const genapiFile = "genapi_client.go"

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			p := imp.Path.Value
			switch {
			case !strings.HasPrefix(p, serverInternal):
				// external / stdlib
			case strings.HasPrefix(p, adapterInternal):
				// intra-adapter
			case p == secretsPkg:
				// copy-portable, allowed anywhere
			case p == genapiPkg && filepath.Base(path) == genapiFile:
				// codegen-portable, confined to the client impl
			default:
				t.Errorf("%s imports server-internal %s — not extraction-portable (decoupling seam)", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk adapter tree: %v", err)
	}
}
