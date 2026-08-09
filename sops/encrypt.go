// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import (
	"context"
	"errors"
	"fmt"

	"github.com/awnumar/memguard"
	"github.com/trendvidia/sops/v4"
	sopsage "github.com/trendvidia/sops/v4/age"
	"github.com/trendvidia/sops/v4/encrypt"
	"github.com/trendvidia/sops/v4/keys"
	"github.com/trendvidia/sops/v4/kms"
	"github.com/trendvidia/sops/v4/pgp"

	"github.com/trendvidia/chameleon/crypt"
)

// ErrEmptyPlaintext is returned when Encrypt is handed a zero-length
// plaintext. Sops itself accepts empty input but emits a structurally
// odd ciphertext envelope; failing loudly here surfaces caller bugs
// (e.g. an unset memguard buffer) at the encrypt boundary.
var ErrEmptyPlaintext = errors.New("sops: empty plaintext")

// ErrNoRecipients is returned when Encrypt is handed Recipients with
// no entries across KMS / Age / PGP. Matches sops's "at least one
// KeyGroup is required" check, with a less generic message.
var ErrNoRecipients = errors.New("sops: no recipients (need at least one KMS / age / PGP key)")

// DefaultEncrypter returns a [crypt.Encrypter] wired to the in-process
// trendvidia/sops/v4 encryption stack. Credentials for KMS / age / PGP
// come from the ambient environment exactly as the sops CLI does
// (see the package doc for the variable list).
//
// Importing this package for its side effect registers the same
// Encrypter under the name "sops"; see the package doc.
func DefaultEncrypter() crypt.Encrypter { return defaultEncrypter{} }

type defaultEncrypter struct{}

func (defaultEncrypter) Encrypt(ctx context.Context, plaintext []byte, format crypt.Format, recipients crypt.Recipients) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, ErrEmptyPlaintext
	}
	if recipients.IsEmpty() {
		return nil, ErrNoRecipients
	}

	group, err := buildKeyGroup(recipients)
	if err != nil {
		return nil, err
	}

	ciphertext, err := encrypt.DataWithContext(ctx, plaintext, sopsFormat(format), encrypt.Options{
		KeyGroups: []sops.KeyGroup{group},
	})
	if err != nil {
		return nil, fmt.Errorf("sops: encrypt: %w", err)
	}

	memguard.WipeBytes(plaintext)
	return ciphertext, nil
}

// buildKeyGroup converts Recipients into the single sops.KeyGroup
// the encrypt API expects. Each key class has its own constructor
// in the sops fork; this function fans the recipient strings out to
// the right one and concatenates the results.
//
// Note: kms.MasterKeysFromArnString accepts a comma-separated
// string; we Join with ',' rather than calling it per-ARN to keep
// the sops-side parsing logic centralized (it splits, trims, and
// validates). Age and PGP follow the same convention.
func buildKeyGroup(r crypt.Recipients) (sops.KeyGroup, error) {
	var group sops.KeyGroup

	for _, arn := range r.KMS {
		for _, k := range kms.MasterKeysFromArnString(arn, nil /* encryption context */, "" /* aws profile */) {
			group = append(group, keys.MasterKey(k))
		}
	}

	for _, recipient := range r.Age {
		mk, err := sopsage.MasterKeyFromRecipient(recipient)
		if err != nil {
			return nil, fmt.Errorf("sops: age recipient %q: %w", recipient, err)
		}
		group = append(group, keys.MasterKey(mk))
	}

	for _, fingerprint := range r.PGP {
		for _, k := range pgp.MasterKeysFromFingerprintString(fingerprint) {
			group = append(group, keys.MasterKey(k))
		}
	}

	if len(group) == 0 {
		// All inputs failed to parse — surface this so the caller
		// doesn't see sops's downstream "no keys" complaint and
		// chase the wrong layer.
		return nil, ErrNoRecipients
	}
	return group, nil
}

// sopsFormat maps chameleon's Format enum to the string spelling
// sops's encrypt API accepts. Equivalent to format.String() but
// kept as a small named helper so any future divergence between
// our Format and sops's FormatFromString table has one place to
// land.
func sopsFormat(f crypt.Format) string {
	return f.String()
}
