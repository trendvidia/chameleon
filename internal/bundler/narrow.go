// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/internal/secretcatalog"
)

// Narrow walks msg and clears every pxf.Secret-bearing field whose
// schema path is denied by filter. Non-secret structural fields are
// preserved verbatim — the goal is "smaller blast radius for
// secrets", not general config trimming.
//
// Narrow operates on the schema path (no `[i]` / `[key]` brackets),
// matching the form Filter.Allows expects. This means a denied
// repeated or map of Secrets clears the ENTIRE container — you
// can't whitelist only some elements. Per-element narrowing would
// require schema-form patterns to carry index expressions, which
// would tie the manifest to runtime data rather than to the schema.
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
// fields whose path is denied are cleared.
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
		}
	}
}

// narrowScalarMessageField handles `MyType field = N;` (not list,
// not map). If the field's value type IS pxf.Secret, the field is
// the leaf — clear it on deny. Otherwise it's a nested user
// message — recurse into it (only if set; unset nested messages
// have nothing to narrow).
func narrowScalarMessageField(m protoreflect.Message, f protoreflect.FieldDescriptor, filter *Filter, path string) {
	if secretcatalog.IsSecret(f.Message()) {
		if !filter.Allows(path) {
			m.Clear(f)
		}
		return
	}
	if !m.Has(f) {
		return
	}
	narrowMessage(m.Mutable(f).Message(), filter, path)
}

// narrowListField handles `repeated T field = N;`. For repeated
// scalars (no Secrets possible), no-op. For repeated Secret, the
// whole list is the leaf — clear on deny. For repeated user
// message, iterate each element and recurse.
func narrowListField(m protoreflect.Message, f protoreflect.FieldDescriptor, filter *Filter, path string) {
	if f.Kind() != protoreflect.MessageKind {
		return
	}
	if secretcatalog.IsSecret(f.Message()) {
		if !filter.Allows(path) {
			m.Clear(f)
		}
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

// narrowMapField handles `map<K, V> field = N;`. For map<K, scalar>
// no Secrets possible — no-op. For map<K, Secret>, the whole map
// is the leaf — clear on deny. For map<K, user message>, iterate
// each value and recurse.
func narrowMapField(m protoreflect.Message, f protoreflect.FieldDescriptor, filter *Filter, path string) {
	valFd := f.MapValue()
	if valFd.Kind() != protoreflect.MessageKind {
		return
	}
	if secretcatalog.IsSecret(valFd.Message()) {
		if !filter.Allows(path) {
			m.Clear(f)
		}
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
