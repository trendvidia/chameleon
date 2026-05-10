// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package sops bridges getsops/sops/v3 into chameleon's memguard-aware
// loading pipeline. The standard sops library returns plaintext as a
// regular []byte; this package immediately copies that into a
// memguard.LockedBuffer (which mlocks the page and wipes on Destroy)
// and wipes both the ciphertext and the transient plaintext slice.
//
// Best-effort caveat: sops itself reads the entire ciphertext into a
// regular []byte to verify its MAC, and produces plaintext as a Go
// []byte. We can't avoid those allocations — only narrow the window in
// which they exist. This package wipes both as soon as the data is
// safely in the LockedBuffer; what's left is the GC heap residue from
// sops's intermediate allocations, which is the same caveat documented
// for parse.Move.
//
// Credentials. Sops sources keys from the ambient environment exactly
// like the sops CLI does. For age-encrypted files:
//
//	SOPS_AGE_KEY            inline private keys (comma-separated)
//	SOPS_AGE_KEY_FILE       path to keys.txt
//	SOPS_AGE_KEY_CMD        shell command that emits keys
//	~/.config/sops/age/keys.txt          (XDG default)
//	SOPS_AGE_SSH_PRIVATE_KEY_FILE        SSH-derived
//
// For KMS / GCP-KMS / Azure-KV / Vault, sops uses ambient cloud
// credentials. For PGP, gpg-agent. None of these are configurable
// through this package — that's a deliberate non-goal. If you need
// programmatic key injection, build a custom Decrypter.
package sops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/awnumar/memguard"
	"github.com/getsops/sops/v3/decrypt"
)

// Format identifies the sops store backend. Values mirror sops's
// internal `formats.Format` enum so callers don't have to import the
// sops cmd subpackage.
type Format int

const (
	// FormatAuto defers to sops's own auto-detection. Useful when
	// neither the path nor an explicit format is known.
	FormatAuto Format = iota
	FormatBinary
	FormatDotenv
	FormatINI
	FormatJSON
	FormatYAML
	// FormatProtowire is the PXF text format (recognized by sops as
	// "protowire" or "pxf").
	FormatProtowire
)

// String returns the canonical string spelling sops accepts for this
// format. FormatAuto returns "" so [decrypt.Data]'s "infer from MIME"
// path engages.
func (f Format) String() string {
	switch f {
	case FormatBinary:
		return "binary"
	case FormatDotenv:
		return "dotenv"
	case FormatINI:
		return "ini"
	case FormatJSON:
		return "json"
	case FormatYAML:
		return "yaml"
	case FormatProtowire:
		return "pxf"
	default:
		return ""
	}
}

// FormatForPath infers a Format from a filename extension. Returns
// FormatAuto for unrecognized extensions; sops itself will then
// auto-detect from the ciphertext envelope.
//
// Recognized extensions match sops's defaults: .json, .yaml, .yml,
// .env, .ini, .pxf, .bin (binary).
func FormatForPath(path string) Format {
	low := strings.ToLower(path)
	// Strip any sops convention suffix like ".sops.yaml" — the
	// rightmost extension is what matters.
	if i := strings.LastIndex(low, "."); i >= 0 {
		switch low[i+1:] {
		case "json":
			return FormatJSON
		case "yaml", "yml":
			return FormatYAML
		case "env":
			return FormatDotenv
		case "ini":
			return FormatINI
		case "pxf":
			return FormatProtowire
		case "bin":
			return FormatBinary
		}
	}
	return FormatAuto
}

// ErrEmptyCiphertext is returned by [Decrypter.Decrypt] when handed an
// empty byte slice. Sops itself errors on this with a less obvious
// message.
var ErrEmptyCiphertext = errors.New("sops: empty ciphertext")

// Decrypter is the in-process sops decryption hook used by the
// resolver. Implementations must be safe for concurrent use.
type Decrypter interface {
	// Decrypt consumes ciphertext and returns plaintext sealed in a
	// memguard.LockedBuffer. The ciphertext slice is wiped on success;
	// on error it is left intact and the caller may retry. The caller
	// owns the returned LockedBuffer — Destroy when done.
	Decrypt(ctx context.Context, ciphertext []byte, format Format) (*memguard.LockedBuffer, error)
}

// Default returns a Decrypter wired to the in-process getsops/sops/v3
// decryption stack. Credentials are sourced from the ambient
// environment (see package doc).
func Default() Decrypter { return defaultDecrypter{} }

type defaultDecrypter struct{}

func (defaultDecrypter) Decrypt(ctx context.Context, ciphertext []byte, format Format) (*memguard.LockedBuffer, error) {
	if len(ciphertext) == 0 {
		return nil, ErrEmptyCiphertext
	}
	// Honor caller cancellation. sops's decrypt API has no ctx hook,
	// so we check ctx before the (potentially slow) cloud-KMS round-
	// trip and let the call run to completion afterward.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	plaintext, err := decrypt.Data(ciphertext, format.String())
	if err != nil {
		return nil, fmt.Errorf("sops: decrypt: %w", err)
	}

	// NewBufferFromBytes copies plaintext into the locked region and
	// wipes the source slice. Ciphertext is no longer needed; wipe.
	buf := memguard.NewBufferFromBytes(plaintext)
	memguard.WipeBytes(ciphertext)
	return buf, nil
}
