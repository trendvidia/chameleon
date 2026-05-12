// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package pathfmt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/internal/pathfmt"
)

// TestMapKey covers every kind protobuf permits for map keys plus the
// notable string-escape paths. The chameleon parse.Move walker, the
// interpolate OnApplied callback, and the protoc-gen-chameleon-go
// plugin all rely on this formatter producing the same bytes —
// drift between any two breaks secret.Map.Get lookups silently in
// consumer code. Lock the format in here.
func TestMapKey(t *testing.T) {
	cases := []struct {
		name string
		key  protoreflect.MapKey
		want string
	}{
		// String keys are double-quoted (and Go-escaped) so paths
		// like `tenant_keys["acme"]` round-trip cleanly.
		{
			name: "string_simple",
			key:  protoreflect.ValueOfString("acme").MapKey(),
			want: `"acme"`,
		},
		{
			name: "string_with_quote_inside",
			key:  protoreflect.ValueOfString(`acme"co`).MapKey(),
			want: `"acme\"co"`,
		},
		{
			name: "string_empty",
			key:  protoreflect.ValueOfString("").MapKey(),
			want: `""`,
		},
		{
			name: "string_with_backslash",
			key:  protoreflect.ValueOfString(`a\b`).MapKey(),
			want: `"a\\b"`,
		},

		// Signed integer kinds — bare decimal, including negative
		// values (so sint32/sint64 vs int32/int64 don't matter at
		// the formatter level — protoreflect canonicalizes both to
		// int32/int64 at the MapKey interface).
		{
			name: "int32_positive",
			key:  protoreflect.ValueOfInt32(42).MapKey(),
			want: "42",
		},
		{
			name: "int32_negative",
			key:  protoreflect.ValueOfInt32(-1).MapKey(),
			want: "-1",
		},
		{
			name: "int32_zero",
			key:  protoreflect.ValueOfInt32(0).MapKey(),
			want: "0",
		},
		{
			name: "int64_positive",
			key:  protoreflect.ValueOfInt64(9_000_000_000).MapKey(),
			want: "9000000000",
		},
		{
			name: "int64_negative",
			key:  protoreflect.ValueOfInt64(-9_000_000_000).MapKey(),
			want: "-9000000000",
		},

		// Unsigned integer kinds.
		{
			name: "uint32",
			key:  protoreflect.ValueOfUint32(7).MapKey(),
			want: "7",
		},
		{
			name: "uint32_max",
			key:  protoreflect.ValueOfUint32(0xFFFFFFFF).MapKey(),
			want: "4294967295",
		},
		{
			name: "uint64_high_bit",
			key:  protoreflect.ValueOfUint64(1 << 63).MapKey(),
			want: "9223372036854775808",
		},

		// Bool keys.
		{
			name: "bool_true",
			key:  protoreflect.ValueOfBool(true).MapKey(),
			want: "true",
		},
		{
			name: "bool_false",
			key:  protoreflect.ValueOfBool(false).MapKey(),
			want: "false",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pathfmt.MapKey(c.key)
			assert.Equal(t, c.want, got,
				"MapKey(%v) — three callers must agree byte-for-byte",
				c.key.Interface())
		})
	}
}
