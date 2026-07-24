// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"reflect"
	"testing"
)

func TestGitDiffArgv(t *testing.T) {
	cases := []struct {
		name string
		args gitDiffArgs
		want []string
	}{
		{
			name: "no refs defaults to HEAD so the working tree and index both diff against the last commit",
			args: gitDiffArgs{},
			want: []string{"git", "diff", "HEAD"},
		},
		{
			name: "explicit refs pass through verbatim",
			args: gitDiffArgs{Refs: []string{"main..feat"}},
			want: []string{"git", "diff", "main..feat"},
		},
		{
			name: "refs and paths keep the -- separator",
			args: gitDiffArgs{Refs: []string{"HEAD~1"}, Paths: []string{"src/app.go"}},
			want: []string{"git", "diff", "HEAD~1", "--", "src/app.go"},
		},
		{
			name: "staged with no ref diffs the index against HEAD",
			args: gitDiffArgs{Staged: true},
			want: []string{"git", "diff", "--staged"},
		},
		{
			name: "staged with one ref diffs the index against that ref",
			args: gitDiffArgs{Staged: true, Refs: []string{"HEAD~1"}},
			want: []string{"git", "diff", "--staged", "HEAD~1"},
		},
		{
			name: "staged with paths keeps the -- separator",
			args: gitDiffArgs{Staged: true, Paths: []string{"src/app.go"}},
			want: []string{"git", "diff", "--staged", "--", "src/app.go"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gitDiffArgv(c.args); !reflect.DeepEqual(got, c.want) {
				t.Errorf("gitDiffArgv(%+v) = %v; want %v", c.args, got, c.want)
			}
		})
	}
}
