// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package secretcatalog enumerates every pxf.Secret-bearing field in
// a protobuf schema. The enumeration is the same one the chameleon
// parse.Move walker performs at resolve time, so a Site's path
// matches the secret.Map key that runtime resolution stores the
// Secret under.
//
// Two consumers share this catalog:
//
//   - protoc-gen-chameleon-go (codegen-time): emits typed accessors,
//     one per Site, so consumer code reaches secrets without raw
//     path-string lookups.
//
//   - chameleon bundle build (cli-time, planned): validates module
//     manifest patterns against the catalog so a typo'd allow-glob
//     fails the bundler instead of silently producing an empty
//     bundle.
//
// The catalog operates on protoreflect descriptors only — no Go-side
// type information — so it works against any source of descriptors
// (a protoc plugin's *protogen.File.Desc, a buf image's FileDescriptor,
// or a runtime-registered protoreflect.FileDescriptor).
package secretcatalog

import "google.golang.org/protobuf/reflect/protoreflect"

// SecretFullName is the canonical pxf.Secret well-known type. A
// message field whose value-type descriptor matches this name is a
// secret site. Defined in the protowire spec at proto/pxf/secret.proto.
const SecretFullName protoreflect.FullName = "pxf.Secret"

// PathStep is one segment of a Secret's reach-path from a root
// message. The final step in a Site.Path is the Secret-bearing field
// itself; preceding steps describe the user-message containers
// traversed to reach it. Container indicates how that step's value is
// reached from its parent: scalar (`.field`), repeated (`.field[i]`),
// or map (`.field[key]`). MapKeyKind is set only when Container == Map.
type PathStep struct {
	Name       string
	Container  Container
	MapKeyKind protoreflect.Kind
}

// Container reports how a path step's value is reached from its
// parent message.
type Container int

const (
	ContainerScalar Container = iota
	ContainerRepeated
	ContainerMap
)

// Site captures one place a pxf.Secret-typed value appears in the
// schema. Root is the top-level message the path originates from
// (drives accessor naming in the codegen plugin and module-scope
// validation in the bundler). Path is the ordered list of steps from
// Root to the Secret.
type Site struct {
	Root protoreflect.MessageDescriptor
	Path []PathStep
}

// IsSecret reports whether md is the pxf.Secret well-known type.
// Nil-safe receiver.
func IsSecret(md protoreflect.MessageDescriptor) bool {
	return md != nil && md.FullName() == SecretFullName
}

// FindInFile returns every Secret site reachable from any top-level
// message declared in fd. Order is deterministic: messages are
// visited in their declared order; within each message, fields are
// visited in field-declaration order.
//
// Cycles in the message graph (Foo containing Foo, directly or
// transitively) are broken by tracking the path-set of in-flight
// descriptors. The same message type CAN appear on two distinct
// paths from the same root — only direct re-entry is blocked.
func FindInFile(fd protoreflect.FileDescriptor) []Site {
	var sites []Site
	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		msg := msgs.Get(i)
		visited := map[protoreflect.FullName]bool{msg.FullName(): true}
		walkMessage(msg, msg, nil, &sites, visited)
	}
	return sites
}

// FindInMessage returns every Secret site reachable from md, treating
// md as the root. Useful when filtering against a single root type
// (e.g. the bundler narrowing one consumer's AppConfig) without
// pulling in unrelated top-level messages from the same file.
func FindInMessage(md protoreflect.MessageDescriptor) []Site {
	var sites []Site
	visited := map[protoreflect.FullName]bool{md.FullName(): true}
	walkMessage(md, md, nil, &sites, visited)
	return sites
}

// walkMessage recurses through `current`, prefixing discovered sites
// with `path`. `root` stays the top-level message descriptor for the
// reported Site.Root.
func walkMessage(root, current protoreflect.MessageDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	fields := current.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		switch {
		case f.IsMap():
			handleMapField(root, f, path, out, visited)
		case f.IsList():
			handleListField(root, f, path, out, visited)
		case f.Kind() == protoreflect.MessageKind:
			handleScalarMessageField(root, f, path, out, visited)
		}
	}
}

func handleScalarMessageField(root protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	step := PathStep{Name: string(f.Name()), Container: ContainerScalar}
	md := f.Message()
	if IsSecret(md) {
		*out = append(*out, Site{Root: root, Path: appendStep(path, step)})
		return
	}
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkMessage(root, md, appendStep(path, step), out, visited)
	delete(visited, md.FullName())
}

func handleListField(root protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	if f.Kind() != protoreflect.MessageKind {
		// repeated scalar — no Secrets possible.
		return
	}
	step := PathStep{Name: string(f.Name()), Container: ContainerRepeated}
	md := f.Message()
	if IsSecret(md) {
		*out = append(*out, Site{Root: root, Path: appendStep(path, step)})
		return
	}
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkMessage(root, md, appendStep(path, step), out, visited)
	delete(visited, md.FullName())
}

func handleMapField(root protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	valFd := f.MapValue()
	if valFd.Kind() != protoreflect.MessageKind {
		// map<K, scalar> — no Secrets possible.
		return
	}
	step := PathStep{
		Name:       string(f.Name()),
		Container:  ContainerMap,
		MapKeyKind: f.MapKey().Kind(),
	}
	md := valFd.Message()
	if IsSecret(md) {
		*out = append(*out, Site{Root: root, Path: appendStep(path, step)})
		return
	}
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkMessage(root, md, appendStep(path, step), out, visited)
	delete(visited, md.FullName())
}

// appendStep returns path+step in a fresh backing slice so concurrent
// branches of the walk can't stomp each other's tails.
func appendStep(path []PathStep, step PathStep) []PathStep {
	out := make([]PathStep, len(path)+1)
	copy(out, path)
	out[len(path)] = step
	return out
}
