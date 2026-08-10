// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package defaults_test

import (
	"context"
	"testing"
	"time"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/defaults"
	"github.com/trendvidia/chameleon/interpolate"
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
  repeated string tags = 7;
}

message Endpoint {
  string host = 1 [(pxf.default) = "localhost"];
  int32 port = 2 [(pxf.default) = "443"];
}
`

func compileApp(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return compileMessage(t, appProtoSrc, "def_test.v1.AppConfig")
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

// A (pxf.default) on a repeated or map field used to be dropped in
// silence — the failure mode issue #131 was filed about, and the one
// place #129 deliberately left alone. protowire-go v1.4.0 answered it
// upstream (trendvidia/protowire-go#68, draft -01
// §annotation-extensions): one literal cannot denote a list, so the
// placement does not bind at all and no implementation may invent a
// semantics for it.
//
// What this pins is therefore no longer chameleon's behaviour but the
// contract chameleon relies on: the annotation is rejected before
// defaults.Apply is ever reached. A protowire-go bump that quietly
// re-admitted the placement would make defaults.Apply's silence
// reachable again, which is what this catches.
func TestRepeatedFieldDefaultDoesNotBind(t *testing.T) {
	const src = `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
message M {
  repeated string tags = 1 [(pxf.default) = "ignored"];
  map<string, string> labels = 2 [(pxf.default) = "ignored"];
}`

	violations := pxf.ValidateDescriptor(compileMessage(t, src, "def_test.v1.M"))
	require.NotEmpty(t, violations, "a (pxf.default) on a list must not bind")

	byElement := map[string]pxf.Violation{}
	for _, v := range violations {
		byElement[v.Element] = v
	}
	for _, field := range []string{"def_test.v1.M.tags", "def_test.v1.M.labels"} {
		v, ok := byElement[field]
		require.Truef(t, ok, "no violation reported for %s (got %v)", field, violations)
		assert.Equal(t, pxf.ViolationDefaultOption, v.Kind)
		assert.Equal(t, "ignored", v.Name, "the violation carries the offending literal")
	}
}

// And the same placement fails the parse, which is the path every
// chameleon layer actually takes.
func TestRepeatedFieldDefaultFailsTheParse(t *testing.T) {
	const src = `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
message M { repeated string tags = 1 [(pxf.default) = "ignored"]; }`

	desc := compileMessage(t, src, "def_test.v1.M")
	_, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFull([]byte(``), dynamicpb.NewMessage(desc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "def_test.v1.M.tags")
}

// TestApply_SkipsListsAndMaps covers the skip that survives in
// [defaults.Apply] for the callers the bind-time check does not reach:
// Apply is exported and takes any descriptor, and a resolve whose
// layers are all optional-and-absent never parses a layer, so nothing
// ever binds its schema. Without the skip those callers would get
// "default values not supported for repeated field" out of
// pxf.ApplyDefault instead of the documented no-op.
//
// The two tests above pin the placement's rejection upstream; this one
// pins what Apply itself still does, so deleting the guard is a test
// failure rather than a silent behaviour change.
func TestApply_SkipsListsAndMaps(t *testing.T) {
	const src = `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
message M {
  repeated string tags = 1 [(pxf.default) = "ignored"];
  map<string, string> labels = 2 [(pxf.default) = "ignored"];
}`

	desc := compileMessage(t, src, "def_test.v1.M")
	msg := dynamicpb.NewMessage(desc)
	present := map[string]struct{}{}

	require.NoError(t, defaults.Apply(msg, present),
		"a (pxf.default) on a list or map must be skipped, not surfaced as an error")

	r := msg.ProtoReflect()
	assert.Equal(t, 0, r.Get(desc.Fields().ByName("tags")).List().Len(),
		"repeated fields don't accept defaults")
	assert.Equal(t, 0, r.Get(desc.Fields().ByName("labels")).Map().Len(),
		"map fields don't accept defaults")
	assert.Empty(t, present, "a skipped default must not join the presence union")
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

// --- WKT fields (issue #129) ---

const pxfSecretProtoSrc = `
syntax = "proto3";
package pxf;
message Secret { string value = 1; string hint = 2; string fingerprint = 3; }
`

const pxfBignumProtoSrc = `
syntax = "proto3";
package pxf;
message BigInt { bytes abs = 1; bool negative = 2; }
message Decimal { bytes unscaled = 1; int32 scale = 2; bool negative = 3; }
message BigFloat { bytes mantissa = 1; int32 exponent = 2; uint32 prec = 3; bool negative = 4; }
`

const wktProtoSrc = `
syntax = "proto3";
package def_test.v1;

import "pxf/annotations.proto";
import "pxf/bignum.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/timestamp.proto";
import "google/protobuf/wrappers.proto";

message WKTConfig {
  google.protobuf.Duration shutdown_grace = 1 [(pxf.default) = "250ms"];
  google.protobuf.Timestamp epoch = 2 [(pxf.default) = "2026-05-09T12:00:00Z"];
  google.protobuf.StringValue label = 3 [(pxf.default) = "hello"];
  google.protobuf.Int32Value retries = 4 [(pxf.default) = "3"];
  google.protobuf.UInt64Value budget = 5 [(pxf.default) = "9000000000"];
  google.protobuf.BoolValue verbose = 6 [(pxf.default) = "true"];
  google.protobuf.DoubleValue ratio = 7 [(pxf.default) = "3.5"];
  pxf.BigInt big = 8 [(pxf.default) = "12345"];
  pxf.Decimal dec = 9 [(pxf.default) = "3.14"];
  pxf.BigFloat bf = 10 [(pxf.default) = "2.71828"];
  google.protobuf.Duration undecorated = 11;
  Timeouts timeouts = 12;
  Timeouts absent_timeouts = 13;
  google.protobuf.FloatValue gain = 14 [(pxf.default) = "1.25"];
  google.protobuf.Int64Value offset = 15 [(pxf.default) = "-9000000000"];
  google.protobuf.UInt32Value slots = 16 [(pxf.default) = "64"];
}

message Timeouts {
  google.protobuf.Duration read = 1 [(pxf.default) = "5s"];
  string name = 2 [(pxf.default) = "rpc"];
}
`

// compileMessage compiles src (as "u.proto") against the pxf
// annotation/Secret/bignum stubs and returns the named message.
func compileMessage(t *testing.T, src, fullName string) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"u.proto":               src,
		"pxf/annotations.proto": annotationsProtoSrc,
		"pxf/secret.proto":      pxfSecretProtoSrc,
		"pxf/bignum.proto":      pxfBignumProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "u.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName(protoreflect.FullName(fullName))
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

func compileWKT(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	return compileMessage(t, wktProtoSrc, "def_test.v1.WKTConfig")
}

// sub returns the WKT sub-message at field, requiring it to be set.
func sub(t *testing.T, msg proto.Message, field string) protoreflect.Message {
	t.Helper()
	r := msg.ProtoReflect()
	fd := r.Descriptor().Fields().ByName(protoreflect.Name(field))
	require.NotNil(t, fd, "field %q", field)
	require.True(t, r.Has(fd), "field %q must be set", field)
	return r.Get(fd).Message()
}

func inner(t *testing.T, m protoreflect.Message, name string) protoreflect.Value {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "inner field %q of %s", name, m.Descriptor().FullName())
	return m.Get(fd)
}

// TestApply_WKTDefaultsApplied is the issue #129 regression: a
// (pxf.default) on an atomic-WKT field must set the whole value
// instead of being dropped by the atomic check. The table covers
// every type pxf can parse a default literal for: Timestamp,
// Duration, BigInt, Decimal, BigFloat, and eight of the nine
// google.protobuf wrappers. The ninth, BytesValue, takes a base64
// literal and is covered by TestApply_BytesDefaultsDecode.
func TestApply_WKTDefaultsApplied(t *testing.T) {
	desc := compileWKT(t)
	msg, present := parseInput(t, desc, ``) // nothing set by any layer
	require.NoError(t, defaults.Apply(msg, present))

	cases := []struct {
		field string
		check func(t *testing.T, m protoreflect.Message)
	}{
		{"shutdown_grace", func(t *testing.T, m protoreflect.Message) {
			assert.Equal(t, int64(0), inner(t, m, "seconds").Int())
			assert.Equal(t, int64(250_000_000), inner(t, m, "nanos").Int())
		}},
		{"epoch", func(t *testing.T, m protoreflect.Message) {
			want := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC).Unix()
			assert.Equal(t, want, inner(t, m, "seconds").Int())
		}},
		{"label", func(t *testing.T, m protoreflect.Message) {
			assert.Equal(t, "hello", inner(t, m, "value").String())
		}},
		{"retries", func(t *testing.T, m protoreflect.Message) {
			assert.Equal(t, int64(3), inner(t, m, "value").Int())
		}},
		{"budget", func(t *testing.T, m protoreflect.Message) {
			assert.Equal(t, uint64(9_000_000_000), inner(t, m, "value").Uint())
		}},
		{"verbose", func(t *testing.T, m protoreflect.Message) {
			assert.True(t, inner(t, m, "value").Bool())
		}},
		{"ratio", func(t *testing.T, m protoreflect.Message) {
			assert.InDelta(t, 3.5, inner(t, m, "value").Float(), 0.0001)
		}},
		{"big", func(t *testing.T, m protoreflect.Message) {
			assert.NotEmpty(t, inner(t, m, "abs").Bytes())
			assert.False(t, inner(t, m, "negative").Bool())
		}},
		{"dec", func(t *testing.T, m protoreflect.Message) {
			assert.Equal(t, int64(2), inner(t, m, "scale").Int())
		}},
		{"bf", func(t *testing.T, m protoreflect.Message) {
			assert.Greater(t, inner(t, m, "prec").Uint(), uint64(0))
		}},
		{"gain", func(t *testing.T, m protoreflect.Message) {
			assert.InDelta(t, 1.25, inner(t, m, "value").Float(), 0.0001)
		}},
		{"offset", func(t *testing.T, m protoreflect.Message) {
			assert.Equal(t, int64(-9_000_000_000), inner(t, m, "value").Int())
		}},
		{"slots", func(t *testing.T, m protoreflect.Message) {
			assert.Equal(t, uint64(64), inner(t, m, "value").Uint())
		}},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			c.check(t, sub(t, msg, c.field))
		})
	}
}

func TestApply_WKTDefaultDoesNotOverwriteLayerValue(t *testing.T) {
	desc := compileWKT(t)
	msg, present := parseInput(t, desc, `shutdown_grace = 30s`)
	require.NoError(t, defaults.Apply(msg, present))

	g := sub(t, msg, "shutdown_grace")
	assert.Equal(t, int64(30), inner(t, g, "seconds").Int(),
		"layer-set duration must survive the defaults pass")
	assert.Equal(t, int64(0), inner(t, g, "nanos").Int())
}

func TestApply_WKTFieldWithoutDefaultStaysAbsent(t *testing.T) {
	desc := compileWKT(t)
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))

	r := msg.ProtoReflect()
	fd := r.Descriptor().Fields().ByName("undecorated")
	assert.False(t, r.Has(fd),
		"a WKT field with no (pxf.default) must not be materialized")
}

func TestApply_WKTDefaultMarksPresenceAndFiresCallback(t *testing.T) {
	desc := compileWKT(t)
	msg, present := parseInput(t, desc, ``)

	var applied []string
	require.NoError(t, defaults.Apply(msg, present,
		defaults.OnApplied(func(p string) { applied = append(applied, p) })))

	_, ok := present["shutdown_grace"]
	assert.True(t, ok, "default-filled WKT path must join the presence union for validate")
	assert.Contains(t, applied, "shutdown_grace",
		"OnApplied must report the WKT path so Trace marks it FromDefault")
	assert.NotContains(t, applied, "undecorated")
}

func TestApply_NestedWKTDefaultAppliedWhenParentLive(t *testing.T) {
	desc := compileWKT(t)
	msg, present := parseInput(t, desc, `
timeouts {
  name = "http"
}
`)
	require.NoError(t, defaults.Apply(msg, present))

	to := sub(t, msg, "timeouts")
	readFd := to.Descriptor().Fields().ByName("read")
	require.True(t, to.Has(readFd), "timeouts.read must be defaulted")
	assert.Equal(t, int64(5), inner(t, to.Get(readFd).Message(), "seconds").Int())
	assert.Equal(t, "http", inner(t, to, "name").String())

	_, ok := present["timeouts.read"]
	assert.True(t, ok)
}

func TestApply_NestedWKTDefaultSkippedWhenParentAbsent(t *testing.T) {
	desc := compileWKT(t)
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))

	r := msg.ProtoReflect()
	fd := r.Descriptor().Fields().ByName("absent_timeouts")
	assert.False(t, r.Has(fd),
		"an absent parent must not be materialized to hold a nested WKT default")
}

// TestApply_UnsupportedMessageDefaultErrors pins the other half of
// issue #129: a (pxf.default) the pass cannot honor is loud, not
// silently dropped. pxf has no literal form for these message types.
func TestApply_UnsupportedMessageDefaultErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "any",
			src: `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
import "google/protobuf/any.proto";
message M { google.protobuf.Any a = 1 [(pxf.default) = "whatever"]; }`,
			want: "google.protobuf.Any",
		},
		{
			name: "secret",
			src: `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
import "pxf/secret.proto";
message M { pxf.Secret s = 1 [(pxf.default) = "hunter2"]; }`,
			want: "pxf.Secret",
		},
		{
			name: "struct",
			src: `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
import "google/protobuf/struct.proto";
message M { google.protobuf.Struct s = 1 [(pxf.default) = "{}"]; }`,
			want: "google.protobuf.Struct",
		},
		{
			name: "user_defined_message",
			src: `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
message Inner { string x = 1; }
message M { Inner i = 1 [(pxf.default) = "nope"]; }`,
			want: "def_test.v1.Inner",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			desc := compileMessage(t, c.src, "def_test.v1.M")
			msg := dynamicpb.NewMessage(desc)
			err := defaults.Apply(msg, map[string]struct{}{})
			require.Error(t, err, "an unhonorable (pxf.default) must not be silently skipped")
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestApply_InvalidWKTDefaultLiteralErrors(t *testing.T) {
	const src = `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
import "google/protobuf/duration.proto";
message M { google.protobuf.Duration d = 1 [(pxf.default) = "not-a-duration"]; }`

	desc := compileMessage(t, src, "def_test.v1.M")
	msg := dynamicpb.NewMessage(desc)
	err := defaults.Apply(msg, map[string]struct{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defaults: d:", "error must name the field path")
	assert.Contains(t, err.Error(), "not-a-duration")
}

// TestApply_UnsupportedMessageDefaultSkippedWhenLayerSet documents the
// gating rule: the annotation is only ever consulted for a path no
// layer set, so a bogus default on a message field a layer did set
// stays out of the way (and the pass still descends into it).
//
// Parsed with SkipValidate rather than hand-built. Since protowire-go
// v1.4.0 a (pxf.default) on a message type PXF has no literal for does
// not bind (trendvidia/protowire-go#69), so this schema fails an
// ordinary parse and the gating can no longer be reached through a
// layer — which is what issue #134 asked for, answered upstream. The
// rule still governs defaults.Apply for a caller who hands it a
// descriptor that did not come through a validated parse, and that is
// the path pinned here; the error it gates is pinned by
// TestApply_UnsupportedMessageDefaultErrors.
//
// SkipValidate keeps the presence map coming from a real decode. The
// gate reads the *parent* path ("i"), not just "i.y", so hardcoding
// the set would stop asking upstream what a nested block marks present
// and would keep passing if that shape ever changed — the coupling is
// the point of the test.
func TestApply_UnsupportedMessageDefaultSkippedWhenLayerSet(t *testing.T) {
	const src = `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
message Inner { string x = 1 [(pxf.default) = "filled"]; string y = 2; }
message M { Inner i = 1 [(pxf.default) = "nope"]; }`

	desc := compileMessage(t, src, "def_test.v1.M")
	msg := dynamicpb.NewMessage(desc)
	res, err := pxf.UnmarshalOptions{SkipPostDecode: true, SkipValidate: true}.
		UnmarshalFull([]byte("i {\n  y = \"set\"\n}\n"), msg)
	require.NoError(t, err)
	present := make(map[string]struct{})
	for _, p := range res.PresentFields() {
		present[p] = struct{}{}
	}
	require.Contains(t, present, "i",
		"the gate reads the parent path; without it this test proves nothing")

	require.NoError(t, defaults.Apply(msg, present),
		"a bogus default on a path some layer set must not be consulted")
	assert.Equal(t, "filled", get(t, msg, "i", "x").String(),
		"and the pass still descends into the message that layer set")
}

// --- oneof members ---

const oneofProtoSrc = `
syntax = "proto3";
package def_test.v1;

import "pxf/annotations.proto";
import "google/protobuf/duration.proto";

message OneofConfig {
  oneof backend {
    string file = 1;
    google.protobuf.Duration timeout = 2 [(pxf.default) = "250ms"];
  }
  optional string nickname = 3 [(pxf.default) = "anon"];
}
`

// TestApply_OneofSiblingSetIsNotClobbered is the data-loss case: a
// layer picked one member of a oneof, and a (pxf.default) on a
// sibling must not silently swap the operator's choice. Setting any
// member clears the rest, so applying the default here would drop
// file="/tmp/x" on the floor and substitute a timeout nobody asked
// for. The presence gate alone cannot catch it — "timeout" really is
// absent from the union; it is "file" that is present.
func TestApply_OneofSiblingSetIsNotClobbered(t *testing.T) {
	desc := compileMessage(t, oneofProtoSrc, "def_test.v1.OneofConfig")
	msg, present := parseInput(t, desc, `file = "/tmp/x"`)
	require.NoError(t, defaults.Apply(msg, present))

	r := msg.ProtoReflect()
	fields := r.Descriptor().Fields()
	fileFd, timeoutFd := fields.ByName("file"), fields.ByName("timeout")

	assert.True(t, r.Has(fileFd), "the layer-selected oneof member must survive")
	assert.Equal(t, "/tmp/x", r.Get(fileFd).String())
	assert.False(t, r.Has(timeoutFd),
		"a default must not take over a oneof whose case a layer already set")
	_, marked := present["timeout"]
	assert.False(t, marked, "a skipped default must not join the presence union")
}

// TestApply_OneofDefaultAppliesWhenCaseUnset is the other half: with
// no member selected, the oneof default behaves like any other.
func TestApply_OneofDefaultAppliesWhenCaseUnset(t *testing.T) {
	desc := compileMessage(t, oneofProtoSrc, "def_test.v1.OneofConfig")
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))

	g := sub(t, msg, "timeout")
	assert.Equal(t, int64(250_000_000), inner(t, g, "nanos").Int())
	_, marked := present["timeout"]
	assert.True(t, marked)
}

// TestApply_NulledOneofMemberSuppressesSiblingDefault is the same
// data-loss shape reached through an explicit clear. `file = null`
// leaves "file" in the presence union but unset on the message, so a
// guard that asked msg.WhichOneof would see a free oneof and fill
// timeout — turning the operator's deliberate "no backend" into a
// backend nobody asked for.
//
// The rule is upstream's: [pxf.ApplyDefault]'s godoc makes it the
// SkipPostDecode caller's obligation, and postDecode reads the same
// presence set, "with a member bound to null counting as present"
// (trendvidia/protowire-go#72). TestApply_NulledOneofMatchesUpstream
// pins the two against each other.
func TestApply_NulledOneofMemberSuppressesSiblingDefault(t *testing.T) {
	desc := compileMessage(t, oneofProtoSrc, "def_test.v1.OneofConfig")
	msg, present := parseInput(t, desc, `file = null`)
	require.Contains(t, present, "file",
		"a nulled member must reach the presence union, or this test proves nothing")

	require.NoError(t, defaults.Apply(msg, present))

	r := msg.ProtoReflect()
	fields := r.Descriptor().Fields()
	assert.False(t, r.Has(fields.ByName("timeout")),
		"an explicitly nulled member must suppress its sibling's default")
	assert.False(t, r.Has(fields.ByName("file")), "and null must stay cleared")
	_, marked := present["timeout"]
	assert.False(t, marked, "a skipped default must not join the presence union")
}

// TestApply_NulledOneofMatchesUpstream is the contract half: chameleon
// runs the defaults pass itself (SkipPostDecode), so the only thing
// keeping it honest is agreeing with the pass it replaced. Same
// document, both paths, same answer.
func TestApply_NulledOneofMatchesUpstream(t *testing.T) {
	desc := compileMessage(t, oneofProtoSrc, "def_test.v1.OneofConfig")

	chameleonMsg, present := parseInput(t, desc, `file = null`)
	require.NoError(t, defaults.Apply(chameleonMsg, present))

	upstreamMsg := dynamicpb.NewMessage(desc)
	_, err := pxf.UnmarshalFull([]byte(`file = null`), upstreamMsg) // post-decode ON
	require.NoError(t, err)

	timeoutFd := desc.Fields().ByName("timeout")
	assert.Equal(t,
		upstreamMsg.ProtoReflect().Has(timeoutFd),
		chameleonMsg.ProtoReflect().Has(timeoutFd),
		"chameleon's defaults pass must agree with protowire-go's on a nulled oneof member")
}

// TestApply_SyntheticOneofStillDefaults guards the guard: proto3
// `optional` compiles to a synthetic one-field oneof. There is no
// sibling to protect and no choice to override, so those fields must
// stay defaultable.
func TestApply_SyntheticOneofStillDefaults(t *testing.T) {
	desc := compileMessage(t, oneofProtoSrc, "def_test.v1.OneofConfig")
	require.True(t, desc.Fields().ByName("nickname").ContainingOneof().IsSynthetic(),
		"proto3 optional must compile to a synthetic oneof for this test to mean anything")

	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))
	assert.Equal(t, "anon", get(t, msg, "nickname").String())
}

// --- pinned protowire-go divergences ---

// A base64 (pxf.default) decodes on both bytes shapes. This used to be
// two tests: BytesValue was the ninth wrapper and the one exception —
// protowire-go's wrapper path had no BytesKind branch, so the identical
// literal that decoded on a plain `bytes` scalar was rejected on the
// wrapper — and the plain-scalar case existed as the contrast that made
// it a wrapper limitation rather than a bytes one. protowire-go v1.4.0
// added the branch (trendvidia/protowire-go#67/#69), the contrast
// disappeared, and the pair collapses to this.
func TestApply_BytesDefaultsDecode(t *testing.T) {
	cases := []struct {
		name string
		src  string
		// field is the annotated field; wrapper says whether the decoded
		// bytes live one level down in the WKT's `value`. Reading it off
		// the field *name* would silently mis-dispatch a case added later.
		field   string
		wrapper bool
	}{
		{
			name: "bytes_value_wrapper",
			src: `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
import "google/protobuf/wrappers.proto";
message M { google.protobuf.BytesValue wrapped = 1 [(pxf.default) = "aGVsbG8="]; }`,
			field:   "wrapped",
			wrapper: true,
		},
		{
			name: "plain_bytes_scalar",
			src: `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
message M { bytes raw = 1 [(pxf.default) = "aGVsbG8="]; }`,
			field: "raw",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			desc := compileMessage(t, c.src, "def_test.v1.M")
			msg := dynamicpb.NewMessage(desc)
			require.NoError(t, defaults.Apply(msg, map[string]struct{}{}))

			got := get(t, msg, c.field)
			if c.wrapper {
				w := sub(t, msg, c.field)
				got = inner(t, w, "value")
			}
			assert.Equal(t, []byte("hello"), got.Bytes(), "base64 literal must decode")
		})
	}
}

// TestApply_NonCanonicalBoolDefaultCoercesToFalse pins a sharp edge
// this PR newly exposes to WKT wrappers: protowire-go parses bool
// defaults as `def == "true"`, so "True" silently becomes false
// instead of erroring the way every other literal kind does — and the
// path still joins the presence union, so a (pxf.required) on it
// validates as satisfied with the wrong value. Documented in the
// package godoc; pinned here so an upstream fix is noticed.
func TestApply_NonCanonicalBoolDefaultCoercesToFalse(t *testing.T) {
	const src = `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
import "google/protobuf/wrappers.proto";
message M {
  google.protobuf.BoolValue wrapped = 1 [(pxf.default) = "True"];
  bool plain = 2 [(pxf.default) = "yes"];
}`
	desc := compileMessage(t, src, "def_test.v1.M")
	msg := dynamicpb.NewMessage(desc)
	present := map[string]struct{}{}
	require.NoError(t, defaults.Apply(msg, present),
		"a non-canonical bool literal is coerced, not rejected")

	assert.False(t, inner(t, sub(t, msg, "wrapped"), "value").Bool(),
		`"True" != "true", so the wrapper lands on false`)
	assert.False(t, get(t, msg, "plain").Bool())
	_, marked := present["wrapped"]
	assert.True(t, marked, "the coerced value still counts as satisfying (pxf.required)")
}

// TestApply_WKTDefaultIsNotInterpolated pins the gap between "defaults
// run before interpolation so a default may be a template" and the
// atomic boundary interpolate draws. A `${...}` in a default on a
// google.protobuf.StringValue is never expanded, while the same
// literal on a plain string field is. Documented in the package
// godoc and README; pinned so closing the gap is a deliberate change.
func TestApply_WKTDefaultIsNotInterpolated(t *testing.T) {
	const src = `
syntax = "proto3";
package def_test.v1;
import "pxf/annotations.proto";
import "google/protobuf/wrappers.proto";
message M {
  string base = 1 [(pxf.default) = "example.com"];
  string api_plain = 2 [(pxf.default) = "api.${base}"];
  google.protobuf.StringValue api_wrapped = 3 [(pxf.default) = "api.${base}"];
}`
	desc := compileMessage(t, src, "def_test.v1.M")
	msg, present := parseInput(t, desc, ``)
	require.NoError(t, defaults.Apply(msg, present))
	require.NoError(t, interpolate.Apply(msg))

	assert.Equal(t, "api.example.com", get(t, msg, "api_plain").String(),
		"a scalar default is a template, as documented")
	assert.Equal(t, "api.${base}", inner(t, sub(t, msg, "api_wrapped"), "value").String(),
		"a WKT default is NOT interpolated: interpolate stops at the atomic boundary")
}

// TestApply_ExplicitNullIsNotReDefaulted pins the invariant that
// keeps a deliberately-cleared WKT cleared. `x = null` in a higher
// layer must not be quietly refilled by the schema default one pass
// later. This holds because pxf.Result records nulled paths in
// PresentFields, so the path is in the presence union and the default
// is gated out — an upstream coupling worth a chameleon-side test.
func TestApply_ExplicitNullIsNotReDefaulted(t *testing.T) {
	desc := compileWKT(t)
	msg, present := parseInput(t, desc, `shutdown_grace = null`)
	require.Contains(t, present, "shutdown_grace",
		"an explicit null must reach the presence union, or the default resurrects it")

	require.NoError(t, defaults.Apply(msg, present))

	r := msg.ProtoReflect()
	assert.False(t, r.Has(r.Descriptor().Fields().ByName("shutdown_grace")),
		"an explicitly nulled WKT must stay cleared, not fall back to (pxf.default)")
}
