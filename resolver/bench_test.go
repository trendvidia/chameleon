// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package resolver_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
)

// Benchmarks for the per-Resolve hot path. These cover the
// canonical resolver invocations the v1.0 readiness review identified
// as worth tracking for regressions: a 2-layer baseline, a 5-layer
// fan-out (to catch O(N) decode cost), and a Secret-bearing path
// (which exercises the OnSecretField hook + memguard Enclave seal).
//
// Run locally:
//
//	go test -bench=. -benchmem -run=^$ ./resolver/...
//
// In CI the bench job runs `-benchtime=1s -count=3` per PR and
// captures the result for benchstat comparison vs main.

func BenchmarkResolve_TwoLayers(b *testing.B) {
	chainDesc, appDesc := compileAll(b)

	spec := parseSpec(b, chainDesc, `
version = "1.0.0"
default_environment = "dev"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`)

	fsys := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "mybigproject.com"
api_url = "api.mybigproject.com"
feature_flags = ["fast_path"]
`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
base_url = "dev.mybigproject.com"
feature_flags = ["fast_path", "new_dashboard"]
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](
		sample, spec,
		resolver.WithSource("default", source.FS(fsys)),
	)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	req := resolver.Request{Environment: "dev"}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		res, err := r.Resolve(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		res.Destroy()
	}
}

func BenchmarkResolve_FiveLayers(b *testing.B) {
	chainDesc, appDesc := compileAll(b)

	var layers strings.Builder
	layers.WriteString(`{ id = "base" path = "base.pxf" for_each = ONCE required = true }` + "\n")
	for i := range 4 {
		fmt.Fprintf(&layers, `  { id = "overlay%d" path = "overlay%d.pxf" for_each = ONCE }`+"\n", i, i)
	}

	spec := parseSpec(b, chainDesc, `
version = "1.0.0"
default_environment = "dev"
environments = [{ name = "dev" }]
layers = [
  `+layers.String()+`]
`)

	fsys := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "base.example.com"
api_url = "api.base.example.com"
feature_flags = ["one"]
`)},
		"overlay0.pxf": &fstest.MapFile{Data: []byte(`base_url = "o0.example.com"`)},
		"overlay1.pxf": &fstest.MapFile{Data: []byte(`api_url = "api.o1.example.com"`)},
		"overlay2.pxf": &fstest.MapFile{Data: []byte(`feature_flags = ["one", "two"]`)},
		"overlay3.pxf": &fstest.MapFile{Data: []byte(`base_url = "final.example.com"`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](
		sample, spec,
		resolver.WithSource("default", source.FS(fsys)),
	)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	req := resolver.Request{Environment: "dev"}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		res, err := r.Resolve(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		res.Destroy()
	}
}

func BenchmarkResolve_WithSecret(b *testing.B) {
	chainDesc, appDesc := compileAll(b)

	spec := parseSpec(b, chainDesc, `
version = "1.0.0"
default_environment = "dev"
environments = [{ name = "dev" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`)

	fsys := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "mybigproject.com"
db_password = "base-pw"
`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
db_password { value = "dev-pw" hint = "Postgres primary" }
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](
		sample, spec,
		resolver.WithSource("default", source.FS(fsys)),
	)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	req := resolver.Request{Environment: "dev"}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		res, err := r.Resolve(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		res.Destroy()
	}
}
