// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secretcatalog

import "google.golang.org/protobuf/reflect/protoreflect"

// FindNonSecretFieldsInMessage returns a Site for every non-secret
// field reachable from md. Mirrors FindInMessage but emits at every
// field whose value type is NOT pxf.Secret — leaf scalars, repeated
// scalars, map<K, scalar>, intermediate non-secret messages, lists
// of non-secret messages, and maps of non-secret messages.
//
// Used by the bundler to typo-guard attribute_allow / attribute_deny
// patterns: every pattern must match at least one Site.SchemaPath in
// this enumeration, mirroring the existing Secret-side guard.
//
// Traversal rules:
//
//   - Every non-secret field (scalar, list, map, message) emits one
//     Site whose path ends at the field.
//   - For non-secret message-typed fields (scalar/list/map) the walk
//     ALSO recurses into the message's children, so nested fields
//     are individually enumerated too.
//   - Secret-bearing fields are skipped — they belong to the Secret
//     catalog (FindInMessage), not this one.
//   - Cycles in the message graph are broken with the same path-set
//     tracking used in FindInMessage.
func FindNonSecretFieldsInMessage(md protoreflect.MessageDescriptor) []Site {
	var sites []Site
	visited := map[protoreflect.FullName]bool{md.FullName(): true}
	walkNonSecret(md, md, nil, &sites, visited)
	return sites
}

// FindNonSecretFieldsInFile is the file-scoped counterpart, mirroring
// FindInFile. Walks every top-level message in fd and returns the
// union of their non-secret field sites.
func FindNonSecretFieldsInFile(fd protoreflect.FileDescriptor) []Site {
	var sites []Site
	msgs := fd.Messages()
	for i := range msgs.Len() {
		msg := msgs.Get(i)
		visited := map[protoreflect.FullName]bool{msg.FullName(): true}
		walkNonSecret(msg, msg, nil, &sites, visited)
	}
	return sites
}

func walkNonSecret(root, current protoreflect.MessageDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	fields := current.Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		switch {
		case f.IsMap():
			handleNonSecretMap(root, f, path, out, visited)
		case f.IsList():
			handleNonSecretList(root, f, path, out, visited)
		case f.Kind() == protoreflect.MessageKind:
			handleNonSecretScalarMessage(root, f, path, out, visited)
		default:
			// Plain scalar (string, int, bool, enum, bytes, ...).
			step := PathStep{Name: string(f.Name()), Container: ContainerScalar}
			*out = append(*out, Site{Root: root, Path: appendStep(path, step)})
		}
	}
}

func handleNonSecretScalarMessage(root protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	md := f.Message()
	if IsSecret(md) {
		return
	}
	step := PathStep{Name: string(f.Name()), Container: ContainerScalar}
	fieldPath := appendStep(path, step)
	*out = append(*out, Site{Root: root, Path: fieldPath})
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkNonSecret(root, md, fieldPath, out, visited)
	delete(visited, md.FullName())
}

func handleNonSecretList(root protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	step := PathStep{Name: string(f.Name()), Container: ContainerRepeated}
	fieldPath := appendStep(path, step)
	if f.Kind() != protoreflect.MessageKind {
		// repeated scalar — the container itself is a non-secret site.
		*out = append(*out, Site{Root: root, Path: fieldPath})
		return
	}
	md := f.Message()
	if IsSecret(md) {
		return
	}
	*out = append(*out, Site{Root: root, Path: fieldPath})
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkNonSecret(root, md, fieldPath, out, visited)
	delete(visited, md.FullName())
}

func handleNonSecretMap(root protoreflect.MessageDescriptor, f protoreflect.FieldDescriptor, path []PathStep, out *[]Site, visited map[protoreflect.FullName]bool) {
	valFd := f.MapValue()
	step := PathStep{
		Name:       string(f.Name()),
		Container:  ContainerMap,
		MapKeyKind: f.MapKey().Kind(),
	}
	fieldPath := appendStep(path, step)
	if valFd.Kind() != protoreflect.MessageKind {
		// map<K, scalar> — the container itself is a non-secret site.
		*out = append(*out, Site{Root: root, Path: fieldPath})
		return
	}
	md := valFd.Message()
	if IsSecret(md) {
		return
	}
	*out = append(*out, Site{Root: root, Path: fieldPath})
	if visited[md.FullName()] {
		return
	}
	visited[md.FullName()] = true
	walkNonSecret(root, md, fieldPath, out, visited)
	delete(visited, md.FullName())
}
