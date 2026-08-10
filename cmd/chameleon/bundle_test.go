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
	sopskeypb "github.com/trendvidia/sops/v4/age/keypb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
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
	require.Error(t, err, "must refuse to overwrite existing --out path by default")
	assert.Contains(t, err.Error(), "exist", "error should mention the existence collision")
}

func TestBundleBuildCmd_ForceOverwrite(t *testing.T) {
	dir := bundleFixtures(t)
	outPath := filepath.Join(dir, "out.bundle")
	// Pre-create with placeholder bytes; --force should truncate
	// + overwrite without complaint.
	require.NoError(t, writeFile(outPath, []byte("stale-content-that-must-be-replaced")))

	err := bundleCmd(&bytes.Buffer{}, append([]string{"build"}, bundleArgs(dir, "--out="+outPath, "--force")...))
	require.NoError(t, err, "--force should allow overwriting an existing --out path")

	data, err := io.ReadAll(mustOpen(t, outPath))
	require.NoError(t, err)
	// The replacement must be a valid bundle, NOT prefixed by the
	// stale bytes (which would happen if we used O_APPEND or forgot
	// O_TRUNC and the new content was shorter than the old).
	assert.False(t, bytes.HasPrefix(data, []byte("stale-content")),
		"--force must truncate, not append — stale bytes survived the rewrite")
	entries := readZip(t, data)
	assert.Contains(t, string(entries["base.pxf"]), "db_password",
		"forced rewrite should contain the freshly-built narrowed layer")
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

// TestBundleBuildCmd_WiresKeyNameResolver pins the one production line
// the module split added: bundle.go handing sops.RecipientsFromKeyName
// to BuildOptions.RecipientsForKeyName. Nothing else in this module
// exercises it — every other bundle test builds a plaintext chain, and
// the bundler's own tests supply a fake — so deleting the wiring left
// the whole suite green while `chameleon bundle build` on any chain
// spec declaring key_name started failing outright.
//
// The assertion is where the build gets to. planLayers resolves
// recipients before any layer is read, so with the hook wired the run
// reaches processLayer and stops on the missing decrypter; with it
// unwired it never gets that far and dies in unionRecipients instead.
// No crypto needed either way.
func TestBundleBuildCmd_WiresKeyNameResolver(t *testing.T) {
	dir := bundleFixtures(t)
	noUserConfig(t)
	writeAgeKeystore(t, map[string]string{"prod-key": bundleTestIdentity})

	// Same fixture, but the env names a key and the layer is encrypted.
	writeFixture(t, dir, map[string]string{
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" key_name = "prod-key" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true encrypted = true }]
`,
	})

	err := bundleCmd(&bytes.Buffer{}, append([]string{"build"}, bundleArgs(dir)...))
	require.Error(t, err, "an encrypted layer with --decrypter=none must fail")
	assert.Contains(t, err.Error(), "no Decrypter was provided",
		"key_name must have resolved through the real key store before the pipeline reached the layer")
	assert.NotContains(t, err.Error(), "RecipientsForKeyName is nil",
		"bundle.go must pass sops.RecipientsFromKeyName to the bundler")
}

// TestBundleBuildCmd_KeyNameLookupFailureSurfaces is the other half:
// the CLI must not swallow a keystore miss into a generic bundle
// failure — the operator needs the name that could not be resolved.
func TestBundleBuildCmd_KeyNameLookupFailureSurfaces(t *testing.T) {
	dir := bundleFixtures(t)
	noUserConfig(t)
	writeAgeKeystore(t, map[string]string{"some-other-key": bundleTestIdentity})

	writeFixture(t, dir, map[string]string{
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" key_name = "prod-key" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true encrypted = true }]
`,
	})

	err := bundleCmd(&bytes.Buffer{}, append([]string{"build"}, bundleArgs(dir)...))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prod-key", "the unresolvable key name belongs in the error")
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

// bundleTestIdentity is a throwaway age secret key. Fixed rather than
// generated so the fixture stays deterministic; it guards nothing.
const bundleTestIdentity = "AGE-SECRET-KEY-1G0Q5K9TV4REQ3ZSQRMTMG8NSWQGYT0T7TZ33RAZEE0GZYVZN0APSU24RK7"

// noUserConfig points the CLI's user-config lookup at a path that does
// not exist. Without it these tests inherit whatever ~/.chameleon the
// developer happens to have, and an app entry setting `decrypter =
// "sops"` silently overrides the --decrypter default (see
// applyAppDefaults, which treats "none" as unset).
func noUserConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CHAMELEON_USER_CONFIG", filepath.Join(t.TempDir(), "no-such-config"))
}

// writeAgeKeystore points SOPS_AGE_KEY_FILE at a temp PXF keystore
// carrying the named identities — the store sops.RecipientsFromKeyName
// reads. Mirrors sops/keyname_test.go's helper.
func writeAgeKeystore(t *testing.T, keys map[string]string) {
	t.Helper()
	data, err := pxf.Marshal(&sopskeypb.AgeKeyFile{Keys: keys})
	require.NoError(t, err, "marshal keystore")
	path := filepath.Join(t.TempDir(), "keys.pxf")
	require.NoError(t, os.WriteFile(path, data, 0o600), "write keystore")
	t.Setenv("SOPS_AGE_KEY_FILE", path)
}

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
