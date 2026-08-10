// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package interpolate_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/interpolate"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// This file covers #135: interpolate descends into
// google.protobuf.StringValue, and into nothing else it did not
// already. Because that moves a boundary across a dispatch, every
// presence-like shape gets its own case rather than one happy-path
// assertion — a default on a StringValue can land in any of them, and
// the walker reaches each by a different route (field, oneof, list
// element, map value, nested message).

const wrapperAppSrc = `
syntax = "proto3";
package wrap_test.v1;

import "pxf/secret.proto";
import "google/protobuf/wrappers.proto";
import "google/protobuf/duration.proto";

message WrapConfig {
  string base = 1;

  google.protobuf.StringValue plain    = 2;
  optional google.protobuf.StringValue opt = 3;

  oneof choice {
    google.protobuf.StringValue one_of_a = 4;
    google.protobuf.StringValue one_of_b = 5;
  }

  repeated google.protobuf.StringValue many = 6;
  map<string, google.protobuf.StringValue> tagged = 7;

  Nested nested = 8;

  pxf.Secret secret = 9;
  google.protobuf.Duration dur = 10;
  google.protobuf.Int32Value num = 11;
}

message Nested {
  google.protobuf.StringValue deep = 1;
}
`

func compileWrapApp(tb testing.TB) protoreflect.MessageDescriptor {
	tb.Helper()
	sources := map[string]string{
		"app.proto":        wrapperAppSrc,
		"pxf/secret.proto": secretProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "app.proto")
	require.NoError(tb, err)
	d, err := files.AsResolver().FindDescriptorByName("wrap_test.v1.WrapConfig")
	require.NoError(tb, err)
	return d.(protoreflect.MessageDescriptor)
}

func parseWrap(tb testing.TB, input string) proto.Message {
	tb.Helper()
	msg := dynamicpb.NewMessage(compileWrapApp(tb))
	require.NoError(tb, pxf.Unmarshal([]byte(input), msg))
	return msg
}

// wrapValue reads the .value of a StringValue reached by field name.
func wrapValue(t *testing.T, msg proto.Message, field string) string {
	t.Helper()
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(field))
	require.NotNil(t, fd, "no field %q", field)
	inner := m.Get(fd).Message()
	vfd := inner.Descriptor().Fields().ByName("value")
	require.NotNil(t, vfd)
	return inner.Get(vfd).String()
}

func TestWrapper_PlainFieldIsInterpolated(t *testing.T) {
	msg := parseWrap(t, `
base  = "example.com"
plain = "api.${base}"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "api.example.com", wrapValue(t, msg, "plain"))
}

// Row: synthetic oneof (proto3 optional).
func TestWrapper_Proto3OptionalIsInterpolated(t *testing.T) {
	msg := parseWrap(t, `
base = "example.com"
opt  = "api.${base}"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "api.example.com", wrapValue(t, msg, "opt"))
}

// Row: real oneof. The member the layer chose is interpolated, and the
// walk must not resurrect its sibling — Has() is false for it, so a
// bug here shows up as one_of_a becoming present.
func TestWrapper_OneofMemberIsInterpolated(t *testing.T) {
	msg := parseWrap(t, `
base     = "example.com"
one_of_b = "api.${base}"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "api.example.com", wrapValue(t, msg, "one_of_b"))

	m := msg.ProtoReflect()
	assert.False(t, m.Has(m.Descriptor().Fields().ByName("one_of_a")),
		"interpolating one member must not materialize its sibling")
}

// Row: repeated. listField reaches message elements through w.message,
// so the carve-out applies per element.
func TestWrapper_RepeatedElementsAreInterpolated(t *testing.T) {
	msg := parseWrap(t, `
base = "example.com"
many = ["a.${base}", "b.${base}"]
`)
	require.NoError(t, interpolate.Apply(msg))

	m := msg.ProtoReflect()
	list := m.Get(m.Descriptor().Fields().ByName("many")).List()
	require.Equal(t, 2, list.Len())
	vfd := list.Get(0).Message().Descriptor().Fields().ByName("value")
	assert.Equal(t, "a.example.com", list.Get(0).Message().Get(vfd).String())
	assert.Equal(t, "b.example.com", list.Get(1).Message().Get(vfd).String())
}

// Row: map values.
func TestWrapper_MapValuesAreInterpolated(t *testing.T) {
	msg := parseWrap(t, `
base = "example.com"
tagged = { "k" : "api.${base}" }
`)
	require.NoError(t, interpolate.Apply(msg))

	m := msg.ProtoReflect()
	mp := m.Get(m.Descriptor().Fields().ByName("tagged")).Map()
	v := mp.Get(protoreflect.ValueOfString("k").MapKey()).Message()
	vfd := v.Descriptor().Fields().ByName("value")
	assert.Equal(t, "api.example.com", v.Get(vfd).String())
}

// Row: nested liveness. A StringValue under a message no layer set
// must stay untouched — the parent is absent, so the walker never
// descends and must not create it.
func TestWrapper_AbsentNestedParentIsNotMaterialized(t *testing.T) {
	msg := parseWrap(t, `base = "example.com"`)
	require.NoError(t, interpolate.Apply(msg))

	m := msg.ProtoReflect()
	assert.False(t, m.Has(m.Descriptor().Fields().ByName("nested")),
		"walking must not materialize an absent nested parent")
}

func TestWrapper_LiveNestedParentIsInterpolated(t *testing.T) {
	msg := parseWrap(t, `
base = "example.com"
nested { deep = "api.${base}" }
`)
	require.NoError(t, interpolate.Apply(msg))

	m := msg.ProtoReflect()
	nested := m.Get(m.Descriptor().Fields().ByName("nested")).Message()
	deep := nested.Get(nested.Descriptor().Fields().ByName("deep")).Message()
	vfd := deep.Descriptor().Fields().ByName("value")
	assert.Equal(t, "api.example.com", deep.Get(vfd).String())
}

// Row: explicit null. PXF's explicit-null clears presence, so there is
// no payload to template and nothing may be invented.
func TestWrapper_ExplicitNullStaysAbsent(t *testing.T) {
	msg := parseWrap(t, `
base  = "example.com"
plain = null
`)
	require.NoError(t, interpolate.Apply(msg))

	m := msg.ProtoReflect()
	assert.False(t, m.Has(m.Descriptor().Fields().ByName("plain")),
		"an explicitly-nulled wrapper must not be materialized by the walk")
}

// The security-critical half of "exactly one type wide": a pxf.Secret
// sits beside a StringValue in the same message, carries the same
// template syntax, and must come out untouched. The exhaustive
// per-type check lives in the wkt package, on the predicate itself.
func TestWrapper_SecretIsStillSkipped(t *testing.T) {
	msg := parseWrap(t, `
base   = "example.com"
plain  = "api.${base}"
secret = "shh.${base}"
`)
	require.NoError(t, interpolate.Apply(msg))

	m := msg.ProtoReflect()
	sec := m.Get(m.Descriptor().Fields().ByName("secret")).Message()
	vfd := sec.Descriptor().Fields().ByName("value")
	assert.Equal(t, "shh.${base}", sec.Get(vfd).String(),
		"a pxf.Secret's plaintext must never be run through expand")
	assert.Equal(t, "api.example.com", wrapValue(t, msg, "plain"),
		"...while its StringValue neighbour in the same message is expanded")
}

// The read side is unchanged: a StringValue may be WRITTEN into and
// still not be READ from. Writing into a WKT leaks nothing; reading
// out of one is what the lookup boundary exists to prevent, and it
// stays shut for every atomic WKT including this one.
func TestWrapper_StringValueIsStillNotReferenceable(t *testing.T) {
	msg := parseWrap(t, `
base  = "example.com"
plain = "x"
opt   = "${plain.value}"
`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.ErrorIs(t, err, interpolate.ErrInvalidReference,
		"descending into a StringValue from a template must stay rejected")
}

// The path reported to OnApplied is the WKT field's own path, not an
// inner ".value". resolver.Trace keys entries by path and every other
// pass — merge, defaults, presence — keys a WKT at the field. A
// ".value" here would tag a phantom entry and leave the real one bare,
// so `chameleon explain` would stop showing the interpolated marker on
// exactly the fields this change enables.
func TestWrapper_OnAppliedReportsTheWKTFieldPath(t *testing.T) {
	msg := parseWrap(t, `
base   = "example.com"
plain  = "api.${base}"
nested { deep = "d.${base}" }
many   = ["m.${base}"]
`)
	var applied []string
	require.NoError(t, interpolate.Apply(msg, interpolate.OnApplied(func(p string) {
		applied = append(applied, p)
	})))

	assert.ElementsMatch(t, []string{"plain", "nested.deep", "many[0]"}, applied)
}
