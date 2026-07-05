// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"reflect"
	"testing"
)

func TestGrepArgv(t *testing.T) {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := grepArgv(c.engine, c.pattern, c.paths, c.caseInsensitive, c.include)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("argv = %v; want %v", got, c.want)
			}
		})
	}
}
