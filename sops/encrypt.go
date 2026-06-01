// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import (
	"context"
	"errors"
	"fmt"

	"github.com/awnumar/memguard"
	"github.com/trendvidia/sops/v3"
	sopsage "github.com/trendvidia/sops/v3/age"
	"github.com/trendvidia/sops/v3/encrypt"
	"github.com/trendvidia/sops/v3/keys"
	"github.com/trendvidia/sops/v3/kms"
	"github.com/trendvidia/sops/v3/pgp"
)

// Recipients is the set of master keys an encrypted layer can be
// decrypted by. Mirrors chameleonv1.BundleManifest_RecipientSet's
// three fields, decoupled from the proto type so this package
// doesn't depend on the generated bindings.
//
// At least one recipient across all three fields is required for
// Encrypt to succeed. The fields are concatenated into a single
// sops.KeyGroup — every listed key can decrypt the resulting
// ciphertext on its own (no Shamir splitting).
type Recipients struct {
	// KMS is the comma-equivalent list of AWS KMS ARNs. Each entry
	// is one full ARN (arn:aws:kms:region:account:key/UUID); the
	// caller, not this package, is responsible for sourcing them.
	KMS []string

	// Age is the list of age recipients in the canonical
	// `age1...` Bech32 encoding. Use this for self-hosted or
	// air-gapped deployments where cloud KMS isn't an option.
	Age []string

	// PGP is the list of OpenPGP public-key fingerprints. Kept for
	// parity with sops's input surface; discouraged for new
	// deployments because gpg-agent state is operationally painful
	// compared to KMS / age.
	PGP []string
}

// IsEmpty reports whether r lists no recipients. Used by Encrypt to
// fail fast before invoking sops's pipeline (sops returns a less
// obvious "at least one KeyGroup" error otherwise).
func (r Recipients) IsEmpty() bool {
	return len(r.KMS) == 0 && len(r.Age) == 0 && len(r.PGP) == 0
}

// ErrEmptyPlaintext is returned when Encrypt is handed a zero-length
// plaintext. Sops itself accepts empty input but emits a structurally
// odd ciphertext envelope; failing loudly here surfaces caller bugs
// (e.g. an unset memguard buffer) at the encrypt boundary.
var ErrEmptyPlaintext = errors.New("sops: empty plaintext")

// ErrNoRecipients is returned when Encrypt is handed Recipients with
// no entries across KMS / Age / PGP. Matches sops's "at least one
// KeyGroup is required" check, with a less generic message.
var ErrNoRecipients = errors.New("sops: no recipients (need at least one KMS / age / PGP key)")

// Encrypter wraps trendvidia/sops/v3/encrypt for the in-process bundler
// pipeline. Implementations must be safe for concurrent use.
type Encrypter interface {
	// Encrypt produces sops ciphertext for plaintext under the
	// given recipients. The plaintext slice is wiped on success;
	// on error it is left intact and the caller may retry.
	//
	// The plaintext input may alias a memguard.LockedBuffer's
	// Bytes() — slices into mlocked memory pass by reference
	// without copy, so the plaintext stays mlock-resident
	// through encryption. Caller owns the LockedBuffer's
	// lifecycle (Encrypt does not Destroy it; the post-call
	// WipeBytes on success leaves the LockedBuffer valid but
	// zeroed, which is the appropriate state for an encrypted-
	// then-released secret).
	//
	// The returned ciphertext lives on the regular heap; it
	// carries no plaintext residue and does not need wiping.
	Encrypt(ctx context.Context, plaintext []byte, format Format, recipients Recipients) ([]byte, error)
}

// DefaultEncrypter returns an Encrypter wired to the in-process
// trendvidia/sops/v3 encryption stack. Credentials for KMS / age / PGP
// come from the ambient environment exactly as the sops CLI does
// (see the package doc on Decrypter for the variable list).
func DefaultEncrypter() Encrypter { return defaultEncrypter{} }

type defaultEncrypter struct{}

func (defaultEncrypter) Encrypt(ctx context.Context, plaintext []byte, format Format, recipients Recipients) ([]byte, error) {
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
func buildKeyGroup(r Recipients) (sops.KeyGroup, error) {
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
func sopsFormat(f Format) string {
	return f.String()
}
