// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"reflect"
	"testing"
)

func TestGrepArgv(t *testing.T) {
	dirs := map[string]bool{"src": true, "docs": true}
	isDir := func(p string) bool { return dirs[p] }
	cases := []struct {
		name            string
		engine, pattern string
		paths           []string
		caseInsensitive bool
		include         string
		want            []string
	}{
		{
			name: "rg minimal", engine: "rg", pattern: "TODO", paths: []string{"."},
			want: []string{"rg", "-n", "--no-heading", "--", "TODO", "."},
		},
		{
			name: "rg all flags", engine: "rg", pattern: "-dash", paths: []string{"src", "docs"},
			caseInsensitive: true, include: "*.go",
			want: []string{"rg", "-n", "--no-heading", "-i", "-g", "*.go", "--", "-dash", "src", "docs"},
		},
		{
			name: "grep minimal", engine: "grep", pattern: "TODO", paths: []string{"."},
			want: []string{"grep", "-rn", "--", "TODO", "."},
		},
		{
			name: "grep all flags", engine: "grep", pattern: "-dash", paths: []string{"src"},
			caseInsensitive: true, include: "*.go",
			want: []string{"grep", "-rn", "-i", "--include=*.go", "--", "-dash", "src"},
		},
		{
			name: "findstr minimal", engine: "findstr", pattern: "TODO", paths: []string{"."},
			want: []string{"findstr", "/S", "/N", "/R", "/C:TODO", "*"},
		},
		{
			name: "findstr spaced pattern stays single via /C", engine: "findstr",
			pattern: "exact phrase", paths: []string{"."},
			want: []string{"findstr", "/S", "/N", "/R", "/C:exact phrase", "*"},
		},
		{
			name: "findstr all flags composes filespecs", engine: "findstr", pattern: "TODO",
			paths: []string{"src", "docs"}, caseInsensitive: true, include: "*.go",
			want: []string{"findstr", "/S", "/N", "/R", "/I", "/C:TODO", `src\*.go`, `docs\*.go`},
		},
		{
			name: "findstr file scope passes through without recursion", engine: "findstr",
			pattern: "TODO", paths: []string{"README.md"},
			want: []string{"findstr", "/N", "/R", "/C:TODO", "README.md"},
		},
		{
			name: "findstr mixed file and dir keeps recursion", engine: "findstr",
			pattern: "TODO", paths: []string{"README.md", "src"},
			want: []string{"findstr", "/S", "/N", "/R", "/C:TODO", "README.md", `src\*`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := grepArgv(c.engine, c.pattern, c.paths, c.caseInsensitive, c.include, isDir)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("argv = %v; want %v", got, c.want)
			}
		})
	}
}
