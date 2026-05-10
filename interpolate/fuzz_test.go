// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package interpolate_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/interpolate"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// FuzzApply pushes arbitrary bytes through interpolate.Apply via a
// fuzzed string-scalar field. Property: Apply must never panic. It
// can return an error (cycle, malformed, unknown ref) but the
// process stays standing.
//
// Run with:
//   go test ./interpolate/ -fuzz=FuzzApply -fuzztime=30s
//
// The seed corpus hits the categories the existing tests already
// cover plus a few cute pathologies (nested ${}, lone $, lone {,
// unterminated, multi-byte UTF-8 inside placeholders).
func FuzzApply(f *testing.F) {
	desc := compileFuzzApp(f)

	seeds := []string{
		"plain text",
		"",
		"${base_url}",
		"prefix-${api_url}-suffix",
		"${a}.${b}.${c}",
		"${api_url}",                  // depends on the seed for api_url
		"${nonexistent}",              // unknown ref
		"${}",                         // empty path
		"unterminated ${",             // malformed
		"$",                           // lone dollar
		"$$",                          // double dollar
		"${.}",                        // empty segments
		"${a.b.c.d.e.f.g}",            // deep path
		"${api_url with spaces}",      // unusual identifier
		"${api​url}",             // zero-width joiner
		"\xff\xfe\xfd",                // invalid UTF-8 bytes
		"${" + string([]byte{0}) + "}",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		// Fresh message per iteration so iterations don't interfere.
		msg := dynamicpb.NewMessage(desc)
		fd := desc.Fields().ByName("api_url")
		msg.Set(fd, protoreflect.ValueOfString(in))

		// Property: Apply must never panic. Errors are fine.
		_ = interpolate.Apply(msg)
	})
}

const fuzzAppProtoSrc = `
syntax = "proto3";
package fuzz_app.v1;
message FuzzApp {
  string base_url = 1;
  string api_url = 2;
  string admin_url = 3;
}
`

func compileFuzzApp(f *testing.F) protoreflect.MessageDescriptor {
	f.Helper()
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(
				map[string]string{"f.proto": fuzzAppProtoSrc},
			)},
		),
	}
	files, err := c.Compile(context.Background(), "f.proto")
	if err != nil {
		f.Fatal(err)
	}
	d, err := files.AsResolver().FindDescriptorByName("fuzz_app.v1.FuzzApp")
	if err != nil {
		f.Fatal(err)
	}
	return d.(protoreflect.MessageDescriptor)
}

// keep pxf import live for parity with sibling tests; not used in
// the fuzz body but referenced when extending the seed corpus.
var _ = pxf.Unmarshal
