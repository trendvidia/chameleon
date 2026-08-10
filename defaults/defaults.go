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
//     all nine google.protobuf wrapper types — BytesValue included,
//     its literal being base64. (BytesValue was the one exception
//     until protowire-go v1.4.0 added the missing branch;
//     trendvidia/protowire-go#67.)
//   - A (pxf.default) on any other message-typed field —
//     google.protobuf.Any, pxf.Secret, a google.protobuf.Struct, a
//     user-defined message — does not bind at all since protowire-go
//     v1.4.0 (trendvidia/protowire-go#69): the schema is rejected when
//     the layer is parsed, before this pass runs, and identically for
//     every environment. Apply keeps its own error for the same
//     placement because a caller can reach it with a descriptor that
//     never went through a PXF parse.
//   - Repeated and map fields are skipped here, and never reach this
//     pass in the first place: since protowire-go v1.4.0 a
//     (pxf.default) on a list or map does not bind at all, because one
//     literal cannot denote a list (trendvidia/protowire-go#68, draft
//     -01 §annotation-extensions). The skip below is what remains for
//     a caller who reaches Apply with a descriptor that did not come
//     through a PXF parse.
//   - A field in a real (non-synthetic) oneof is not defaulted when
//     some layer already spoke for a different member — including a
//     member a layer explicitly cleared with `= null`, which counts as
//     spoken for. Setting any member clears its siblings, so applying
//     the default would silently discard the operator's choice.
//     Synthetic oneofs — the wrappers proto3 `optional` generates —
//     hold a single field and express presence rather than a choice,
//     so they stay defaultable.
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
		// [oneofTaken] — filling it would not evict a sibling some
		// layer already spoke for.
		if _, isPresent := present[path]; !isPresent && !oneofTaken(m, fd, present, prefix) {
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

// oneofTaken reports whether fd sits in a real oneof some other member
// of which the layers already spoke for. Applying a default there would
// not just fill a gap — protoreflect clears the siblings on Set, so
// the layer-supplied member would silently lose its value and the
// config would come back with a field nobody asked for.
//
// The presence check on fd's own path cannot catch this — the default's
// path really is absent from the union; it is the *sibling's* path that
// is present. Synthetic oneofs (proto3 `optional`) wrap exactly one
// field, so there is no sibling to protect and no choice to override.
//
// "Spoke for" is read from the presence union first and the message
// state second, and the union is the load-bearing half: a member the
// operator explicitly cleared (`file = null`) is in the union but is
// NOT set on the message, so consulting WhichOneof alone would treat
// the oneof as free and fill a sibling's default — turning an explicit
// clear into a value nobody asked for. That is the guard
// [pxf.ApplyDefault]'s godoc assigns to a SkipPostDecode caller, and
// upstream's own postDecode reads the same set (trendvidia/protowire-go
// #72, draft -01 §annotation-extensions, "Oneof Members"). The message
// check is kept for a caller that hands Apply a populated message with
// a partial or empty presence map.
func oneofTaken(m protoreflect.Message, fd protoreflect.FieldDescriptor, present map[string]struct{}, prefix string) bool {
	od := fd.ContainingOneof()
	if od == nil || od.IsSynthetic() {
		return false
	}
	members := od.Fields()
	for i, n := 0, members.Len(); i < n; i++ {
		sibling := members.Get(i)
		if sibling.Number() == fd.Number() {
			continue
		}
		if _, ok := present[joinPath(prefix, string(sibling.Name()))]; ok {
			return true
		}
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
