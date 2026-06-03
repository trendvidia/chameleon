// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// rotate-key binary integration. PR #110 unit-tested rotateKeyCmd
// to 97% via in-process invocation; this file rounds out the
// destructive operation with a real subprocess run against on-disk
// ciphertext.
//
// The load-bearing property:
//
//     after `chameleon rotate-key`, the file MUST decrypt with the
//     new key alone AND MUST NOT decrypt with the old key alone
//
// This is what distinguishes "rotation actually happened" from "we
// re-encrypted to the same recipient set" — the unit tests can't
// confirm the second half without a separate decrypt step under a
// pared-down keystore.
//
// Gated behind `-tags integration`. Reuses encryptForTest /
// runBinary / rotation identities from the same package.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/sops/v4/age/keypb"
	"github.com/trendvidia/protowire-go/encoding/pxf"

	chsops "github.com/trendvidia/chameleon/sops"
)

// writeMultiKeyKeystore writes the named keystore file with both
// rotation identities under "old-key" and "new-key" labels, then
// returns the path. The caller wires SOPS_AGE_KEY_FILE separately.
func writeMultiKeyKeystore(t *testing.T, dir string) string {
	t.Helper()
	file := &keypb.AgeKeyFile{
		Keys: map[string]string{
			"old-key": rotOldIdentity,
			"new-key": rotNewIdentity,
		},
	}
	data, err := pxf.Marshal(file)
	require.NoError(t, err)
	path := filepath.Join(dir, "keys.pxf")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func TestRotateKeyBinary_RewritesLayerToNewKeyOnly(t *testing.T) {
	// Full destructive-op round-trip through the binary:
	//   1. Encrypt secrets/prod.pxf to old-key
	//   2. Multi-key keystore on disk holds old + new
	//   3. chain spec says env "prod" key_name = "new-key"
	//   4. Run `chameleon rotate-key`
	//   5. Assert the file was rewritten and is byte-different
	//   6. Decrypt with ONLY new-key in env → succeeds
	//   7. Decrypt with ONLY old-key in env → FAILS (load-bearing)
	//
	// Step 7 is the canary: if the binary silently re-encrypted
	// back to the original recipient set, step 7 would succeed and
	// the rotation would be a no-op. We need both halves.
	dir := t.TempDir()

	// Layer fixture: PER_ENV layer for prod, encrypted to old-key.
	layerPath := filepath.Join(dir, "secrets/prod.pxf")
	require.NoError(t, os.MkdirAll(filepath.Dir(layerPath), 0o755))
	plaintext := []byte(`db_password = "hunter2"
`)
	originalCT, err := encryptForTest(t, plaintext, rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layerPath, originalCT, 0o644))

	// Multi-key keystore so the binary can READ the file (old-key)
	// while having the new-key recipient available for the rewrite.
	keystorePath := writeMultiKeyKeystore(t, dir)
	t.Setenv("SOPS_AGE_KEY_FILE", keystorePath)
	// Clear SOPS_AGE_KEY so it doesn't shadow the keystore.
	t.Setenv("SOPS_AGE_KEY", "")

	// Chain spec: env prod -> key_name = "new-key" → bundler
	// resolves recipients to ONLY new-key's recipient.
	chainPath := filepath.Join(dir, "chain.pxf")
	require.NoError(t, os.WriteFile(chainPath, []byte(`version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true required = true }]
`), 0o644))

	// Run the destructive operation through the binary.
	stdout, stderr, exit := runBinary(t,
		"rotate-key",
		"--chain="+chainPath,
		"--layers="+dir,
	)
	require.Equal(t, 0, exit, "rotate-key exit; stderr: %s", stderr)
	assert.Contains(t, stdout, "ROTATED",
		"rotate-key should report ROTATED for the rewritten layer")
	assert.Contains(t, stdout, "summary: rotated 1 layer(s); skipped 0",
		"summary line should reflect one rotation")

	// File on disk is byte-different from the original (the DEK
	// is fresh per encrypt operation, so even a same-recipient
	// re-encrypt would produce different bytes — but combined with
	// the cross-decrypt checks below, this rules out "no write
	// happened" trivially).
	rewritten, err := os.ReadFile(layerPath)
	require.NoError(t, err)
	require.NotEqual(t, string(originalCT), string(rewritten),
		"file must have been rewritten (recipient set + DEK changed)")

	// Canary 1: NEW key alone must decrypt the rewritten file.
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	t.Setenv("SOPS_AGE_KEY", rotNewIdentity)
	buf, err := chsops.Default().Decrypt(
		context.Background(), rewritten, chsops.FormatProtowire,
	)
	require.NoError(t, err, "new-key alone must decrypt the rotated layer")
	defer buf.Destroy()
	assert.Equal(t, string(plaintext), string(buf.Bytes()),
		"rotated ciphertext must round-trip to original plaintext under new-key")

	// Canary 2: OLD key alone must NOT decrypt. This is the
	// load-bearing rotation invariant — if it succeeded, the
	// recipient set wasn't actually swapped.
	t.Setenv("SOPS_AGE_KEY", rotOldIdentity)
	_, err = chsops.Default().Decrypt(
		context.Background(), rewritten, chsops.FormatProtowire,
	)
	require.Error(t, err,
		"old-key MUST NOT decrypt the rotated layer; if it can, rotation didn't actually swap recipients")
	// sops's error message varies by version; assert the broad
	// class rather than pinning specific wording.
	assert.True(t,
		strings.Contains(err.Error(), "decrypt") ||
			strings.Contains(err.Error(), "no key could decrypt") ||
			strings.Contains(err.Error(), "MAC"),
		"error should identify the decrypt failure, got: %v", err)
}

func TestRotateKeyBinary_DryRunDoesNotMutateFile(t *testing.T) {
	// Through the binary boundary: --dry-run prints WOULD-ROTATE
	// and leaves the file on disk byte-identical. Pins the
	// destructive-by-default vs --dry-run distinction at the
	// subprocess level.
	dir := t.TempDir()
	layerPath := filepath.Join(dir, "secrets/prod.pxf")
	require.NoError(t, os.MkdirAll(filepath.Dir(layerPath), 0o755))

	original, err := encryptForTest(t, []byte(`x = "y"`+"\n"), rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layerPath, original, 0o644))

	keystorePath := writeMultiKeyKeystore(t, dir)
	t.Setenv("SOPS_AGE_KEY_FILE", keystorePath)
	t.Setenv("SOPS_AGE_KEY", "")

	chainPath := filepath.Join(dir, "chain.pxf")
	require.NoError(t, os.WriteFile(chainPath, []byte(`version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true required = true }]
`), 0o644))

	stdout, stderr, exit := runBinary(t,
		"rotate-key",
		"--chain="+chainPath,
		"--layers="+dir,
		"--dry-run",
	)
	require.Equal(t, 0, exit, "rotate-key --dry-run exit; stderr: %s", stderr)
	assert.Contains(t, stdout, "WOULD-ROTATE",
		"dry-run should announce intent")
	assert.Contains(t, stdout, "summary: would rotate 1 layer(s); skipped 0",
		"dry-run summary must use 'would rotate' verb")

	after, err := os.ReadFile(layerPath)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after),
		"dry-run must NOT mutate the file on disk")
}

func TestRotateKeyBinary_RefusesWhenAnEnvLacksKeyName(t *testing.T) {
	// Shared layer reachable from two envs, only one of which has
	// key_name. rotate-key must SKIP the file rather than re-encrypt
	// it to a subset of recipients (which would lock the no-key env
	// out). Verifies the refusal contract holds through the binary.
	dir := t.TempDir()
	layerPath := filepath.Join(dir, "shared.pxf")
	original, err := encryptForTest(t, []byte(`shared = "value"`+"\n"), rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layerPath, original, 0o644))

	keystorePath := writeMultiKeyKeystore(t, dir)
	t.Setenv("SOPS_AGE_KEY_FILE", keystorePath)
	t.Setenv("SOPS_AGE_KEY", "")

	chainPath := filepath.Join(dir, "chain.pxf")
	require.NoError(t, os.WriteFile(chainPath, []byte(`version = "1.0.0"
environments = [
  { name = "prod" key_name = "new-key" }
  { name = "sandbox" }
]
layers = [{ id = "shared" path = "shared.pxf" for_each = ONCE encrypted = true required = true }]
`), 0o644))

	stdout, stderr, exit := runBinary(t,
		"rotate-key",
		"--chain="+chainPath,
		"--layers="+dir,
	)
	require.Equal(t, 0, exit, "rotate-key exit; stderr: %s", stderr)
	assert.Contains(t, stdout, "SKIP",
		"shared layer with a no-key env should be skipped")
	assert.Contains(t, stdout, "sandbox",
		"skip message should name the env lacking key_name")
	assert.Contains(t, stdout, "summary: rotated 0 layer(s); skipped 1",
		"summary should count the skip")

	// Critical safety property: the file is UNTOUCHED. A
	// regression that wrote a partial rotation to disk would lose
	// access for the sandbox env.
	after, err := os.ReadFile(layerPath)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after),
		"refused rotation must NOT touch the file on disk")
}
