// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package interpolate_test

import (
	"testing"

	"github.com/trendvidia/chameleon/interpolate"
)

// Benchmarks for the template expansion hot path. Cover three
// scenarios that the v1.0 readiness review flagged as worth tracking:
// no-op pass (config without templates), shallow substitution
// (typical user config), and depth-bounded chain (defense-against-
// pathological-input regression watch — the v0.10.0 MaxResolveDepth
// fix introduced a stack walk every reference).

func BenchmarkApply_NoTemplates(b *testing.B) {
	desc := compileApp(b)
	src := `
base_url = "mybigproject.com"
api_url  = "api.mybigproject.com"
admin_url = "admin.mybigproject.com"
`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		msg := parse(b, desc, src)
		if err := interpolate.Apply(msg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApply_ShallowSubstitution(b *testing.B) {
	desc := compileApp(b)
	src := `
base_url = "mybigproject.com"
api_url  = "api.${base_url}"
admin_url = "admin.${base_url}"
`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		msg := parse(b, desc, src)
		if err := interpolate.Apply(msg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkApply_ThreeFieldChain — chained scalar references through
// the only three string fields the schema offers. Measures the
// per-reference resolve cost when the walker has to follow >1 hop.
// (interpolate.MaxResolveDepth — the v0.10.0 defense — only kicks in
// at 64+ frames; this bench is the steady-state cost, not the limit.)
func BenchmarkApply_ThreeFieldChain(b *testing.B) {
	desc := compileApp(b)
	src := `
admin_url = "terminus"
api_url = "${admin_url}.api"
base_url = "${api_url}.base"
`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		msg := parse(b, desc, src)
		if err := interpolate.Apply(msg); err != nil {
			b.Fatal(err)
		}
	}
}
