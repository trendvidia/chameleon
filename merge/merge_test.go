// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package merge_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/merge"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// Test schema captures every shape merge must handle: scalar with and
// without explicit-presence (proto3 optional), nested message, repeated
// scalar, repeated message, map<string, scalar>, map<string, message>.
const protoSrc = `
syntax = "proto3";
package merge_test.v1;

message Endpoint {
  string url = 1;
  int32 port = 2;
}

message AppConfig {
  string base_url = 1;
  optional string service_name = 2;
  Endpoint primary = 3;
  repeated string tags = 4;
  repeated Endpoint replicas = 5;
  map<string, string> labels = 6;
  map<string, Endpoint> tenants = 7;
}
`

func compileApp(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{"test.proto": protoSrc}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(sources),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := result.AsResolver().FindDescriptorByName("merge_test.v1.AppConfig")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// parseLayer compiles input via UnmarshalFull, returning a freshly-
// constructed dynamic message + presence result. Each call returns an
// independent message so tests can inspect parsed-vs-merged separately.
func parseLayer(t *testing.T, desc protoreflect.MessageDescriptor, input string) (proto.Message, *pxf.Result) {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	res, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	return msg, res
}

func newDst(t *testing.T, desc protoreflect.MessageDescriptor) proto.Message {
	t.Helper()
	return dynamicpb.NewMessage(desc)
}

func get(t *testing.T, msg proto.Message, name string) protoreflect.Value {
	t.Helper()
	r := msg.ProtoReflect()
	fd := r.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "field %q", name)
	return r.Get(fd)
}

// --- scalar overlay ---

func TestMerge_ScalarOverwritesScalar(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `base_url = "base.example.com"`)
	require.NoError(t, merge.Merge(dst, base, baseP))
	assert.Equal(t, "base.example.com", get(t, dst, "base_url").String())

	over, overP := parseLayer(t, desc, `base_url = "dev.example.com"`)
	require.NoError(t, merge.Merge(dst, over, overP))
	assert.Equal(t, "dev.example.com", get(t, dst, "base_url").String())
}

func TestMerge_AbsentFieldFallsThrough(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `base_url = "base"`)
	require.NoError(t, merge.Merge(dst, base, baseP))

	// Higher layer doesn't mention base_url; old value must survive.
	over, overP := parseLayer(t, desc, `service_name = "api"`)
	require.NoError(t, merge.Merge(dst, over, overP))

	assert.Equal(t, "base", get(t, dst, "base_url").String())
	assert.Equal(t, "api", get(t, dst, "service_name").String())
}

func TestMerge_NullClearsScalar(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `service_name = "api"`)
	require.NoError(t, merge.Merge(dst, base, baseP))
	assert.Equal(t, "api", get(t, dst, "service_name").String())

	over, overP := parseLayer(t, desc, `service_name = null`)
	require.NoError(t, merge.Merge(dst, over, overP))

	dr := dst.ProtoReflect()
	fd := dr.Descriptor().Fields().ByName("service_name")
	assert.False(t, dr.Has(fd), "service_name should be cleared after null overlay")
}

// --- nested message: deep merge ---

func TestMerge_NestedMessageDeepMerges(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `
primary {
  url = "https://base.example.com"
  port = 8080
}
`)
	require.NoError(t, merge.Merge(dst, base, baseP))

	// Higher layer touches only `port`. `url` from base must survive.
	over, overP := parseLayer(t, desc, `
primary {
  port = 9090
}
`)
	require.NoError(t, merge.Merge(dst, over, overP))

	primary := get(t, dst, "primary").Message()
	d := primary.Descriptor()
	assert.Equal(t, "https://base.example.com", primary.Get(d.Fields().ByName("url")).String())
	assert.Equal(t, int64(9090), primary.Get(d.Fields().ByName("port")).Int())
}

func TestMerge_NullOnNestedMessageClearsWhole(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `
primary {
  url = "u"
  port = 1
}
`)
	require.NoError(t, merge.Merge(dst, base, baseP))

	over, overP := parseLayer(t, desc, `primary = null`)
	require.NoError(t, merge.Merge(dst, over, overP))

	dr := dst.ProtoReflect()
	fd := dr.Descriptor().Fields().ByName("primary")
	assert.False(t, dr.Has(fd), "primary should be cleared after null overlay")
}

// --- repeated/map: replace whole ---

func TestMerge_RepeatedListReplacesWhole(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `tags = ["a", "b", "c"]`)
	require.NoError(t, merge.Merge(dst, base, baseP))
	assert.Equal(t, 3, get(t, dst, "tags").List().Len())

	over, overP := parseLayer(t, desc, `tags = ["x"]`)
	require.NoError(t, merge.Merge(dst, over, overP))

	list := get(t, dst, "tags").List()
	require.Equal(t, 1, list.Len())
	assert.Equal(t, "x", list.Get(0).String())
}

func TestMerge_AbsentRepeatedFallsThrough(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `tags = ["a", "b"]`)
	require.NoError(t, merge.Merge(dst, base, baseP))

	over, overP := parseLayer(t, desc, `base_url = "x"`)
	require.NoError(t, merge.Merge(dst, over, overP))

	assert.Equal(t, 2, get(t, dst, "tags").List().Len(), "base tags must survive when higher layer doesn't touch")
}

func TestMerge_MapReplacesWhole(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `
labels = {
  "env": "prod"
  "tier": "1"
}
`)
	require.NoError(t, merge.Merge(dst, base, baseP))
	assert.Equal(t, 2, get(t, dst, "labels").Map().Len())

	over, overP := parseLayer(t, desc, `
labels = {
  "env": "dev"
}
`)
	require.NoError(t, merge.Merge(dst, over, overP))

	m := get(t, dst, "labels").Map()
	assert.Equal(t, 1, m.Len(), "map must be replaced wholesale, tier should be gone")
}

func TestMerge_MapOfMessagesReplacesWhole(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `
tenants = {
  "acme": {
    url = "u-a"
    port = 1
  }
  "globex": {
    url = "u-g"
    port = 2
  }
}
`)
	require.NoError(t, merge.Merge(dst, base, baseP))

	over, overP := parseLayer(t, desc, `
tenants = {
  "acme": {
    url = "u-a-new"
    port = 10
  }
}
`)
	require.NoError(t, merge.Merge(dst, over, overP))

	m := get(t, dst, "tenants").Map()
	require.Equal(t, 1, m.Len())
	acme := m.Get(protoreflect.ValueOfString("acme").MapKey()).Message()
	d := acme.Descriptor()
	assert.Equal(t, "u-a-new", acme.Get(d.Fields().ByName("url")).String())
	assert.Equal(t, int64(10), acme.Get(d.Fields().ByName("port")).Int())
}

// --- chain materialization ---

func TestMerge_LongChain(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	// base
	l, p := parseLayer(t, desc, `
base_url = "example.com"
primary {
  url = "https://api.example.com"
  port = 8080
}
tags = ["base"]
labels = { "tier": "free" }
`)
	require.NoError(t, merge.Merge(dst, l, p))

	// prod overlay
	l, p = parseLayer(t, desc, `
service_name = "billing"
labels = { "tier": "enterprise" "env": "prod" }
`)
	require.NoError(t, merge.Merge(dst, l, p))

	// region overlay (replaces tags)
	l, p = parseLayer(t, desc, `
tags = ["prod", "us-east"]
primary {
  port = 443
}
`)
	require.NoError(t, merge.Merge(dst, l, p))

	// Verify final state.
	assert.Equal(t, "example.com", get(t, dst, "base_url").String())
	assert.Equal(t, "billing", get(t, dst, "service_name").String())

	primary := get(t, dst, "primary").Message()
	pd := primary.Descriptor()
	assert.Equal(t, "https://api.example.com", primary.Get(pd.Fields().ByName("url")).String(),
		"primary.url from base must survive — region only set port")
	assert.Equal(t, int64(443), primary.Get(pd.Fields().ByName("port")).Int())

	tags := get(t, dst, "tags").List()
	require.Equal(t, 2, tags.Len())
	assert.Equal(t, "prod", tags.Get(0).String())
	assert.Equal(t, "us-east", tags.Get(1).String())

	labels := get(t, dst, "labels").Map()
	require.Equal(t, 2, labels.Len(), "prod replaced base's map; region didn't touch labels")
}

// --- error paths ---

func TestMerge_TypeMismatch(t *testing.T) {
	desc := compileApp(t)
	a := newDst(t, desc)

	// Build a different message type.
	const otherSrc = `
syntax = "proto3";
package merge_test.v1;
message Other { string s = 1; }
`
	sources := map[string]string{"other.proto": otherSrc}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	res, err := comp.Compile(context.Background(), "other.proto")
	require.NoError(t, err)
	otherDesc := res[0].Messages().ByName("Other")
	b := dynamicpb.NewMessage(otherDesc)

	// Need any non-nil presence — Merge errors before consulting it.
	_, p := parseLayer(t, desc, `base_url = "x"`)
	err = merge.Merge(a, b, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type mismatch")
}

func TestMerge_NilArgs(t *testing.T) {
	desc := compileApp(t)
	a := newDst(t, desc)
	_, p := parseLayer(t, desc, `base_url = "x"`)

	require.ErrorContains(t, merge.Merge(nil, a, p), "nil message")
	require.ErrorContains(t, merge.Merge(a, nil, p), "nil message")
	require.ErrorContains(t, merge.Merge(a, a, nil), "nil presence")
}
