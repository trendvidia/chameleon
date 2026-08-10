// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// rotate-key alternative-path coverage. The headline binary
// integration test (#122) pinned the load-bearing happy + canary
// paths (cross-decrypt invariant, dry-run, missing-key-name
// refusal). This file fills in three production-relevant
// alternatives:
//
//   - Plaintext-only chain (no encrypted layers): rotate-key is a
//     no-op with the right summary line, NOT an error.
//   - Multi-env union recipients (both envs have key_name): a
//     ONCE-shared layer is re-encrypted to the UNION; either env's
//     key alone can decrypt the rewritten file.
//   - Corrupt source ciphertext: error propagates from the binary
//     with a non-zero exit; the file stays unchanged so the
//     operator can investigate.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/crypt"
	chsops "github.com/trendvidia/chameleon/sops"
)

func TestRotateKeyBinary_PlaintextOnlyChain_NoOpSummary(t *testing.T) {
	// A chain whose layers are all `encrypted = false` has nothing
	// to rotate. The binary must exit 0 with "rotated 0 layer(s);
	// skipped 0" so scripts that gate on the exit code don't treat
	// a no-op as a failure, and so an operator running rotate-key
	// in the wrong directory gets clear feedback rather than a
	// destructive surprise.
	dir := t.TempDir()
	// One plaintext layer on disk — the body content is irrelevant;
	// rotate-key shouldn't touch it.
	layerPath := filepath.Join(dir, "public.pxf")
	original := []byte(`hello = "world"` + "\n")
	require.NoError(t, os.WriteFile(layerPath, original, 0o644))

	chainPath := filepath.Join(dir, "chain.pxf")
	require.NoError(t, os.WriteFile(chainPath, []byte(`version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "public" path = "public.pxf" for_each = ONCE encrypted = false }]
`), 0o644))

	stdout, stderr, exit := runBinary(t,
		"rotate-key",
		"--chain="+chainPath,
		"--layers="+dir,
	)
	require.Equal(t, 0, exit, "plaintext-only chain rotate must exit 0; stderr: %s", stderr)
	assert.Contains(t, stdout, "summary: rotated 0 layer(s); skipped 0",
		"summary must clearly say zero — distinguishes a no-op from a refusal")

	// File untouched.
	after, err := os.ReadFile(layerPath)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(after),
		"plaintext layer must not be touched by rotate-key")
}

func TestRotateKeyBinary_MultiEnvUnion_BothKeysDecryptShared(t *testing.T) {
	// Both envs have key_name; a ONCE shared layer is reachable
	// from both. After rotation the layer must be encrypted to the
	// UNION of recipients, so each env's secret key alone decrypts
	// it. Without this property, sharing a layer across envs
	// requires re-encrypting per env (defeating the point of ONCE).
	//
	// Verified by:
	//   1. Encrypt source to old-key
	//   2. Chain spec: prod = "new-key", sandbox = "old-key" so
	//      both keys end up in the rotation target set
	//   3. rotate-key
	//   4. Decrypt rewritten file with ONLY new-key → succeeds
	//   5. Decrypt rewritten file with ONLY old-key → succeeds
	dir := t.TempDir()

	layerPath := filepath.Join(dir, "shared.pxf")
	plaintext := []byte(`shared_value = "union-test"
`)
	originalCT, err := encryptForTest(t, plaintext, rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layerPath, originalCT, 0o644))

	keystorePath := writeMultiKeyKeystore(t, dir)
	t.Setenv("SOPS_AGE_KEY_FILE", keystorePath)
	t.Setenv("SOPS_AGE_KEY", "")

	// prod -> new-key, sandbox -> old-key. ONCE layer is shared
	// → re-encrypted to recipients(new-key) ∪ recipients(old-key).
	chainPath := filepath.Join(dir, "chain.pxf")
	require.NoError(t, os.WriteFile(chainPath, []byte(`version = "1.0.0"
environments = [
  { name = "prod"    key_name = "new-key" }
  { name = "sandbox" key_name = "old-key" }
]
layers = [{ id = "shared" path = "shared.pxf" for_each = ONCE encrypted = true required = true }]
`), 0o644))

	stdout, stderr, exit := runBinary(t,
		"rotate-key",
		"--chain="+chainPath,
		"--layers="+dir,
	)
	require.Equal(t, 0, exit, "rotate-key exit; stderr: %s", stderr)
	assert.Contains(t, stdout, "ROTATED",
		"shared layer must be rotated, not skipped")
	assert.Contains(t, stdout, "summary: rotated 1 layer(s); skipped 0")

	rewritten, err := os.ReadFile(layerPath)
	require.NoError(t, err)

	// chsops.Default().Decrypt WIPES its ciphertext input on
	// success, so the second decrypt below has to receive its
	// own copy — otherwise it sees a zero-filled buffer and
	// surfaces a baffling unmarshal error.
	ciphForNew := append([]byte(nil), rewritten...)
	ciphForOld := append([]byte(nil), rewritten...)

	// Canary 1: new-key alone decrypts.
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	t.Setenv("SOPS_AGE_KEY", rotNewIdentity)
	buf, err := chsops.Default().Decrypt(
		context.Background(), ciphForNew, crypt.FormatProtowire,
	)
	require.NoError(t, err, "new-key alone must decrypt the union-encrypted layer")
	defer buf.Destroy()
	assert.Equal(t, string(plaintext), string(buf.Bytes()))

	// Canary 2: old-key alone decrypts (this is the UNION property —
	// without it, sandbox would have lost read access after the
	// rotation that targeted prod's new-key).
	t.Setenv("SOPS_AGE_KEY", rotOldIdentity)
	buf2, err := chsops.Default().Decrypt(
		context.Background(), ciphForOld, crypt.FormatProtowire,
	)
	require.NoError(t, err, "old-key alone must ALSO decrypt — proves UNION not subset")
	defer buf2.Destroy()
	assert.Equal(t, string(plaintext), string(buf2.Bytes()))
}

func TestRotateKeyBinary_CorruptSourceLayer_PropagatesError(t *testing.T) {
	// rotate-key needs to decrypt the source layer before
	// re-encrypting it. A corrupt source (garbage bytes pretending
	// to be sops ciphertext) must surface the decrypt failure
	// rather than silently skip or — worse — overwrite the file
	// with re-encrypted garbage.
	dir := t.TempDir()
	layerPath := filepath.Join(dir, "secrets/prod.pxf")
	require.NoError(t, os.MkdirAll(filepath.Dir(layerPath), 0o755))
	garbage := []byte("\x00\x01\x02 not sops ciphertext at all")
	require.NoError(t, os.WriteFile(layerPath, garbage, 0o644))

	keystorePath := writeMultiKeyKeystore(t, dir)
	t.Setenv("SOPS_AGE_KEY_FILE", keystorePath)
	t.Setenv("SOPS_AGE_KEY", "")

	chainPath := filepath.Join(dir, "chain.pxf")
	require.NoError(t, os.WriteFile(chainPath, []byte(`version = "1.0.0"
environments = [{ name = "prod" key_name = "new-key" }]
layers = [{ id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true required = true }]
`), 0o644))

	_, stderr, exit := runBinary(t,
		"rotate-key",
		"--chain="+chainPath,
		"--layers="+dir,
	)
	assert.NotEqual(t, 0, exit, "corrupt source must surface as non-zero exit")
	assert.True(t,
		strings.Contains(stderr, "decrypt") ||
			strings.Contains(stderr, "unmarshal") ||
			strings.Contains(stderr, "ERROR"),
		"stderr should identify the decrypt / parse failure; got: %s", stderr)

	// Critical: file content unchanged. A regression that wrote a
	// partial rewrite to disk would leave the operator with a
	// corrupted file AND no recoverable original.
	after, err := os.ReadFile(layerPath)
	require.NoError(t, err)
	assert.Equal(t, string(garbage), string(after),
		"corrupt source must NOT be overwritten — operator needs the original to investigate")
}
