// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package sops bridges trendvidia/sops/v4 into chameleon's memguard-aware
// loading pipeline. The standard sops library returns plaintext as a
// regular []byte; this package immediately copies that into a
// memguard.LockedBuffer (which mlocks the page and wipes on Destroy)
// and wipes both the ciphertext and the transient plaintext slice.
//
// # Selecting this backend
//
// The package registers itself with chameleon/crypt from init, so a
// program that wants the default wiring links it with a blank import
// and never mentions it again:
//
//	import _ "github.com/trendvidia/chameleon/sops"
//
// The resolver picks the registered backend up the first time an
// encrypted layer appears. Nothing in chameleon imports this package
// on its own — that is the point. Importing it is what pulls in the
// AWS / Azure / GCP / Vault key-management tree, which is why the
// resolver takes crypt.Decrypter rather than anything from here
// (trendvidia/chameleon#130).
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
// like the sops CLI does. For age-encrypted files the four-source
// contract is identical on encrypt and decrypt sides (CLI flag values
// override env; env overrides default file):
//
//	SOPS_AGE_KEY_FILE                    path to a key file (PXF if .pxf, else line-based)
//	SOPS_AGE_KEY                         inline key material (PXF-sniffed or line-based)
//	SOPS_AGE_KEY_CMD                     command whose stdout is a key file (sniffed)
//	$HOME/.config/sops/age/keys.pxf      standardized default (PXF only, exists-gated)
//
// Plus a decrypt-only side channel:
//
//	SOPS_AGE_SSH_PRIVATE_KEY_FILE        SSH-derived
//	SOPS_AGE_SSH_PRIVATE_KEY_CMD         command emitting an SSH private key
//	~/.ssh/id_ed25519, ~/.ssh/id_rsa     default SSH-derived
//
// For KMS / GCP-KMS / Azure-KV / Vault, sops uses ambient cloud
// credentials. For PGP, gpg-agent. None of these are configurable
// through this package — that's a deliberate non-goal. If you need
// programmatic key injection, implement crypt.Decrypter yourself and
// pass it to resolver.WithDecrypter (or register it under your own
// name).
package sops

import (
	"context"
	"errors"
	"fmt"

	"github.com/awnumar/memguard"
	"github.com/trendvidia/sops/v4/decrypt"

	"github.com/trendvidia/chameleon/crypt"
)

// ErrEmptyCiphertext is returned by [crypt.Decrypter.Decrypt] when handed an
// empty byte slice. Sops itself errors on this with a less obvious
// message.
var ErrEmptyCiphertext = errors.New("sops: empty ciphertext")

// ErrPlaintextTooLarge is returned when decrypted plaintext would
// exceed the memguard sink's hard ceiling. The resolver caps ciphertext
// upstream via [resolver.ErrLayerTooLarge]; this is defense-in-depth
// for direct [crypt.Decrypter.Decrypt] callers and guards the growth loop
// against int overflow.
var ErrPlaintextTooLarge = errors.New("sops: decrypted plaintext exceeds size limit")

// Default returns a [crypt.Decrypter] wired to the in-process
// trendvidia/sops/v4 decryption stack. Credentials are sourced from
// the ambient environment (see package doc).
//
// Importing this package for its side effect registers the same
// Decrypter under the name "sops", so a program that only needs the
// default wiring can blank-import instead of calling this — see the
// package doc.
func Default() crypt.Decrypter { return defaultDecrypter{} }

type defaultDecrypter struct{}

func (defaultDecrypter) Decrypt(ctx context.Context, ciphertext []byte, format crypt.Format) (*memguard.LockedBuffer, error) {
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
