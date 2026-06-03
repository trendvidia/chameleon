// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// Validate the bash/zsh/fish completion scripts emitted by
// `chameleon completion <shell>` parse cleanly in their target
// shells. The in-process completionCmd test asserts the dispatch
// (TestCompletionCmd_DispatchesPerShell-style coverage), but
// nothing pins that the embedded script strings are syntactically
// valid for the shells that consume them — a typo or unbalanced
// brace in any of the three const blocks would only surface in a
// user's shell at source time.
//
// Each test pipes the emitted script into the corresponding shell's
// "parse without executing" mode:
//
//	bash -n   (no-exec; parse only)
//	zsh  -n   (same)
//	fish --no-execute   (fish's equivalent)
//
// Each test skips automatically when its shell isn't on PATH —
// contributors without all three installed don't see spurious
// failures.
//
// Gated behind `-tags integration`. Reuses buildBinary() from
// binary_integration_test.go.
package main

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emitCompletion runs `chameleon completion <shell>` and returns
// the produced script. Fails the test if the chameleon command
// itself errors — the script-content tests for that are in
// the existing in-process completion tests.
func emitCompletion(t *testing.T, shell string) []byte {
	t.Helper()
	stdout, stderr, exit := runBinary(t, "completion", shell)
	require.Equal(t, 0, exit,
		"`chameleon completion %s` exit; stderr: %s", shell, stderr)
	require.NotEmpty(t, stdout,
		"`chameleon completion %s` produced no script", shell)
	return []byte(stdout)
}

// parseInShell pipes script into the named shell with the parse-only
// flag. Skips the test if the shell isn't on PATH.
//
// parseFlag is the shell-specific "syntax check, don't run" option:
//
//	bash: -n
//	zsh : -n
//	fish: --no-execute
func parseInShell(t *testing.T, shellBin, parseFlag string, script []byte) (string, int) {
	t.Helper()
	bin, err := exec.LookPath(shellBin)
	if err != nil {
		t.Skipf("%s not on PATH; skipping", shellBin)
	}
	cmd := exec.Command(bin, parseFlag)
	cmd.Stdin = bytes.NewReader(script)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err = cmd.Run()
	var exit int
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("run %s %s: %v", bin, parseFlag, err)
		}
	}
	return errBuf.String(), exit
}

func TestCompletion_BashScriptParsesCleanly(t *testing.T) {
	// Pin: a `bash -n` over the emitted completion script returns
	// exit 0 with no stderr. A regression that introduced
	// unbalanced quotes, a missing `fi` / `;;`, or a stray '$'
	// without a brace would surface here as exit != 0 with the
	// bash parse-error message on stderr.
	script := emitCompletion(t, "bash")
	stderr, exit := parseInShell(t, "bash", "-n", script)
	assert.Equal(t, 0, exit, "bash -n exit; stderr: %s", stderr)
	assert.Empty(t, stderr, "bash -n must produce no stderr on clean parse")
}

func TestCompletion_ZshScriptParsesCleanly(t *testing.T) {
	// Same shape for zsh. The embedded script uses `#compdef
	// chameleon` + `_arguments` syntax — different parse rules
	// than bash, with their own subtle traps (escape sequences in
	// description strings, trailing `:` markers on _arguments
	// specs, etc.). A regression in any of those would shipping
	// silently before this test landed.
	script := emitCompletion(t, "zsh")
	stderr, exit := parseInShell(t, "zsh", "-n", script)
	assert.Equal(t, 0, exit, "zsh -n exit; stderr: %s", stderr)
	assert.Empty(t, stderr, "zsh -n must produce no stderr on clean parse")
}

func TestCompletion_FishScriptParsesCleanly(t *testing.T) {
	// fish has its own quirk: `--no-execute` (not `-n`) is the
	// parse-check flag. The completion script uses `complete -c`
	// idioms; unbalanced quotes or a malformed `-xa` value list
	// would surface here.
	script := emitCompletion(t, "fish")
	stderr, exit := parseInShell(t, "fish", "--no-execute", script)
	assert.Equal(t, 0, exit, "fish --no-execute exit; stderr: %s", stderr)
	assert.Empty(t, stderr, "fish --no-execute must produce no stderr on clean parse")
}

func TestCompletion_UnknownShellExitsNonZero(t *testing.T) {
	// `chameleon completion ksh` is a user error — exit 1 with
	// the failing shell name on stderr. Pinned through the binary
	// boundary so the exit-code contract matches what shell users
	// will hit.
	stdout, stderr, exit := runBinary(t, "completion", "ksh")
	assert.NotEqual(t, 0, exit, "unknown shell should error")
	assert.Empty(t, stdout, "no completion script should leak to stdout on error")
	assert.Contains(t, stderr, "ksh",
		"stderr should identify the unsupported shell")
}

func TestCompletion_NoArgsExitsNonZero(t *testing.T) {
	// Missing the shell positional is a usage error — `chameleon
	// completion` alone exits non-zero. The in-process
	// completionCmd test pinned the wording; this one pins the
	// exit-code contract through the actual binary so shell users
	// see consistent behavior.
	stdout, _, exit := runBinary(t, "completion")
	assert.NotEqual(t, 0, exit, "no-args should error")
	assert.Empty(t, stdout, "no script should be emitted on usage error")
}
