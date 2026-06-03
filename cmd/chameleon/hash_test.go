// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hashCmd error-path coverage. verify_test.go's TestHashCmd pinned
// the happy path (file with @header → SHA-256 of body). The
// arg-count / help / missing-file / parse-failure branches were at
// 0% within an otherwise 55%-covered function.

func TestHashCmd_NoArgsErrors(t *testing.T) {
	// `chameleon hash` (no positional arg) is a usage error.
	// Usage banner goes to fs.Output (stderr-equivalent); error
	// wording mentions the arg-count requirement.
	var out bytes.Buffer
	err := hashCmd(&out, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one file argument",
		"error should identify the arg-count requirement")
}

func TestHashCmd_MultipleArgsErrors(t *testing.T) {
	// Two positional args is also a usage error — hashCmd hashes
	// ONE file at a time. A future bulk variant would be a
	// separate subcommand, not multiplicity here.
	var out bytes.Buffer
	err := hashCmd(&out, []string{"a.pxf", "b.pxf"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one file argument")
}

func TestHashCmd_HelpFlagReturnsNil(t *testing.T) {
	// --help is the universal subcommand convention: returns nil
	// so main() exits 0. flag.ErrHelp is swallowed inside hashCmd.
	var out bytes.Buffer
	err := hashCmd(&out, []string{"--help"})
	require.NoError(t, err, "--help must return nil")
}

func TestHashCmd_MissingFileErrors(t *testing.T) {
	// os.ReadFile surfaces a wrapped "no such file" error. hashCmd
	// returns it verbatim; the operator sees the path that wasn't
	// readable.
	var out bytes.Buffer
	missing := filepath.Join(t.TempDir(), "definitely-not-there.pxf")
	err := hashCmd(&out, []string{missing})
	require.Error(t, err)
	assert.True(t,
		os.IsNotExist(err) || strings.Contains(err.Error(), "no such file"),
		"error should identify the missing file; got: %v", err)
}

func TestHashCmd_UnreadableHeaderSurfacesParseError(t *testing.T) {
	// A file whose @header preamble is malformed must surface the
	// header.BodyHash parse error rather than emitting a stale or
	// fabricated hash. The exact wording is owned by the parser;
	// the load-bearing fact here is that the function does NOT
	// proceed silently to print a hash.
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.pxf")
	// @header with an unbalanced brace — BodyHash splits on the
	// closing }, and a missing close should fail the split.
	require.NoError(t, os.WriteFile(path,
		[]byte(`@header chameleon.v1.LayerHeader { id = "x"`), 0o644))

	var out bytes.Buffer
	err := hashCmd(&out, []string{path})
	if err == nil {
		// If BodyHash is permissive enough to accept this, at
		// least the OUTPUT must not be a normal-looking hash —
		// fail loudly so we notice the contract drift.
		got := strings.TrimSpace(out.String())
		t.Fatalf("expected error for malformed @header; got hash=%q", got)
	}
}

func TestHashCmd_PlainFileNoHeader_HashesWholeFile(t *testing.T) {
	// Happy path complement to verify_test.go's TestHashCmd: a
	// file with NO @header preamble should hash its entire body.
	// Pins the "body is the whole file when no header" behavior
	// documented on the function.
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.pxf")
	content := []byte(`base_url = "http://x"` + "\n")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	var out bytes.Buffer
	require.NoError(t, hashCmd(&out, []string{path}))
	got := strings.TrimSpace(out.String())
	want := sha256hex(content)
	assert.Equal(t, want, got,
		"no-header file should hash the entire file contents")
}
