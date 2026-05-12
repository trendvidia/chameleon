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

// ClobberInfo describes a WKT-metadata clobber: a higher-layer write
// of an atomic WKT field (pxf.Secret, etc.) whose own metadata is
// empty replaced a lower-layer value that DID have metadata, so that
// metadata is silently dropped by the whole-replace semantics.
//
// Path is the dotted field path. Type is the WKT's FullName.
// LostFields names the metadata fields that were non-empty in dst and
// empty in src.
//
// Currently chameleon detects clobber only on pxf.Secret (metadata
// fields: hint, fingerprint). Other atomic WKTs (Timestamp, BigInt,
// etc.) have no metadata concept and are not checked.
type ClobberInfo struct {
	Path       string
	Type       string
	LostFields []string
}

// Options configure a single [MergeWithOptions] call.
type Options struct {
	// OnClobberedMetadata, if non-nil, is invoked once per detected
	// clobber. Returning an error from the callback aborts the merge
	// — used by the resolver to implement strict-metadata mode.
	OnClobberedMetadata func(ClobberInfo) error
}

// Merge overlays src onto dst using srcPresence to distinguish set,
// null, and absent fields. Mutates dst; src is read-only.
//
// Both messages must be of the same proto type. A type mismatch is a
// programming error and returns an explicit error rather than panicking
// inside protoreflect.
func Merge(dst, src proto.Message, srcPresence *pxf.Result) error {
	return MergeWithOptions(dst, src, srcPresence, Options{})
}

// MergeWithOptions is [Merge] with caller-supplied [Options]. The
// resolver uses it to register an [Options.OnClobberedMetadata]
// callback so WKT-metadata clobbers surface on Resolution.Trace +
// the chameleon.merge.clobbered_metadata OTel counter.
func MergeWithOptions(dst, src proto.Message, srcPresence *pxf.Result, opts Options) error {
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
	return mergeMessage(dstR, srcR, srcPresence, "", opts)
}

func mergeMessage(dst, src protoreflect.Message, presence *pxf.Result, parentPath string, opts Options) error {
	fields := src.Descriptor().Fields()
	for i, n := 0, fields.Len(); i < n; i++ {
		fd := fields.Get(i)
		path := joinPath(parentPath, string(fd.Name()))
		if err := mergeField(dst, src, fd, presence, path, opts); err != nil {
			return err
		}
	}
	return nil
}

func mergeField(dst, src protoreflect.Message, fd protoreflect.FieldDescriptor, presence *pxf.Result, path string, opts Options) error {
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
	case fd.Kind() == protoreflect.MessageKind:
		if wkt.Atomic(fd.Message()) {
			// WKTs are atomic values. Replace whole — same rule as
			// list/map, for the same reason: merging at sub-field
			// granularity is meaningless for value types. A higher
			// layer's `db_password = "new"` means "replace this
			// secret," not "rewrite its inner value while preserving
			// the lower layer's hint."
			//
			// Before replacing, check whether the lower layer (dst)
			// had metadata fields the upper layer (src) doesn't — if
			// so, the replace silently loses that metadata. The
			// resolver wires an Options.OnClobberedMetadata callback
			// so the loss surfaces on Resolution.Trace + the
			// chameleon.merge.clobbered_metadata OTel counter.
			if opts.OnClobberedMetadata != nil && dst.Has(fd) {
				if lost := detectClobber(fd, dst.Get(fd).Message(), src.Get(fd).Message()); len(lost) > 0 {
					if err := opts.OnClobberedMetadata(ClobberInfo{
						Path:       path,
						Type:       string(fd.Message().FullName()),
						LostFields: lost,
					}); err != nil {
						return err
					}
				}
			}
			dst.Set(fd, src.Get(fd))
			return nil
		}
		// User-defined nested message: deep merge. Mutable() returns
		// the dst sub-message, creating it if absent. Recurse with the
		// prefixed path.
		return mergeMessage(dst.Mutable(fd).Message(), src.Get(fd).Message(), presence, path, opts)
	default:
		// Scalar — overwrite.
		dst.Set(fd, src.Get(fd))
	}
	return nil
}

// wktMetadataFields names the per-WKT metadata sub-fields chameleon
// inspects for clobber detection. The values are field NAMES (not
// numbers) so this stays robust to descriptor evolution.
//
// Only pxf.Secret currently has a metadata concept (hint, fingerprint).
// Other atomic WKTs (Timestamp, Duration, BigInt, etc.) have value-only
// shapes — replacement is the only sensible semantic.
var wktMetadataFields = map[protoreflect.FullName][]protoreflect.Name{
	"pxf.Secret": {"hint", "fingerprint"},
}

// detectClobber returns the list of metadata sub-field names that
// dst had set (non-empty) but src does not. An empty result means no
// clobber occurred — either the WKT has no metadata concept, or the
// upper layer carries metadata equivalent to (or richer than) the
// lower's.
//
// "Non-empty" is field-shape-aware: strings are non-empty when
// non-zero-length; bytes likewise; messages via Has. Currently both
// pxf.Secret metadata fields (hint, fingerprint) are strings, so the
// string check suffices.
func detectClobber(fd protoreflect.FieldDescriptor, dst, src protoreflect.Message) []string {
	md := fd.Message()
	metaNames, ok := wktMetadataFields[md.FullName()]
	if !ok {
		return nil
	}
	var lost []string
	for _, name := range metaNames {
		mfd := md.Fields().ByName(name)
		if mfd == nil {
			continue
		}
		dstVal := dst.Get(mfd)
		srcVal := src.Get(mfd)
		if isEmpty(dstVal) || !isEmpty(srcVal) {
			continue // dst had no metadata here, or src preserved it
		}
		lost = append(lost, string(name))
	}
	return lost
}

// isEmpty reports whether a protoreflect.Value is the zero value for
// its kind (in the only kinds chameleon's WKT-metadata checker handles).
// String → "", bytes → len 0, message → !IsValid. Other kinds default
// to "consider non-empty" (false) — they don't appear in the metadata
// list today.
func isEmpty(v protoreflect.Value) bool {
	switch x := v.Interface().(type) {
	case string:
		return x == ""
	case []byte:
		return len(x) == 0
	case protoreflect.Message:
		return !x.IsValid()
	default:
		return false
	}
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

