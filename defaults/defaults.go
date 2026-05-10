// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package defaults runs chameleon's post-merge (pxf.default) pass.
// After merge produces the final layered message, [Apply] walks the
// schema and fills in defaults for scalar fields that no layer set.
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
//   - WKT subtrees are skipped — the WKT atomic boundary applies here
//     too. (pxf.default) on a WKT field should set the whole WKT, but
//     the (pxf.default) annotation is documented for scalar values
//     only.
//   - Repeated and map fields are skipped — defaulting a list to
//     non-empty is surprise-prone; users who need it can declare a
//     scalar default for each element separately.
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

// OnApplied registers a callback that fires once per scalar field
// whose default was applied. The path is the dotted field path,
// matching parse.Move's format. Used by the resolver's Trace plumbing
// to mark default-applied paths in the per-field provenance map.
func OnApplied(fn func(path string)) Option {
	return func(o *options) { o.onApplied = fn }
}

// Apply walks msg and applies (pxf.default) values to scalar fields
// not present in the union set. Mutates both msg and present.
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
		if fd.Kind() == protoreflect.MessageKind {
			md := fd.Message()
			if md == nil || wkt.Atomic(md) {
				continue
			}
			if !m.Has(fd) {
				// Nested message not live; don't materialize it just
				// to fill a default.
				continue
			}
			if err := apply(m.Mutable(fd).Message(), present, onApplied, path); err != nil {
				return err
			}
			continue
		}

		// Scalar.
		if _, isPresent := present[path]; isPresent {
			continue
		}
		def, ok := pxf.Default(fd)
		if !ok {
			continue
		}
		if err := pxf.ApplyDefault(m, fd, def); err != nil {
			return fmt.Errorf("defaults: %s: %w", path, err)
		}
		present[path] = struct{}{}
		if onApplied != nil {
			onApplied(path)
		}
	}
	return nil
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

