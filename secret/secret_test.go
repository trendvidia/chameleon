// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secret_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/secret"
)

// TestSetMetadata_OverwritesUnconditionally locks in the contract:
// both arguments overwrite without a skip-when-empty short-circuit.
// Skip-when-empty semantics are the CALLER's responsibility (see
// parse.MoveInto.takeSecret). Drift in this contract would shift
// the responsibility silently and surprise downstream code.
func TestSetMetadata_OverwritesUnconditionally(t *testing.T) {
	s := secret.FromString("x", "old-hint", "old-fp")
	t.Cleanup(s.Destroy)

	// Empty arguments overwrite — they do NOT leave the prior
	// values intact.
	s.SetMetadata("", "")
	assert.Equal(t, "", s.Hint(),
		"empty hint should overwrite the prior 'old-hint'; caller is responsible for skip-when-empty")
	assert.Equal(t, "", s.Fingerprint(),
		"empty fingerprint should overwrite the prior 'old-fp'")

	// Subsequent non-empty SetMetadata replaces both fields together.
	s.SetMetadata("new-hint", "new-fp")
	assert.Equal(t, "new-hint", s.Hint())
	assert.Equal(t, "new-fp", s.Fingerprint())

	// Mixed: one empty, one non-empty — both are still applied
	// verbatim, no auto-merge with the prior values.
	s.SetMetadata("solo", "")
	assert.Equal(t, "solo", s.Hint())
	assert.Equal(t, "", s.Fingerprint(),
		"empty fingerprint must overwrite the 'new-fp' from the prior call")
}

// TestSetMetadata_NilReceiverIsNoOp — calling SetMetadata on a nil
// *Secret is safe (matches the nil-safety pattern of Hint() /
// Fingerprint() / Destroy()). Lets callers chain through nil
// secrets.Map.Get returns without an explicit nil check.
func TestSetMetadata_NilReceiverIsNoOp(t *testing.T) {
	var s *secret.Secret
	assert.NotPanics(t, func() {
		s.SetMetadata("hint", "fp")
	}, "SetMetadata on nil *Secret must not panic")
}

// TestSetMetadata_DoesNotAffectValue — hint/fingerprint mutation
// must not touch the memguard-backed enclave. After SetMetadata,
// Open returns the original plaintext unchanged.
func TestSetMetadata_DoesNotAffectValue(t *testing.T) {
	s := secret.FromString("supersecret", "", "")
	t.Cleanup(s.Destroy)

	s.SetMetadata("rotated 2026", "sha256:abc")

	buf, err := s.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "supersecret", string(buf.Bytes()),
		"SetMetadata should leave the enclave-backed value intact")
}

// TestOpen_RaceWithDestroyDoesNotPanic — concurrent Open / Destroy is
// documented as racy (Destroy may "win" and subsequent Opens return
// ErrDestroyed), but the in-flight Open must NEVER nil-deref the
// enclave. The implementation snapshots s.enclave to a local before
// calling memguard's Enclave.Open, so a Destroy interleaved between
// the nil-check and the dereference is safe. Run under -race to catch
// any reintroduction.
func TestOpen_RaceWithDestroyDoesNotPanic(t *testing.T) {
	const goroutines = 32
	for range goroutines {
		s := secret.FromString("payload", "", "")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			buf, err := s.Open()
			if err == nil {
				buf.Destroy()
			}
		}()
		go func() {
			defer wg.Done()
			s.Destroy()
		}()
		wg.Wait()
	}
}
