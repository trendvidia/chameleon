// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chameleon_test

import (
	"fmt"
	"testing/fstest"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/trendvidia/chameleon"
	"github.com/trendvidia/chameleon/chain"
)

// ExampleLoad illustrates the shortest path from "chain spec + layer
// directory" to a resolved typed config. The Load convenience
// function is appropriate when the layer source is a single
// fs.FS-rooted directory, layers aren't encrypted, no
// request-context values are needed, and the parsed ChainSpec is
// already in hand.
//
// For encrypted layers, multiple sources, request context, or
// trace, reach for resolver.New directly — Load is a thin wrapper
// over the common configuration of those primitives.
//
// The example uses google.protobuf.Empty as the typed config so
// the example stays self-contained without a protoc-gen-go step;
// in production T is your AppConfig type.
func ExampleLoad() {
	spec, err := chain.ParseSpec([]byte(`version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`))
	if err != nil {
		fmt.Println("parse:", err)
		return
	}

	// Layer source — in production an os.DirFS, embed.FS, or any
	// fs.FS-conforming type. fstest.MapFS keeps the example
	// self-contained.
	layersFS := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte("# Empty layer (Empty has no fields)\n")},
	}

	res, err := chameleon.Load[*emptypb.Empty](&emptypb.Empty{}, spec, layersFS, "prod")
	if err != nil {
		fmt.Println("load:", err)
		return
	}
	defer res.Destroy()

	fmt.Printf("resolved: %d secret(s)\n", res.Secrets.Len())
	// Output: resolved: 0 secret(s)
}
