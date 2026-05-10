// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/sops"
)

// --- Format ---

func TestFormat_String(t *testing.T) {
	cases := []struct {
		f    sops.Format
		want string
	}{
		{sops.FormatAuto, ""},
		{sops.FormatBinary, "binary"},
		{sops.FormatDotenv, "dotenv"},
		{sops.FormatINI, "ini"},
		{sops.FormatJSON, "json"},
		{sops.FormatYAML, "yaml"},
		{sops.FormatProtowire, "pxf"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.f.String(), "Format(%d).String()", c.f)
	}
}

// --- FormatForPath ---

func TestFormatForPath(t *testing.T) {
	cases := []struct {
		path string
		want sops.Format
	}{
		{"config.json", sops.FormatJSON},
		{"config.yaml", sops.FormatYAML},
		{"config.yml", sops.FormatYAML},
		{"config.env", sops.FormatDotenv},
		{"config.ini", sops.FormatINI},
		{"config.pxf", sops.FormatProtowire},
		{"config.bin", sops.FormatBinary},
		{"layers/prod.sops.pxf", sops.FormatProtowire},
		{"CONFIG.YAML", sops.FormatYAML}, // case-insensitive
		{"no_extension", sops.FormatAuto},
		{"unknown.xyz", sops.FormatAuto},
		{"", sops.FormatAuto},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sops.FormatForPath(c.path), "path=%q", c.path)
	}
}

// --- Decrypter wrapper ---

func TestDefault_EmptyCiphertext(t *testing.T) {
	d := sops.Default()
	_, err := d.Decrypt(context.Background(), []byte{}, sops.FormatYAML)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sops.ErrEmptyCiphertext))
}

func TestDefault_GarbageBytes(t *testing.T) {
	// Sops will reject non-sops bytes with its own metadata-not-found
	// error. Verify the chameleon wrapper propagates as wrapped error.
	d := sops.Default()
	_, err := d.Decrypt(context.Background(), []byte("not a sops file"), sops.FormatYAML)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sops: decrypt:",
		"wrapper must annotate sops errors with the package prefix")
}

func TestDefault_RespectsContextCancellation(t *testing.T) {
	d := sops.Default()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := d.Decrypt(ctx, []byte("any-non-empty-bytes"), sops.FormatYAML)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled),
		"a pre-cancelled context must short-circuit before sops is invoked")
}
