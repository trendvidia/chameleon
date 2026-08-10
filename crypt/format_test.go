// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package crypt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/trendvidia/chameleon/crypt"
)

// Format and FormatForPath moved here from chameleon/sops in v0.23.0
// along with the type itself; these cases came with them.

func TestFormat_String(t *testing.T) {
	cases := []struct {
		f    crypt.Format
		want string
	}{
		{crypt.FormatAuto, ""},
		{crypt.FormatBinary, "binary"},
		{crypt.FormatDotenv, "dotenv"},
		{crypt.FormatINI, "ini"},
		{crypt.FormatJSON, "json"},
		{crypt.FormatYAML, "yaml"},
		{crypt.FormatProtowire, "pxf"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.f.String(), "Format(%d).String()", c.f)
	}
}

func TestFormat_StringOnOutOfRangeValue(t *testing.T) {
	// Unknown enum values spell as "" — the same as FormatAuto, which
	// is what routes a backend to its own envelope sniffing. A
	// panicking or garbage spelling here would reach sops as a format
	// name.
	assert.Empty(t, crypt.Format(99).String())
	assert.Empty(t, crypt.Format(-1).String())
}

func TestFormatForPath(t *testing.T) {
	cases := []struct {
		path string
		want crypt.Format
	}{
		{"config.json", crypt.FormatJSON},
		{"config.yaml", crypt.FormatYAML},
		{"config.yml", crypt.FormatYAML},
		{"config.env", crypt.FormatDotenv},
		{"config.ini", crypt.FormatINI},
		{"config.pxf", crypt.FormatProtowire},
		{"config.bin", crypt.FormatBinary},
		{"layers/prod.sops.pxf", crypt.FormatProtowire},
		{"CONFIG.YAML", crypt.FormatYAML}, // case-insensitive
		{"no_extension", crypt.FormatAuto},
		{"unknown.xyz", crypt.FormatAuto},
		{"", crypt.FormatAuto},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, crypt.FormatForPath(c.path), "path=%q", c.path)
	}
}
