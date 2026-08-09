// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package defaults runs chameleon's post-merge (pxf.default) pass.
// After merge produces the final layered message, [Apply] walks the
// schema and fills in defaults for the scalar and atomic-WKT fields
// that no layer set.
//
// "No layer set" means the field's dotted path is NOT in the
// presence union — the set computed by combining each layer's
// [pxf.Result.PresentFields] before merge. Implicit-presence proto3
// scalars are zero-valued for both "no layer touched it" and
// "explicitly set to zero", so the presence union — not Has() — is
// what we consult.
//
// Scope:
//
//   - Top-level scalar defaults always apply (root is always live).
//   - Nested-message scalar defaults apply only if the nested message
//     is live (has at least one sub-field set by some layer). A
//     defaults pass that conjures up an empty parent message just to
//     fill its inner default would be surprising.
//   - WKT subtrees are never descended into — the atomic boundary
//     applies here too — but a (pxf.default) on the WKT field itself
//     does apply, setting the whole value: `(pxf.default) = "250ms"`
//     on a google.protobuf.Duration field yields 250ms. The literal
//     is parsed by protowire-go's [pxf.ApplyDefault], which covers
//     Timestamp, Duration, pxf.BigInt, pxf.Decimal, pxf.BigFloat and
//     eight of the nine google.protobuf wrapper types. A (pxf.default)
//     on any other message-typed field — google.protobuf.Any,
//     pxf.Secret, a google.protobuf.Struct, a user-defined message —
//     is an error rather than a silent no-op, because pxf has no
//     literal form to parse it from.
//   - google.protobuf.BytesValue is the ninth wrapper and the one
//     exception: protowire-go's wrapper-default path routes through
//     parseScalarDefault, which has no BytesKind branch, so a default
//     on a BytesValue field is rejected ("unsupported default kind
//     bytes") rather than base64-decoded. A default on a plain
//     `bytes` scalar decodes fine — only the wrapper is affected.
//     Pinned by TestApply_BytesValueWrapperDefaultRejected.
//   - Repeated and map fields are skipped — defaulting a list to
//     non-empty is surprise-prone; users who need it can declare a
//     scalar default for each element separately. A (pxf.default) on
//     a repeated or map field is ignored rather than rejected; see
//     https://github.com/trendvidia/chameleon/issues/131.
//   - A field in a real (non-synthetic) oneof is not defaulted when
//     some layer already selected a different member. Setting any
//     member clears its siblings, so applying the default would
//     silently discard the operator's choice. Synthetic oneofs — the
//     wrappers proto3 `optional` generates — hold a single field and
//     express presence rather than a choice, so they stay
//     defaultable.
//
// Two rough edges inherited from protowire-go's literal parser, both
// pinned by tests here so a future upstream change is deliberate:
//
//   - Bool literals are compared against the exact string "true", so
//     `(pxf.default) = "True"` (or "1", or "yes") yields false with
//     no error. Every other literal kind reports a parse failure.
//   - A WKT default is never template-expanded. Defaults run before
//     interpolation precisely so a default may itself be a template
//     ("api.${base_url}"), but interpolate stops at the same atomic
//     boundary, so a `${...}` in a default on a
//     google.protobuf.StringValue reaches the resolved config
//     verbatim. Use a plain `string` field if the default needs
//     interpolation.
//
// Apply mutates `present` to mark default-applied paths as present.
// This lets the validation pass treat "had a default" as satisfying
// (pxf.required), which is the natural semantic.
package defaults

import (
	"fmt"

	"github.com/trendvidia/chameleon/wkt"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Option configures Apply.
type Option func(*options)

type options struct {
	onApplied func(path string)
}

// OnApplied registers a callback that fires once per field whose
// default was applied. The path is the dotted field path,
// matching parse.Move's format. Used by the resolver's Trace plumbing
// to mark default-applied paths in the per-field provenance map.
func OnApplied(fn func(path string)) Option {
	return func(o *options) { o.onApplied = fn }
}

// Apply walks msg and applies (pxf.default) values to the scalar and
// atomic-WKT fields not present in the union set. Mutates both msg
// and present.
func Apply(msg proto.Message, present map[string]struct{}, opts ...Option) error {
	if msg == nil {
		return fmt.Errorf("defaults: nil message")
	}
	if present == nil {
		// Nothing was marked present — every field with a default gets
		// it. Allocate an empty set so we can still mark applied paths.
		present = make(map[string]struct{})
	}
	o := options{}
	for _, fn := range opts {
		fn(&o)
	}
	return apply(msg.ProtoReflect(), present, o.onApplied, "")
}

func apply(m protoreflect.Message, present map[string]struct{}, onApplied func(path string), prefix string) error {
	if wkt.Atomic(m.Descriptor()) {
		return nil
	}
	fields := m.Descriptor().Fields()
	for i, n := 0, fields.Len(); i < n; i++ {
		fd := fields.Get(i)
		path := joinPath(prefix, string(fd.Name()))

		if fd.IsList() || fd.IsMap() {
			continue
		}

		// The (pxf.default) lookup comes before the message dispatch
		// below, so an annotation on an atomic WKT field reaches
		// pxf.ApplyDefault instead of being dropped by the atomic
		// check. The atomic boundary is about merge semantics —
		// whole-value replacement — and does not imply "cannot be
		// defaulted". Same ordering as validate's (pxf.required)
		// check, and as protowire-go's own per-parse pass.
		//
		// Two gates: the path is untouched by every layer, and — see
		// [oneofTaken] — filling it would not evict a sibling the
		// operator did choose.
		if _, isPresent := present[path]; !isPresent && !oneofTaken(m, fd) {
			if def, ok := pxf.Default(fd); ok {
				if err := pxf.ApplyDefault(m, fd, def); err != nil {
					return fmt.Errorf("defaults: %s: %w", path, err)
				}
				present[path] = struct{}{}
				if onApplied != nil {
					onApplied(path)
				}
				// The default is the field's whole value: a WKT has
				// no inner defaults to fill, and any other message
				// type would have errored above.
				continue
			}
		}

		if fd.Kind() != protoreflect.MessageKind {
			continue
		}
		md := fd.Message()
		if md == nil || wkt.Atomic(md) {
			continue
		}
		if !m.Has(fd) {
			// Nested message not live; don't materialize it just to
			// fill a default.
			continue
		}
		if err := apply(m.Mutable(fd).Message(), present, onApplied, path); err != nil {
			return err
		}
	}
	return nil
}

// oneofTaken reports whether fd sits in a real oneof whose case is
// already held by a different member. Applying a default there would
// not just fill a gap — protoreflect clears the siblings on Set, so
// the layer-supplied member would silently lose its value and the
// config would come back with a field nobody asked for.
//
// The presence check alone cannot catch this — the default's own path
// really is absent from the union; it is the *sibling's* path that is
// present. Synthetic oneofs (proto3 `optional`) wrap exactly one
// field, so there is no sibling to protect and no choice to override.
func oneofTaken(m protoreflect.Message, fd protoreflect.FieldDescriptor) bool {
	od := fd.ContainingOneof()
	if od == nil || od.IsSynthetic() {
		return false
	}
	which := m.WhichOneof(od)
	return which != nil && which.Number() != fd.Number()
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
