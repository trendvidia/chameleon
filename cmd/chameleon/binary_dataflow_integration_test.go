// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// Cross-subcommand binary data-flow tests. binary_integration_test.go
// pinned dispatch / exit codes / usage; this file runs the actual
// data path through the binary boundary:
//
//     chameleon bundle build  → bundle.zip on disk
//     chameleon get --bundle=…  → reads it
//     chameleon explain --bundle=…  → reads it
//
// What we'd miss without it: a regression in the bundle.zip on-disk
// format, in the bundleflags parser, or in the cross-subcommand
// consistency contract (writer + reader must agree about file
// layout, entry names, base64 wrapping) — none of which the
// in-process tests can catch since they wire writer-output directly
// into reader-input via bytes.Buffer.
//
// Gated behind `-tags integration`. Reuses buildBinary() from
// binary_integration_test.go (same package, same build tag).
//
// Run with:
//
//	go test -tags=integration ./cmd/chameleon/...
package main

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dataFlowFixtures stamps the on-disk layout each subtest needs:
// pxf.Secret WKT, a 2-field consumer schema, single-env chain spec,
// plaintext layer, and a narrowing manifest. Identical shape to
// bundle_test.go's bundleFixtures so a developer comparing the
// in-process and binary-boundary tests sees the same inputs.
func dataFlowFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
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
		"manifest.pxf": `modules = [{
  name = "billing-worker"
  allow = ["db_password"]
}]
`,
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return dir
}

// commonBundleArgs builds the shared --manifest/--module/--schema/etc.
// flags. Matches the in-process bundleArgs helper.
func commonBundleArgs(dir string) []string {
	return []string{
		"--manifest=" + filepath.Join(dir, "manifest.pxf"),
		"--module=billing-worker",
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
	}
}

func TestBinaryDataFlow_BundleBuildThenGet(t *testing.T) {
	// Headline e2e: build a bundle through the binary, write it to
	// disk, then `get` it back through the binary. Asserts:
	//   - the producer creates a structurally valid zip on disk
	//   - the consumer reads it via --bundle=PATH
	//   - the manifest's narrowing survived the round-trip (api_key
	//     gone; db_password survives as `<redacted>` per the get
	//     subcommand's redaction contract)
	//   - the plain scalar base_url comes through verbatim
	dir := dataFlowFixtures(t)
	bundlePath := filepath.Join(dir, "billing-worker.bundle")

	// 1. bundle build → bundle.zip on disk.
	args := append([]string{"bundle", "build", "--out=" + bundlePath}, commonBundleArgs(dir)...)
	_, stderr, exit := runBinary(t, args...)
	require.Equal(t, 0, exit, "bundle build exit; stderr: %s", stderr)

	info, err := os.Stat(bundlePath)
	require.NoError(t, err, "bundle file must exist after build")
	assert.Greater(t, info.Size(), int64(0), "bundle file must be non-empty")

	// Quick sanity: it's a valid zip (PK signature) and contains
	// the entries the bundle reader needs.
	data, err := os.ReadFile(bundlePath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(data), 4)
	assert.Equal(t, "PK\x03\x04", string(data[:4]), "bundle file must be a zip")

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	names := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"chain.pxf", "base.pxf"} {
		assert.True(t, names[want], "bundle should contain %q", want)
	}

	// 2. get --bundle=PATH → resolved Cfg, secrets redacted.
	getArgs := []string{
		"get",
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--env=prod",
	}
	stdout, stderr, exit := runBinary(t, getArgs...)
	require.Equal(t, 0, exit, "get --bundle exit; stderr: %s", stderr)

	// Plain scalar untouched.
	assert.Contains(t, stdout, `base_url = "http://x"`,
		"plain scalar must survive the round-trip verbatim")

	// db_password (allowed) surfaces with the redaction sentinel,
	// confirming the get subcommand's redact pass ran over a
	// re-opened bundle.
	assert.Contains(t, stdout, `<redacted>`,
		"allowed secret must be present and redacted in output")

	// api_key (denied) must NOT appear in any form. The manifest's
	// narrowing happens at build time; if it failed to apply, the
	// ciphertext / plaintext would surface here.
	assert.NotContains(t, stdout, "api_key",
		"denied secret must not appear in resolved output")
	assert.NotContains(t, stdout, "live-api-key-do-not-leak",
		"the load-bearing invariant: plaintext of a denied secret never reaches stdout")
	assert.NotContains(t, stdout, "live-db-password-do-not-leak",
		"plaintext of the allowed secret is also redacted in `get` output")
}

func TestBinaryDataFlow_BundleBuildBase64ThenGet(t *testing.T) {
	// Verifies the --base64 wrapping survives the binary boundary
	// in both directions: the producer writes base64 bytes, the
	// consumer reads them via --bundle=PATH + --base64. This is
	// the shape used for "paste into a secret manager value" UX.
	dir := dataFlowFixtures(t)
	bundlePath := filepath.Join(dir, "billing-worker.b64.bundle")

	buildArgs := append([]string{"bundle", "build", "--base64", "--out=" + bundlePath},
		commonBundleArgs(dir)...)
	_, stderr, exit := runBinary(t, buildArgs...)
	require.Equal(t, 0, exit, "bundle build --base64 exit; stderr: %s", stderr)

	// File should be base64 (alphanumeric + padding only).
	data, err := os.ReadFile(bundlePath)
	require.NoError(t, err)
	for i, c := range data {
		if !isBase64ByteB(c) {
			t.Fatalf("bundle byte %d = %q is not base64 — encoder not invoked?", i, c)
		}
	}

	getArgs := []string{
		"get",
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--base64",
		"--env=prod",
	}
	stdout, stderr, exit := runBinary(t, getArgs...)
	require.Equal(t, 0, exit, "get --bundle --base64 exit; stderr: %s", stderr)
	assert.Contains(t, stdout, `base_url = "http://x"`)
}

func TestBinaryDataFlow_BundleViaStdin(t *testing.T) {
	// The --bundle=- variant feeds the bundle bytes through stdin.
	// Useful for pipelines: `gcloud secrets versions access ...
	// | chameleon get --bundle=-`. We can't easily pipe via
	// runBinary's exec.Cmd helper without reaching for stdin
	// plumbing, so build the bundle in step 1 and then use cmd
	// stdin directly in step 2.
	dir := dataFlowFixtures(t)
	bundlePath := filepath.Join(dir, "via-stdin.bundle")

	buildArgs := append([]string{"bundle", "build", "--out=" + bundlePath}, commonBundleArgs(dir)...)
	_, stderr, exit := runBinary(t, buildArgs...)
	require.Equal(t, 0, exit, "bundle build exit; stderr: %s", stderr)

	bundleBytes, err := os.ReadFile(bundlePath)
	require.NoError(t, err)

	stdout, stderr, exit := runBinaryWithStdin(t, bundleBytes,
		"get",
		"--schema="+filepath.Join(dir, "app.proto"),
		"--include="+dir,
		"--message=smoke.v1.Cfg",
		"--bundle=-",
		"--env=prod",
	)
	require.Equal(t, 0, exit, "get --bundle=- exit; stderr: %s", stderr)
	assert.Contains(t, stdout, `base_url = "http://x"`,
		"stdin-fed bundle must resolve identically to file-fed")
}

func TestBinaryDataFlow_BundleBuildRefusesExistingOut(t *testing.T) {
	// --out=PATH where PATH already exists must fail without
	// --force. Protects against accidental overwrite of a
	// previously-distributed bundle.
	dir := dataFlowFixtures(t)
	bundlePath := filepath.Join(dir, "conflict.bundle")
	// Pre-create the file with a sentinel — bundle build must
	// refuse to truncate it.
	sentinel := []byte("pre-existing content, must not be overwritten")
	require.NoError(t, os.WriteFile(bundlePath, sentinel, 0o644))

	buildArgs := append([]string{"bundle", "build", "--out=" + bundlePath}, commonBundleArgs(dir)...)
	_, stderr, exit := runBinary(t, buildArgs...)
	assert.NotEqual(t, 0, exit, "bundle build must refuse existing --out without --force")
	assert.NotEmpty(t, stderr, "refusal should explain itself on stderr")

	// File untouched.
	after, err := os.ReadFile(bundlePath)
	require.NoError(t, err)
	assert.Equal(t, sentinel, after, "existing file must not be overwritten on refusal")
}

func TestBinaryDataFlow_BundleBuildForceOverwrites(t *testing.T) {
	// --force on an existing --out file truncates and replaces.
	// The new file must be a valid bundle (PK header), not a
	// zero-length artifact.
	dir := dataFlowFixtures(t)
	bundlePath := filepath.Join(dir, "force-overwrite.bundle")
	require.NoError(t, os.WriteFile(bundlePath, []byte("stale"), 0o644))

	buildArgs := append([]string{"bundle", "build", "--out=" + bundlePath, "--force"}, commonBundleArgs(dir)...)
	_, stderr, exit := runBinary(t, buildArgs...)
	require.Equal(t, 0, exit, "bundle build --force exit; stderr: %s", stderr)

	data, err := os.ReadFile(bundlePath)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(data), 4)
	assert.Equal(t, "PK\x03\x04", string(data[:4]),
		"--force must produce a real bundle, not a truncated/empty file")
}

// isBase64ByteB mirrors bundle_test.go's isBase64Char (its package is
// bundle_test, not main, so we keep a local copy here to avoid a test
// import cycle).
func isBase64ByteB(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	// Allow '\n' so a base64-wrapped payload with trailing newlines
	// still passes the check.
	return c == '+' || c == '/' || c == '=' || c == '\n'
}

// runBinaryWithStdin runs the chameleon binary with the given bytes
// piped to stdin. The base runBinary helper in
// binary_integration_test.go doesn't expose a stdin hook; this
// extension keeps the change small (no signature churn on the
// existing helper) and lives here so the stdin-feeding logic is
// co-located with the only test that needs it today.
func runBinaryWithStdin(t *testing.T, stdin []byte, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("run chameleon: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exit
}
