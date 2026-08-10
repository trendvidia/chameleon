// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/sops"
)

// Importing this package must be enough to make the backend
// selectable — that is the whole contract a consumer relies on when
// they write `import _ "github.com/trendvidia/chameleon/sops"`. This
// test file imports sops normally, which runs the same init.

func TestInit_RegistersBackend(t *testing.T) {
	assert.Contains(t, crypt.Registered(), sops.BackendName,
		"importing chameleon/sops must register it with the crypt registry")

	b, err := crypt.Get(sops.BackendName)
	require.NoError(t, err)
	assert.NotNil(t, b.Decrypter, "the sops backend decrypts")
	assert.NotNil(t, b.Encrypter, "and encrypts")
}

func TestInit_RegisteredBackendIsTheDefaultWiring(t *testing.T) {
	b, err := crypt.Get(sops.BackendName)
	require.NoError(t, err)
	assert.Equal(t, sops.Default(), b.Decrypter,
		"the registered Decrypter must be the same wiring Default() hands out")
	assert.Equal(t, sops.DefaultEncrypter(), b.Encrypter,
		"likewise for DefaultEncrypter()")
}

func TestInit_IsTheSoleDecrypterInASopsBuild(t *testing.T) {
	// A build that links exactly this backend is the case the
	// resolver's auto-selection is defined for.
	d, name, err := crypt.SoleDecrypter()
	require.NoError(t, err)
	assert.Equal(t, sops.BackendName, name)
	assert.Equal(t, sops.Default(), d)
}
