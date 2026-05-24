// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bundleFixtures stamps a self-contained bundle test setup into dir:
// pxf.Secret WKT, a small consumer schema with one Secret + one
// scalar, a single-env chain spec, a single plaintext layer, and a
// manifest allowlisting only the scalar (so narrowing has work to
// do). Returns the dir for flag-path convenience.
func bundleFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"pxf/secret.proto": `syntax = "proto3";
package pxf;
message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`,
		"app.proto": `syntax = "proto3";
package smoke.v1;
import "pxf/secret.proto";
message Cfg {
  string base_url = 1;
  pxf.Secret api_key = 2;
  pxf.Secret db_password = 3;
}
`,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "http://x"
api_key = "live-api-key-do-not-leak"
db_password = "live-db-password-do-not-leak"
`,
		// Manifest narrows to db_password only — exercises the
		// allow path (db_password survives) AND the implicit-
		// DENY_ALL fallback (api_key is dropped). base_url is a
		// non-secret so it's untouched by Narrow regardless of
		// the filter.
		"manifest.pxf": `modules = [{
  name = "billing-worker"
  allow = ["db_password"]
}]
`,
	})
	return dir
}

// bundleArgs builds the common flag set for the bundle build CLI
// against the bundleFixtures layout. Extra flags appended at the end.
func bundleArgs(dir string, extra ...string) []string {
	base := []string{
		"--manifest=" + filepath.Join(dir, "manifest.pxf"),
		"--module=billing-worker",
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
	}
	return append(base, extra...)
}

// readZip pulls the entries out of a zip stream into a name → bytes
// map for assertions.
func readZip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	out := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func TestBundleBuildCmd_Smoke(t *testing.T) {
	dir := bundleFixtures(t)
	var out bytes.Buffer
	err := bundleCmd(&out, append([]string{"build"}, bundleArgs(dir)...))
	require.NoError(t, err)

	entries := readZip(t, out.Bytes())
	for _, want := range []string{"base.pxf", "chain.pxf", "META/audit.txt"} {
		_, ok := entries[want]
		assert.True(t, ok, "bundle should contain %q (got: %v)", want, keys(entries))
	}

	// Narrowing actually happened:
	//   - base_url (non-secret) is untouched
	//   - db_password (Secret, allowed) survives
	//   - api_key (Secret, denied) is gone with no plaintext residue
	base := string(entries["base.pxf"])
	assert.Contains(t, base, `base_url = "http://x"`, "non-secret scalar untouched")
	assert.Contains(t, base, "db_password", "allowed secret survives")
	assert.NotContains(t, base, "api_key", "denied secret's key absent from narrowed layer")
	assert.NotContains(t, base, "live-api-key-do-not-leak", "denied secret plaintext stripped — load-bearing invariant")

	// Audit carries the module + manifest identity.
	audit := string(entries["META/audit.txt"])
	assert.Contains(t, audit, "module: billing-worker")
	assert.Contains(t, audit, "manifest_name: manifest.pxf")
	assert.Contains(t, audit, "manifest_sha256: ")
}

func TestBundleBuildCmd_Base64(t *testing.T) {
	dir := bundleFixtures(t)
	var out bytes.Buffer
	err := bundleCmd(&out, append([]string{"build"}, bundleArgs(dir, "--base64")...))
	require.NoError(t, err)

	// Stdout must be valid base64. Anything else means a leak of raw
	// zip bytes past the encoder, which would corrupt console paste.
	raw, err := base64.StdEncoding.DecodeString(out.String())
	require.NoError(t, err, "stdout must decode as base64")

	// And the decoded bytes must be the same zip we'd produce without
	// the flag — round-trip integrity check.
	entries := readZip(t, raw)
	assert.Contains(t, string(entries["base.pxf"]), "db_password",
		"base64-decoded zip should contain the narrowed layer")
	assert.NotContains(t, string(entries["base.pxf"]), "live-api-key",
		"base64 round-trip preserves narrowing — denied secret still stripped")
}

func TestBundleBuildCmd_OutToFile(t *testing.T) {
	dir := bundleFixtures(t)
	outPath := filepath.Join(dir, "out.bundle")
	var stdout bytes.Buffer
	err := bundleCmd(&stdout, append([]string{"build"}, bundleArgs(dir, "--out="+outPath)...))
	require.NoError(t, err)
	assert.Empty(t, stdout.String(), "with --out=<path>, stdout should be untouched")

	data, err := io.ReadAll(mustOpen(t, outPath))
	require.NoError(t, err)
	entries := readZip(t, data)
	assert.Contains(t, string(entries["base.pxf"]), "db_password")
}

func TestBundleBuildCmd_RefusesOverwrite(t *testing.T) {
	dir := bundleFixtures(t)
	outPath := filepath.Join(dir, "out.bundle")
	// Pre-create the file so O_EXCL refuses.
	require.NoError(t, writeFile(outPath, []byte("existing")))
	err := bundleCmd(&bytes.Buffer{}, append([]string{"build"}, bundleArgs(dir, "--out="+outPath)...))
	require.Error(t, err, "must refuse to overwrite existing --out path")
	assert.Contains(t, err.Error(), "exist", "error should mention the existence collision")
}

func TestBundleBuildCmd_MissingRequiredFlag(t *testing.T) {
	cases := []struct {
		name string
		omit string
	}{
		{"no manifest", "--manifest"},
		{"no module", "--module"},
		{"no schema", "--schema"},
		{"no message", "--message"},
		{"no chain", "--chain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := bundleFixtures(t)
			all := append([]string{"build"}, bundleArgs(dir)...)
			filtered := all[:0]
			for _, a := range append([]string{}, all...) {
				if !strings.HasPrefix(a, tc.omit+"=") {
					filtered = append(filtered, a)
				}
			}
			err := bundleCmd(&bytes.Buffer{}, filtered)
			require.Error(t, err, "missing %s should error", tc.omit)
		})
	}
}

func TestBundleCmd_NoSubcommand(t *testing.T) {
	err := bundleCmd(&bytes.Buffer{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subcommand required")
}

func TestBundleCmd_UnknownSubcommand(t *testing.T) {
	err := bundleCmd(&bytes.Buffer{}, []string{"frobnicate"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subcommand")
}

func TestBundleCmd_Help(t *testing.T) {
	var out bytes.Buffer
	err := bundleCmd(&out, []string{"--help"})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "chameleon bundle")
	assert.Contains(t, out.String(), "build")
}

// --- small file-test helpers used only by this file ---

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func mustOpen(t *testing.T, path string) io.Reader {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}
