// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops_test

import (
	"context"
	"os"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/sops"
)

// clearAgeKeySources unsets every age key source the sops fork reads,
// so a test controls exactly which ones are present. t.Setenv(name, "")
// alone would leave each one set-but-empty, which is the very case these
// tests are about.
func clearAgeKeySources(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no default keys.pxf
	for _, name := range []string{
		"SOPS_AGE_KEY", "SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY_CMD",
		"SOPS_AGE_SSH_PRIVATE_KEY_FILE", "SOPS_AGE_SSH_PRIVATE_KEY_CMD",
	} {
		t.Setenv(name, "") // registers the restore
		require.NoError(t, os.Unsetenv(name))
	}
}

// encryptToNewIdentity encrypts a one-field PXF layer to a fresh age
// identity and returns the ciphertext and that identity.
func encryptToNewIdentity(t *testing.T) ([]byte, *age.X25519Identity) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	ct, err := sops.DefaultEncrypter().Encrypt(context.Background(),
		[]byte("api_key = \"layer-secret\"\n"), crypt.FormatProtowire,
		crypt.Recipients{Age: []string{id.Recipient().String()}})
	require.NoError(t, err)
	return ct, id
}

// TestDecrypt_EmptyKeyCmdFallsThroughToOtherSources pins #238. sops v4.0.1
// panicked ("slice bounds out of range") when SOPS_AGE_KEY_CMD was set
// but empty, taking down any runtime whose environment carried
// `SOPS_AGE_KEY_CMD=`. From v4.0.2 (trendvidia/sops#38) the empty command
// is a per-source error, and the next source still gets its turn: here,
// SOPS_AGE_KEY decrypts the layer.
func TestDecrypt_EmptyKeyCmdFallsThroughToOtherSources(t *testing.T) {
	ct, id := encryptToNewIdentity(t)
	clearAgeKeySources(t)
	t.Setenv("SOPS_AGE_KEY", id.String())
	t.Setenv("SOPS_AGE_KEY_CMD", "")

	buf, err := sops.Default().Decrypt(context.Background(), ct, crypt.FormatProtowire)
	require.NoError(t, err, "an empty key command must not stop SOPS_AGE_KEY from decrypting")
	defer buf.Destroy()
	assert.Contains(t, string(buf.Bytes()), "layer-secret")
}

// TestDecrypt_EmptyKeyCmdAloneIsAnError: with the empty command as the
// only key source, decryption fails with an error rather than a panic.
func TestDecrypt_EmptyKeyCmdAloneIsAnError(t *testing.T) {
	ct, _ := encryptToNewIdentity(t)
	clearAgeKeySources(t)
	t.Setenv("SOPS_AGE_KEY_CMD", "")

	_, err := sops.Default().Decrypt(context.Background(), ct, crypt.FormatProtowire)
	require.Error(t, err)
}
