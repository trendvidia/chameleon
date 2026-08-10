// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/resolver"
)

// CLI subcommand smoke tests. The *Cmd entry points went from 0%
// coverage (TestX flag-parsing tests at most) to end-to-end exercise
// via the io.Writer parameter. Each test writes a minimal fixture
// under t.TempDir(), invokes the Cmd with a bytes.Buffer, and
// asserts against substrings of the output. Goal isn't golden-file
// byte-exact output (the format may evolve); the goal is verifying
// the Cmd plumbing — flag parsing → file I/O → resolve → print —
// works end-to-end and the output contains the load-bearing
// substrings consumers actually grep for.

// minimalSchema is a single-message .proto file used by the explain
// smoke. Kept inline so each test owns its fixture.
const minimalSchema = `syntax = "proto3";
package smoke.v1;
message Cfg {
  string base_url = 1;
  int32 max_replicas = 2;
}
`

// writeFixture writes path/content pairs under dir and returns dir
// for convenience. t.TempDir is auto-cleaned.
func writeFixture(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
}

// --- explain ---

// TestExplainCmd_Smoke walks the full explain pipeline: load schema,
// parse chain, resolve, print trace. Verifies stdout carries the
// per-field provenance shape with layer attribution and the
// resolved value.
func TestExplainCmd_Smoke(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"app.proto": minimalSchema,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "x"
max_replicas = 3
`,
	})

	var out bytes.Buffer
	err := explainCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
	})
	require.NoError(t, err)
	got := out.String()

	assert.Contains(t, got, "base_url", "field name in output")
	assert.Contains(t, got, `"x"`, "resolved value in output")
	assert.Contains(t, got, "base", "layer id in trace attribution")
	assert.Contains(t, got, "max_replicas", "second field should also appear in printAll")
}

// TestExplainCmd_SingleField — when --field is set, only that field's
// trace is printed.
func TestExplainCmd_SingleField(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"app.proto": minimalSchema,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "x"
max_replicas = 3
`,
	})

	var out bytes.Buffer
	require.NoError(t, explainCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--field=base_url",
	}))
	got := out.String()
	assert.Contains(t, got, "base_url")
	assert.NotContains(t, got, "max_replicas",
		"--field=base_url should print only base_url, not other fields")
}

// TestExplainCmd_MissingRequiredFlag — missing --schema must surface
// a usage error, not panic.
//
// Isolates from the developer's ~/.chameleon: with no override,
// applyAppDefaults would silently fill in --schema from the user
// config and the required-flag check would never fire (the test
// would then fail at schema-compile time with a path that exists
// on the author's laptop but not in CI or other developers').
func TestExplainCmd_MissingRequiredFlag(t *testing.T) {
	t.Setenv("CHAMELEON_USER_CONFIG", filepath.Join(t.TempDir(), "nonexistent"))
	var out bytes.Buffer
	err := explainCmd(nil, &out, []string{"--chain=/dev/null"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// --- get ---

// TestGetCmd_Smoke walks the full get pipeline: load schema, parse
// chain, resolve, marshal as PXF. Verifies stdout is a parseable
// PXF document starting with the @type directive and carrying the
// resolved scalars by name.
func TestGetCmd_Smoke(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"app.proto": minimalSchema,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "x"
max_replicas = 3
`,
	})

	var out bytes.Buffer
	err := getCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
	})
	require.NoError(t, err)
	got := out.String()

	assert.Contains(t, got, "@type smoke.v1.Cfg", "emit-type=true default should prefix with @type")
	assert.Contains(t, got, "base_url", "field name in output")
	assert.Contains(t, got, `"x"`, "scalar value preserved")
	assert.Contains(t, got, "max_replicas", "second field preserved")
}

// TestGetCmd_RedactsSecrets — a schema with a pxf.Secret-typed
// field should marshal as `value = "<redacted>"`, distinguishing
// "deliberately suppressed" from an empty string. Plain scalars
// (base_url) are untouched. Mirrors the website/config bug report:
// before this change the output was `value = ""` and a reader
// couldn't tell if the field was absent, set to empty, or
// deliberately redacted.
func TestGetCmd_RedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		// pxf.Secret well-known type — vendored inline so the test
		// is self-contained and doesn't rely on protowire-go's
		// import path being on disk.
		"pxf/secret.proto": `syntax = "proto3";
package pxf;
message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`,
		// Consumer schema mixes a plain scalar with a Secret-typed
		// field so the test asserts both branches: redacted for
		// the secret, untouched for the scalar.
		"app.proto": `syntax = "proto3";
package smoke.v1;
import "pxf/secret.proto";
message Cfg {
  string base_url = 1;
  pxf.Secret api_key = 2;
}
`,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "http://x"
api_key = "live-plaintext-do-not-leak"
`,
	})

	var out bytes.Buffer
	err := getCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
	})
	require.NoError(t, err)
	got := out.String()

	// pxf.Marshal renders Secret-typed fields with scalar shorthand
	// when only `value` is populated — `api_key = "<x>"` rather than
	// the block form `api_key { value = "<x>" }`. The redaction
	// sentinel rides through that codec choice unchanged.
	assert.Contains(t, got, `api_key = "<redacted>"`,
		"secret field must marshal with the redaction sentinel")
	assert.NotContains(t, got, "live-plaintext-do-not-leak",
		"plaintext must not appear in the output — invariant load-bearing for the whole pipeline")
	assert.Contains(t, got, `base_url = "http://x"`,
		"plain scalars must be untouched by the redaction walker")
}

// TestGetCmd_NoEmitType — --emit-type=false strips the @type
// directive (for callers concatenating output or supplying their
// own type binding).
func TestGetCmd_NoEmitType(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"app.proto": minimalSchema,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "x"
`,
	})

	var out bytes.Buffer
	require.NoError(t, getCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--emit-type=false",
	}))
	got := out.String()
	assert.NotContains(t, got, "@type", "--emit-type=false should strip the directive")
	assert.Contains(t, got, "base_url", "the message body itself is still emitted")
}

// TestGetCmd_MissingRequiredFlag — missing --schema must surface a
// usage error, not panic. CHAMELEON_USER_CONFIG override pins this
// to a non-existent path so a developer's ~/.chameleon app entry
// can't silently fill in --schema and skip the required check —
// see the matching note on TestExplainCmd_MissingRequiredFlag.
func TestGetCmd_MissingRequiredFlag(t *testing.T) {
	t.Setenv("CHAMELEON_USER_CONFIG", filepath.Join(t.TempDir(), "nonexistent"))
	var out bytes.Buffer
	err := getCmd(nil, &out, []string{"--chain=/dev/null"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// TestGetCmd_AppDefaults — end-to-end exercise of the userconfig
// merge path: write a $HOME/.chameleon entry that points at a
// fixture project, run getCmd with NO --schema/--message/--chain
// flags, and assert it still resolves. Proves the wiring (flag
// parse → loadUserConfig → selectApp → applyAppDefaults → rest of
// the get pipeline) ties together correctly, not just the helper
// in isolation.
func TestGetCmd_AppDefaults(t *testing.T) {
	projDir := t.TempDir()
	writeFixture(t, projDir, map[string]string{
		"app.proto": minimalSchema,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"layers/base.pxf": `base_url = "from-app-defaults"
max_replicas = 7
`,
	})

	cfgPath := filepath.Join(t.TempDir(), ".chameleon")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`@type chameleon.v1.UserConfig

default_app = "smoke"

apps = {
  "smoke": {
    root    = "`+projDir+`"
    schema  = "app.proto"
    message = "smoke.v1.Cfg"
    chain   = "chain.pxf"
    layers  = "layers"
    env     = "prod"
  }
}
`), 0o644))
	t.Setenv("CHAMELEON_USER_CONFIG", cfgPath)

	var out bytes.Buffer
	// Only --emit-type=false to keep the assertion simple; no
	// schema/message/chain/layers/env flags at all.
	require.NoError(t, getCmd(nil, &out, []string{"--emit-type=false"}))
	got := out.String()
	assert.Contains(t, got, `base_url = "from-app-defaults"`,
		"app defaults from $HOME/.chameleon should drive resolution")
	assert.Contains(t, got, "max_replicas = 7")
}

// --- reveal ---

// revealFixture lays out a self-contained schema with a plain
// scalar + a pxf.Secret field, a chain.pxf, and a base.pxf layer
// supplying both. Shared by the reveal smoke tests because they
// all need the same descriptor + resolution surface.
func revealFixture(t *testing.T) string {
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
}
`,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "http://x"
api_key = "live-plaintext-from-layer"
`,
	})
	return dir
}

// TestRevealCmd_Smoke writes the plaintext of a Secret-typed field
// to the supplied io.Writer with no trailing newline — the contract
// load-bearing for $(...) command substitution.
func TestRevealCmd_Smoke(t *testing.T) {
	dir := revealFixture(t)

	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--field=api_key",
	})
	require.NoError(t, err)
	assert.Equal(t, "live-plaintext-from-layer", out.String(),
		"reveal must emit exact plaintext bytes, no trailing newline, no metadata")
}

// TestRevealCmd_NonSecretFieldRefused — plain scalars route through
// chameleon get | pxf query. reveal refuses them with a clear
// diagnostic.
func TestRevealCmd_NonSecretFieldRefused(t *testing.T) {
	dir := revealFixture(t)

	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--field=base_url",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a pxf.Secret field",
		"non-Secret target should produce the type-mismatch error")
	assert.Empty(t, out.String(),
		"reveal must not emit any bytes when the field check fails")
}

// TestRevealCmd_UnknownFieldRefused — a typo'd dotted path errors
// before the resolver runs, with a message naming the bad segment.
func TestRevealCmd_UnknownFieldRefused(t *testing.T) {
	dir := revealFixture(t)

	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--field=nonexistent",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no field "nonexistent"`,
		"typo path should name the bad segment")
}

// TestRevealCmd_MissingRequiredFlag — --field is required alongside
// --schema/--message/--chain. Missing any of them surfaces the
// same usage error.
func TestRevealCmd_MissingRequiredFlag(t *testing.T) {
	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{"--schema=/dev/null", "--chain=/dev/null", "--message=x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

// --- verify ---

// TestVerifyCmd_OK — a layer whose @header.content_sha256 matches
// its body hash should produce an OK status line.
func TestVerifyCmd_OK(t *testing.T) {
	dir := t.TempDir()
	body := "\nbase_url = \"x\"\n"
	hash := sha256hex([]byte(body))
	writeFixture(t, dir, map[string]string{
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `@header chameleon.v1.LayerHeader { id = "base" content_sha256 = "` + hash + `" }` + body,
	})

	var out bytes.Buffer
	require.NoError(t, verifyCmd(&out, []string{
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
	}))
	assert.Contains(t, out.String(), "OK",
		"matching content_sha256 should produce an OK line")
}

// TestVerifyCmd_Mismatch — a layer with the wrong content_sha256
// should surface FAIL + an error return.
func TestVerifyCmd_Mismatch(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `@header chameleon.v1.LayerHeader { id = "base" content_sha256 = "deadbeef" }
base_url = "x"
`,
	})

	var out bytes.Buffer
	err := verifyCmd(&out, []string{
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verification")
	assert.Contains(t, out.String(), "FAIL")
}

// TestVerifyCmd_StrictMissingHash — under --strict, a layer without
// @header.content_sha256 should be an error. Without --strict, it's
// a SKIP.
func TestVerifyCmd_StrictMissingHash(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": `base_url = "x"
`, // no @header
	})

	// Without --strict: skip without error.
	var out bytes.Buffer
	require.NoError(t, verifyCmd(&out, []string{
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
	}))
	assert.Contains(t, out.String(), "SKIP")

	// With --strict: error.
	var outStrict bytes.Buffer
	err := verifyCmd(&outStrict, []string{
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--strict",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

// --- migrate ---

// TestMigrateCmd_DryRunAddField — exercise the migrate Cmd through
// to a dry-run add-field on a plaintext layer; assert the
// WOULD-CHANGE status line + summary on the output, and the layer
// file on disk is unchanged.
func TestMigrateCmd_DryRunAddField(t *testing.T) {
	dir := t.TempDir()
	original := "base_url = \"x\"\n"
	writeFixture(t, dir, map[string]string{
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": original,
	})

	var out bytes.Buffer
	require.NoError(t, migrateCmd(&out, []string{
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--transform=add-field",
		"--path=feature_flag",
		"--bool=true",
		"--dry-run",
	}))

	got := out.String()
	assert.Contains(t, got, "WOULD-CHANGE", "dry-run should print WOULD-CHANGE per modified layer")
	assert.Contains(t, got, "summary: would migrate", "dry-run summary verb")

	// File on disk must be untouched.
	after, err := os.ReadFile(filepath.Join(dir, "base.pxf"))
	require.NoError(t, err)
	assert.Equal(t, original, string(after), "dry-run must not write the layer file")
}

// TestMigrateCmd_AddFieldWritesBack — without --dry-run, the
// transform actually writes back. Verifies CHANGED status + the
// layer file is updated with the new field.
func TestMigrateCmd_AddFieldWritesBack(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`,
		"base.pxf": "base_url = \"x\"\n",
	})

	var out bytes.Buffer
	require.NoError(t, migrateCmd(&out, []string{
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--transform=add-field",
		"--path=feature_flag",
		"--bool=true",
	}))

	got := out.String()
	assert.Contains(t, got, "CHANGED")
	assert.Contains(t, got, "summary: migrated 1 layer(s)")

	after, err := os.ReadFile(filepath.Join(dir, "base.pxf"))
	require.NoError(t, err)
	assert.Contains(t, string(after), "feature_flag = true")
}

// --- hash already covered by TestHashCmd in verify_test.go, with
// the io.Writer signature update applied as part of this refactor.

// --- main usage path ---

// TestUsage exercises the standalone usage function; no Cmd-specific
// state, just verifies the help text reaches the writer.
func TestUsage(t *testing.T) {
	// Capture os.Stdout via a pipe since usage() takes *os.File. The
	// CLI's main entry is the only path where exit codes matter; the
	// usage subroutine is called from main, not the Cmd functions.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	usage(w)
	require.NoError(t, w.Close())

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	got := string(buf[:n])

	for _, sub := range []string{"explain", "get", "reveal", "verify", "migrate", "hash", "lint", "version", "completion"} {
		assert.Contains(t, got, sub,
			"usage text should mention every subcommand; missing %q", sub)
	}
	assert.NotContains(t, got, "TODO",
		"usage text shouldn't carry leftover placeholders")
	_ = strings.Repeat // keep strings import
}

// --- --decrypter=none ---

// encryptedFixture is a chain whose one layer is marked
// `encrypted = true`, with garbage where the ciphertext should be.
// Nothing here can be decrypted; the point is which error comes out.
func encryptedFixture(t *testing.T) string {
	t.Helper()
	// The user config can set `decrypter = "sops"` for the default
	// app, and applyAppDefaults adopts it when --decrypter is absent —
	// which would decide this test on the developer's machine config
	// rather than on the code. Point the loader at a path that does
	// not exist. CHAMELEON_USER_CONFIG rather than HOME because
	// userConfigPath() checks the env var *first*: overriding HOME
	// alone leaves an exported CHAMELEON_USER_CONFIG in charge, which
	// is the same environment dependency one level down.
	t.Setenv("CHAMELEON_USER_CONFIG", filepath.Join(t.TempDir(), "nonexistent"))
	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		// A pxf.Secret field so `reveal` gets past its is-this-a-secret
		// check and actually reaches the resolve — the case that would
		// otherwise print decrypted plaintext to stdout.
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
}
`,
		"chain.pxf": `version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`,
		"secret.pxf": "not-actually-ciphertext",
	})
	return dir
}

// TestDecrypterNone_RefusesEncryptedLayer pins the flag's documented
// contract: "none" errors if any encrypted layer is hit.
//
// It regressed silently when the crypt registry landed
// (trendvidia/chameleon#130). "none" produced a nil Decrypter, the
// caller passed no option, and "no decrypter configured" used to be
// the same thing as "do not decrypt" — but this binary links
// chameleon/sops for bundle/migrate/rotate-key, so sops's init
// registers the backend and the resolver's new fallback auto-selects
// it. `--decrypter=none` reached for ambient KMS/age credentials the
// operator had explicitly declined, and for `reveal` that means
// plaintext on stdout.
//
// The tell is which error surfaces: ErrNoDecrypter means the layer
// was refused, a sops unmarshal error means it was attempted.
func TestDecrypterNone_RefusesEncryptedLayer(t *testing.T) {
	require.NotEmpty(t, crypt.Registered(),
		"this binary must link a backend, or this test cannot regress")

	dir := encryptedFixture(t)
	base := []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--decrypter=none",
	}

	for _, tc := range []struct {
		name string
		run  func(w *bytes.Buffer, args []string) error
		args []string
	}{
		{"explain", func(w *bytes.Buffer, a []string) error { return explainCmd(nil, w, a) }, base},
		{"get", func(w *bytes.Buffer, a []string) error { return getCmd(nil, w, a) }, base},
		{
			"reveal", func(w *bytes.Buffer, a []string) error { return revealCmd(nil, w, a) },
			append(append([]string{}, base...), "--field=api_key"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := tc.run(&out, tc.args)
			require.Error(t, err, "an encrypted layer under --decrypter=none must fail")
			assert.ErrorIs(t, err, resolver.ErrNoDecrypter,
				"want the layer refused; got %v", err)
			assert.NotContains(t, err.Error(), "sops:",
				"sops must not have been invoked at all: %v", err)
			assert.Empty(t, out.String(), "and nothing may reach the writer")
		})
	}
}

// TestDecrypterNone_IsTheDefault: the same refusal with the flag
// omitted entirely, since "none" is the flag's default value.
func TestDecrypterNone_IsTheDefault(t *testing.T) {
	dir := encryptedFixture(t)

	var out bytes.Buffer
	err := explainCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, resolver.ErrNoDecrypter)
}

// TestDecrypterNone_SurvivesUserConfig is the same opt-out with a user
// config that says otherwise.
//
// applyAppDefaults used to read "none" as "unset" — the flag's Go
// default is "none", so the value alone cannot say whether the
// operator typed it — and adopt `decrypter = "sops"` from
// $HOME/.chameleon. That turned the one explicit way to decline
// decryption into a no-op on any machine whose config named a
// decrypter, and for `reveal` the result was plaintext on stdout. The
// flag is now looked up on the FlagSet, so typing it wins.
//
// The `reveal` leg is the one with teeth: it is the subcommand whose
// success path prints the secret.
func TestDecrypterNone_SurvivesUserConfig(t *testing.T) {
	dir := encryptedFixture(t)

	cfgPath := filepath.Join(t.TempDir(), ".chameleon")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`@type chameleon.v1.UserConfig

default_app = "smoke"

apps = {
  "smoke": {
    root      = "`+dir+`"
    decrypter = "sops"
  }
}
`), 0o644))
	// Overrides the fixture's own isolation, on purpose: this test is
	// about a config that *does* name a decrypter.
	t.Setenv("CHAMELEON_USER_CONFIG", cfgPath)

	base := []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--layers=" + dir,
		"--env=prod",
		"--field=api_key",
	}

	var out bytes.Buffer
	err := revealCmd(nil, &out, append(slices.Clone(base), "--decrypter=none"))
	require.Error(t, err, "an explicit --decrypter=none must outrank the user config")
	assert.ErrorIs(t, err, resolver.ErrNoDecrypter,
		"want the layer refused; got %v", err)
	assert.Empty(t, out.String(), "and nothing may reach the writer")

	// The other half of the contract: with the flag absent, the
	// config's decrypter is still adopted. sops then fails on the
	// garbage ciphertext, which is what proves it was reached.
	out.Reset()
	err = revealCmd(nil, &out, slices.Clone(base))
	require.Error(t, err)
	assert.NotErrorIs(t, err, resolver.ErrNoDecrypter,
		"the config's decrypter should have been adopted; got %v", err)
}
