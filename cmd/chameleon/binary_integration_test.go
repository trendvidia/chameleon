// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// End-to-end binary smoke. Builds the chameleon binary and runs it
// as a subprocess to exercise:
//
//   - main() dispatch (subcommand routing)
//   - exit codes (0 / 1 / 2 contract)
//   - stdout / stderr stream separation
//   - usage emission for missing-arg / unknown-subcommand paths
//
// The in-process tests in cli_smoke_test.go cover each *Cmd entry
// point's flag-parsing and pipeline logic, but never exercise
// main() — leaving the dispatch table and exit-code contract at 0%
// coverage. This test closes that gap.
//
// Gated behind `-tags integration` so it stays out of the default
// `go test ./...` path (it `go build`s a binary per package run).
//
// Run with:
//
//	go test -tags=integration ./cmd/chameleon/...
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// binPath is populated once per test process by buildBinary() and
// reused across subtests. Saves ~1 second per invocation.
var (
	binPath  string
	binBuilt sync.Once
	binErr   error
)

// buildBinary compiles the chameleon binary into a temp directory
// shared across this test process. The temp directory is OS-managed
// (TMPDIR) and not cleaned up after this process exits — `go test`
// typically runs each test binary in a fresh PID, so leak per run
// is bounded.
func buildBinary(t *testing.T) string {
	t.Helper()
	binBuilt.Do(func() {
		// Build into a stable per-OS temp path so concurrent test
		// runs in different repos don't collide. os.MkdirTemp puts
		// it under TMPDIR with a random suffix.
		dir, err := os.MkdirTemp("", "chameleon-binary-*")
		if err != nil {
			binErr = err
			return
		}
		bin := filepath.Join(dir, "chameleon")
		// Build from the cmd/chameleon package — same as this test
		// file lives in. `go build` defaults to the current
		// directory's package, which works in `go test` cwd.
		cmd := exec.Command("go", "build", "-o", bin, ".")
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = err
			t.Logf("go build output:\n%s", out)
			return
		}
		binPath = bin
	})
	if binErr != nil {
		t.Fatalf("build chameleon binary: %v", binErr)
	}
	return binPath
}

// hermeticEnv is the child environment for a CLI invocation: the
// caller's, with the user-config lookup pointed at a path that does
// not exist.
//
// Without it the binary reads the developer's real $HOME/.chameleon
// and its per-app defaults silently answer flags the test never
// passed. Not hypothetical: `--decrypter` resolves to "none" (which
// wires resolver.WithoutDecrypter) unless something supplies a value,
// so the multi-env tests passed on a laptop whose default app sets
// `decrypter = "sops"` and failed on every machine without one —
// unnoticed, because cmd/chameleon's integration tests only began
// running in CI with the module split. userConfigPath documents this
// env var as existing for exactly this purpose.
func hermeticEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(), "CHAMELEON_USER_CONFIG="+filepath.Join(t.TempDir(), "no-user-config"))
}

// runBinary invokes the built chameleon binary with the given args.
// Returns stdout, stderr, exit code. Never returns an error — the
// exit code IS the signal.
func runBinary(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Env = hermeticEnv(t)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("run chameleon: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exit
}

func TestBinary_Version_ExitsZero(t *testing.T) {
	stdout, stderr, exit := runBinary(t, "version")
	if exit != 0 {
		t.Errorf("exit = %d, want 0\nstdout: %s\nstderr: %s", exit, stdout, stderr)
	}
	if stdout == "" {
		t.Errorf("stdout empty; expected version output")
	}
	// version output uses the Keep-A-Changelog-ish "chameleon
	// <semver>" prefix today; the assertion is intentionally loose
	// so a future format polish doesn't break the test.
	assert.Contains(t, strings.ToLower(stdout), "chameleon",
		"version output should mention chameleon")
}

func TestBinary_VersionFlag_ExitsZero(t *testing.T) {
	// `chameleon --version` should be equivalent to `chameleon version`
	// per the main.go case label. Independent test so a regression in
	// the alias doesn't masquerade as the canonical form's test passing.
	stdout, _, exit := runBinary(t, "--version")
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if stdout == "" {
		t.Errorf("stdout empty; expected version output")
	}
}

func TestBinary_Help_ExitsZero(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			stdout, _, exit := runBinary(t, arg)
			if exit != 0 {
				t.Errorf("exit = %d, want 0", exit)
			}
			assert.Contains(t, stdout, "Usage:", "usage block should appear in help output")
			// Sanity that the subcommand inventory is rendered —
			// catches a `usage()` template that lost its
			// subcommand list.
			for _, sub := range []string{"explain", "get", "reveal", "bundle"} {
				assert.Contains(t, stdout, sub, "help output should list %s", sub)
			}
		})
	}
}

func TestBinary_NoArgs_ExitsTwoWithUsageOnStderr(t *testing.T) {
	// Per main.go: missing subcommand prints usage to stderr and
	// exits 2. This is the load-bearing CLI contract — shells use
	// exit 2 to distinguish "user error" from "runtime failure"
	// (exit 1). A regression to exit 0 or exit 1 here would break
	// scripts that key on it.
	stdout, stderr, exit := runBinary(t)
	if exit != 2 {
		t.Errorf("exit = %d, want 2 (no-args contract)", exit)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty (usage must go to stderr)", stdout)
	}
	assert.Contains(t, stderr, "Usage:", "usage must appear on stderr for missing-subcommand")
}

func TestBinary_UnknownSubcommand_ExitsTwoWithSubcommandName(t *testing.T) {
	stdout, stderr, exit := runBinary(t, "definitely-not-a-real-subcommand")
	if exit != 2 {
		t.Errorf("exit = %d, want 2 (unknown-subcommand contract)", exit)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty (error must go to stderr)", stdout)
	}
	assert.Contains(t, stderr, "unknown subcommand", "stderr should mention the failure mode")
	assert.Contains(t, stderr, "definitely-not-a-real-subcommand",
		"stderr should echo the bad subcommand name so the user sees the typo")
	// Usage block should also be rendered (so the user can recover
	// without re-running help).
	assert.Contains(t, stderr, "Usage:")
}

func TestBinary_MissingRequiredFlag_ExitsOneWithSubcommandPrefix(t *testing.T) {
	// `chameleon explain` with no flags fails flag-parsing inside
	// explainCmd, which returns an error. main.go wraps that with
	// "chameleon explain:" on stderr and exits 1.
	stdout, stderr, exit := runBinary(t, "explain")
	if exit != 1 {
		t.Errorf("exit = %d, want 1 (subcommand-error contract)", exit)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty (error must go to stderr)", stdout)
	}
	assert.Contains(t, stderr, "chameleon explain:",
		"main.go must prefix subcommand errors with 'chameleon <sub>:'")
}

func TestBinary_BundleSubcommand_DispatchesToBundleCmd(t *testing.T) {
	// `chameleon bundle` with no further args should dispatch to
	// bundleCmd, which prints its own subcommand usage and exits
	// non-zero. Confirms the main.go dispatch entry (line 158-162)
	// hits bundleCmd rather than the unknown-subcommand fallthrough.
	_, stderr, exit := runBinary(t, "bundle")
	if exit == 0 {
		t.Errorf("exit = 0, expected non-zero for bundle with no further args")
	}
	// Either "bundle build" usage or a "missing required" error —
	// the point of the assertion is that we got into bundleCmd, not
	// the "unknown subcommand" path.
	if strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("dispatch missed bundleCmd; stderr=%q", stderr)
	}
}

func TestBinary_KeystoreSubcommand_DispatchesToKeystoreCmd(t *testing.T) {
	// Same pattern as bundle — verifies the dispatch table covers
	// keystore. Independent test (rather than a table-driven loop)
	// because subcommand dispatch is the load-bearing piece we want
	// pinned per-route.
	_, stderr, exit := runBinary(t, "keystore")
	if exit == 0 {
		t.Errorf("exit = 0, expected non-zero for keystore with no further args")
	}
	if strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("dispatch missed keystoreCmd; stderr=%q", stderr)
	}
}
