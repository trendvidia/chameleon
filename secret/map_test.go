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

func TestMap_GetSetLen(t *testing.T) {
	m := secret.NewMap()
	require.Equal(t, 0, m.Len())

	m.Set("a", secret.FromString("alpha", "", ""))
	m.Set("b", secret.FromString("beta", "", ""))
	require.Equal(t, 2, m.Len())

	assert.True(t, m.Has("a"))
	assert.False(t, m.Has("missing"))
	assert.NotNil(t, m.Get("a"))
	assert.Nil(t, m.Get("missing"))

	m.Destroy()
}

func TestMap_SetReplacesAndDestroysOld(t *testing.T) {
	m := secret.NewMap()
	old := secret.FromString("v1", "", "")
	m.Set("k", old)

	m.Set("k", secret.FromString("v2", "", ""))

	// The old Secret was Destroy'd in place when overwritten — Open
	// returns ErrDestroyed.
	_, err := old.Open()
	assert.ErrorIs(t, err, secret.ErrDestroyed)
}

func TestMap_DestroyClearsAll(t *testing.T) {
	m := secret.NewMap()
	m.Set("a", secret.FromString("alpha", "", ""))
	m.Set("b", secret.FromString("beta", "", ""))

	m.Destroy()

	assert.Equal(t, 0, m.Len())
	assert.Nil(t, m.Get("a"))
	assert.False(t, m.Has("a"))
}

func TestMap_DestroyIdempotent(t *testing.T) {
	m := secret.NewMap()
	m.Set("a", secret.FromString("alpha", "", ""))

	m.Destroy()
	m.Destroy() // second call is a no-op, must not panic
	m.Destroy() // third for good measure
}

func TestMap_SetAfterDestroyDestroysIncoming(t *testing.T) {
	m := secret.NewMap()
	m.Destroy()

	s := secret.FromString("late", "", "")
	m.Set("k", s) // map is closed; Set destroys s rather than retaining

	_, err := s.Open()
	assert.ErrorIs(t, err, secret.ErrDestroyed,
		"Set on a destroyed Map must wipe its argument so plaintext can't outlive the Map")
	assert.Nil(t, m.Get("k"))
}

func TestMap_NilReceiverSafe(t *testing.T) {
	var m *secret.Map
	assert.Nil(t, m.Get("anything"))
	assert.False(t, m.Has("anything"))
	assert.Equal(t, 0, m.Len())
	m.Range(func(string, *secret.Secret) bool { return true })
	m.Destroy()
	m.Set("k", secret.FromString("x", "", ""))
}

func TestMap_RangeSnapshotsEntries(t *testing.T) {
	m := secret.NewMap()
	m.Set("a", secret.FromString("alpha", "", ""))
	m.Set("b", secret.FromString("beta", "", ""))

	seen := map[string]bool{}
	m.Range(func(path string, s *secret.Secret) bool {
		seen[path] = true
		// Range snapshots — calling back into Map methods is safe.
		assert.True(t, m.Has(path))
		return true
	})
	assert.Equal(t, map[string]bool{"a": true, "b": true}, seen)

	m.Destroy()
}

func TestMap_RangeEarlyExit(t *testing.T) {
	m := secret.NewMap()
	for _, k := range []string{"a", "b", "c", "d"} {
		m.Set(k, secret.FromString(k, "", ""))
	}

	count := 0
	m.Range(func(path string, s *secret.Secret) bool {
		count++
		return false // stop after the first
	})
	assert.Equal(t, 1, count)

	m.Destroy()
}

// --- concurrency: the P1 fix this file exists to verify ---

func TestMap_ConcurrentDestroyIsSafe(t *testing.T) {
	m := secret.NewMap()
	for i := range 32 {
		m.Set(string(rune('a'+i)), secret.FromString("x", "", ""))
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Destroy()
		}()
	}
	wg.Wait()
	// Test passes if we didn't panic on a double-Destroy of an
	// already-wiped memguard Enclave.
}

func TestMap_ConcurrentReadDuringDestroy(t *testing.T) {
	m := secret.NewMap()
	for i := range 64 {
		m.Set(string(rune('a'+i%26))+string(rune('A'+i%26)), secret.FromString("x", "", ""))
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = m.Get("aA")
				_ = m.Has("bB")
				_ = m.Len()
				m.Range(func(string, *secret.Secret) bool { return true })
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Destroy()
	}()
	wg.Wait()
	// Test passes if -race doesn't fire and no Get/Range observed
	// torn state. After Destroy, all reads return nil/false/0 — this
	// is verified post-wait.
	assert.Equal(t, 0, m.Len())
}

func TestMap_ConcurrentSetAndDestroy(t *testing.T) {
	m := secret.NewMap()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 50 {
				m.Set(string(rune('a'+i))+string(rune('0'+j%10)),
					secret.FromString("v", "", ""))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Destroy()
	}()
	wg.Wait()
	// Test passes if neither -race fires nor a panic from
	// double-Destroy of the same Secret. After Destroy, Set destroys
	// its argument, so no Secret leaks.
}
