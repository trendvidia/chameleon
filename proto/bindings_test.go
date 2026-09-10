// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	pxfpb "github.com/trendvidia/chameleon/proto/pxf"
)

// TestBindingsCarryRegisteredAnnotationNumbers guards against #179: the
// generated bindings falling behind the vendored pxf/annotations.proto.
// #177 moved the annotation numbers to the registered block and
// regenerated annotations.pb.go, but not the bindings that use them, so
// chain.pb.go kept 50000-series field options in its raw descriptor.
// protowire-go read 1314/1315, saw nothing it knew, and every
// (pxf.required) on ChainSpec went unenforced with no diagnostic.
//
// After regeneration the options resolve to pxfpb's extension types and
// nothing is left over. So: every field option on every message in the
// chameleon.v1 files parses fully (no unknown fields — a retired number
// would be one), and the annotations we know are there are seen through
// the registered extensions (positive control, so a binding with no
// options at all cannot pass vacuously).
func TestBindingsCarryRegisteredAnnotationNumbers(t *testing.T) {
	files := []protoreflect.FileDescriptor{
		chameleonv1.File_chameleon_v1_chain_proto,
		chameleonv1.File_chameleon_v1_layer_proto,
		chameleonv1.File_chameleon_v1_bundle_manifest_proto,
		chameleonv1.File_chameleon_v1_user_config_proto,
	}
	var required int
	for _, fd := range files {
		walkMessages(fd.Messages(), func(md protoreflect.MessageDescriptor) {
			fields := md.Fields()
			for i := range fields.Len() {
				f := fields.Get(i)
				opts := f.Options()
				if opts == nil {
					continue
				}
				assert.Empty(t, opts.ProtoReflect().GetUnknown(),
					"%s: field options carry bytes no known extension claims — a retired annotation number? run `make generate`", f.FullName())
				if proto.HasExtension(opts, pxfpb.E_Required) {
					required++
				}
			}
		})
	}
	require.Positive(t, required, "no field in chameleon.v1 is seen as (pxf.required) through the registered extension; the bindings do not match pxf/annotations.proto")
}

func walkMessages(mds protoreflect.MessageDescriptors, fn func(protoreflect.MessageDescriptor)) {
	for i := range mds.Len() {
		md := mds.Get(i)
		fn(md)
		walkMessages(md.Messages(), fn)
	}
}
