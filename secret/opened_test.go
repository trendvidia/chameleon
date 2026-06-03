// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secret_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/secret"
)

// OpenedBuffer.Bytes / Destroy are exercised end-to-end in
// instruments_test.go via the Map → Secret → Open flow; this file
// pins the only direct gap: Locked(), which returns the underlying
// memguard.LockedBuffer for callers that need to pass it to APIs
// typed in terms of memguard. A regression that returned nil or a
// different buffer would not have surfaced anywhere else.

func TestOpenedBuffer_Locked_ReturnsUnderlyingBuffer(t *testing.T) {
	m := secret.NewMap()
	m.Set("api_key", secret.FromString("locked-test-payload", "", ""))

	buf, err := m.Get("api_key").Open()
	require.NoError(t, err)
	defer buf.Destroy()

	locked := buf.Locked()
	require.NotNil(t, locked, "Locked() must return a non-nil memguard buffer")

	// The two views must point at the same bytes. memguard's
	// LockedBuffer.Bytes returns the SAME backing slice as the
	// fresh OpenedBuffer.Bytes call — assert byte equality
	// (pointer equality is a memguard internal we don't pin here).
	assert.Equal(t, string(buf.Bytes()), string(locked.Bytes()),
		"Locked().Bytes() must match OpenedBuffer.Bytes()")
	assert.Equal(t, "locked-test-payload", string(locked.Bytes()),
		"Locked().Bytes() should carry the original plaintext")
}

func TestOpenedBuffer_Locked_StillValidAfterMultipleCalls(t *testing.T) {
	// Locked() is documented to return the underlying buffer with
	// a lifetime tied to the OpenedBuffer. Calling it repeatedly
	// must not invalidate prior returns nor create a new buffer.
	// (Implementation is `return o.buf`, but the contract is the
	// load-bearing piece here.)
	m := secret.NewMap()
	m.Set("api_key", secret.FromString("multi-call", "", ""))
	buf, err := m.Get("api_key").Open()
	require.NoError(t, err)
	defer buf.Destroy()

	first := buf.Locked()
	second := buf.Locked()
	if first != second {
		t.Errorf("Locked() must return the same buffer on repeated calls")
	}
}
