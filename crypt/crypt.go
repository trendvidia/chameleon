// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package crypt is the boundary between the resolver and whatever
// actually decrypts an encrypted layer. It holds the [Decrypter] and
// [Encrypter] contracts, the [Format] hint, the [Recipients] set, and
// a registry of named backends — and nothing else. Its own dependency
// set is the standard library plus memguard.
//
// That last sentence is the whole point. Until v0.23.0 the resolver
// imported chameleon/sops directly for the Decrypter interface, so
// every consumer linked the entire sops key-management tree — AWS,
// Azure, GCP, Vault, Huawei KMS and what those drag behind them —
// whether or not a single layer was encrypted. A program resolving two
// plaintext layers paid 56 MB of cloud SDKs for a capability it never
// called (trendvidia/chameleon#130). The interface now lives where it
// is consumed; the implementation is something a build opts into.
//
// # Linking a backend
//
// The registration model is the one database/sql uses for drivers:
// the implementation package registers itself from init, and a program
// selects it with a blank import.
//
//	import (
//	    "github.com/trendvidia/chameleon/resolver"
//	    _ "github.com/trendvidia/chameleon/sops" // registers the "sops" backend
//	)
//
//	r, err := resolver.New[*AppConfig](&AppConfig{}, spec,
//	    resolver.WithSource("default", source.FS(os.DirFS("./layers"))),
//	)
//
// With exactly one backend linked in, the resolver picks it up for
// encrypted layers with no further wiring — see [SoleDecrypter]. A
// program that links more than one names the one it wants with
// resolver.WithDecrypterNamed; a program that constructs its own
// Decrypter (programmatic key injection, a test double, an HSM) keeps
// passing it to resolver.WithDecrypter and can ignore the registry
// entirely.
//
// A backend reaches a build only through an import, so "linked in"
// is decided by the module graph, not by configuration — including
// through a dependency that imports chameleon/sops for its own
// reasons. That is the same trade database/sql makes, and the reason
// [Registered] exists: it reports what a build actually ended up with.
package crypt

import (
	"context"

	"github.com/awnumar/memguard"
)

// Decrypter is the in-process decryption hook the resolver calls for
// every layer whose chain entry declares `encrypted = true`.
// Implementations must be safe for concurrent use.
type Decrypter interface {
	// Decrypt consumes ciphertext and returns plaintext sealed in a
	// memguard.LockedBuffer. The ciphertext slice is wiped on success;
	// on error it is left intact and the caller may retry. The caller
	// owns the returned LockedBuffer — Destroy when done.
	Decrypt(ctx context.Context, ciphertext []byte, format Format) (*memguard.LockedBuffer, error)
}

// Encrypter is the write-side counterpart, used by the bundler and the
// CLI's re-encrypting subcommands (`bundle`, `migrate`, `rotate-key`).
// The resolver never encrypts. Implementations must be safe for
// concurrent use.
type Encrypter interface {
	// Encrypt produces ciphertext for plaintext under the given
	// recipients. The plaintext slice is wiped on success; on error
	// it is left intact and the caller may retry.
	//
	// The plaintext input may alias a memguard.LockedBuffer's
	// Bytes() — slices into mlocked memory pass by reference
	// without copy, so the plaintext stays mlock-resident through
	// encryption. Caller owns the LockedBuffer's lifecycle (Encrypt
	// does not Destroy it; the post-call wipe on success leaves the
	// LockedBuffer valid but zeroed, which is the appropriate state
	// for an encrypted-then-released secret).
	//
	// The returned ciphertext lives on the regular heap; it carries
	// no plaintext residue and does not need wiping.
	Encrypt(ctx context.Context, plaintext []byte, format Format, recipients Recipients) ([]byte, error)
}

// Recipients is the set of master keys an encrypted layer can be
// decrypted by. Mirrors chameleonv1.BundleManifest_RecipientSet's
// three fields, decoupled from the proto type so this package
// doesn't depend on the generated bindings.
//
// At least one recipient across all three fields is required for
// [Encrypter.Encrypt] to succeed. A backend is expected to treat the
// fields as one flat set — every listed key can decrypt the resulting
// ciphertext on its own (no Shamir splitting).
type Recipients struct {
	// KMS is the list of AWS KMS ARNs. Each entry is one full ARN
	// (arn:aws:kms:region:account:key/UUID); the caller, not the
	// backend, is responsible for sourcing them.
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

// IsEmpty reports whether r lists no recipients. Used by backends to
// fail fast before invoking an encryption pipeline.
func (r Recipients) IsEmpty() bool {
	return len(r.KMS) == 0 && len(r.Age) == 0 && len(r.PGP) == 0
}
