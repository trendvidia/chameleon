// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chameleon_test

import (
	"context"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const chainSpecProtoSrc = `
syntax = "proto3";
package chameleon.v1;

message ChainSpec {
  string version = 1;
  string default_environment = 2;
  repeated Environment environments = 3;
  repeated LayerEntry layers = 4;
  repeated ContextKey context_keys = 5;
  map<string, SourceRef> sources = 6;
}
message Environment { string name = 1; string extends = 2; map<string, string> labels = 3; }
message LayerEntry {
  string id = 1;
  string path = 2;
  ForEach for_each = 3;
  bool required = 4;
  bool encrypted = 5;
  string source = 6;
  enum ForEach { ONCE = 0; PER_ENV = 1; }
}
message SourceRef { string kind = 1; map<string, string> options = 2; }
message ContextKey { string name = 1; string description = 2; repeated string allowed_values = 3; string default = 4; bool required = 5; }
`

const appProtoSrc = `
syntax = "proto3";
package app.v1;
message AppConfig {
  string base_url = 1;
  string api_url = 2;
}
`

func compileAll(t *testing.T) (chainDesc, appDesc protoreflect.MessageDescriptor) {
	t.Helper()
	sources := map[string]string{
		"chain.proto": chainSpecProtoSrc,
		"app.proto":   appProtoSrc,
	}
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := c.Compile(context.Background(), "chain.proto", "app.proto")
	require.NoError(t, err)
	cd, err := files.AsResolver().FindDescriptorByName("chameleon.v1.ChainSpec")
	require.NoError(t, err)
	ad, err := files.AsResolver().FindDescriptorByName("app.v1.AppConfig")
	require.NoError(t, err)
	return cd.(protoreflect.MessageDescriptor), ad.(protoreflect.MessageDescriptor)
}

func TestLoad_SimpleCase(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := dynamicpb.NewMessage(chainDesc)
	require.NoError(t, pxf.Unmarshal([]byte(`
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`), spec))

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "mybigproject.com"
api_url = "api.${base_url}"
`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
base_url = "dev.mybigproject.com"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	res, err := chameleon.Load[*dynamicpb.Message](sample, spec, fs, "dev")
	require.NoError(t, err)
	defer res.Destroy()

	get := func(name string) string {
		fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
		return res.Config.Get(fd).String()
	}
	assert.Equal(t, "dev.mybigproject.com", get("base_url"))
	assert.Equal(t, "api.dev.mybigproject.com", get("api_url"))
}

// keep proto import live (used by upstream resolver but unreferenced
// directly in this test).
var _ = proto.Marshal

// TestLoad_NoGoroutineLeak runs Load+Destroy in a tight loop and
// checks the goroutine count doesn't grow. Memguard's own goroutines
// (the interrupt handler, the secret-scrambler) are spawned once at
// init and counted in the baseline; this test only catches NEW
// leaks introduced by the resolver's own paths.
func TestLoad_NoGoroutineLeak(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := dynamicpb.NewMessage(chainDesc)
	require.NoError(t, pxf.Unmarshal([]byte(`
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`), spec))

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
api_url = "api.${base_url}"
`)},
	}

	// Warm-up: one resolve to settle any first-call init.
	res, err := chameleon.Load[*dynamicpb.Message](
		dynamicpb.NewMessage(appDesc), spec, fs, "prod")
	require.NoError(t, err)
	res.Destroy()

	runtime.GC()
	baseline := runtime.NumGoroutine()

	const iterations = 500
	for range iterations {
		res, err := chameleon.Load[*dynamicpb.Message](
			dynamicpb.NewMessage(appDesc), spec, fs, "prod")
		require.NoError(t, err)
		res.Destroy()
	}
	runtime.GC()

	// Allow a few-goroutine drift from runtime housekeeping. A real
	// leak would scale with iterations.
	final := runtime.NumGoroutine()
	assert.LessOrEqual(t, final-baseline, 5,
		"goroutine count grew by %d after %d Resolve+Destroy iterations (baseline=%d, final=%d) — likely a leak",
		final-baseline, iterations, baseline, final)
}
