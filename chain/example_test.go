// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain_test

import (
	"fmt"

	"github.com/trendvidia/chameleon/chain"
)

// ExampleParseSpec illustrates loading a chain spec from inline PXF
// bytes and expanding it for a specific environment. ParseSpec
// (and its file-based sibling LoadSpec) returns a proto.Message
// view of the spec; Expand resolves the layer order for a given
// (env, ctx) and produces the slice of LayerRef the resolver
// iterates.
//
// The chain.proto schema is compiled lazily inside chain — no
// codegen step required on the consumer side.
func ExampleParseSpec() {
	spec, err := chain.ParseSpec([]byte(`version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base"  path = "base.pxf"             for_each = ONCE  required = true }
  { id = "env"   path = "env/${env}.pxf"       for_each = PER_ENV }
]
`))
	if err != nil {
		fmt.Println("parse:", err)
		return
	}

	// Resolve the chain for env=dev. PER_ENV layers walk the env's
	// ancestry (dev extends prod), so the env layer yields TWO
	// refs: env/prod.pxf and env/dev.pxf.
	refs, err := chain.Expand(spec, "dev", nil /*ctx*/)
	if err != nil {
		fmt.Println("expand:", err)
		return
	}
	for _, r := range refs {
		fmt.Printf("%s %s required=%v\n", r.EntryID, r.Path, r.Required)
	}
	// Output:
	// base base.pxf required=true
	// env env/prod.pxf required=false
	// env env/dev.pxf required=false
}
