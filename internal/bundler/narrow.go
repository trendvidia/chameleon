// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/internal/secretcatalog"
)

// Narrow walks msg and clears every field whose schema path is
// denied by the relevant half of filter:
//
//   - pxf.Secret-bearing fields (scalar / repeated / map<K, Secret>)
//     are gated by filter.AllowsSecret — the historical behavior.
//   - Every other (non-secret) field is gated by
//     filter.AllowsAttribute. When no attribute_* patterns are set
//     on the module manifest, the attribute selector defaults to
//     ALLOW_ALL and non-secrets pass through unchanged (matching
//     the pre-attribute-filter behavior).
//
// Narrow operates on the schema path (no `[i]` / `[key]` brackets),
// matching the form Filter.* expects. This means a denied repeated
// or map clears the ENTIRE container — you can't whitelist only
// some elements. Per-element narrowing would require schema-form
// patterns to carry index expressions, which would tie the manifest
// to runtime data rather than to the schema.
//
// A nil filter is a no-op (allows everything). Mutates msg in place
// and returns nil; the only error currently possible would be a
// schema mismatch surfaced by protoreflect, which is reserved for
// future extension.
func Narrow(msg proto.Message, filter *Filter) error {
	if msg == nil || filter == nil {
		return nil
	}
	narrowMessage(msg.ProtoReflect(), filter, "")
	return nil
}

// narrowMessage walks the fields of m, recursing into nested
// user-messages with an extended schema-path prefix. Secret-bearing
// fields whose path is denied by AllowsSecret are cleared;
// non-secret fields whose path is denied by AllowsAttribute are
// cleared.
//
// Concrete-instance traversal: protobuf message instances form a
// tree (no shared sub-instances) so no cycle tracking is needed —
// each field's set/unset state is read directly from m.
func narrowMessage(m protoreflect.Message, filter *Filter, prefix string) {
	desc := m.Descriptor()
	fields := desc.Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		path := joinPath(prefix, string(f.Name()))
		switch {
		case f.IsMap():
			narrowMapField(m, f, filter, path)
		case f.IsList():
			narrowListField(m, f, filter, path)
		case f.Kind() == protoreflect.MessageKind:
			narrowScalarMessageField(m, f, filter, path)
		default:
			narrowScalarField(m, f, filter, path)
		}
	}
}

// narrowScalarMessageField handles `MyType field = N;` (not list,
// not map). If the field's value type IS pxf.Secret, the field is
// the leaf — clear on secret-side deny. Otherwise it's a nested
// user message — clear on attribute-side deny; if allowed, recurse
// to apply both filters to children.
func narrowScalarMessageField(m protoreflect.Message, f protoreflect.FieldDescriptor, filter *Filter, path string) {
	if secretcatalog.IsSecret(f.Message()) {
		if !filter.AllowsSecret(path) {
			m.Clear(f)
		}
		return
	}
	if !filter.AllowsAttribute(path) {
		m.Clear(f)
		return
	}
	if !m.Has(f) {
		return
	}
	narrowMessage(m.Mutable(f).Message(), filter, path)
}

// narrowListField handles `repeated T field = N;`. For repeated
// Secret, secret-side gate clears the whole list on deny. For
// repeated non-secret message, attribute-side gate clears the
// container on deny; if allowed, recurse into each element. For
// repeated scalar, attribute-side gate clears on deny (no recursion
// needed — scalars have no children).
func narrowListField(m protoreflect.Message, f protoreflect.FieldDescriptor, filter *Filter, path string) {
	if f.Kind() == protoreflect.MessageKind && secretcatalog.IsSecret(f.Message()) {
		if !filter.AllowsSecret(path) {
			m.Clear(f)
		}
		return
	}
	if !filter.AllowsAttribute(path) {
		m.Clear(f)
		return
	}
	if f.Kind() != protoreflect.MessageKind {
		return
	}
	if !m.Has(f) {
		return
	}
	list := m.Mutable(f).List()
	for i := range list.Len() {
		narrowMessage(list.Get(i).Message(), filter, path)
	}
}

// narrowMapField handles `map<K, V> field = N;`. For map<K, Secret>,
// secret-side gate clears on deny. For map<K, non-secret message>,
// attribute-side gate clears on deny; if allowed, recurse into each
// value. For map<K, scalar>, attribute-side gate clears on deny.
func narrowMapField(m protoreflect.Message, f protoreflect.FieldDescriptor, filter *Filter, path string) {
	valFd := f.MapValue()
	if valFd.Kind() == protoreflect.MessageKind && secretcatalog.IsSecret(valFd.Message()) {
		if !filter.AllowsSecret(path) {
			m.Clear(f)
		}
		return
	}
	if !filter.AllowsAttribute(path) {
		m.Clear(f)
		return
	}
	if valFd.Kind() != protoreflect.MessageKind {
		return
	}
	if !m.Has(f) {
		return
	}
	mp := m.Mutable(f).Map()
	mp.Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
		narrowMessage(v.Message(), filter, path)
		return true
	})
}

// narrowScalarField handles non-message singular fields (string,
// int, bool, enum, bytes, etc.) — these never carry secrets, so
// only the attribute-side gate applies.
func narrowScalarField(m protoreflect.Message, f protoreflect.FieldDescriptor, filter *Filter, path string) {
	if !filter.AllowsAttribute(path) {
		m.Clear(f)
	}
}

func joinPath(prefix, segment string) string {
	if prefix == "" {
		return segment
	}
	var b strings.Builder
	b.Grow(len(prefix) + 1 + len(segment))
	b.WriteString(prefix)
	b.WriteByte('.')
	b.WriteString(segment)
	return b.String()
}
