// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"testing"

	sops "github.com/trendvidia/sops/v4"
	"github.com/trendvidia/sops/v4/age"
	"github.com/trendvidia/sops/v4/decrypt"
	"github.com/trendvidia/sops/v4/encrypt"
)

// encryptForTest produces a sops-encrypted PXF payload using age. Used
// by the migrate test fixtures so the test stays self-contained (no
// checked-in encrypted file).
func encryptForTest(t *testing.T, plain []byte, recipient string) ([]byte, error) {
	t.Helper()
	mk, err := age.MasterKeyFromRecipient(recipient)
	if err != nil {
		return nil, err
	}
	return encrypt.DataWithContext(context.Background(), plain, "protowire", encrypt.Options{
		KeyGroups: []sops.KeyGroup{{mk}},
	})
}

// decryptForTest is the symmetric helper.
func decryptForTest(t *testing.T, ciphertext []byte) ([]byte, error) {
	t.Helper()
	return decrypt.DataWithContext(context.Background(), ciphertext, "protowire")
}
