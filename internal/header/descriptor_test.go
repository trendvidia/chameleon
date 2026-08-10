// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// legacyLayerHeaderProto is the inline mirror internal/header compiled
// at runtime before #137 replaced it with the generated descriptor.
// Kept here — and ONLY here — as the reference shape the parse path is
// contractually bound to. protocompile is a test-only import in this
// package now; it does not reach a consumer's binary.
//
// If proto/chameleon/v1/layer.proto changes LayerHeader in a way that
// moves a field name, number, kind or cardinality, this test fails and
// the change is a wire-compat decision, not a refactor.
//
// Compiling it here costs consumers nothing beyond what they already
// carry: protocompile is how this repo builds schemas from source in
// tests throughout (resolver, merge, wkt, chain, validate, …), so it is
// a direct requirement of this module either way.
const legacyLayerHeaderProto = `
syntax = "proto3";
package chameleon.v1;

message LayerHeader {
  string id = 1;
  string environment = 2;
  bool encrypted = 3;
  string notes = 4;
  string content_sha256 = 5;
}
`

func legacyDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(
				map[string]string{"layer.proto": legacyLayerHeaderProto},
			)},
		),
	}
	files, err := comp.Compile(context.Background(), "layer.proto")
	if err != nil {
		t.Fatalf("compile legacy mirror: %v", err)
	}
	d, err := files.AsResolver().FindDescriptorByName(TypeName)
	if err != nil {
		t.Fatalf("resolve %s in legacy mirror: %v", TypeName, err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("legacy %s is not a message descriptor", TypeName)
	}
	return md
}

// TestDescriptorMatchesGeneratedShape pins the #137 migration: the
// descriptor now read from the generated bindings must be
// indistinguishable, for everything the parse path reads, from the
// inline mirror it replaced.
func TestDescriptorMatchesGeneratedShape(t *testing.T) {
	legacy := legacyDescriptor(t)
	got := descriptor()

	if got.FullName() != legacy.FullName() {
		t.Errorf("FullName = %q, want %q", got.FullName(), legacy.FullName())
	}
	if got.Syntax() != legacy.Syntax() {
		t.Errorf("Syntax = %v, want %v", got.Syntax(), legacy.Syntax())
	}
	if got.Oneofs().Len() != legacy.Oneofs().Len() {
		t.Errorf("Oneofs = %d, want %d", got.Oneofs().Len(), legacy.Oneofs().Len())
	}

	gf, lf := got.Fields(), legacy.Fields()
	if gf.Len() != lf.Len() {
		t.Fatalf("field count = %d, want %d", gf.Len(), lf.Len())
	}
	for i := range lf.Len() {
		want, have := lf.Get(i), gf.Get(i)
		if have.Name() != want.Name() {
			t.Errorf("field %d: Name = %q, want %q", i, have.Name(), want.Name())
		}
		if have.Number() != want.Number() {
			t.Errorf("field %s: Number = %d, want %d", want.Name(), have.Number(), want.Number())
		}
		if have.Kind() != want.Kind() {
			t.Errorf("field %s: Kind = %v, want %v", want.Name(), have.Kind(), want.Kind())
		}
		if have.Cardinality() != want.Cardinality() {
			t.Errorf("field %s: Cardinality = %v, want %v", want.Name(), have.Cardinality(), want.Cardinality())
		}
		// Check reads presence via pxf.Result, but a descriptor-level
		// presence divergence would still change dynamicpb's behaviour.
		if have.HasPresence() != want.HasPresence() {
			t.Errorf("field %s: HasPresence = %v, want %v", want.Name(), have.HasPresence(), want.HasPresence())
		}
	}
}

// TestDescriptorCarriesNoFieldExtensions covers the specific worry #137
// raised: the real layer.proto imports pxf/annotations.proto and the
// inline mirror did not. The import is inert for this message — no
// LayerHeader field carries a pxf extension — so pxf.UnmarshalFull sees
// the same thing either way. If someone later annotates a LayerHeader
// field, this fails and the parse path needs re-examining rather than
// silently changing behaviour.
func TestDescriptorCarriesNoFieldExtensions(t *testing.T) {
	fields := descriptor().Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		proto.RangeExtensions(f.Options(), func(xt protoreflect.ExtensionType, _ any) bool {
			t.Errorf("field %s carries unexpected extension %s; the parse path assumed LayerHeader is annotation-free",
				f.Name(), xt.TypeDescriptor().FullName())
			return true
		})
	}
}
