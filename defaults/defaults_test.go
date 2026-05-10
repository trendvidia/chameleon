// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package defaults_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/defaults"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const annotationsProtoSrc = `
syntax = "proto3";
package pxf;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FieldOptions {
  bool required = 50000;
  string default = 50001;
}
`

const appProtoSrc = `
syntax = "proto3";
package def_test.v1;

import "pxf/annotations.proto";

message AppConfig {
  string base_url = 1 [(pxf.default) = "mybigproject.com"];
  int32 port = 2 [(pxf.default) = "8080"];
  bool feature_on = 3 [(pxf.default) = "true"];
  string no_default = 4;
  Endpoint primary = 5;
  Endpoint optional_endpoint = 6;
  repeated string tags = 7 [(pxf.default) = "ignored"];
}

message Endpoint {
  string host = 1 [(pxf.default) = "localhost"];
  int32 port = 2 [(pxf.default) = "443"];
}
`

func compileApp(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"app.proto":             appProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "app.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("def_test.v1.AppConfig")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// parseInput uses SkipPostDecode so this test exercises chameleon's
// post-merge defaults behavior, not protowire-go's per-parse one.
func parseInput(t *testing.T, desc protoreflect.MessageDescriptor, input string) (proto.Message, map[string]struct{}) {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	present := make(map[string]struct{})
	for _, p := range res.PresentFields() {
		present[p] = struct{}{}
	}
	return msg, present
}

func get(t *testing.T, msg proto.Message, path ...string) protoreflect.Value {
	t.Helper()
	cur := msg.ProtoReflect()
	for i, name := range path {
		fd := cur.Descriptor().Fields().ByName(protoreflect.Name(name))
		require.NotNil(t, fd, "field %q", name)
		if i == len(path)-1 {
			return cur.Get(fd)
		}
		cur = cur.Get(fd).Message()
	}
	return protoreflect.Value{}
}

// --- core ---

func TestApply_TopLevelScalarDefault(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, ``) // nothing set
	require.NoError(t, defaults.Apply(msg, present))

	assert.Equal(t, "mybigproject.com", get(t, msg, "base_url").String())
	assert.Equal(t, int64(8080), get(t, msg, "port").Int())
	assert.True(t, get(t, msg, "feature_on").Bool())
}

func TestApply_LayerSetValueIsPreserved(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, `base_url = "explicit.example.com"`)
	require.NoError(t, defaults.Apply(msg, present))

	assert.Equal(t, "explicit.example.com", get(t, msg, "base_url").String(),
		"explicit value must not be overwritten by default")
	assert.Equal(t, int64(8080), get(t, msg, "port").Int(),
		"port still gets its default")
}

func TestApply_NoDefaultStaysZero(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))

	// no_default has no annotation; stays empty.
	assert.Equal(t, "", get(t, msg, "no_default").String())
}

func TestApply_DefaultAppliedPathMarkedPresent(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))

	_, ok := present["base_url"]
	assert.True(t, ok, "default-filled path must be added to presence map for validate")
}

// --- nested ---

func TestApply_NestedDefaultsAppliedWhenParentLive(t *testing.T) {
	desc := compileApp(t)
	// primary has SOME field set, making the parent live.
	msg, present := parseInput(t, desc, `
primary {
  host = "explicit.example.com"
}
`)
	require.NoError(t, defaults.Apply(msg, present))

	assert.Equal(t, "explicit.example.com", get(t, msg, "primary", "host").String())
	assert.Equal(t, int64(443), get(t, msg, "primary", "port").Int(),
		"primary.port gets its default because primary is live")
}

func TestApply_NestedDefaultsSkippedWhenParentAbsent(t *testing.T) {
	desc := compileApp(t)
	// optional_endpoint is never mentioned. Defaults pass must not
	// materialize a stub Endpoint just to fill its inner defaults.
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))

	r := msg.ProtoReflect()
	fd := r.Descriptor().Fields().ByName("optional_endpoint")
	assert.False(t, r.Has(fd), "optional_endpoint must remain absent")
}

// --- skip lists, maps, WKTs ---

func TestApply_RepeatedFieldDefaultIgnored(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))

	r := msg.ProtoReflect()
	tags := r.Get(r.Descriptor().Fields().ByName("tags")).List()
	assert.Equal(t, 0, tags.Len(), "repeated fields don't accept defaults")
}

func TestApply_NilMessage(t *testing.T) {
	require.Error(t, defaults.Apply(nil, nil))
}

func TestApply_NilPresenceMapTreatedAsEmpty(t *testing.T) {
	desc := compileApp(t)
	msg := dynamicpb.NewMessage(desc)
	// Nil present map: Apply should treat as "nothing present", apply
	// every default. Nil means "no layer mentioned anything", which is
	// equivalent to an empty presence union.
	require.NoError(t, defaults.Apply(msg, nil))
	assert.Equal(t, "mybigproject.com", get(t, msg, "base_url").String())
}
