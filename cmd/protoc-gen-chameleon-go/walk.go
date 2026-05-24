// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/internal/secretcatalog"
)

// The descriptor-level walk lives in internal/secretcatalog so the
// chameleon bundle CLI (planned) can validate module manifest
// patterns against the same site enumeration the codegen plugin
// uses. This file is the protogen-flavored adapter: it re-attaches
// the *protogen.Message Root that the codegen emit paths need for Go
// naming, and keeps the local type names (pathStep, secretSite, ...)
// the rest of the plugin and its tests refer to.

// secretFullName is re-exported as a local alias for legibility in
// existing call sites; the canonical constant lives in secretcatalog.
const secretFullName = secretcatalog.SecretFullName

// pathStep / container are descriptor-level concepts — alias straight
// through to the catalog types so the plugin and its tests use the
// same struct without an extra conversion step.
type pathStep = secretcatalog.PathStep

type container = secretcatalog.Container

const (
	containerScalar   = secretcatalog.ContainerScalar
	containerRepeated = secretcatalog.ContainerRepeated
	containerMap      = secretcatalog.ContainerMap
)

// secretSite pairs a catalog Site's path with the *protogen.Message
// Root the emit paths need (siteFuncName uses Root.GoIdent.GoName,
// emitOneResolvedWrapper iterates root.Fields, ...). The catalog
// Site itself only carries a protoreflect.MessageDescriptor.
type secretSite struct {
	Root *protogen.Message
	Path []pathStep
}

// findSecretSites bridges protogen → catalog → codegen-shaped sites.
// It maps each catalog Site back to its *protogen.Message Root using
// the file's top-level message list, so downstream emit code keeps
// access to Go-side naming and field iteration.
func findSecretSites(file *protogen.File) []secretSite {
	byName := make(map[protoreflect.FullName]*protogen.Message, len(file.Messages))
	for _, m := range file.Messages {
		byName[m.Desc.FullName()] = m
	}
	catalog := secretcatalog.FindInFile(file.Desc)
	sites := make([]secretSite, 0, len(catalog))
	for _, s := range catalog {
		sites = append(sites, secretSite{
			Root: byName[s.Root.FullName()],
			Path: s.Path,
		})
	}
	return sites
}

// mapValueMessage returns the *protogen.Message for the value type V
// of a `map<K, V>` field, or nil if V isn't a message (e.g.
// map<string, int>). Stays here because *protogen.Message is a
// codegen-side wrapper; the catalog walker uses protoreflect map-
// value descriptors directly and has no need for this helper.
//
// protogen represents `map<K, V>` as a synthetic entry message with
// `key` and `value` fields — f.Message is the entry, not V. Iterate
// by name (not by field number) for resilience against non-canonical
// entry shapes.
func mapValueMessage(f *protogen.Field) *protogen.Message {
	if f.Message == nil {
		return nil
	}
	for _, sub := range f.Message.Fields {
		if string(sub.Desc.Name()) == "value" && sub.Message != nil {
			return sub.Message
		}
	}
	return nil
}

// mapValueEnum is the enum-typed counterpart of mapValueMessage. For
// a `map<K, MyEnum>` field, returns the *protogen.Enum for MyEnum so
// the codegen can render `map[K]MyEnum` with the protogen-managed
// import. Returns nil for maps whose value isn't an enum.
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

// isSecret reports whether d is the pxf.Secret WKT. Thin wrapper so
// existing call sites in the plugin don't need to know about the
// catalog package.
func isSecret(d protoreflect.MessageDescriptor) bool {
	return secretcatalog.IsSecret(d)
}
