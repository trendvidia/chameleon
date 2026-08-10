// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// PGP round-trip integration. The age-side coverage in
// integration_test.go validates one half of buildKeyGroup's
// recipient-fan-out; this file does the symmetric thing for PGP.
// Without it, a regression in pgp.MasterKeyFromFingerprint, in
// sops's gpg-agent dispatch, or in the chameleon Decrypter's
// format-string for PXF would have shipped silently — the unit
// tests cover construction shape but never round-trip ciphertext.
//
// Strategy:
//
//   - Skip the test if `gpg` is not on PATH (CI without GnuPG
//     just doesn't run this).
//   - Carve out an isolated GNUPGHOME under t.TempDir() so the
//     test never touches the developer's real keyring.
//   - Generate a passphrase-less RSA key via `gpg --batch
//     --quick-gen-key` and read the resulting fingerprint via
//     `--list-secret-keys --with-colons`.
//   - Encrypt a PXF payload through sops's programmatic API,
//     keyed on that fingerprint.
//   - Decrypt via chameleon's Decrypter and assert byte-for-byte
//     round-trip.
//
// The whole flow runs in ~1-2 seconds on a developer laptop
// (entropy + a few subprocess calls), which is why this is gated
// behind -tags integration rather than the default test path.
//
// To run:
//
//	go test -tags=integration ./sops/...
package sops_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/sops/v4"
	"github.com/trendvidia/sops/v4/encrypt"
	sopspgp "github.com/trendvidia/sops/v4/pgp"

	"github.com/trendvidia/chameleon/crypt"
	chsops "github.com/trendvidia/chameleon/sops"
)

// gpgOrSkip resolves the gpg binary, skipping the test if it's
// not on PATH. Returns the absolute path for subsequent invocations.
func gpgOrSkip(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("gpg not on PATH; skipping PGP integration test")
	}
	return bin
}

// isolatedGNUPGHOME creates a short-path GNUPGHOME and points the
// env at it. The directory dies with the test (registered via
// t.Cleanup); the env var is unset by t.Setenv after the test.
//
// Why NOT t.TempDir(): gpg-agent's Unix socket is GNUPGHOME/S.gpg-agent,
// and Unix socket paths have a 104-char limit on macOS. The default
// TMPDIR path (/var/folders/.../TestNameNNNNN/NNN/...) routinely
// blows past that, surfacing as a baffling "can't connect to the
// gpg-agent: File name too long". /tmp keeps the prefix short
// enough that the socket fits.
//
// 0o700 on GNUPGHOME is gpg's requirement — relaxing it makes gpg
// refuse to use the directory ("WARNING: unsafe permissions").
func isolatedGNUPGHOME(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "chgpg-")
	require.NoError(t, err, "create short-path GNUPGHOME")
	require.NoError(t, os.Chmod(home, 0o700))
	t.Cleanup(func() {
		// Shut down any gpg-agent we spawned in this home before
		// removing the directory, so the agent doesn't keep file
		// handles open across cleanup.
		_ = exec.Command("gpgconf", "--homedir", home, "--kill", "gpg-agent").Run()
		_ = os.RemoveAll(home)
	})
	t.Setenv("GNUPGHOME", home)
	return home
}

// generateTestPGPKey runs `gpg --quick-gen-key` with a deterministic
// UID and returns the fingerprint. Uses --pinentry-mode loopback +
// an empty --passphrase to skip interactive prompts (the secret key
// has no passphrase, which is fine for a throwaway test key).
//
// Do not spell the empty passphrase as a pair of single quotes in
// this comment: gofmt's doc-comment printer rewrites that pair into
// a Unicode curly quote, leaving the comment documenting a command
// that does not run. The argv below passes a real empty string.
//
// Algorithm "default default 0" = gpg's defaults (rsa3072 today),
// no expiry. The 0 expiry matters: a non-zero default could make
// the test flake at the year boundary.
func generateTestPGPKey(t *testing.T, gpgBin string) string {
	t.Helper()
	const uid = "Chameleon PGP Test <pgp-test@chameleon.invalid>"
	cmd := exec.Command(gpgBin,
		"--batch",
		"--pinentry-mode", "loopback",
		"--passphrase", "",
		"--quick-gen-key", uid,
		"default", "default", "0",
	)
	combined, err := cmd.CombinedOutput()
	require.NoError(t, err, "gpg --quick-gen-key: %s", combined)

	// Read back the fingerprint via the machine-readable colon
	// format. Layout: per `man gpg --with-colons`, the first `fpr`
	// row after a `sec` row carries the secret-key fingerprint.
	listCmd := exec.Command(gpgBin,
		"--batch",
		"--list-secret-keys",
		"--with-colons",
		uid,
	)
	listOut, err := listCmd.Output()
	require.NoError(t, err, "list-secret-keys: %v", err)
	fpr := firstFPRField(string(listOut))
	require.NotEmpty(t, fpr, "no fpr line found in gpg --with-colons output:\n%s", listOut)
	return fpr
}

// firstFPRField returns the first fingerprint value (column 10) of
// the first `fpr` row in gpg --with-colons output.
func firstFPRField(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "fpr:") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 10 {
			continue
		}
		return fields[9]
	}
	return ""
}

func TestRoundTrip_PGP_PXF(t *testing.T) {
	// End-to-end: gpg-generated key -> sops encrypt to that
	// fingerprint -> chameleon Decrypter pulls plaintext back.
	// The chameleon side does NOT see gpg explicitly — sops's
	// PGP MasterKey dispatches through gpg-agent transparently,
	// which is exactly the production code path.
	gpgBin := gpgOrSkip(t)
	isolatedGNUPGHOME(t)
	fpr := generateTestPGPKey(t, gpgBin)

	// Build a sops PGP MasterKey for the fingerprint we just made.
	mks := sopspgp.MasterKeysFromFingerprintString(fpr)
	require.Len(t, mks, 1, "fingerprint fan-out should produce one key")

	// Plaintext fixture — same canonical PXF shape the age test
	// uses so any normalization regression in the PXF codec
	// surfaces on both round-trip tests symmetrically.
	plaintext := []byte(`base_url = "mybigproject.com"
api_url = "api.mybigproject.com"
db_password = "real-secret-do-not-leak"
feature_flags = [
  "fast_path",
  "new_dashboard"
]
`)

	ciphertext, err := encrypt.Data(plaintext, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{mks[0]}},
	})
	require.NoError(t, err, "sops encrypt.Data via PGP")
	require.NotEmpty(t, ciphertext)
	require.NotEqual(t, plaintext, ciphertext,
		"ciphertext must differ from plaintext")
	// Sanity envelope checks: AES leaf encryption + sops trailer
	// metadata block. These are stable across age and PGP — the
	// recipient class only affects the metadata.pgp section, not
	// the leaf encryption envelope.
	assert.Contains(t, string(ciphertext), "sops {",
		"PXF sops envelope should include the trailing metadata block")
	assert.Contains(t, string(ciphertext), "ENC[AES256_GCM",
		"individual leaf values should be wrapped in the AES256_GCM envelope")
	assert.Contains(t, string(ciphertext), "pgp = [",
		"metadata block should reference the pgp recipient class")
	assert.Contains(t, string(ciphertext), fpr,
		"metadata should record the fingerprint we encrypted to")

	// Decrypt via the chameleon entry point. GNUPGHOME points at
	// the isolated keyring; sops's gpg-agent dispatch finds the
	// secret key there.
	buf, err := chsops.Default().Decrypt(
		context.Background(),
		ciphertext,
		crypt.FormatProtowire,
	)
	require.NoError(t, err, "chameleon Decrypter on PGP-encrypted ciphertext")
	defer buf.Destroy()

	got := buf.Bytes()
	assert.Equal(t, string(plaintext), string(got),
		"decrypted plaintext must match the original byte-for-byte")
}

func TestRoundTrip_PGP_WrongKeyFails(t *testing.T) {
	// Symmetric to TestRoundTrip_Age_WrongKey in integration_test.go:
	// encrypt under one PGP key, swap to an EMPTY keyring, expect
	// decrypt to fail rather than silently emitting wrong bytes.
	gpgBin := gpgOrSkip(t)

	// First keyring: generate a key, encrypt with its fingerprint.
	homeA := isolatedGNUPGHOME(t)
	fprA := generateTestPGPKey(t, gpgBin)
	mksA := sopspgp.MasterKeysFromFingerprintString(fprA)
	require.Len(t, mksA, 1)

	plaintext := []byte(`secret_token = "x"`)
	ciphertext, err := encrypt.Data(plaintext, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{mksA[0]}},
	})
	require.NoError(t, err)

	// Swap to an empty isolated keyring. The decrypter has no
	// secret key matching fprA, so sops must surface the failure.
	homeB := filepath.Join(t.TempDir(), "homeB")
	require.NoError(t, os.MkdirAll(homeB, 0o700))
	t.Setenv("GNUPGHOME", homeB)
	require.NotEqual(t, homeA, homeB, "test must change GNUPGHOME between encrypt and decrypt")

	_, err = chsops.Default().Decrypt(
		context.Background(),
		ciphertext,
		crypt.FormatProtowire,
	)
	require.Error(t, err, "decrypt with empty keyring must fail")
	assert.Contains(t, strings.ToLower(err.Error()), "decrypt",
		"error should mention the failing operation; got: %v", err)
}

// Sanity that the helper's column parser is doing what we think
// — gpg --with-colons output is space-sensitive and a small change
// in our parser would silently break the test.
func TestFirstFPRField_ParsesGPGColonOutput(t *testing.T) {
	sample := strings.Join([]string{
		"sec:u:3072:1:DEADBEEF:1700000000:::-:::scESC::::::23::0:",
		"fpr:::::::::ABCDEF0123456789ABCDEF0123456789ABCDEF01:",
		"uid:u::::1700000000::ABC123::Test User <test@example.invalid>::::::::::0:",
		"ssb:u:3072:1:CAFEBABE:1700000000::::::e::::::23:",
		"fpr:::::::::1111222233334444555566667777888899990000:",
	}, "\n")
	got := firstFPRField(sample)
	want := "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	if got != want {
		t.Errorf("firstFPRField = %q, want %q", got, want)
	}
}

// Ensure the `bytes` import stays referenced if a refactor drops
// every use elsewhere — keeps the test file stable while the body
// gets edited.
var _ = bytes.NewReader
