// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import (
	"context"
	"errors"
	"testing"

	sopskms "github.com/trendvidia/sops/v4/kms"
	sopspgp "github.com/trendvidia/sops/v4/pgp"
)

func TestRecipients_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		r    Recipients
		want bool
	}{
		{"all unset", Recipients{}, true},
		{"empty slices", Recipients{KMS: []string{}, Age: []string{}, PGP: []string{}}, true},
		{"only kms", Recipients{KMS: []string{"arn:..."}}, false},
		{"only age", Recipients{Age: []string{"age1..."}}, false},
		{"only pgp", Recipients{PGP: []string{"DEAD..."}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.IsEmpty(); got != tc.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	_, err := DefaultEncrypter().Encrypt(context.Background(), nil, FormatProtowire, Recipients{Age: []string{"age1xyz"}})
	if !errors.Is(err, ErrEmptyPlaintext) {
		t.Errorf("Encrypt(nil) = %v, want %v", err, ErrEmptyPlaintext)
	}
}

func TestEncrypt_NoRecipients(t *testing.T) {
	_, err := DefaultEncrypter().Encrypt(context.Background(), []byte("payload"), FormatProtowire, Recipients{})
	if !errors.Is(err, ErrNoRecipients) {
		t.Errorf("Encrypt(empty recipients) = %v, want %v", err, ErrNoRecipients)
	}
}

func TestEncrypt_InvalidAgeRecipient(t *testing.T) {
	// Malformed age recipient should error during buildKeyGroup
	// before sops's own pipeline runs, so the caller sees a clear
	// per-recipient message.
	_, err := DefaultEncrypter().Encrypt(context.Background(), []byte("payload"), FormatProtowire, Recipients{
		Age: []string{"not-a-real-age-recipient"},
	})
	if err == nil {
		t.Fatal("Encrypt with malformed age recipient should error")
	}
	// Want the per-recipient wrapping to surface so the user knows
	// WHICH input was bad — not just "no keys".
	if !contains(err.Error(), "age recipient") {
		t.Errorf("error should identify the bad age recipient, got: %v", err)
	}
}

// contains is a tiny strings.Contains alternative kept local so the
// test file's import list stays minimal.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Stable, syntactically-valid fixtures. None of these touch the
// network — sops's KMS / PGP MasterKey constructors validate at
// encrypt/decrypt time, not at construction.
const (
	testKMSARN1     = "arn:aws:kms:us-east-1:111122223333:key/1111aaaa-bbbb-cccc-dddd-eeeeffff0000"
	testKMSARN2     = "arn:aws:kms:eu-west-2:444455556666:key/2222aaaa-bbbb-cccc-dddd-eeeeffff1111"
	testPGPFinger1  = "AAAA1111BBBB2222CCCC3333DDDD4444EEEE5555"
	testPGPFinger2  = "FFFF1111000022223333444455556666AAAABBBB"
	testAgeRecipKey = "age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun"
)

func TestBuildKeyGroup_KMSOnly(t *testing.T) {
	group, err := buildKeyGroup(Recipients{KMS: []string{testKMSARN1}})
	if err != nil {
		t.Fatalf("buildKeyGroup: %v", err)
	}
	if len(group) != 1 {
		t.Fatalf("group len = %d, want 1", len(group))
	}
	k, ok := group[0].(*sopskms.MasterKey)
	if !ok {
		t.Fatalf("group[0] is %T, want *sopskms.MasterKey", group[0])
	}
	if k.Arn != testKMSARN1 {
		t.Errorf("Arn = %q, want %q", k.Arn, testKMSARN1)
	}
}

func TestBuildKeyGroup_MultipleKMS_Separate(t *testing.T) {
	// Each entry in r.KMS gets its own MasterKeysFromArnString call.
	group, err := buildKeyGroup(Recipients{KMS: []string{testKMSARN1, testKMSARN2}})
	if err != nil {
		t.Fatalf("buildKeyGroup: %v", err)
	}
	if len(group) != 2 {
		t.Fatalf("group len = %d, want 2", len(group))
	}
}

func TestBuildKeyGroup_KMSCommaSeparated(t *testing.T) {
	// Sops's MasterKeysFromArnString splits on comma internally — so
	// a single entry carrying "arn1,arn2" must fan out to two
	// MasterKey instances on the chameleon side too. Locks down the
	// "Join with ','" comment in buildKeyGroup.
	group, err := buildKeyGroup(Recipients{KMS: []string{testKMSARN1 + "," + testKMSARN2}})
	if err != nil {
		t.Fatalf("buildKeyGroup: %v", err)
	}
	if len(group) != 2 {
		t.Fatalf("group len = %d, want 2 (comma-split fan-out)", len(group))
	}
}

func TestBuildKeyGroup_PGPOnly(t *testing.T) {
	group, err := buildKeyGroup(Recipients{PGP: []string{testPGPFinger1}})
	if err != nil {
		t.Fatalf("buildKeyGroup: %v", err)
	}
	if len(group) != 1 {
		t.Fatalf("group len = %d, want 1", len(group))
	}
	k, ok := group[0].(*sopspgp.MasterKey)
	if !ok {
		t.Fatalf("group[0] is %T, want *sopspgp.MasterKey", group[0])
	}
	if k.Fingerprint != testPGPFinger1 {
		t.Errorf("Fingerprint = %q, want %q", k.Fingerprint, testPGPFinger1)
	}
}

func TestBuildKeyGroup_MultiplePGP(t *testing.T) {
	group, err := buildKeyGroup(Recipients{PGP: []string{testPGPFinger1, testPGPFinger2}})
	if err != nil {
		t.Fatalf("buildKeyGroup: %v", err)
	}
	if len(group) != 2 {
		t.Fatalf("group len = %d, want 2", len(group))
	}
}

func TestBuildKeyGroup_PGPCommaSeparated(t *testing.T) {
	group, err := buildKeyGroup(Recipients{PGP: []string{testPGPFinger1 + "," + testPGPFinger2}})
	if err != nil {
		t.Fatalf("buildKeyGroup: %v", err)
	}
	if len(group) != 2 {
		t.Fatalf("group len = %d, want 2 (comma-split fan-out)", len(group))
	}
}

func TestBuildKeyGroup_MixedAllThree(t *testing.T) {
	// All three key classes contribute to a single KeyGroup. Sanity
	// check on the concatenation order: KMS first, then age, then PGP.
	group, err := buildKeyGroup(Recipients{
		KMS: []string{testKMSARN1},
		Age: []string{testAgeRecipKey},
		PGP: []string{testPGPFinger1},
	})
	if err != nil {
		t.Fatalf("buildKeyGroup: %v", err)
	}
	if len(group) != 3 {
		t.Fatalf("group len = %d, want 3", len(group))
	}
	if _, ok := group[0].(*sopskms.MasterKey); !ok {
		t.Errorf("group[0] is %T, want KMS first", group[0])
	}
	if _, ok := group[2].(*sopspgp.MasterKey); !ok {
		t.Errorf("group[2] is %T, want PGP last", group[2])
	}
}

func TestBuildKeyGroup_EmptyStringsCollapseToNoKeys(t *testing.T) {
	// Recipients with non-empty slices but empty strings inside —
	// IsEmpty() returns false (slices have length), but
	// MasterKeysFromArnString / MasterKeysFromFingerprintString
	// short-circuit on empty input and return no keys. Verifies the
	// all-parse-failed guard in buildKeyGroup (line 150) catches this.
	_, err := buildKeyGroup(Recipients{KMS: []string{""}, PGP: []string{""}})
	if !errors.Is(err, ErrNoRecipients) {
		t.Errorf("buildKeyGroup with empty-string recipients = %v, want ErrNoRecipients", err)
	}
}

func TestEncrypt_KMSRecipient_PipelineRejectsBeyondBuild(t *testing.T) {
	// buildKeyGroup succeeds for KMS (no construction-time validation),
	// but sops's encrypt pipeline will later reject the unreachable
	// ARN. The test asserts the error wraps to "sops: encrypt: ..."
	// so the caller can identify the failing operation. This is the
	// only KMS-path test that doesn't require AWS — a real KMS
	// round-trip belongs in integration_test.go.
	_, err := DefaultEncrypter().Encrypt(
		context.Background(),
		[]byte("payload"),
		FormatProtowire,
		Recipients{KMS: []string{testKMSARN1}},
	)
	if err == nil {
		t.Fatal("Encrypt against unreachable KMS ARN should error")
	}
	if !contains(err.Error(), "sops: encrypt") {
		t.Errorf("error should be wrapped with 'sops: encrypt' prefix, got: %v", err)
	}
}

func TestSopsFormat_AllValues(t *testing.T) {
	// sopsFormat is currently a thin Format.String() pass-through,
	// but it's the single point where chameleon's Format enum meets
	// sops's string spelling — pin every value so a future divergence
	// surfaces here.
	cases := []struct {
		f    Format
		want string
	}{
		{FormatAuto, ""},
		{FormatBinary, "binary"},
		{FormatDotenv, "dotenv"},
		{FormatINI, "ini"},
		{FormatJSON, "json"},
		{FormatYAML, "yaml"},
		{FormatProtowire, "pxf"},
	}
	for _, tc := range cases {
		if got := sopsFormat(tc.f); got != tc.want {
			t.Errorf("sopsFormat(%v) = %q, want %q", tc.f, got, tc.want)
		}
	}
}
