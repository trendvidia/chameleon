// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package merge_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/merge"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
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
  repeated string aliases = 3;
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

// An empty container is a set value: "replace whole" with nothing, which
// is what `tags = []` says. It is also the one set value protoreflect
// will not hand back in a form Set accepts — Get on an empty list or map
// returns a read-only placeholder — so it gets its own cases, in both
// message implementations: generated code panics on that placeholder and
// dynamicpb has its own path, so one passing says nothing about the other.

func TestMerge_EmptyContainerClearsLowerLayer(t *testing.T) {
	cases := []struct {
		name        string
		base, empty string
		field       string
		size        func(proto.Message) int
	}{
		{
			name:  "repeated scalar",
			base:  `tags = ["a", "b"]`,
			empty: `tags = []`,
			field: "tags",
			size:  func(m proto.Message) int { return get(t, m, "tags").List().Len() },
		},
		{
			name:  "repeated message",
			base:  `replicas = [{ url = "u" }]`,
			empty: `replicas = []`,
			field: "replicas",
			size:  func(m proto.Message) int { return get(t, m, "replicas").List().Len() },
		},
		{
			name:  "map of scalars",
			base:  `labels = { "env": "prod" }`,
			empty: `labels = {}`,
			field: "labels",
			size:  func(m proto.Message) int { return get(t, m, "labels").Map().Len() },
		},
		{
			name:  "map of messages",
			base:  `tenants = { "acme": { url = "u" } }`,
			empty: `tenants = {}`,
			field: "tenants",
			size:  func(m proto.Message) int { return get(t, m, "tenants").Map().Len() },
		},
	}
	desc := compileApp(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := newDst(t, desc)
			base, baseP := parseLayer(t, desc, tc.base)
			require.NoError(t, merge.Merge(dst, base, baseP))
			require.NotZero(t, tc.size(dst), "control: the base layer must populate %s", tc.field)

			over, overP := parseLayer(t, desc, tc.empty)
			require.True(t, overP.IsSet(tc.field), "control: %s must parse as set, not absent or null", tc.field)
			require.NoError(t, merge.Merge(dst, over, overP))
			assert.Equal(t, 0, tc.size(dst), "an empty %s must replace the lower layer's", tc.field)
		})
	}
}

func TestMerge_EmptyContainerInNestedMessage(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)

	base, baseP := parseLayer(t, desc, `primary { url = "u" aliases = ["a"] }`)
	require.NoError(t, merge.Merge(dst, base, baseP))

	over, overP := parseLayer(t, desc, `primary { aliases = [] }`)
	require.NoError(t, merge.Merge(dst, over, overP))

	primary := get(t, dst, "primary").Message()
	d := primary.Descriptor()
	assert.Equal(t, 0, primary.Get(d.Fields().ByName("aliases")).List().Len())
	assert.Equal(t, "u", primary.Get(d.Fields().ByName("url")).String(), "the sibling must survive the deep merge")
}

func TestMerge_EmptyContainerInGeneratedMessage(t *testing.T) {
	cases := []struct {
		name   string
		empty  string
		base   func() *chameleonv1.ChainSpec
		sizeOf func(*chameleonv1.ChainSpec) int
	}{
		{
			name:  "repeated",
			empty: `version = "1" environments = [] layers = []`,
			base: func() *chameleonv1.ChainSpec {
				return &chameleonv1.ChainSpec{Environments: []*chameleonv1.Environment{{Name: "prod"}}}
			},
			sizeOf: func(c *chameleonv1.ChainSpec) int { return len(c.GetEnvironments()) },
		},
		{
			name:  "map",
			empty: `version = "1" environments = [] layers = [] sources = {}`,
			base: func() *chameleonv1.ChainSpec {
				return &chameleonv1.ChainSpec{Sources: map[string]*chameleonv1.SourceRef{"s": {}}}
			},
			sizeOf: func(c *chameleonv1.ChainSpec) int { return len(c.GetSources()) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := tc.base()
			require.NotZero(t, tc.sizeOf(dst), "control: dst must start populated")

			over := &chameleonv1.ChainSpec{}
			overP, err := pxf.UnmarshalFull([]byte(tc.empty), over)
			require.NoError(t, err)
			require.NoError(t, merge.Merge(dst, over, overP))
			assert.Zero(t, tc.sizeOf(dst))
		})
	}
}

// Merge writes only to dst, and what it hands over is shared rather than
// copied. Both halves are the documented contract.

func TestMerge_DoesNotWriteSrc(t *testing.T) {
	desc := compileApp(t)
	dst := newDst(t, desc)
	base, baseP := parseLayer(t, desc, `base_url = "b" primary { url = "u" } tags = ["old"]`)
	require.NoError(t, merge.Merge(dst, base, baseP))

	src, srcP := parseLayer(t, desc, `primary { port = 1 } tags = ["a"] labels = { "k": "v" }`)
	before := proto.Clone(src)
	require.NoError(t, merge.Merge(dst, src, srcP))
	assert.True(t, proto.Equal(before, src), "Merge must not write src")
}

func TestMerge_DstSharesSrcStorage(t *testing.T) {
	desc := compileApp(t)

	t.Run("list", func(t *testing.T) {
		dst := newDst(t, desc)
		src, p := parseLayer(t, desc, `tags = ["a"]`)
		require.NoError(t, merge.Merge(dst, src, p))
		get(t, dst, "tags").List().Set(0, protoreflect.ValueOfString("changed"))
		assert.Equal(t, "changed", get(t, src, "tags").List().Get(0).String())
	})
	t.Run("map", func(t *testing.T) {
		dst := newDst(t, desc)
		src, p := parseLayer(t, desc, `labels = { "k": "v" }`)
		require.NoError(t, merge.Merge(dst, src, p))
		key := protoreflect.ValueOfString("k").MapKey()
		get(t, dst, "labels").Map().Set(key, protoreflect.ValueOfString("changed"))
		assert.Equal(t, "changed", get(t, src, "labels").Map().Get(key).String())
	})
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
