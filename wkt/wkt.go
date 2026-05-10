// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package wkt names the well-known proto types that chameleon treats
// as atomic values. Four packages depend on this list — merge,
// interpolate, defaults, validate — to draw the same boundary:
//
//   - WKT subtrees are NEVER traversed. merge replaces them whole;
//     interpolate skips them so plaintext can't leak into other
//     fields' templates; defaults and validate skip them so their
//     inner fields aren't subject to (pxf.default) or (pxf.required)
//     handling that doesn't make sense at the WKT-internals level.
//
// The list mirrors what protowire-go's PXF codec recognizes for
// scalar-shorthand decoding (encoding/pxf/wellknown.go), plus
// google.protobuf.Any. If a new WKT lands upstream and chameleon
// wants to treat it atomically, [Atomic] is the one place to update.
//
// The decision to treat WKTs atomically is semantic, not a workaround
// for any parser quirk. WKTs model values: merging
// `Timestamp.seconds` from one layer with `Timestamp.nanos` from
// another is meaningless, and a higher layer's `db_password = "new"`
// is meant to replace a Secret as a unit rather than rewrite its
// `value` while preserving an unrelated lower-layer `hint`.
package wkt

import "google.golang.org/protobuf/reflect/protoreflect"

// atomicSet is the FullName set of WKTs chameleon treats as atomic.
// Hardcoded for stability — protowire-go could in principle expose a
// predicate, but layering the decision in chameleon (a) keeps
// protowire-go free of "chameleon-flavored merge semantics", and (b)
// lets chameleon evolve its set independently if a future use-case
// needs to mark a non-WKT type atomic too.
var atomicSet = map[protoreflect.FullName]struct{}{
	"google.protobuf.Timestamp":   {},
	"google.protobuf.Duration":    {},
	"google.protobuf.Any":         {},
	"google.protobuf.BoolValue":   {},
	"google.protobuf.BytesValue":  {},
	"google.protobuf.DoubleValue": {},
	"google.protobuf.FloatValue":  {},
	"google.protobuf.Int32Value":  {},
	"google.protobuf.Int64Value":  {},
	"google.protobuf.StringValue": {},
	"google.protobuf.UInt32Value": {},
	"google.protobuf.UInt64Value": {},
	"pxf.BigInt":                  {},
	"pxf.Decimal":                 {},
	"pxf.BigFloat":                {},
	"pxf.Secret":                  {},
}

// Atomic reports whether the message descriptor names a WKT chameleon
// treats as an atomic value. nil-safe.
func Atomic(d protoreflect.MessageDescriptor) bool {
	if d == nil {
		return false
	}
	_, ok := atomicSet[d.FullName()]
	return ok
}
