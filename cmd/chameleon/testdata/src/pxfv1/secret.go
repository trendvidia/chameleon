// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package pxfv1 mirrors the canonical pxf.Secret well-known type's Go
// binding (subset of fields) so analysistest can resolve the type by
// structural match.
package pxfv1

// Secret is a minimal stand-in for the canonical pxf.Secret. Real
// generated code includes proto-internal state fields and methods
// (Reset, String, ProtoMessage, ProtoReflect, etc.). The linter
// recognizes the type by package name + struct field shape, not by
// proto-interface satisfaction, so this stub is sufficient.
type Secret struct {
	Value       string
	Hint        string
	Fingerprint string
}
