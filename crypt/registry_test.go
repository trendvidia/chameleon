// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package crypt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/awnumar/memguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/crypt"
)

// The registry is process-global, so every test here registers under
// its own name and unregisters on cleanup. None of them call
// t.Parallel: two tests mutating the same map would make
// SoleDecrypter's "how many are linked in" answer depend on
// scheduling.

type fakeDecrypter struct{ tag string }

func (f fakeDecrypter) Decrypt(_ context.Context, ciphertext []byte, _ crypt.Format) (*memguard.LockedBuffer, error) {
	return memguard.NewBufferFromBytes(ciphertext), nil
}

type fakeEncrypter struct{ tag string }

func (f fakeEncrypter) Encrypt(_ context.Context, plaintext []byte, _ crypt.Format, _ crypt.Recipients) ([]byte, error) {
	return plaintext, nil
}

// register installs b under name for the duration of the test.
func register(t *testing.T, name string, b crypt.Backend) {
	t.Helper()
	crypt.Register(name, b)
	t.Cleanup(func() { crypt.Unregister(name) })
}

func TestRegister_GetRoundTrip(t *testing.T) {
	register(t, "rt", crypt.Backend{Decrypter: fakeDecrypter{tag: "d"}, Encrypter: fakeEncrypter{tag: "e"}})

	got, err := crypt.Get("rt")
	require.NoError(t, err)
	assert.Equal(t, fakeDecrypter{tag: "d"}, got.Decrypter)
	assert.Equal(t, fakeEncrypter{tag: "e"}, got.Encrypter)
}

func TestGet_UnknownNamesWhatIsRegistered(t *testing.T) {
	register(t, "present", crypt.Backend{Decrypter: fakeDecrypter{}})

	_, err := crypt.Get("absent")
	require.Error(t, err)
	assert.ErrorIs(t, err, crypt.ErrUnknownBackend)
	// The point of the error is telling a caller what the build DID
	// link — a bare "unknown backend" leaves them guessing whether
	// they typo'd the name or forgot the import.
	assert.Contains(t, err.Error(), `"absent"`)
	assert.Contains(t, err.Error(), "present")
}

func TestGet_UnknownOnEmptyRegistrySaysNone(t *testing.T) {
	require.Empty(t, crypt.Registered(), "test binary must link no backends")

	_, err := crypt.Get("sops")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registered: none")
}

func TestRegistered_SortedAndComplete(t *testing.T) {
	register(t, "zulu", crypt.Backend{Decrypter: fakeDecrypter{}})
	register(t, "alpha", crypt.Backend{Decrypter: fakeDecrypter{}})
	register(t, "mike", crypt.Backend{Encrypter: fakeEncrypter{}})

	assert.Equal(t, []string{"alpha", "mike", "zulu"}, crypt.Registered(),
		"Registered lists every backend, encrypt-only included, in sorted order")
}

func TestUnregister_ReportsWhetherItWasThere(t *testing.T) {
	crypt.Register("temp", crypt.Backend{Decrypter: fakeDecrypter{}})
	assert.True(t, crypt.Unregister("temp"), "first Unregister removes it")
	assert.False(t, crypt.Unregister("temp"), "second finds nothing to remove")
	assert.Empty(t, crypt.Registered())
}

func TestRegister_PanicsOnBuildTimeMistakes(t *testing.T) {
	assert.Panics(t, func() { crypt.Register("", crypt.Backend{Decrypter: fakeDecrypter{}}) },
		"empty name")
	assert.Panics(t, func() { crypt.Register("empty", crypt.Backend{}) },
		"Backend with neither half")

	register(t, "dup", crypt.Backend{Decrypter: fakeDecrypter{}})
	assert.Panics(t, func() { crypt.Register("dup", crypt.Backend{Decrypter: fakeDecrypter{}}) },
		"duplicate name")
}

func TestSoleDecrypter_NoneRegistered(t *testing.T) {
	require.Empty(t, crypt.Registered())

	_, _, err := crypt.SoleDecrypter()
	assert.ErrorIs(t, err, crypt.ErrNoBackend)
}

func TestSoleDecrypter_OneRegistered(t *testing.T) {
	register(t, "only", crypt.Backend{Decrypter: fakeDecrypter{tag: "the one"}})

	d, name, err := crypt.SoleDecrypter()
	require.NoError(t, err)
	assert.Equal(t, "only", name)
	assert.Equal(t, fakeDecrypter{tag: "the one"}, d)
}

func TestSoleDecrypter_SkipsEncryptOnlyBackends(t *testing.T) {
	// An encrypt-only backend is not a candidate: two backends are
	// registered, but only one can decrypt, so auto-selection is
	// still unambiguous.
	register(t, "writer", crypt.Backend{Encrypter: fakeEncrypter{}})
	register(t, "reader", crypt.Backend{Decrypter: fakeDecrypter{tag: "reader"}})

	d, name, err := crypt.SoleDecrypter()
	require.NoError(t, err)
	assert.Equal(t, "reader", name)
	assert.Equal(t, fakeDecrypter{tag: "reader"}, d)
}

func TestSoleDecrypter_EncryptOnlyIsNotADecrypter(t *testing.T) {
	register(t, "writer", crypt.Backend{Encrypter: fakeEncrypter{}})

	_, _, err := crypt.SoleDecrypter()
	assert.ErrorIs(t, err, crypt.ErrNoBackend,
		"a build with only an encrypt-only backend can't decrypt anything")
	// ...but it did link something, and saying otherwise sends the
	// operator off to add an import they already have. The resolver
	// appends "link one with import _ …/sops" to this; the list is
	// what tells them the real gap is the decrypt half.
	assert.Contains(t, err.Error(), "writer",
		"name what the build does have when it has anything at all")
}

func TestSoleDecrypter_EmptyRegistryDoesNotSayRegisteredNone(t *testing.T) {
	require.Empty(t, crypt.Registered())

	_, _, err := crypt.SoleDecrypter()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "registered,",
		`"no decryption backend registered (registered: none)" only repeats itself`)
}

func TestSoleDecrypter_AmbiguousNamesTheCandidates(t *testing.T) {
	register(t, "beta", crypt.Backend{Decrypter: fakeDecrypter{}})
	register(t, "alpha", crypt.Backend{Decrypter: fakeDecrypter{}})

	_, _, err := crypt.SoleDecrypter()
	require.Error(t, err)
	assert.ErrorIs(t, err, crypt.ErrAmbiguousBackend)
	assert.Contains(t, err.Error(), "alpha, beta",
		"the error must name the candidates, sorted, so the caller can pick one")
	assert.False(t, errors.Is(err, crypt.ErrNoBackend),
		"ambiguous and absent are different failures with opposite fixes")
}
