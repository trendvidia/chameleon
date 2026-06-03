// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trendvidia/sops/v4/age/keypb"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Direct unit tests for RecipientsFromKeyName. Previously covered
// only transitively through internal/bundler/recipients_test.go's
// unionRecipients flow — empty-name semantics, error-wrapping shape,
// and the success-case envelope all went unasserted.

// writePXFKeystore points SOPS_AGE_KEY_FILE at a temp PXF file
// carrying the named identities. Mirrors the helper used in
// internal/bundler/recipients_test.go.
func writePXFKeystore(t *testing.T, keys map[string]string) {
	t.Helper()
	file := &keypb.AgeKeyFile{Keys: keys}
	data, err := pxf.Marshal(file)
	if err != nil {
		t.Fatalf("marshal keystore: %v", err)
	}
	path := filepath.Join(t.TempDir(), "keys.pxf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write keystore: %v", err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", path)
}

const (
	keynameTestIdentity  = "AGE-SECRET-KEY-1G0Q5K9TV4REQ3ZSQRMTMG8NSWQGYT0T7TZ33RAZEE0GZYVZN0APSU24RK7"
	keynameTestRecipient = "age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun"
)

func TestRecipientsFromKeyName_EmptyNameReturnsEmptyNoError(t *testing.T) {
	// The documented "caller-falls-back" contract: an empty name
	// must return an empty Recipients value and a nil error so the
	// caller can drop into its own default-recipient source. A
	// regression that errored on empty input would break the
	// bundler's unionRecipients caller.
	got, err := RecipientsFromKeyName("")
	if err != nil {
		t.Errorf("err = %v, want nil for empty name", err)
	}
	if !got.IsEmpty() {
		t.Errorf("recipients = %+v, want empty for empty name", got)
	}
}

func TestRecipientsFromKeyName_KnownKeyResolvesToAgeRecipient(t *testing.T) {
	// Happy path: a named key in the keystore resolves to its
	// derived age recipient, returned in Recipients.Age. KMS and
	// PGP slots stay empty — this entry point only deals with age
	// keys (the keystore format is age-specific).
	writePXFKeystore(t, map[string]string{
		"prod-key": keynameTestIdentity,
	})
	got, err := RecipientsFromKeyName("prod-key")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got.Age) != 1 {
		t.Fatalf("Age = %v, want exactly one recipient", got.Age)
	}
	if got.Age[0] != keynameTestRecipient {
		t.Errorf("Age[0] = %q, want %q", got.Age[0], keynameTestRecipient)
	}
	if len(got.KMS) != 0 || len(got.PGP) != 0 {
		t.Errorf("KMS / PGP must stay empty; got KMS=%v PGP=%v", got.KMS, got.PGP)
	}
}

func TestRecipientsFromKeyName_UnknownKeyErrorsWithName(t *testing.T) {
	// An unknown name surfaces as a wrapped sops error. The wrapping
	// must include the requested name so the operator sees WHICH
	// key was missing — generic "key not found" would force them to
	// hunt through the keystore manually.
	writePXFKeystore(t, map[string]string{
		"present-key": keynameTestIdentity,
	})
	_, err := RecipientsFromKeyName("missing-key")
	if err == nil {
		t.Fatal("expected error for unknown key name")
	}
	if !contains(err.Error(), "missing-key") {
		t.Errorf("error should mention the unknown key name, got: %v", err)
	}
	// The sops: prefix is the package's wrapping convention.
	if !contains(err.Error(), "sops") {
		t.Errorf("error should be wrapped through the sops package, got: %v", err)
	}
}
