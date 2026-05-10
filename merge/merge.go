// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package merge implements chameleon's presence-aware proto-message
// overlay. Given a destination message, a source message parsed from
// PXF with [pxf.UnmarshalFull], and the source's [pxf.Result], [Merge]
// applies the source on top of the destination using these rules:
//
//   - Absent fields fall through (destination unchanged).
//   - Null fields clear the destination.
//   - Set scalars overwrite.
//   - Set user-defined message fields merge deep, recursively.
//   - Set repeated/map fields replace whole — no per-element appending,
//     no per-key entry merging.
//   - Set well-known-type message fields (pxf.Secret, pxf.BigInt,
//     google.protobuf.Timestamp, etc.) replace whole, NOT deep-merge.
//     WKTs model atomic values: merging `Timestamp.seconds` from one
//     layer with `Timestamp.nanos` from another is meaningless, and a
//     higher layer's `db_password = "new"` is meant to replace the
//     whole Secret rather than just rewrite its `value` while
//     preserving an unrelated lower-layer `hint`.
//
// The Resolver runs Merge once per layer, low-to-high, after parsing
// each layer with [pxf.UnmarshalFull]. The PXF codec's three-state
// presence model (set / null / absent — see protowire spec) is what
// makes the fall-through behavior unambiguous.
package merge

import (
	"fmt"

	"github.com/trendvidia/chameleon/wkt"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Merge overlays src onto dst using srcPresence to distinguish set,
// null, and absent fields. Mutates dst; src is read-only.
//
// Both messages must be of the same proto type. A type mismatch is a
// programming error and returns an explicit error rather than panicking
// inside protoreflect.
func Merge(dst, src proto.Message, srcPresence *pxf.Result) error {
	if dst == nil || src == nil {
		return fmt.Errorf("merge: nil message")
	}
	if srcPresence == nil {
		return fmt.Errorf("merge: nil presence (use pxf.UnmarshalFull, not pxf.Unmarshal)")
	}
	dstR := dst.ProtoReflect()
	srcR := src.ProtoReflect()
	if dstR.Descriptor().FullName() != srcR.Descriptor().FullName() {
		return fmt.Errorf("merge: type mismatch: dst=%s src=%s",
			dstR.Descriptor().FullName(), srcR.Descriptor().FullName())
	}
	return mergeMessage(dstR, srcR, srcPresence, "")
}

func mergeMessage(dst, src protoreflect.Message, presence *pxf.Result, parentPath string) error {
	fields := src.Descriptor().Fields()
	for i, n := 0, fields.Len(); i < n; i++ {
		fd := fields.Get(i)
		path := joinPath(parentPath, string(fd.Name()))
		if err := mergeField(dst, src, fd, presence, path); err != nil {
			return err
		}
	}
	return nil
}

func mergeField(dst, src protoreflect.Message, fd protoreflect.FieldDescriptor, presence *pxf.Result, path string) error {
	// Null wins regardless of shape — src explicitly cleared the field
	// at this layer, masking everything below.
	if presence.IsNull(path) {
		dst.Clear(fd)
		return nil
	}
	// Absent — let lower layers (already in dst) keep their value.
	if presence.IsAbsent(path) {
		return nil
	}

	// IsSet from here on.
	switch {
	case fd.IsMap():
		// Replace whole map. The source value here is already a fully
		// constructed map — Set takes a copy.
		dst.Set(fd, src.Get(fd))
	case fd.IsList():
		// Replace whole list, same reasoning.
		dst.Set(fd, src.Get(fd))
	case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
		if wkt.Atomic(fd.Message()) {
			// WKTs are atomic values. Replace whole — same rule as
			// list/map, for the same reason: merging at sub-field
			// granularity is meaningless for value types. A higher
			// layer's `db_password = "new"` means "replace this
			// secret," not "rewrite its inner value while preserving
			// the lower layer's hint."
			dst.Set(fd, src.Get(fd))
			return nil
		}
		// User-defined nested message: deep merge. Mutable() returns
		// the dst sub-message, creating it if absent. Recurse with the
		// prefixed path.
		return mergeMessage(dst.Mutable(fd).Message(), src.Get(fd).Message(), presence, path)
	default:
		// Scalar — overwrite.
		dst.Set(fd, src.Get(fd))
	}
	return nil
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

