// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lintCmd CLI-entry coverage. The Analyzer itself is well-tested via
// analysistest.Run in lint_test.go (badpkg / goodpkg / genpkg
// fixtures), but the outer `lintCmd` driver — flag parsing, exit-code
// contract, packages.Load wiring — was at 0%. A regression in the
// argument-routing, the error-emission stream choice (stdout vs
// stderr), or the documented exit codes (0 clean / 1 error / 2 bad
// flags) would not have surfaced.

// runLintCmd captures stdout + stderr from a lintCmd invocation. The
// command takes *os.File concretely (not io.Writer), so we use
// os.Pipe to redirect each stream and read the captured output back.
func runLintCmd(t *testing.T, args []string) (stdout, stderr string, code int) {
	t.Helper()

	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)

	code = lintCmd(outW, errW, args)

	// Close writers so the readers see EOF.
	require.NoError(t, outW.Close())
	require.NoError(t, errW.Close())

	outBytes, err := io.ReadAll(outR)
	require.NoError(t, err)
	errBytes, err := io.ReadAll(errR)
	require.NoError(t, err)
	require.NoError(t, outR.Close())
	require.NoError(t, errR.Close())

	return string(outBytes), string(errBytes), code
}

func TestLintCmd_NoArgsReturnsTwoWithUsageOnStderr(t *testing.T) {
	// Per the documented exit-code contract: bad flags → 2,
	// runtime error → 1, clean → 0. Missing the package pattern
	// is a bad-flag case. Usage emission must go to stderr so a
	// script consuming the linter's stdout (machine-readable
	// diagnostics) doesn't see the help text.
	stdout, stderr, code := runLintCmd(t, []string{})
	assert.Equal(t, 2, code, "no-args exit code")
	assert.Empty(t, stdout, "stdout must be empty on bad-flag exit")
	assert.Contains(t, stderr, "chameleon lint",
		"usage banner should appear on stderr")
}

func TestLintCmd_HelpFlagReturnsZero(t *testing.T) {
	// --help triggers flag.ErrHelp inside fs.Parse. lintCmd is
	// documented to swallow that and return 0 (the universal
	// `--help` UX expectation).
	stdout, stderr, code := runLintCmd(t, []string{"--help"})
	if code != 0 {
		t.Errorf("--help exit = %d, want 0", code)
	}
	// flag's default --help prints to fs.Output() which we routed
	// to errW. Either stream is acceptable; assert the help text
	// appears somewhere reachable.
	combined := stdout + stderr
	assert.Contains(t, combined, "chameleon lint",
		"--help should print the help banner")
}

func TestLintCmd_BadFlagReturnsTwo(t *testing.T) {
	// An unrecognized flag should fail flag.Parse with a non-help
	// error. lintCmd returns 2 (bad flags) in that branch.
	stdout, stderr, code := runLintCmd(t, []string{"--definitely-not-a-real-flag"})
	assert.Equal(t, 2, code, "bad-flag exit code")
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "flag",
		"flag.Parse error wording should mention 'flag'")
}

func TestLintCmd_NonExistentPatternReturnsOne(t *testing.T) {
	// A pattern that matches zero packages is a runtime error
	// (not a flag error). Per the function's exit-code contract,
	// it returns 1. This is the path developers hit when they
	// typo a package name.
	//
	// Note: packages.PrintErrors writes to os.Stderr directly
	// (not via the errW we pipe through). The test asserts the
	// EXIT CODE since that's what main() acts on; the user-visible
	// error message reaching the real terminal isn't routed through
	// our pipe and so isn't asserted here.
	stdout, _, code := runLintCmd(t,
		[]string{"./this/path/does/not/exist/in/any/module/anywhere"})
	assert.Equal(t, 1, code, "no-match exit code")
	assert.Empty(t, stdout, "stdout must be empty when no packages matched")
}

func TestLintCmd_CleanPackageReturnsZero(t *testing.T) {
	// Run lintCmd against a chameleon-internal package known to be
	// clean (no Secret.Value reads/writes). exit 0 = no
	// diagnostics. Uses the resolver package — pure typed config
	// runtime, no PXF Secret consumption. If a future change adds
	// a Value usage to resolver, this test would correctly fail.
	stdout, _, code := runLintCmd(t,
		[]string{"github.com/trendvidia/chameleon/resolver"})
	assert.Equal(t, 0, code, "clean package exit code")
	// graph.PrintText emits "<analyzer>: no diagnostics" or
	// similar header; either way stdout shouldn't contain a
	// "FAIL" / "Value" line for the resolver.
	assert.NotContains(t, stdout, "pxfv1.Secret.Value",
		"clean package must not flag any Value usage")
}
