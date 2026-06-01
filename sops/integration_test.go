// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// Real-sops round-trip integration test. Gated behind `-tags integration`
// so it stays out of the default `go test ./...` path — sops's encrypt
// stack pulls in age key generation + the full keyservice graph, which
// is heavier than what unit tests should run on every iteration.
//
// To run:
//
//	go test -tags=integration ./sops/...
//
// What it covers:
//
//   - A fresh age keypair is generated in-process via filippo.io/age.
//   - A PXF plaintext fixture is encrypted via sops's programmatic
//     encrypt.Data API (PXF format = "protowire").
//   - SOPS_AGE_KEY is set to the private key for the duration of the
//     test so chameleon's Decrypter can find it via the same ambient-
//     env lookup production callers use.
//   - chameleon's sops.Default().Decrypt is invoked on the ciphertext
//     and the round-tripped plaintext is asserted byte-for-byte equal
//     to the original.
//
// Why a true round-trip matters: the unit tests in this package mock
// the decrypt path so they verify the memguard plumbing, but they
// don't catch e.g. a format-string regression in FormatForPath or a
// breaking change in the trendvidia/sops fork's PXF store. This test
// is the canary for those drift modes.
package sops_test

import (
	"context"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/trendvidia/sops/v4"
	sopsage "github.com/trendvidia/sops/v4/age"
	"github.com/trendvidia/sops/v4/encrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chsops "github.com/trendvidia/chameleon/sops"
)

func TestRoundTrip_Age_PXF(t *testing.T) {
	// 1. Generate an age keypair. X25519 — fast, no network.
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err, "age key generation")
	publicKey := identity.Recipient().String()
	privateKey := identity.String()

	// 2. Bridge into sops's master-key view.
	masterKey, err := sopsage.MasterKeyFromRecipient(publicKey)
	require.NoError(t, err, "sops age MasterKey from recipient")

	// 3. Plaintext fixture — keep small + realistic PXF. Authored in
	// the PXF store's canonical emit form so the round-trip is
	// byte-for-byte stable (lists multi-line, no inline brackets).
	// A normalization regression in the sops PXF store would surface
	// here as a diff against this canonical shape.
	plaintext := []byte(`base_url = "mybigproject.com"
api_url = "api.mybigproject.com"
db_password = "real-secret-do-not-leak"
feature_flags = [
  "fast_path",
  "new_dashboard"
]
`)

	// 4. Encrypt via sops programmatic API.
	ciphertext, err := encrypt.Data(plaintext, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{masterKey}},
	})
	require.NoError(t, err, "sops encrypt.Data")
	require.NotEmpty(t, ciphertext)
	require.NotEqual(t, plaintext, ciphertext, "ciphertext must differ from plaintext")
	// Sanity: ciphertext envelope carries the sops metadata block (a
	// `sops { ... }` trailer in the PXF store form).
	assert.Contains(t, string(ciphertext), "sops {",
		"PXF sops envelope should include the trailing metadata block")
	assert.Contains(t, string(ciphertext), "ENC[AES256_GCM",
		"individual leaf values should be wrapped in the AES256_GCM envelope")

	// 5. Wire the private key into the ambient env so chameleon's
	// Decrypter (which delegates to trendvidia/sops/v4/decrypt) finds it.
	// t.Setenv handles cleanup automatically.
	t.Setenv("SOPS_AGE_KEY", privateKey)

	// 6. Decrypt via the chameleon entry point — the same path
	// production callers exercise through resolver.WithDecrypter.
	buf, err := chsops.Default().Decrypt(context.Background(), ciphertext, chsops.FormatProtowire)
	require.NoError(t, err, "chameleon Decrypter on real ciphertext")
	defer buf.Destroy()

	// 7. The round-tripped plaintext must equal the original byte-for-
	// byte. PXF store preserves field order + literal formatting on
	// round-trip; this assertion would catch a normalization regression
	// in the sops PXF codec.
	got := buf.Bytes()
	assert.Equal(t, string(plaintext), string(got),
		"decrypted plaintext should match the original byte-for-byte")
}

func TestRoundTrip_Age_WrongKey(t *testing.T) {
	// Encrypt with one keypair; try to decrypt with another. Should
	// fail at the Decrypt step — a sanity check that the integration
	// path isn't accidentally accepting any old key.

	identityA, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	identityB, err := age.GenerateX25519Identity()
	require.NoError(t, err)

	masterKeyA, err := sopsage.MasterKeyFromRecipient(identityA.Recipient().String())
	require.NoError(t, err)

	plaintext := []byte(`secret_token = "x"`)
	ciphertext, err := encrypt.Data(plaintext, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{masterKeyA}},
	})
	require.NoError(t, err)

	// Wrong private key.
	t.Setenv("SOPS_AGE_KEY", identityB.String())

	_, err = chsops.Default().Decrypt(context.Background(), ciphertext, chsops.FormatProtowire)
	require.Error(t, err, "wrong-key decrypt must fail")
	assert.Contains(t, strings.ToLower(err.Error()), "decrypt",
		"error should mention the failing operation")
}

// TestRoundTrip_ChameleonEncrypter_Age covers the encrypt side of
// the symmetry: plaintext → chameleon's DefaultEncrypter →
// ciphertext → chameleon's Decrypter → original plaintext, all
// keyed on a freshly generated age identity. This is the canary
// for the bundler's re-encrypt step.
func TestRoundTrip_ChameleonEncrypter_Age(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	publicKey := identity.Recipient().String()
	privateKey := identity.String()

	plaintext := []byte(`base_url = "mybigproject.com"
db_password = "real-secret-do-not-leak"
`)
	// Take a copy so we can assert the encrypter wiped the input.
	plaintextCopy := append([]byte(nil), plaintext...)

	ciphertext, err := chsops.DefaultEncrypter().Encrypt(
		context.Background(),
		plaintext,
		chsops.FormatProtowire,
		chsops.Recipients{Age: []string{publicKey}},
	)
	require.NoError(t, err, "chameleon Encrypter on age recipient")
	require.NotEmpty(t, ciphertext)
	assert.Contains(t, string(ciphertext), "sops {",
		"PXF sops envelope should include the trailing metadata block")
	assert.Contains(t, string(ciphertext), "ENC[AES256_GCM",
		"individual leaf values should be wrapped in the AES256_GCM envelope")

	// Plaintext slice must be wiped on success (matches Decrypter's
	// ciphertext-wipe contract).
	for i, b := range plaintext {
		assert.Equal(t, byte(0), b, "plaintext byte %d should be wiped after Encrypt", i)
	}

	// Decrypt round-trips to the original.
	t.Setenv("SOPS_AGE_KEY", privateKey)
	buf, err := chsops.Default().Decrypt(context.Background(), ciphertext, chsops.FormatProtowire)
	require.NoError(t, err, "round-trip decrypt of chameleon-encrypted ciphertext")
	defer buf.Destroy()

	assert.Equal(t, string(plaintextCopy), string(buf.Bytes()),
		"decrypted plaintext should match the original byte-for-byte")
}
