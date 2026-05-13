// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

// These are white-box tests for the memguardWriter — they live in the
// internal `sops` (not `sops_test`) package because memguardWriter is
// not exported. The Writer is a load-bearing implementation detail of
// the Decrypter and warrants direct testing rather than only the
// integration coverage from decrypt_test.go.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemguardWriter_SingleWrite(t *testing.T) {
	w := &memguardWriter{}
	defer w.Destroy()

	payload := []byte("hello")
	n, err := w.Write(payload)
	require.NoError(t, err)
	assert.Equal(t, len(payload), n)

	buf := w.finalize()
	defer buf.Destroy()
	assert.Equal(t, payload, buf.Bytes())
}

func TestMemguardWriter_MultipleWrites(t *testing.T) {
	w := &memguardWriter{}
	defer w.Destroy()

	for _, chunk := range [][]byte{[]byte("hel"), []byte("lo "), []byte("world")} {
		n, err := w.Write(chunk)
		require.NoError(t, err)
		assert.Equal(t, len(chunk), n)
	}

	buf := w.finalize()
	defer buf.Destroy()
	assert.Equal(t, []byte("hello world"), buf.Bytes())
}

// TestMemguardWriter_GrowsBeyondInitialSize forces the buffer to grow
// past the initial 4KiB allocation. The result must contain the full
// payload byte-for-byte.
func TestMemguardWriter_GrowsBeyondInitialSize(t *testing.T) {
	w := &memguardWriter{}
	defer w.Destroy()

	// 10 KiB — forces at least two doublings from the 4KiB initial.
	payload := bytes.Repeat([]byte("abcdef0123"), 1024)
	require.Equal(t, 10240, len(payload))

	// Write in chunks of varying sizes to exercise the grow loop.
	for off := 0; off < len(payload); off += 700 {
		end := off + 700
		if end > len(payload) {
			end = len(payload)
		}
		_, err := w.Write(payload[off:end])
		require.NoError(t, err)
	}

	buf := w.finalize()
	defer buf.Destroy()
	assert.Equal(t, payload, buf.Bytes())
}

// TestMemguardWriter_FirstWriteLargerThanInitial covers the
// "first chunk exceeds initialMemguardSize" path in Write.
func TestMemguardWriter_FirstWriteLargerThanInitial(t *testing.T) {
	w := &memguardWriter{}
	defer w.Destroy()

	payload := []byte(strings.Repeat("x", 8192)) // 2× initialMemguardSize
	_, err := w.Write(payload)
	require.NoError(t, err)

	buf := w.finalize()
	defer buf.Destroy()
	assert.Equal(t, payload, buf.Bytes())
}

// TestMemguardWriter_FinalizeOnEmptyWriter returns a zero-length
// LockedBuffer without panicking — the Decrypter relies on this when a
// decrypt fails before any bytes are written.
func TestMemguardWriter_FinalizeOnEmptyWriter(t *testing.T) {
	w := &memguardWriter{}
	buf := w.finalize()
	defer buf.Destroy()
	assert.Equal(t, 0, buf.Size())
}

// TestMemguardWriter_DestroyOnPartialStateIdempotent ensures Destroy
// after a partial Write doesn't panic and is safely re-callable.
func TestMemguardWriter_DestroyOnPartialStateIdempotent(t *testing.T) {
	w := &memguardWriter{}
	_, err := w.Write([]byte("partial"))
	require.NoError(t, err)

	w.Destroy()
	w.Destroy() // second call must be safe (idempotent)
	w.Destroy()
}

// TestMemguardWriter_RejectsOversizedPlaintext checks the
// defense-in-depth cap. A single Write whose payload exceeds
// maxMemguardSize must return ErrPlaintextTooLarge without ever
// allocating an mlocked buffer (the cap fires before w.buf is
// touched), so no Destroy is needed on the writer for this path.
func TestMemguardWriter_RejectsOversizedPlaintext(t *testing.T) {
	w := &memguardWriter{}
	// We need len(p) > maxMemguardSize but allocating that much would
	// be wasteful. Drive the check via two writes: a small first write
	// then a second one whose w.n+len(p) exceeds the cap.
	_, err := w.Write([]byte("seed"))
	require.NoError(t, err)
	defer w.Destroy()

	// Construct a slice header that lies about its length without
	// actually allocating maxMemguardSize bytes. We can't do that in
	// safe Go, so fall back to a smaller cap-violating payload: write
	// up to the cap via a stub by directly setting w.n.
	w.n = maxMemguardSize - 1
	_, err = w.Write([]byte("xx")) // need = maxMemguardSize+1
	require.ErrorIs(t, err, ErrPlaintextTooLarge)
}
