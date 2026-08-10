// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package crypt

import "strings"

// Format identifies the store backend a ciphertext was produced with.
// Values mirror sops's internal `formats.Format` enum so callers don't
// have to import the sops cmd subpackage — but the type is part of the
// [Decrypter] contract, so any backend takes it.
type Format int

const (
	// FormatAuto defers to the backend's own auto-detection. Useful
	// when neither the path nor an explicit format is known.
	FormatAuto Format = iota

	// FormatBinary is the opaque-byte-string store. The plaintext is
	// treated as a single value; layers using this format produce raw
	// bytes, not field-structured PXF.
	FormatBinary

	// FormatDotenv is the `KEY=VALUE` newline-delimited store. Layer
	// content is a flat key-value list; the values are encrypted.
	FormatDotenv

	// FormatINI is the classic `[section]\nkey=value` store. String
	// values are encrypted; sections and keys stay clear.
	FormatINI

	// FormatJSON is the JSON store. Layer content is a JSON object;
	// leaf string values are encrypted, along with any field matching
	// the encrypted_regex / encrypted_suffix rules.
	FormatJSON

	// FormatYAML is the YAML store. Same encryption semantics as
	// FormatJSON, with a comment-preserving round trip.
	FormatYAML

	// FormatProtowire is the PXF text format (recognized by sops as
	// "protowire" or "pxf"). The canonical store for chameleon
	// layers; provides field-typed encryption with PXF's
	// presence-aware grammar.
	FormatProtowire
)

// String returns the canonical string spelling sops accepts for this
// format. FormatAuto returns "" so the "infer from MIME" path
// engages.
func (f Format) String() string {
	switch f {
	case FormatBinary:
		return "binary"
	case FormatDotenv:
		return "dotenv"
	case FormatINI:
		return "ini"
	case FormatJSON:
		return "json"
	case FormatYAML:
		return "yaml"
	case FormatProtowire:
		return "pxf"
	default:
		return ""
	}
}

// FormatForPath infers a Format from a filename extension. Returns
// FormatAuto for unrecognized extensions; the backend will then
// auto-detect from the ciphertext envelope.
//
// Recognized extensions match sops's defaults: .json, .yaml, .yml,
// .env, .ini, .pxf, .bin (binary).
func FormatForPath(path string) Format {
	low := strings.ToLower(path)
	// Strip any sops convention suffix like ".sops.yaml" — the
	// rightmost extension is what matters.
	if i := strings.LastIndex(low, "."); i >= 0 {
		switch low[i+1:] {
		case "json":
			return FormatJSON
		case "yaml", "yml":
			return FormatYAML
		case "env":
			return FormatDotenv
		case "ini":
			return FormatINI
		case "pxf":
			return FormatProtowire
		case "bin":
			return FormatBinary
		}
	}
	return FormatAuto
}
