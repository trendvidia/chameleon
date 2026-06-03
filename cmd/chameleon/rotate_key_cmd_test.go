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

// CLI-entry tests for rotateKeyCmd. The existing rotate_key_test.go
// pins planEncryptedLayers, rotateOne, and resolveChainRecipients
// well, but rotateKeyCmd itself (the outer dispatch + chain.LoadSpec
// + summary emission + error reporting) was only at 25.7%.
//
// These tests author a chain.pxf as PXF text on disk and invoke
// rotateKeyCmd through its real entry point, asserting on stdout
// summary lines + file mutations + exit-style return values.

// writeChainPXF writes a chain.pxf at the named path with the given
// body. Helper so each test scopes its own chain spec.
func writeChainPXF(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestRotateKeyCmd_HappyPath_RewritesLayerAndSummarizes(t *testing.T) {
	writeRotateKeyFile(t)
	dir := t.TempDir()

	// Encrypted layer at secrets/prod.pxf, originally encrypted to
	// rotOldRecipient. After rotation the chain spec lists key_name
	// = "new-key", so the file should be re-encrypted to that key.
	plain := []byte(`db_password = { value = "hunter2" }` + "\n")
	ciph, err := encryptForTest(t, plain, rotOldRecipient)
	require.NoError(t, err)
	full := filepath.Join(dir, "secrets/prod.pxf")
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, ciph, 0o644))

	chainPath := filepath.Join(dir, "chain.pxf")
	writeChainPXF(t, chainPath, `version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true required = true }]
`)

	var out bytes.Buffer
	err = rotateKeyCmd(&out, []string{"--chain=" + chainPath, "--layers=" + dir})
	require.NoError(t, err)

	assert.Contains(t, out.String(), "ROTATED",
		"successful rotation should print a ROTATED line")
	assert.Contains(t, out.String(), "summary: rotated 1 layer(s); skipped 0",
		"summary line should reflect the one rotated layer")

	rewritten, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.NotEqual(t, string(ciph), string(rewritten),
		"file must differ from original after rotation")

	plainAfter, err := decryptForTest(t, rewritten)
	require.NoError(t, err)
	assert.Contains(t, string(plainAfter), "hunter2",
		"rotated ciphertext must round-trip to the original plaintext")
}

func TestRotateKeyCmd_DryRun_PrintsButDoesNotWrite(t *testing.T) {
	writeRotateKeyFile(t)
	dir := t.TempDir()

	original, err := encryptForTest(t, []byte("k = \"v\"\n"), rotOldRecipient)
	require.NoError(t, err)
	full := filepath.Join(dir, "secrets/prod.pxf")
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, original, 0o644))

	chainPath := filepath.Join(dir, "chain.pxf")
	writeChainPXF(t, chainPath, `version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true required = true }]
`)

	var out bytes.Buffer
	err = rotateKeyCmd(&out, []string{"--chain=" + chainPath, "--layers=" + dir, "--dry-run"})
	require.NoError(t, err)

	assert.Contains(t, out.String(), "WOULD-ROTATE",
		"dry-run should announce the would-rotate intent")
	assert.Contains(t, out.String(), "summary: would rotate 1 layer(s); skipped 0",
		"dry-run summary must use 'would rotate' verb, not 'rotated'")

	after, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after),
		"dry-run must not mutate the file")
}

func TestRotateKeyCmd_InvalidChainPath_ReturnsError(t *testing.T) {
	var out bytes.Buffer
	err := rotateKeyCmd(&out, []string{"--chain=/nonexistent/chain.pxf"})
	require.Error(t, err, "missing chain file must surface as error")
	// The chain loader wraps with "chain: read ...: ..."; assert
	// the load-bearing piece so a different chain.LoadSpec error
	// wrapping doesn't quietly mask the file-not-found case.
	assert.Contains(t, err.Error(), "chain",
		"error should identify the chain loader as the failing stage")
}

func TestRotateKeyCmd_HelpFlag_ReturnsNilWithoutError(t *testing.T) {
	// `--help` triggers flag.ErrHelp inside fs.Parse; rotateKeyCmd
	// is documented to swallow that and return nil so the CLI's
	// main() exits 0. A regression here would surface as a non-zero
	// exit for the universal `--help` UX.
	var out bytes.Buffer
	err := rotateKeyCmd(&out, []string{"--help"})
	require.NoError(t, err, "--help must return nil")
}

func TestRotateKeyCmd_CtxTemplateInLayer_RejectedBeforeAnyWrite(t *testing.T) {
	// A chain spec whose layer path references ${ctx.*} is invalid
	// for rotation (no caller context at build time). rotateKeyCmd
	// must fail before touching any file on disk.
	writeRotateKeyFile(t)
	dir := t.TempDir()
	full := filepath.Join(dir, "region/us.pxf")
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	original, err := encryptForTest(t, []byte("k = \"v\"\n"), rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(full, original, 0o644))

	chainPath := filepath.Join(dir, "chain.pxf")
	writeChainPXF(t, chainPath, `version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "regional" path = "region/${ctx.region}.pxf" for_each = ONCE encrypted = true }]
`)

	var out bytes.Buffer
	err = rotateKeyCmd(&out, []string{"--chain=" + chainPath, "--layers=" + dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${ctx.",
		"error should name the rejected template form")

	// File must be untouched.
	after, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after),
		"ctx-template rejection must precede any disk write")
}

func TestRotateKeyCmd_MixedRotateAndSkip_SummaryCountsAccurate(t *testing.T) {
	// Two encrypted layers in the chain spec:
	//   - secrets/prod.pxf (PER_ENV) — rotates: prod has key_name
	//   - shared.pxf (ONCE) — skipped: sandbox has no key_name, so
	//     rotating would lock sandbox out
	// Verifies the summary's "rotated N; skipped M" arithmetic.
	writeRotateKeyFile(t)
	dir := t.TempDir()

	// PER_ENV expands to one path per env, so we need both
	// secrets/prod.pxf AND secrets/sandbox.pxf on disk. shared.pxf
	// is the ONCE layer reachable from both envs.
	for _, p := range []string{"secrets/prod.pxf", "secrets/sandbox.pxf", "shared.pxf"} {
		full := filepath.Join(dir, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		ciph, err := encryptForTest(t, []byte("k = \"v\"\n"), rotOldRecipient)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(full, ciph, 0o644))
	}

	chainPath := filepath.Join(dir, "chain.pxf")
	writeChainPXF(t, chainPath, `version = "1.0.0"
environments = [
  { name = "prod" key_name = "new-key" }
  { name = "sandbox" }
]
layers = [
  { id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true required = true }
  { id = "shared" path = "shared.pxf" for_each = ONCE encrypted = true required = true }
]
`)

	var out bytes.Buffer
	err := rotateKeyCmd(&out, []string{"--chain=" + chainPath, "--layers=" + dir})
	require.NoError(t, err)

	// One rotated (secrets/prod.pxf — prod has key_name).
	// Two skipped:
	//   - secrets/sandbox.pxf (sandbox has no key_name)
	//   - shared.pxf (reachable from sandbox, which has no key_name)
	// The summary line must reflect rotated + skipped separately.
	assert.Contains(t, out.String(), "summary: rotated 1 layer(s); skipped 2",
		"mixed run must count rotated + skipped separately")
	assert.True(t, strings.Contains(out.String(), "ROTATED"), "should have a ROTATED line")
	assert.True(t, strings.Contains(out.String(), "SKIP"), "should have a SKIP line")
}

func TestRotateKeyCmd_PlaintextLayerIgnored(t *testing.T) {
	// A non-encrypted layer in the chain spec should be left alone
	// — rotation is a sops concern. Summary should show 0 rotated.
	writeRotateKeyFile(t)
	dir := t.TempDir()

	full := filepath.Join(dir, "public.pxf")
	original := []byte(`hello = "world"` + "\n")
	require.NoError(t, os.WriteFile(full, original, 0o644))

	chainPath := filepath.Join(dir, "chain.pxf")
	writeChainPXF(t, chainPath, `version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "public" path = "public.pxf" for_each = ONCE encrypted = false }]
`)

	var out bytes.Buffer
	err := rotateKeyCmd(&out, []string{"--chain=" + chainPath, "--layers=" + dir})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "summary: rotated 0 layer(s); skipped 0",
		"plaintext-only chain must rotate nothing")

	after, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after),
		"plaintext layer must not be touched")
}

func TestRotateKeyCmd_DecryptFailurePropagates(t *testing.T) {
	// Layer file exists but its ciphertext is junk (or encrypted to
	// a key the keystore doesn't hold). rotateOne returns an error;
	// rotateKeyCmd writes the ERROR line and returns the wrapped
	// error. No further layers should be touched.
	writeRotateKeyFile(t)
	dir := t.TempDir()

	full := filepath.Join(dir, "secrets/prod.pxf")
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte("not actual sops ciphertext"), 0o644))

	chainPath := filepath.Join(dir, "chain.pxf")
	writeChainPXF(t, chainPath, `version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true required = true }]
`)

	var out bytes.Buffer
	err := rotateKeyCmd(&out, []string{"--chain=" + chainPath, "--layers=" + dir})
	require.Error(t, err, "decrypt failure must surface as error from the cmd")
	assert.Contains(t, err.Error(), "decrypt",
		"error should identify the failing operation")
}
