// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/sops"
)

// --- Decrypter wrapper ---

func TestDefault_EmptyCiphertext(t *testing.T) {
	d := sops.Default()
	_, err := d.Decrypt(context.Background(), []byte{}, crypt.FormatYAML)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sops.ErrEmptyCiphertext))
}

func TestDefault_GarbageBytes(t *testing.T) {
	// Sops will reject non-sops bytes with its own metadata-not-found
	// error. Verify the chameleon wrapper propagates as wrapped error.
	d := sops.Default()
	_, err := d.Decrypt(context.Background(), []byte("not a sops file"), crypt.FormatYAML)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sops: decrypt:",
		"wrapper must annotate sops errors with the package prefix")
}

func TestDefault_RespectsContextCancellation(t *testing.T) {
	d := sops.Default()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := d.Decrypt(ctx, []byte("any-non-empty-bytes"), crypt.FormatYAML)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled),
		"a pre-cancelled context must short-circuit before sops is invoked")
}
