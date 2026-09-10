// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestFindVisibleDescriptor pins the visibility rule findVisibleDescriptor
// re-implements from upstream's linker.ResolverFromFile
// (trendvidia/protocompile#222): the root file, every file it imports,
// and — below that — only what those files import publicly.
//
//	root.proto ─imports─▶ a.proto ─public import─▶ b.proto
//	                              └─imports────────▶ c.proto
//
// A name in root, a or b resolves from root; a name in c does not, because
// a's import of c is not re-exported. That last row is what separates this
// from "search the whole closure", and it is how protoc scopes names too.
func TestFindVisibleDescriptor(t *testing.T) {
	resolver := protocompile.WithStandardImports(&protocompile.SourceResolver{
		Accessor: protocompile.SourceAccessorFromMap(map[string]string{
			"root.proto": `syntax = "proto3"; package v; import "a.proto"; message Root { A a = 1; }`,
			"a.proto":    `syntax = "proto3"; package v; import public "b.proto"; import "c.proto"; message A { B b = 1; C c = 2; }`,
			"b.proto":    `syntax = "proto3"; package v; message B {}`,
			"c.proto":    `syntax = "proto3"; package v; message C {}`,
		}),
	})
	files, err := (&protocompile.Compiler{Resolver: resolver}).Compile(context.Background(), "root.proto")
	require.NoError(t, err)
	root := files[0]

	for _, name := range []string{"v.Root", "v.A", "v.B"} {
		d, err := findVisibleDescriptor(root, protoreflect.FullName(name))
		require.NoError(t, err, name)
		assert.Equal(t, name, string(d.FullName()))
	}

	_, err = findVisibleDescriptor(root, protoreflect.FullName("v.C"))
	require.Error(t, err, "v.C is reachable only through a's non-public import and must not resolve from root")
	assert.True(t, errors.Is(err, protoregistry.NotFound), "not-found is protoregistry.NotFound, as upstream's resolver returned: %v", err)

	_, err = findVisibleDescriptor(root, protoreflect.FullName("v.Nope"))
	assert.True(t, errors.Is(err, protoregistry.NotFound))
}
