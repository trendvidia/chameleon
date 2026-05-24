// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import (
	"context"
	"errors"
	"testing"
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
