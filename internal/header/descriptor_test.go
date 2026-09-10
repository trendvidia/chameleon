// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"context"
	"testing"

	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
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
// It is a mirror, though, so a red test can always be "fixed" by
// editing the mirror to match. TestDescriptorFieldContract writes the
// same five fields out as a literal for exactly that reason: it is the
// assertion that cannot be repaired by moving the reference.
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
	// Paired by field number, not by declaration index. Reordering the
	// declarations in layer.proto while keeping the numbers is
	// wire-compatible and invisible to the parse path, which only ever
	// looks fields up by name; pairing on index would red this test with
	// a cascade of renames and renumbers that did not happen.
	for i := range lf.Len() {
		want := lf.Get(i)
		have := gf.ByNumber(want.Number())
		if have == nil {
			t.Errorf("field %s (number %d) is absent from the generated descriptor", want.Name(), want.Number())
			continue
		}
		if have.Name() != want.Name() {
			t.Errorf("field number %d: Name = %q, want %q", want.Number(), have.Name(), want.Name())
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

// TestDescriptorFieldContract writes out the shape the parse path is
// bound to, rather than deriving it from a mirror that can be edited to
// match (see legacyLayerHeaderProto).
//
// The names matter as much as the numbers, and they are not checked by
// the compiler: Check and ContentSHA256 reach every value through a
// string — `p.result.IsSet("id")`, `Fields().ByName("content_sha256")`.
// A rename in layer.proto does not fail the build and does not panic
// either, because every ByName sits behind the matching IsSet guard;
// it just makes IsSet permanently false, so the assertion silently
// stops being checked and `chameleon verify` silently stops comparing
// the hash. That is the failure this test exists to make loud.
func TestDescriptorFieldContract(t *testing.T) {
	want := []struct {
		name   protoreflect.Name
		number protoreflect.FieldNumber
		kind   protoreflect.Kind
	}{
		{"id", 1, protoreflect.StringKind},
		{"environment", 2, protoreflect.StringKind},
		{"encrypted", 3, protoreflect.BoolKind},
		{"notes", 4, protoreflect.StringKind},
		{"content_sha256", 5, protoreflect.StringKind},
	}

	fields := descriptor().Fields()
	if fields.Len() != len(want) {
		t.Errorf("field count = %d, want %d", fields.Len(), len(want))
	}
	for _, w := range want {
		f := fields.ByName(w.name)
		if f == nil {
			t.Errorf("field %q is absent; the parse path indexes by this name and would silently stop checking it", w.name)
			continue
		}
		if f.Number() != w.number {
			t.Errorf("field %q: Number = %d, want %d", w.name, f.Number(), w.number)
		}
		if f.Kind() != w.kind {
			t.Errorf("field %q: Kind = %v, want %v", w.name, f.Kind(), w.kind)
		}
		if f.Cardinality() != protoreflect.Optional {
			t.Errorf("field %q: Cardinality = %v, want optional", w.name, f.Cardinality())
		}
	}
}

// TestDescriptorClosureHasNoBindTimeViolations pins the other thing the
// switch to the generated descriptor widened.
//
// pxf.UnmarshalFull validates the message's whole file closure on every
// call, and a violation in ANY file in it fails the decode. The inline
// mirror's closure was one self-contained file; the generated one is
// three — chameleon/v1/layer.proto -> pxf/annotations.proto ->
// google/protobuf/descriptor.proto. So a bind-time violation introduced
// in a file nobody edited alongside this one (a new import of a
// chameleon proto with a field named `null`, a misplaced (pxf.key))
// would now fail every header parse, i.e. every layer resolve, with an
// error naming a file the operator never wrote.
//
// TestDescriptorCarriesNoFieldExtensions stays green through all of
// that: it only reads LayerHeader's own field options.
func TestDescriptorClosureHasNoBindTimeViolations(t *testing.T) {
	if v := pxf.ValidateFile(descriptor().ParentFile()); len(v) != 0 {
		t.Errorf("LayerHeader's file closure has %d bind-time violation(s), which fails every header parse: %v", len(v), v)
	}
}
