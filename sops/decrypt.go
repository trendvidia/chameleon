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

	// FormatBinary is sops's opaque-byte-string store. The plaintext
	// is treated as a single value; layers using this format produce
	// raw bytes, not field-structured PXF.
	FormatBinary

	// FormatDotenv is the `KEY=VALUE` newline-delimited store. Layer
	// content is a flat key-value list; sops encrypts the values.
	FormatDotenv

	// FormatINI is the classic `[section]\nkey=value` store. Sops
	// encrypts string values; sections and keys stay clear.
	FormatINI

	// FormatJSON is sops's JSON store. Layer content is a JSON
	// object; sops encrypts leaf string values and any field
	// matching the encrypted_regex / encrypted_suffix rules.
	FormatJSON

	// FormatYAML is sops's YAML store. Same encryption semantics as
	// FormatJSON; comment-preserving round-trip via sops's YAML
	// codec.
	FormatYAML

	// FormatProtowire is the PXF text format (recognized by sops as
	// "protowire" or "pxf"). The canonical store for chameleon
	// layers; provides field-typed encryption with PXF's
	// presence-aware grammar.
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

// ErrPlaintextTooLarge is returned when decrypted plaintext would
// exceed the memguard sink's hard ceiling. The resolver caps ciphertext
// upstream via [resolver.ErrLayerTooLarge]; this is defense-in-depth
// for direct [Decrypter.Decrypt] callers and guards the growth loop
// against int overflow.
var ErrPlaintextTooLarge = errors.New("sops: decrypted plaintext exceeds size limit")

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

	// Stream plaintext directly into a memguard-locked buffer via the
	// fork's decrypt.DataIntoWriterContext (trendvidia/sops v3.13.2+).
	// The previous decrypt.Data + NewBufferFromBytes flow had a
	// transient []byte on the regular Go heap between sops returning
	// and memguard copying — closing that gap is the load-bearing
	// piece of trendvidia/chameleon#9 phase 1.
	//
	// ctx threads end-to-end through sops's keyservice + KMS layers;
	// a deadlined ctx aborts in-flight KMS round-trips instead of
	// blocking until the provider's internal timeout.
	w := &memguardWriter{}
	if err := decrypt.DataIntoWriterContext(ctx, w, ciphertext, format.String()); err != nil {
		w.Destroy()
		return nil, fmt.Errorf("sops: decrypt: %w", err)
	}

	memguard.WipeBytes(ciphertext)
	return w.finalize(), nil
}

// memguardWriter is an io.Writer whose Write calls land bytes inside a
// memguard.LockedBuffer. Used as the sink for
// decrypt.DataIntoWriterContext so plaintext never resides on the
// regular Go heap during the sops → chameleon handoff.
//
// Growth strategy: start with a small mlocked buffer and grow by
// allocating a larger one + copying when full. memguard buffers are
// fixed-size, so growth requires alloc + copy + destroy old. Doubles
// each time.
type memguardWriter struct {
	buf *memguard.LockedBuffer
	n   int
}

const (
	initialMemguardSize = 4096
	// maxMemguardSize caps a single plaintext at 1 GiB. The doubling
	// growth loop below would otherwise wrap int on pathological input;
	// in practice the resolver's per-layer ceiling fires far earlier,
	// so callers reaching this cap are almost certainly buggy or hostile.
	maxMemguardSize = 1 << 30
)

func (w *memguardWriter) Write(p []byte) (int, error) {
	need := w.n + len(p)
	if need < w.n || need > maxMemguardSize {
		return 0, ErrPlaintextTooLarge
	}
	if w.buf == nil {
		size := initialMemguardSize
		if len(p) > size {
			size = len(p)
		}
		w.buf = memguard.NewBuffer(size)
	}
	for need > w.buf.Size() {
		newSize := w.buf.Size() * 2
		for newSize < need {
			newSize *= 2
		}
		if newSize > maxMemguardSize {
			newSize = maxMemguardSize
		}
		next := memguard.NewBuffer(newSize)
		copy(next.Bytes(), w.buf.Bytes()[:w.n])
		w.buf.Destroy()
		w.buf = next
	}
	copy(w.buf.Bytes()[w.n:], p)
	w.n += len(p)
	return len(p), nil
}

// finalize trims the buffer to the exact bytes written and returns it.
// Caller takes ownership.
func (w *memguardWriter) finalize() *memguard.LockedBuffer {
	if w.buf == nil {
		return memguard.NewBuffer(0)
	}
	if w.n == w.buf.Size() {
		buf := w.buf
		w.buf = nil
		return buf
	}
	trimmed := memguard.NewBufferFromBytes(w.buf.Bytes()[:w.n])
	w.buf.Destroy()
	w.buf = nil
	return trimmed
}

// Destroy wipes any partial state. Safe to call on a Writer that has
// never been written to.
func (w *memguardWriter) Destroy() {
	if w.buf != nil {
		w.buf.Destroy()
		w.buf = nil
	}
}
