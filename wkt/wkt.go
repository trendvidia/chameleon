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
// [TemplatableText] is the one documented departure from that:
// interpolate descends into a google.protobuf.StringValue to rewrite
// its text, because a (pxf.default) may itself be a template. The
// other three packages are unaffected, and the lookup half of
// interpolate still refuses every atomic WKT. See that predicate for
// the reasoning.
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

// TemplatableText reports whether an [Atomic] WKT's payload is
// user-authored configuration text — the kind that may contain a
// `${field.path}` template. Only google.protobuf.StringValue qualifies:
// every other atomic WKT either holds a machine-format scalar no
// template belongs in (Timestamp, Duration, the numeric wrappers,
// BigInt, Decimal, BigFloat, BoolValue, BytesValue, Any) or holds a
// value that must stay opaque (pxf.Secret).
//
// This is the one place the four packages sharing [Atomic] do NOT draw
// the same boundary, and the asymmetry is deliberate. merge, defaults
// and validate treat a StringValue as one whole value and must keep
// doing so. interpolate alone descends, because a (pxf.default) may
// itself be a template — `api.${base}` — and the defaults pass fills
// WKT fields as of chameleon#129. Without this, the annotation on a
// StringValue field expanded to nothing and the raw template reached
// the config with neither error nor warning (chameleon#135).
//
// Rewriting a StringValue's text is not the same permission as reading
// it: interpolate's lookup path still refuses to descend into ANY
// atomic WKT, so a StringValue cannot be referenced from another
// field's template any more than a Secret can. Writing into a WKT
// leaks nothing; reading out of one is the vector that boundary
// exists for. nil-safe.
func TemplatableText(d protoreflect.MessageDescriptor) bool {
	return d != nil && d.FullName() == "google.protobuf.StringValue"
}
