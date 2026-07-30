// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/config"
	"github.com/one-harsh/calm/internal/secrets"
)

const credentialsFileName = "credentials"

func (d Deps) initCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(d.Stderr)
	sessionID := fs.String("session", "", "harness conversation id (required with --reset)")
	reset := fs.Bool("reset", false, "clear a persisted auth latch and advance the session generation")
	harness := fs.String("harness", "", "install the hook set for a harness (claude)")
	force := fs.Bool("force", false, "overwrite an existing, differing credential")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *reset {
		if *sessionID == "" {
			_, _ = fmt.Fprintln(d.Stderr, "calm-capture init --reset requires --session <id>")
			return 2
		}
		mgr, err := d.manager(*sessionID)
		if err != nil {
			_, _ = fmt.Fprintln(d.Stderr, "calm-capture init: "+err.Error())
			return 1
		}
		if rerr := mgr.Reset(ctx); rerr != nil {
			_, _ = fmt.Fprintln(d.Stderr, "calm-capture init: reset failed: "+rerr.Error())
			return 1
		}
		_, _ = fmt.Fprintf(d.Stderr, "reset: cleared auth latch and advanced session generation for %q\n", *sessionID)
	}

	if *harness != "" {
		return d.installHarness(ctx, *harness, *force)
	}
	return d.probe(ctx)
}

// `probe` reports whether the currently-resolved credential authenticates;
// surfaced at install before any hook fires.
func (d Deps) probe(ctx context.Context) int {
	pctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	switch _, perr := d.Client.RegisterClient(pctx, d.Cfg.Calm.Client); {
	case perr == nil:
		_, _ = fmt.Fprintf(d.Stderr, "ok: CALM reachable at %s; credentials accepted for client %q\n", d.Cfg.Calm.URL, d.Cfg.Calm.Client)
		return 0
	case errors.Is(perr, calm.ErrAuthRejected):
		_, _ = fmt.Fprintf(d.Stderr, "credential failure: CALM at %s rejected the namespace credential — check the api_key pairing\n", d.Cfg.Calm.URL)
		return 1
	default:
		_, _ = fmt.Fprintf(d.Stderr, "connectivity failure: cannot reach CALM at %s: %s\n", d.Cfg.Calm.URL, perr.Error())
		return 1
	}
}

func (d Deps) installHarness(ctx context.Context, harness string, force bool) int {
	if harness != "claude" {
		_, _ = fmt.Fprintf(d.Stderr, "calm-capture init: unsupported harness %q (supported: claude)\n", harness)
		return 2
	}
	if code := d.writeConfigHome(force); code != 0 {
		return code
	}
	if code := d.validatePairing(ctx); code != 0 {
		return code
	}
	pluginDir, code := d.writeClaudePlugin(hookBinPath())
	if code != 0 {
		return code
	}
	d.scanForOtherHooks()
	d.printClaudeGuidance(pluginDir)
	return 0
}

// writeConfigHome writes the one durable credential location (§9): a 0600
// credentials file plus an adapter.yaml that references it, so a keyless runtime
// (a hook-spawned shell with a scrubbed env) still resolves the pairing. The
// credential content is written to the file only — never to any output stream.
func (d Deps) writeConfigHome(force bool) int {
	if err := os.MkdirAll(d.Root, 0o700); err != nil {
		_, _ = fmt.Fprintf(d.Stderr, "calm-capture init: mkdir %s: %s\n", d.Root, err.Error())
		return 1
	}
	apiKeyRef := ""
	if d.Cfg.Calm.APIKey != "" {
		key, err := secrets.Resolve(d.Cfg.Calm.APIKey)
		if err != nil {
			_, _ = fmt.Fprintf(d.Stderr, "calm-capture init: resolve credential: %s\n", err.Error())
			return 1
		}
		credPath := filepath.Join(d.Root, credentialsFileName)
		if code := writeCredential(d.Stderr, credPath, key, force); code != 0 {
			return code
		}
		// TODO: harden credential-at-rest — store the key in the OS keyring behind
		// a [keyring:…] secret ref, keeping the 0600 file as the portable fallback
		// for headless environments (containers, CI) that have no keyring.
		apiKeyRef = "[file:" + credPath + "]"
	}
	cfgPath := filepath.Join(d.Root, config.AdapterConfigFileName)
	if err := os.WriteFile(cfgPath, []byte(renderAdapterYAML(d.Cfg, apiKeyRef)), 0o600); err != nil {
		_, _ = fmt.Fprintf(d.Stderr, "calm-capture init: write %s: %s\n", cfgPath, err.Error())
		return 1
	}
	if code := enforce0600(d.Stderr, cfgPath); code != 0 {
		return code
	}
	_, _ = fmt.Fprintf(d.Stderr, "wrote %s (0600)\n", cfgPath)
	return 0
}

// writeCredential writes key to path idempotently: an identical existing
// credential is a no-op, and a *different* one is refused without force so a
// stray re-run never clobbers a working pairing. The key is never echoed.
func writeCredential(w io.Writer, path, key string, force bool) int {
	existing, err := os.ReadFile(path) //nolint:gosec // the adapter's own 0600 credential under $CALM_HOME
	switch {
	case err == nil:
		if strings.TrimSpace(string(existing)) == key {
			// An identical credential still gets its mode tightened: a file that
			// predates this run could be world/group-readable.
			return enforce0600(w, path)
		}
		if !force {
			_, _ = fmt.Fprintf(w, "calm-capture init: %s holds a different credential; re-run with --force to overwrite\n", path)
			return 1
		}
	case !os.IsNotExist(err):
		_, _ = fmt.Fprintf(w, "calm-capture init: read %s: %s\n", path, err.Error())
		return 1
	}
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		_, _ = fmt.Fprintf(w, "calm-capture init: write %s: %s\n", path, err.Error())
		return 1
	}
	return enforce0600(w, path)
}

// enforce0600 tightens path to owner-only: os.WriteFile leaves an existing
// file's mode untouched, so a credential that predates this run — or a no-op
// identical write — could otherwise stay group/world-readable.
func enforce0600(w io.Writer, path string) int {
	if err := os.Chmod(path, 0o600); err != nil {
		_, _ = fmt.Fprintf(w, "calm-capture init: chmod %s: %s\n", path, err.Error())
		return 1
	}
	return 0
}

func (d Deps) validatePairing(ctx context.Context) int {
	cfgPath := filepath.Join(d.Root, config.AdapterConfigFileName)
	cfg, err := config.Load(cfgPath, d.Root)
	if err != nil {
		_, _ = fmt.Fprintf(d.Stderr, "credential failure: written config at %s does not re-resolve: %s\n", cfgPath, err.Error())
		return 1
	}
	if cfg.Calm.APIKey != "" {
		if _, err := secrets.Resolve(cfg.Calm.APIKey); err != nil {
			_, _ = fmt.Fprintf(d.Stderr, "credential failure: written credential does not resolve: %s\n", err.Error())
			return 1
		}
	}
	return d.probe(ctx)
}

func (d Deps) writeClaudePlugin(binPath string) (string, int) {
	base := filepath.Join(d.Root, "plugins", "claude")
	metaDir := filepath.Join(base, ".claude-plugin")
	hooksDir := filepath.Join(base, "hooks")
	for _, dir := range []string{metaDir, hooksDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			_, _ = fmt.Fprintf(d.Stderr, "calm-capture init: mkdir %s: %s\n", dir, err.Error())
			return "", 1
		}
	}
	files := []struct {
		path    string
		content []byte
	}{
		{filepath.Join(metaDir, "plugin.json"), []byte(claudePluginManifest)},
		{filepath.Join(metaDir, "marketplace.json"), []byte(claudeMarketplace)},
		{filepath.Join(hooksDir, "hooks.json"), hooksConfigJSON(binPath)},
	}
	for _, f := range files {
		if err := os.WriteFile(f.path, f.content, 0o644); err != nil { //nolint:gosec // plugin manifests are non-secret, world-readable by design
			_, _ = fmt.Fprintf(d.Stderr, "calm-capture init: write %s: %s\n", f.path, err.Error())
			return "", 1
		}
	}
	return base, 0
}

// scanForOtherHooks warns (AD07) when another layer already references
// calm-capture: stacked wraps corrupt capture identity. Warn-only — the exec
// re-entrancy sentinel is the load-bearing guard, and layers can appear after
// install.
func (d Deps) scanForOtherHooks() {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	for _, p := range []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
	} {
		data, err := os.ReadFile(p) //nolint:gosec // operator's own settings, read to warn only
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte(binaryName)) {
			_, _ = fmt.Fprintf(d.Stderr, "warning: %s already references %s — stacked capture layers corrupt capture identity; review and remove the other layer\n", p, binaryName)
		}
	}
}

// printClaudeGuidance is the disclosure surface: the plugin consent screen does
// not itemize hooks, so init states exactly what installs, the permission
// consequence, and the hardened warning — and never writes a permission rule.
func (d Deps) printClaudeGuidance(pluginDir string) {
	w := d.Stderr
	_, _ = fmt.Fprintf(w, "\nClaude Code plugin written to %s\n", pluginDir)
	_, _ = fmt.Fprintln(w, "Install it (user scope is the default):")
	_, _ = fmt.Fprintf(w, "   claude plugin marketplace add %s\n", shellSingleQuote(pluginDir))
	_, _ = fmt.Fprintln(w, "   claude plugin install calm-capture")
	_, _ = fmt.Fprintln(w, "\nWhat this plugin installs (the consent screen does not itemize hooks):")
	_, _ = fmt.Fprintln(w, "   • PreToolUse (Bash): rewrites each shell command to run through calm-capture exec so its output is captured.")
	_, _ = fmt.Fprintln(w, "   • SessionStart: injects the retrieval card and reclaims idle capture state.")
	_, _ = fmt.Fprintln(w, "\nPermissions: the rewrite never approves a command — normal permission prompts still run. An allowlisted command may prompt again for its rewritten form; the wrapped command stays legible in the approval dialog's description.")
	_, _ = fmt.Fprintln(w, "   ! Never choose \"don't ask again\" for the calm-capture exec wrapper — that writes a blanket allow rule that auto-approves every wrapped command.")
}

func renderAdapterYAML(cfg config.Config, apiKeyRef string) string {
	var b bytes.Buffer
	b.WriteString("calm:\n")
	fmt.Fprintf(&b, "  url: %q\n", cfg.Calm.URL)
	fmt.Fprintf(&b, "  client: %q\n", cfg.Calm.Client)
	fmt.Fprintf(&b, "  api_key: %q\n", apiKeyRef)
	fmt.Fprintf(&b, "  session_ttl_minutes: %d\n", cfg.Calm.SessionTTLMinutes)
	fmt.Fprintf(&b, "  gc_sample_rate: %d\n", cfg.Calm.GCSampleRate)
	b.WriteString("log:\n")
	fmt.Fprintf(&b, "  level: %q\n", cfg.Log.Level)
	fmt.Fprintf(&b, "  format: %q\n", cfg.Log.Format)
	fmt.Fprintf(&b, "  file: %q\n", cfg.Log.File)
	return b.String()
}

const claudePluginManifest = `{
  "name": "calm-capture",
  "description": "Capture shell command output into CALM so it stays searchable.",
  "version": "0.1.0"
}
`

const claudeMarketplace = `{
  "name": "calm-capture",
  "owner": { "name": "calm-capture" },
  "plugins": [
    {
      "name": "calm-capture",
      "source": ".",
      "description": "Capture shell command output into CALM so it stays searchable."
    }
  ]
}
`

func hooksConfigJSON(binPath string) []byte {
	// The harness runs this command through a shell, so a binary path with
	// spaces must be quoted or the shell splits it and the wrapped command fails.
	cmd, _ := json.Marshal(shellSingleQuote(binPath) + " hook")
	return []byte(fmt.Sprintf(`{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": %s }] }
    ],
    "SessionStart": [
      { "matcher": "startup|resume|clear|compact", "hooks": [{ "type": "command", "command": %s }] }
    ]
  }
}
`, cmd, cmd))
}
