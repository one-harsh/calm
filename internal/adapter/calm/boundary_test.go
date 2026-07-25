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

// TestAdapterDependencyDirection enforces DESIGN.md §1's dependency rule
// mechanically across the adapter packages and the shell binaries under cmd/:
// the capture engine imports no shell, and shells never import one another (a
// shell importing the engine is the sanctioned direction). This is what keeps
// retiring a shell a deletion, not a refactor.
func TestAdapterDependencyDirection(t *testing.T) {
	const modPrefix = "github.com/one-harsh/calm/"
	const engine = "internal/adapter/capture"
	// Each shell is its internal package(s) plus its cmd binary. A new shell
	// adds its roots here and the cross-import rules cover it for free.
	shells := map[string][]string{
		"mcp": {"internal/adapter/mcp", "cmd/calm-adapter"},
	}

	under := func(rel string, roots ...string) bool {
		for _, root := range roots {
			if rel == root || strings.HasPrefix(rel, root+"/") {
				return true
			}
		}
		return false
	}
	// component names the owner of a repo-relative package path: the engine, a
	// shell, or "" for shared-leaf / server code the direction rules don't bind.
	component := func(rel string) string {
		if under(rel, engine) {
			return "engine"
		}
		for name, roots := range shells {
			if under(rel, roots...) {
				return name
			}
		}
		return ""
	}
	repoRel := func(abs string) string {
		abs = filepath.ToSlash(abs)
		for _, marker := range []string{"/internal/", "/cmd/"} {
			if i := strings.LastIndex(abs, marker); i >= 0 {
				return abs[i+1:]
			}
		}
		return abs
	}

	var roots []string
	for _, r := range []string{"..", "../../../cmd"} {
		abs, aerr := filepath.Abs(r)
		if aerr != nil {
			t.Fatalf("abs %s: %v", r, aerr)
		}
		roots = append(roots, abs)
	}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			owner := component(repoRel(path))
			if owner == "" {
				return nil // shared-leaf or server file: unconstrained by these rules
			}
			f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(p, modPrefix) {
					continue
				}
				target := component(strings.TrimPrefix(p, modPrefix))
				switch {
				case owner == "engine" && target != "" && target != "engine":
					t.Errorf("%s (engine) imports shell package %s — the engine must depend on no shell (DESIGN.md §1)", repoRel(path), p)
				case owner != "engine" && target != "" && target != "engine" && target != owner:
					t.Errorf("%s (%s shell) imports %s (%s shell) — shells must never import each other (DESIGN.md §1)", repoRel(path), owner, p, target)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
