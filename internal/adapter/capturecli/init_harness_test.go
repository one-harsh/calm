// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

// scrubAdapterEnv keeps a developer's exported CALM_ADAPTER_* from overriding
// the values init writes and re-resolves.
func scrubAdapterEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "CALM_ADAPTER_") {
			t.Setenv(k, "")
		}
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// init --harness=claude writes the config home and the plugin, and re-running
// converges to an identical tree — one hook layer, never a second (AD07).
func TestInitHarness_Idempotent(t *testing.T) {
	scrubAdapterEnv(t)
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	d, _, _ := newDeps(t, c)

	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code != 0 {
		t.Fatalf("first install exit = %d; want 0", code)
	}
	first := snapshotTree(t, d.Root)
	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code != 0 {
		t.Fatalf("second install exit = %d; want 0", code)
	}
	if second := snapshotTree(t, d.Root); !reflect.DeepEqual(first, second) {
		t.Errorf("re-install must produce an identical tree (no second layer)")
	}

	for _, rel := range []string{
		"adapter.yaml",
		filepath.Join("plugins", "claude", ".claude-plugin", "plugin.json"),
		filepath.Join("plugins", "claude", ".claude-plugin", "marketplace.json"),
		filepath.Join("plugins", "claude", "hooks", "hooks.json"),
	} {
		if _, err := os.Stat(filepath.Join(d.Root, rel)); err != nil {
			t.Errorf("expected install artifact missing: %s (%v)", rel, err)
		}
	}
	// The hook command invokes the absolute binary path resolved at install.
	hooks, _ := os.ReadFile(filepath.Join(d.Root, "plugins", "claude", "hooks", "hooks.json"))
	if !strings.Contains(string(hooks), " hook") || !strings.Contains(string(hooks), "PreToolUse") {
		t.Errorf("hooks.json must wire the hook command; got:\n%s", hooks)
	}
}

// The durable credential is written once; a re-run with the same key is a
// no-op, a different key is refused without --force, and --force overwrites.
func TestInitHarness_CredentialForce(t *testing.T) {
	scrubAdapterEnv(t)
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	d, _, stderr := newDeps(t, c)
	d.Cfg.Calm.APIKey = "[text:key-one]"

	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code != 0 {
		t.Fatalf("first install exit = %d; want 0", code)
	}
	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code != 0 {
		t.Fatalf("same-key re-run exit = %d; want 0 (idempotent)", code)
	}

	d.Cfg.Calm.APIKey = "[text:key-two]"
	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code == 0 {
		t.Errorf("a different key without --force must be refused")
	}
	if !strings.Contains(stderr.String(), "different credential") {
		t.Errorf("refusal must explain itself; got:\n%s", stderr.String())
	}
	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude", "--force"}); code != 0 {
		t.Errorf("a different key with --force must overwrite; got %d", code)
	}
}

// A pre-existing, world/group-readable credential is tightened to 0600 even when
// its content is unchanged — os.WriteFile leaves an existing file's mode alone.
func TestInitHarness_EnforcesCredentialPerms(t *testing.T) {
	scrubAdapterEnv(t)
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	d, _, _ := newDeps(t, c)
	d.Cfg.Calm.APIKey = "[text:key-one]"

	credPath := filepath.Join(d.Root, credentialsFileName)
	if err := os.MkdirAll(d.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte("key-one"), 0o644); err != nil { //nolint:gosec // deliberately permissive fixture
		t.Fatal(err)
	}

	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code != 0 {
		t.Fatalf("install exit = %d; want 0", code)
	}
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credential perm = %o; want 600 (a pre-existing permissive file must be tightened)", perm)
	}
}

// The printed install guidance single-quotes the plugin path so a space in
// $CALM_HOME cannot split the command the user copy-pastes.
func TestInitHarness_GuidanceQuotesPluginPath(t *testing.T) {
	scrubAdapterEnv(t)
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	d, _, stderr := newDeps(t, c)

	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code != 0 {
		t.Fatalf("install exit = %d; want 0", code)
	}
	pluginDir := filepath.Join(d.Root, "plugins", "claude")
	if !strings.Contains(stderr.String(), "marketplace add "+shellSingleQuote(pluginDir)) {
		t.Errorf("guidance must single-quote the plugin path; got:\n%s", stderr.String())
	}
}

// Cross-layer scan warns (warn-only) when another layer already references
// calm-capture.
func TestInitHarness_WarnsOnOtherLayer(t *testing.T) {
	scrubAdapterEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"hooks":[{"command":"calm-capture hook"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, []string{"init", "--harness=claude"})
	if !strings.Contains(stderr.String(), "already references calm-capture") {
		t.Errorf("must warn about the other capture layer; got:\n%s", stderr.String())
	}
}

// init prints the disclosure the plugin consent screen omits: what installs, the
// permission consequence, and the hardened don't-ask-again warning.
func TestInitHarness_PrintsDisclosure(t *testing.T) {
	scrubAdapterEnv(t)
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, []string{"init", "--harness=claude"})
	out := stderr.String()
	for _, want := range []string{
		"claude plugin marketplace add",
		"PreToolUse",
		"SessionStart",
		`don't ask again`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("disclosure must mention %q; got:\n%s", want, out)
		}
	}
}

// A credential reference that cannot resolve fails the install honestly, before
// writing the config home.
func TestInitHarness_BadCredentialRef(t *testing.T) {
	scrubAdapterEnv(t)
	d, _, stderr := newDeps(t, calm.NewMockClient(t))
	d.Cfg.Calm.APIKey = "[bogus:xyz]"
	if code := Dispatch(context.Background(), d, []string{"init", "--harness=claude"}); code == 0 {
		t.Errorf("a malformed credential ref must fail the install")
	}
	if !strings.Contains(stderr.String(), "resolve credential") {
		t.Errorf("must report the credential resolution failure; got:\n%s", stderr.String())
	}
}

// An unsupported harness is a usage error, not a silent no-op.
func TestInitHarness_Unsupported(t *testing.T) {
	scrubAdapterEnv(t)
	d, _, stderr := newDeps(t, calm.NewMockClient(t))
	if code := Dispatch(context.Background(), d, []string{"init", "--harness=codex"}); code != 2 {
		t.Errorf("unsupported harness exit = %d; want 2", code)
	}
	if !strings.Contains(stderr.String(), "unsupported harness") {
		t.Errorf("must name the unsupported harness; got:\n%s", stderr.String())
	}
}
