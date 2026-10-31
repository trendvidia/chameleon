// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package upstreamtest builds descriptors for tests of the required and
// default pair. It writes their options as raw bytes rather than
// compiling a source, because protocompile refuses the pair too
// (protowire#453), so a compiled fixture stops compiling on the bump
// that brings that release in.
package upstreamtest

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Option numbers from pxf/annotations.proto and, for the carrier that
// holds @ annotations, protowire/schema/v1/descriptor.proto.
const (
	numRequired = 1314
	numDefault  = 1315
	numCarrier  = 1327
)

// Required is the bracket option (pxf.required) = v, as raw option bytes.
func Required(v bool) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, numRequired, protowire.VarintType), protowire.EncodeBool(v))
}

// Default is the bracket option (pxf.default) = lit, as raw option bytes.
func Default(lit string) []byte {
	return protowire.AppendString(protowire.AppendTag(nil, numDefault, protowire.BytesType), lit)
}

// AtRequired is the annotation @required, as raw option bytes.
func AtRequired() []byte {
	return carrierEntry(annotation("protowire.schema.v1.required", nil))
}

// AtDefault is the annotation @default(lit), as raw option bytes.
func AtDefault(lit string) []byte {
	arg := protowire.AppendString(protowire.AppendTag(nil, 10, protowire.BytesType), lit) // AnnotationArg.string_value
	return carrierEntry(annotation("protowire.schema.v1.default", arg))
}

func annotation(name string, arg []byte) []byte {
	b := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), name) // Annotation.name
	if arg != nil {
		b = protowire.AppendBytes(protowire.AppendTag(b, 2, protowire.BytesType), arg) // Annotation.args
	}
	return b
}

func carrierEntry(ann []byte) []byte {
	list := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), ann) // AnnotationList.entries
	return protowire.AppendBytes(protowire.AppendTag(nil, numCarrier, protowire.BytesType), list)
}

// Message declares message name with one string field f carrying raw as
// its options.
func Message(name string, raw ...[]byte) *descriptorpb.DescriptorProto {
	opts := &descriptorpb.FieldOptions{}
	var all []byte
	for _, r := range raw {
		all = append(all, r...)
	}
	opts.ProtoReflect().SetUnknown(protoreflect.RawFields(all))
	return &descriptorpb.DescriptorProto{
		Name: proto.String(name),
		Field: []*descriptorpb.FieldDescriptorProto{{
			Name:     proto.String("f"),
			Number:   proto.Int32(1),
			JsonName: proto.String("f"),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			Options:  opts,
		}},
	}
}

// File builds path, in package pair.v1, declaring msgs. imports must
// already be built, and are listed as its dependencies.
func File(tb testing.TB, path string, msgs []*descriptorpb.DescriptorProto, imports ...protoreflect.FileDescriptor) protoreflect.FileDescriptor {
	tb.Helper()
	fdp := &descriptorpb.FileDescriptorProto{
		Name:        proto.String(path),
		Package:     proto.String("pair.v1"),
		Syntax:      proto.String("proto3"),
		MessageType: msgs,
	}
	reg := new(protoregistry.Files)
	for _, imp := range imports {
		fdp.Dependency = append(fdp.Dependency, imp.Path())
		if err := reg.RegisterFile(imp); err != nil {
			tb.Fatalf("register %s: %v", imp.Path(), err)
		}
	}
	fd, err := protodesc.NewFile(fdp, reg)
	if err != nil {
		tb.Fatalf("build %s: %v", path, err)
	}
	return fd
}
