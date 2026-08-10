// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain_test

import (
	"testing"

	"github.com/trendvidia/chameleon/chain"
)

// FuzzExpand pushes arbitrary env names + a single-key context map
// through chain.Expand against a fixed spec. Property: Expand must
// never panic — invalid inputs return errors, not crashes.
//
// Run with:
//
//	go test ./chain/ -fuzz=FuzzExpand -fuzztime=30s
//
// The fixed spec exercises the path templating with both ${env} and
// ${ctx.region} references, so the fuzzed inputs flow through both
// substitution paths.
func FuzzExpand(f *testing.F) {
	desc := compileChainSpec(f)
	spec := parseSpec(f, desc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
context_keys = [
  { name = "region" }
]
layers = [
  { id = "base"   path = "base.pxf"                for_each = ONCE }
  { id = "env"    path = "env/${env}.pxf"          for_each = PER_ENV }
  { id = "region" path = "region/${ctx.region}.pxf" for_each = ONCE }
]
`)

	seeds := []struct{ env, region string }{
		{"prod", ""},
		{"dev", "us"},
		{"dev", "eu"},
		{"", ""},
		{"unknown", "mars"},
		{"../etc/passwd", "../"},
		{string([]byte{0}), string([]byte{0xff})},
		{"\xff\xfe", ""},
	}
	for _, s := range seeds {
		f.Add(s.env, s.region)
	}

	f.Fuzz(func(t *testing.T, env, region string) {
		// Property: Expand must never panic. Errors expected for
		// invalid env names, unknown context, etc.
		ctx := map[string]string{}
		if region != "" {
			ctx["region"] = region
		}
		_, _ = chain.Options{AllowUnknownContextKeys: true}.Expand(spec, env, ctx)
	})
}
