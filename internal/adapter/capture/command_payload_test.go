// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"testing"

	"github.com/one-harsh/calm/internal/adapter/exec"
)

func TestCommandPayload(t *testing.T) {
	cases := []struct {
		name string
		r    exec.Result
		want string
	}{
		{
			name: "stdout only — no markers",
			r:    exec.Result{Stdout: "hello\n"},
			want: "hello\n",
		},
		{
			name: "stderr only — single section with stderr marker",
			r:    exec.Result{Stderr: "err\n"},
			want: "[stderr]\nerr\n",
		},
		{
			name: "both with trailing newlines — one blank line between sections",
			r:    exec.Result{Stdout: "out\n", Stderr: "err\n"},
			want: "[stdout]\nout\n\n[stderr]\nerr\n",
		},
		{
			name: "both without trailing newlines — extra newline patches the gap",
			r:    exec.Result{Stdout: "out", Stderr: "err"},
			want: "[stdout]\nout\n\n[stderr]\nerr",
		},
		{
			name: "whitespace-only stderr — treated as no stderr (no marker)",
			r:    exec.Result{Stdout: "out\n", Stderr: "  \n"},
			want: "out\n",
		},
		{
			name: "both empty — empty payload",
			r:    exec.Result{},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CommandPayload(c.r); got != c.want {
				t.Errorf("CommandPayload = %q; want %q", got, c.want)
			}
		})
	}
}
