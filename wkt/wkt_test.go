// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package wkt_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/wkt"
)

const protoSrc = `
syntax = "proto3";
package wkt_test.v1;

import "google/protobuf/timestamp.proto";
import "google/protobuf/duration.proto";
import "google/protobuf/wrappers.proto";
import "google/protobuf/any.proto";
import "pxf/secret.proto";
import "pxf/bignum.proto";

message Plain {
  string s = 1;
}

message HasWKTs {
  google.protobuf.Timestamp t = 1;
  google.protobuf.Duration d = 2;
  google.protobuf.StringValue sv = 3;
  google.protobuf.Int32Value iv = 4;
  google.protobuf.Any a = 5;
  pxf.Secret secret = 6;
  pxf.BigInt big = 7;
  pxf.Decimal dec = 8;
  pxf.BigFloat bf = 9;
  Plain plain = 10;
}
`

const pxfSecretSrc = `
syntax = "proto3";
package pxf;
message Secret { string value = 1; string hint = 2; string fingerprint = 3; }
`

const pxfBignumSrc = `
syntax = "proto3";
package pxf;
message BigInt { bytes abs = 1; bool negative = 2; }
message Decimal { bytes unscaled = 1; int32 scale = 2; bool negative = 3; }
message BigFloat { bytes mantissa = 1; int32 exponent = 2; uint32 prec = 3; bool negative = 4; }
`

func compile(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":       protoSrc,
		"pxf/secret.proto": pxfSecretSrc,
		"pxf/bignum.proto": pxfBignumSrc,
	}
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	res, err := c.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	for _, f := range res {
		if f.Path() == "test.proto" {
			return f
		}
	}
	t.Fatal("test.proto not found")
	return nil
}

func TestAtomic_RecognizesAllWKTs(t *testing.T) {
	f := compile(t)
	hasWKTs := f.Messages().ByName("HasWKTs")
	require.NotNil(t, hasWKTs)

	cases := []struct {
		field string
		want  bool
	}{
		{"t", true},      // google.protobuf.Timestamp
		{"d", true},      // google.protobuf.Duration
		{"sv", true},     // google.protobuf.StringValue
		{"iv", true},     // google.protobuf.Int32Value
		{"a", true},      // google.protobuf.Any
		{"secret", true}, // pxf.Secret
		{"big", true},    // pxf.BigInt
		{"dec", true},    // pxf.Decimal
		{"bf", true},     // pxf.BigFloat
		{"plain", false}, // user-defined message
	}
	for _, c := range cases {
		fd := hasWKTs.Fields().ByName(protoreflect.Name(c.field))
		require.NotNil(t, fd, "field %q", c.field)
		assert.Equal(t, c.want, wkt.Atomic(fd.Message()), "Atomic(%s)", c.field)
	}
}

func TestAtomic_NilSafe(t *testing.T) {
	assert.False(t, wkt.Atomic(nil))
}

// TemplatableText is the one place the four packages sharing Atomic
// disagree, so its width is pinned exhaustively rather than by the one
// type that should say yes. Everything here is Atomic; only
// StringValue carries user-authored text, and admitting any other —
// pxf.Secret above all — would put a value through interpolate.expand
// that the boundary exists to keep out. See #135.
func TestTemplatableText_IsExactlyStringValue(t *testing.T) {
	f := compile(t)
	hasWKTs := f.Messages().ByName("HasWKTs")
	require.NotNil(t, hasWKTs)

	cases := []struct {
		field string
		want  bool
	}{
		{"sv", true},      // google.protobuf.StringValue
		{"t", false},      // google.protobuf.Timestamp
		{"d", false},      // google.protobuf.Duration
		{"iv", false},     // google.protobuf.Int32Value
		{"a", false},      // google.protobuf.Any
		{"secret", false}, // pxf.Secret — the one that must never widen
		{"big", false},    // pxf.BigInt
		{"dec", false},    // pxf.Decimal
		{"bf", false},     // pxf.BigFloat
		{"plain", false},  // user-defined message
	}
	for _, c := range cases {
		fd := hasWKTs.Fields().ByName(protoreflect.Name(c.field))
		require.NotNil(t, fd, "field %q", c.field)
		assert.Equal(t, c.want, wkt.TemplatableText(fd.Message()),
			"TemplatableText(%s)", c.field)
	}
}

func TestTemplatableText_NilSafe(t *testing.T) {
	assert.False(t, wkt.TemplatableText(nil))
}

// A user-defined message named StringValue in someone else's package
// must not be admitted, for the same reason Atomic consults FullName.
func TestTemplatableText_FullNameDrivenOnly(t *testing.T) {
	const lookalikeSrc = `
syntax = "proto3";
package not_google.v1;
message StringValue { string value = 1; }
`
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(
				map[string]string{"l.proto": lookalikeSrc},
			)},
		),
	}
	res, err := c.Compile(context.Background(), "l.proto")
	require.NoError(t, err)
	fake := res[0].Messages().ByName("StringValue")
	require.NotNil(t, fake)
	assert.False(t, wkt.TemplatableText(fake),
		"a user-defined StringValue must not be treated as the WKT")
}

func TestAtomic_FullNameDrivenOnly(t *testing.T) {
	// User-defined message that's NOT a WKT but happens to share the
	// shape of one (three fields named value/hint/fingerprint). The
	// predicate must say no — it consults FullName, not shape.
	const lookalikeSrc = `
syntax = "proto3";
package not_pxf.v1;
message Secret { string value = 1; string hint = 2; string fingerprint = 3; }
`
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(
				map[string]string{"l.proto": lookalikeSrc},
			)},
		),
	}
	res, err := c.Compile(context.Background(), "l.proto")
	require.NoError(t, err)
	fakeSecret := res[0].Messages().ByName("Secret")
	require.NotNil(t, fakeSecret)
	assert.False(t, wkt.Atomic(fakeSecret),
		"a user-defined message named Secret must not be treated as the WKT")
}
