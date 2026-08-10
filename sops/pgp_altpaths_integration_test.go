// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// PGP alternative-path coverage. The headline pgp_integration_test.go
// pinned single-recipient round-trip + wrong-key failure; this file
// covers the multi-recipient and mixed-class shapes operators
// actually reach in production rotations + the obvious operator
// error (encrypt to a fingerprint not in any keyring).
//
// What's covered here:
//
//   - Multi-fingerprint recipient: encrypt to {fp1, fp2}, metadata
//     block records both, round-trip decrypts.
//   - Mixed age + PGP: encrypt to one of each class; age key alone
//     decrypts (PGP isn't needed); PGP key alone decrypts (age
//     isn't needed). Pins the KeyGroup "any one member decrypts"
//     contract across recipient classes — the load-bearing
//     property during a class migration.
//   - Encrypt-time error when the requested fingerprint isn't in
//     the keyring (gpg has no public key to encrypt to).
//
// Gated behind `-tags integration`. Reuses gpgOrSkip,
// isolatedGNUPGHOME, firstFPRField from pgp_integration_test.go.

package sops_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/trendvidia/sops/v4"
	sopsage "github.com/trendvidia/sops/v4/age"
	"github.com/trendvidia/sops/v4/encrypt"
	sopspgp "github.com/trendvidia/sops/v4/pgp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/crypt"
	chsops "github.com/trendvidia/chameleon/sops"
)

// generateTestPGPKeyWithUID is the multi-key variant of the
// fixed-UID helper in pgp_integration_test.go. Lets a single test
// stand up two distinct keys in the same keyring.
func generateTestPGPKeyWithUID(t *testing.T, gpgBin, uid string) string {
	t.Helper()
	cmd := exec.Command(gpgBin,
		"--batch",
		"--pinentry-mode", "loopback",
		"--passphrase", "",
		"--quick-gen-key", uid,
		"default", "default", "0",
	)
	combined, err := cmd.CombinedOutput()
	require.NoError(t, err, "gpg --quick-gen-key %s: %s", uid, combined)

	listCmd := exec.Command(gpgBin,
		"--batch",
		"--list-secret-keys",
		"--with-colons",
		uid,
	)
	out, err := listCmd.Output()
	require.NoError(t, err, "list-secret-keys %s", uid)
	fpr := firstFPRField(string(out))
	require.NotEmpty(t, fpr, "no fpr for %s in:\n%s", uid, out)
	return fpr
}

func TestPGP_MultiRecipient_BothFingerprintsRecorded(t *testing.T) {
	// Two PGP keys in one keyring; encrypt to a KeyGroup with both
	// fingerprints. The sops metadata block must record both, and
	// the chameleon Decrypter must round-trip the plaintext under
	// the multi-key keyring.
	//
	// Why two fingerprints matter at the contract level: a
	// regression in pgp.MasterKeysFromFingerprintString's
	// comma-split (covered at the unit level in #107) would still
	// surface here as "only one fingerprint in metadata", and the
	// unit test wouldn't catch a real-gpg interaction bug.
	gpgBin := gpgOrSkip(t)
	isolatedGNUPGHOME(t)

	fp1 := generateTestPGPKeyWithUID(t, gpgBin,
		"Chameleon PGP Multi A <pgp-multi-a@chameleon.invalid>")
	fp2 := generateTestPGPKeyWithUID(t, gpgBin,
		"Chameleon PGP Multi B <pgp-multi-b@chameleon.invalid>")
	require.NotEqual(t, fp1, fp2, "the two test keys must have distinct fingerprints")

	mks1 := sopspgp.MasterKeysFromFingerprintString(fp1)
	mks2 := sopspgp.MasterKeysFromFingerprintString(fp2)
	require.Len(t, mks1, 1)
	require.Len(t, mks2, 1)

	plaintext := []byte(`db_password = "multi-fp-payload"
`)
	ciphertext, err := encrypt.Data(plaintext, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{mks1[0], mks2[0]}},
	})
	require.NoError(t, err)
	assert.Contains(t, string(ciphertext), fp1,
		"metadata should record fp1")
	assert.Contains(t, string(ciphertext), fp2,
		"metadata should record fp2 — the multi-recipient contract")

	// Round-trip: keyring has both secrets, so either path can
	// decrypt; sops picks one and produces plaintext.
	buf, err := chsops.Default().Decrypt(
		context.Background(), ciphertext, crypt.FormatProtowire,
	)
	require.NoError(t, err, "multi-fingerprint ciphertext should round-trip via the full keyring")
	defer buf.Destroy()
	assert.Equal(t, string(plaintext), string(buf.Bytes()))
}

func TestMixedAgePGP_AgeKeyAloneDecrypts(t *testing.T) {
	// Mixed recipient classes — one age, one PGP — in a single
	// KeyGroup. With ONLY age available (empty GNUPGHOME, age
	// identity in SOPS_AGE_KEY), the decrypter must succeed via
	// the age path. This is the load-bearing "either class
	// decrypts" property used during class migrations.
	gpgBin := gpgOrSkip(t)

	// Encryption keyring needs the PGP public key so encrypt.Data
	// can fan out to it; the age side is recipient-only on the
	// encrypt path so no special setup there.
	homeEnc := isolatedGNUPGHOME(t)
	pgpFP := generateTestPGPKeyWithUID(t, gpgBin,
		"Chameleon PGP Mixed <pgp-mixed@chameleon.invalid>")
	pgpMK := sopspgp.MasterKeysFromFingerprintString(pgpFP)[0]

	ageID, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	ageMK, err := sopsage.MasterKeyFromRecipient(ageID.Recipient().String())
	require.NoError(t, err)

	plaintext := []byte(`secret = "mixed-classes"
`)
	ciphertext, err := encrypt.Data(plaintext, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{ageMK, pgpMK}},
	})
	require.NoError(t, err)
	// Sanity: both recipient classes recorded.
	assert.Contains(t, string(ciphertext), "pgp = [",
		"metadata should have a pgp section")
	assert.Contains(t, string(ciphertext), "age = [",
		"metadata should have an age section")

	// Decrypt context: ONLY age. Swap to an empty GNUPGHOME so
	// the PGP path can't possibly succeed; only the age MasterKey's
	// SOPS_AGE_KEY-driven lookup can.
	homeOnlyAge := filepath.Join(t.TempDir(), "no-pgp")
	require.NoError(t, os.MkdirAll(homeOnlyAge, 0o700))
	t.Setenv("GNUPGHOME", homeOnlyAge)
	t.Setenv("SOPS_AGE_KEY", ageID.String())
	// Belt and suspenders: assert the empty GNUPGHOME is actually
	// in effect by verifying the encrypted keyring isn't being
	// inherited.
	require.NotEqual(t, homeEnc, homeOnlyAge,
		"decrypt must use a different GNUPGHOME than encrypt")

	buf, err := chsops.Default().Decrypt(
		context.Background(), ciphertext, crypt.FormatProtowire,
	)
	require.NoError(t, err, "age-only decrypt should succeed via the age MasterKey")
	defer buf.Destroy()
	assert.Equal(t, string(plaintext), string(buf.Bytes()),
		"age path must round-trip the mixed-class ciphertext")
}

func TestMixedAgePGP_PGPKeyAloneDecrypts(t *testing.T) {
	// Symmetric of the above: same encrypt setup, but the decrypt
	// context has ONLY the PGP keyring (SOPS_AGE_KEY unset). Pins
	// the other half of the "either class decrypts" property.
	gpgBin := gpgOrSkip(t)
	homePGP := isolatedGNUPGHOME(t)
	pgpFP := generateTestPGPKeyWithUID(t, gpgBin,
		"Chameleon PGP Mixed Only <pgp-mixed-only@chameleon.invalid>")
	pgpMK := sopspgp.MasterKeysFromFingerprintString(pgpFP)[0]

	ageID, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	ageMK, err := sopsage.MasterKeyFromRecipient(ageID.Recipient().String())
	require.NoError(t, err)

	plaintext := []byte(`secret = "pgp-only-path"
`)
	ciphertext, err := encrypt.Data(plaintext, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{ageMK, pgpMK}},
	})
	require.NoError(t, err)

	// Clear age inputs; PGP keyring is already in GNUPGHOME from
	// isolatedGNUPGHOME above.
	t.Setenv("SOPS_AGE_KEY", "")
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	require.NotEmpty(t, homePGP, "PGP keyring path must be set")

	buf, err := chsops.Default().Decrypt(
		context.Background(), ciphertext, crypt.FormatProtowire,
	)
	require.NoError(t, err, "pgp-only decrypt should succeed via the pgp MasterKey")
	defer buf.Destroy()
	assert.Equal(t, string(plaintext), string(buf.Bytes()),
		"pgp path must round-trip the mixed-class ciphertext")
}

func TestPGP_EncryptToUnknownFingerprint_Fails(t *testing.T) {
	// sops's PGP encrypt path shells out to gpg to do the
	// public-key wrap. If the requested fingerprint has no public
	// key in the keyring, gpg fails the operation and the error
	// propagates up. Pin that — an operator typo on the fingerprint
	// should fail at build time, not at decrypt time when nobody
	// can read the bundle.
	gpgOrSkip(t)
	isolatedGNUPGHOME(t)

	// No key generated → keyring is empty. Use a syntactically
	// valid 40-char hex fingerprint that doesn't exist in any
	// keyring.
	const bogusFP = "DEADBEEF0000000000000000000000000000DEAD"
	mks := sopspgp.MasterKeysFromFingerprintString(bogusFP)
	require.Len(t, mks, 1,
		"MasterKeysFromFingerprintString doesn't validate at construction (regression check)")

	_, err := encrypt.Data([]byte(`x = "y"`), "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{mks[0]}},
	})
	require.Error(t, err, "encrypt to a fingerprint absent from any keyring must fail")
	// sops wording varies; assert the broad failure class.
	assert.True(t,
		strings.Contains(strings.ToLower(err.Error()), "pgp") ||
			strings.Contains(strings.ToLower(err.Error()), "encrypt") ||
			strings.Contains(strings.ToLower(err.Error()), "no public key") ||
			strings.Contains(err.Error(), bogusFP),
		"error should identify the failing fingerprint / class; got: %v", err)
}
