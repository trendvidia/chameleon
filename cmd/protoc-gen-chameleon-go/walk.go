// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// secretFullName matches the canonical pxf.Secret well-known type
// definition (proto/pxf/secret.proto in the protowire spec). The
// chameleon parse.Move walker uses the same FullName.
const secretFullName protoreflect.FullName = "pxf.Secret"

// pathStep is one segment of a Secret's reach-path from a root
// message. Examples:
//
//	{Name: "db_password", Container: scalar}
//	{Name: "endpoints", Container: repeated}
//	{Name: "tenant_keys", Container: map, MapKeyKind: string}
//
// The final step in a secretSite.Path is the Secret-bearing field
// itself; preceding steps describe the user-message containers
// traversed to reach it. Container indicates how that step's value
// is reached from its parent: scalar (`.field`), repeated (`.field[i]`),
// or map (`.field[key]`). MapKeyKind is set only when Container == map.
type pathStep struct {
	Name       string
	Container  container
	MapKeyKind protoreflect.Kind
}

type container int

const (
	containerScalar container = iota
	containerRepeated
	containerMap
)

// secretSite captures one place a pxf.Secret-typed value appears in
// the schema, with enough info to emit a sidecar accessor that
// reaches it via [secret.Map.Get].
type secretSite struct {
	// Root is the top-level message (e.g. AppConfig) the path
	// originates from. Drives the function name prefix.
	Root *protogen.Message

	// Path is the ordered list of steps from Root to the Secret. The
	// last step is the Secret-bearing field itself; its Container
	// indicates how the Secret is held (scalar — direct field;
	// repeated — list of Secrets; map — map<K, Secret>). Earlier
	// steps describe user-message container traversals along the way.
	Path []pathStep
}

// findSecretSites walks all top-level messages in a file and returns
// every Secret-typed field site, recursing through every user-message
// path including repeated and map containers.
//
// Cycles in the message graph (Foo containing Foo, directly or
// transitively) are broken by tracking the path-set of in-flight
// descriptors. The same message type CAN appear on two distinct paths
// from the same root — only direct re-entry is blocked.
func findSecretSites(file *protogen.File) []secretSite {
	var sites []secretSite
	for _, msg := range file.Messages {
		visited := map[protoreflect.FullName]bool{msg.Desc.FullName(): true}
		walkMessage(msg, msg, nil, &sites, visited)
	}
	return sites
}

// walkMessage recurses through `current`, prefixing discovered sites
// with `path`. `root` stays the top-level message for naming.
func walkMessage(root, current *protogen.Message, path []pathStep, out *[]secretSite, visited map[protoreflect.FullName]bool) {
	for _, f := range current.Fields {
		switch {
		case f.Desc.IsMap():
			handleMapField(root, f, path, out, visited)
		case f.Desc.IsList():
			handleListField(root, f, path, out, visited)
		case f.Desc.Kind() == protoreflect.MessageKind:
			handleScalarMessageField(root, f, path, out, visited)
		}
	}
}

// handleScalarMessageField — `MyType field = N;` (not list, not map).
func handleScalarMessageField(root *protogen.Message, f *protogen.Field, path []pathStep, out *[]secretSite, visited map[protoreflect.FullName]bool) {
	step := pathStep{Name: string(f.Desc.Name()), Container: containerScalar}
	md := f.Desc.Message()
	if isSecret(md) {
		*out = append(*out, secretSite{Root: root, Path: append(append([]pathStep{}, path...), step)})
		return
	}
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkMessage(root, f.Message, append(append([]pathStep{}, path...), step), out, visited)
	delete(visited, md.FullName())
}

// handleListField — `repeated T field = N;`.
func handleListField(root *protogen.Message, f *protogen.Field, path []pathStep, out *[]secretSite, visited map[protoreflect.FullName]bool) {
	if f.Desc.Kind() != protoreflect.MessageKind {
		// repeated scalar — no Secrets possible.
		return
	}
	step := pathStep{Name: string(f.Desc.Name()), Container: containerRepeated}
	md := f.Desc.Message()
	if isSecret(md) {
		*out = append(*out, secretSite{Root: root, Path: append(append([]pathStep{}, path...), step)})
		return
	}
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkMessage(root, f.Message, append(append([]pathStep{}, path...), step), out, visited)
	delete(visited, md.FullName())
}

// handleMapField — `map<K, V> field = N;`. Recursion only descends
// into V if V is a user-defined message; map<K, scalar> obviously
// holds no Secrets.
func handleMapField(root *protogen.Message, f *protogen.Field, path []pathStep, out *[]secretSite, visited map[protoreflect.FullName]bool) {
	valFd := f.Desc.MapValue()
	if valFd.Kind() != protoreflect.MessageKind {
		return
	}
	step := pathStep{
		Name:       string(f.Desc.Name()),
		Container:  containerMap,
		MapKeyKind: f.Desc.MapKey().Kind(),
	}
	md := valFd.Message()
	if isSecret(md) {
		*out = append(*out, secretSite{Root: root, Path: append(append([]pathStep{}, path...), step)})
		return
	}
	// Map<K, UserMessage> with potential Secrets inside. protogen
	// represents `map<K, V>` as a synthetic entry message with `key`
	// and `value` fields — `f.Message` is the entry, not V. Dig into
	// the entry's value field to get the *protogen.Message for V.
	valueMsg := mapValueMessage(f)
	if valueMsg == nil {
		return
	}
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkMessage(root, valueMsg, append(append([]pathStep{}, path...), step), out, visited)
	delete(visited, md.FullName())
}

// mapValueMessage returns the *protogen.Message for the value type V
// of a `map<K, V>` field, or nil if the field's value isn't a message
// (e.g. map<string, int>) — caller should have already filtered.
func mapValueMessage(f *protogen.Field) *protogen.Message {
	if f.Message == nil {
		return nil
	}
	// Map entry messages canonically have field number 2 = "value",
	// but iterate by name for resilience against non-canonical entry
	// shapes.
	for _, sub := range f.Message.Fields {
		if string(sub.Desc.Name()) == "value" && sub.Message != nil {
			return sub.Message
		}
	}
	return nil
}

// mapValueEnum is the enum-typed counterpart of mapValueMessage. For a
// `map<K, MyEnum>` field, returns the *protogen.Enum for MyEnum so
// the codegen can render `map[K]MyEnum` with the protogen-managed
// import. Returns nil for maps whose value isn't an enum (caller
// should have already checked valFd.Kind() == protoreflect.EnumKind).
func mapValueEnum(f *protogen.Field) *protogen.Enum {
	if f.Message == nil {
		return nil
	}
	for _, sub := range f.Message.Fields {
		if string(sub.Desc.Name()) == "value" && sub.Enum != nil {
			return sub.Enum
		}
	}
	return nil
}

func isSecret(d protoreflect.MessageDescriptor) bool {
	return d != nil && d.FullName() == secretFullName
}
