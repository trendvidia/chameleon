// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package resolver_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// chainSpecProtoSrc is the same stripped-down ChainSpec schema used in
// the chain package tests — kept inline here so this test file is
// self-contained.
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

message Environment {
  string name = 1;
  string extends = 2;
  map<string, string> labels = 3;
}

message LayerEntry {
  string id = 1;
  string path = 2;
  ForEach for_each = 3;
  bool required = 4;
  bool encrypted = 5;
  string source = 6;

  enum ForEach {
    ONCE = 0;
    PER_ENV = 1;
  }
}

message SourceRef {
  string kind = 1;
  map<string, string> options = 2;
}

message ContextKey {
  string name = 1;
  string description = 2;
  repeated string allowed_values = 3;
  string default = 4;
  bool required = 5;
}
`

// pxfSecretProtoSrc mirrors the canonical pxf.Secret descriptor so the
// test schema can use it without an external import.
const pxfSecretProtoSrc = `
syntax = "proto3";
package pxf;
message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`

// appProtoSrc is the consumer's config schema. base_url is the field
// the user's spec sketched as the canonical "tweaked per env" field.
const appProtoSrc = `
syntax = "proto3";
package app.v1;

import "pxf/secret.proto";

message AppConfig {
  string base_url = 1;
  string api_url = 2;
  pxf.Secret db_password = 3;
  repeated string feature_flags = 4;
}
`

func compileAll(t *testing.T) (chainDesc, appDesc protoreflect.MessageDescriptor) {
	t.Helper()
	sources := map[string]string{
		"chain.proto":       chainSpecProtoSrc,
		"pxf/secret.proto":  pxfSecretProtoSrc,
		"app.proto":         appProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(),
		"chain.proto", "app.proto")
	require.NoError(t, err)

	rl := files.AsResolver()
	cd, err := rl.FindDescriptorByName("chameleon.v1.ChainSpec")
	require.NoError(t, err)
	ad, err := rl.FindDescriptorByName("app.v1.AppConfig")
	require.NoError(t, err)
	return cd.(protoreflect.MessageDescriptor), ad.(protoreflect.MessageDescriptor)
}

func parseSpec(t *testing.T, desc protoreflect.MessageDescriptor, input string) proto.Message {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(input), msg))
	return msg
}

// --- core end-to-end ---

func TestResolve_BaseAndEnvOverlay(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
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

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "mybigproject.com"
api_url = "api.mybigproject.com"
feature_flags = ["fast_path"]
`)},
		// prod.pxf doesn't exist; the optional layer skips silently.
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
base_url = "dev.mybigproject.com"
api_url = "api.dev.mybigproject.com"
feature_flags = ["fast_path", "new_dashboard"]
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](
		sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	get := func(name string) string {
		fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
		return res.Config.Get(fd).String()
	}
	assert.Equal(t, "dev.mybigproject.com", get("base_url"),
		"dev layer overrides base_url")
	assert.Equal(t, "api.dev.mybigproject.com", get("api_url"))

	flags := res.Config.Get(res.Config.Descriptor().Fields().ByName("feature_flags")).List()
	require.Equal(t, 2, flags.Len(), "dev replaces feature_flags wholesale")
	assert.Equal(t, "fast_path", flags.Get(0).String())
	assert.Equal(t, "new_dashboard", flags.Get(1).String())
}

func TestResolve_FallThroughWhenLeafIsSilent(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "base.example.com"`)},
		"env/prod.pxf": &fstest.MapFile{Data: []byte(`api_url = "api.prod.example.com"`)},
		// No env/dev.pxf — dev should fall through to prod's api_url
		// and base's base_url.
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	get := func(name string) string {
		fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
		return res.Config.Get(fd).String()
	}
	assert.Equal(t, "base.example.com", get("base_url"))
	assert.Equal(t, "api.prod.example.com", get("api_url"),
		"prod's api_url survives because dev didn't touch it and dev.pxf is missing")
}

// --- secrets pipeline ---

func TestResolve_SecretsExtractedToMap(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
db_password = "supersecret"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	// Secret moved into the Map.
	require.Equal(t, 1, res.Secrets.Len())
	s := res.Secrets.Get("db_password")
	require.NotNil(t, s)
	buf, err := s.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "supersecret", string(buf.Bytes()))

	// Cleared on the proto message.
	pwField := res.Config.Descriptor().Fields().ByName("db_password")
	pwMsg := res.Config.Get(pwField).Message()
	pd := pwMsg.Descriptor()
	assert.Equal(t, "", pwMsg.Get(pd.Fields().ByName("value")).String())
}

func TestResolve_SecretLayeredOverride(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base"    path = "base.pxf"          for_each = ONCE required = true }
  { id = "env-pwd" path = "env/${env}.pwd.pxf" for_each = ONCE }
]
`)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
db_password {
  value = "base-password"
  hint = "default"
}
`)},
		"env/dev.pwd.pxf": &fstest.MapFile{Data: []byte(`db_password = "dev-override"`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	s := res.Secrets.Get("db_password")
	require.NotNil(t, s)
	buf, err := s.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "dev-override", string(buf.Bytes()),
		"dev layer's scalar shorthand overrides base's block-form Secret")
}

// --- error paths ---

func TestResolve_RequiredLayerMissing(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`)

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fstest.MapFS{})),
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrRequiredLayerMissing))
}

func TestResolve_UnknownSource(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE source = "vault" }
]
`)

	fs := fstest.MapFS{"base.pxf": &fstest.MapFile{Data: []byte(``)}}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		// "vault" intentionally NOT registered.
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrNoSource))
}

func TestResolve_EncryptedWithoutDecrypter(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "secrets" path = "secrets.pxf" for_each = ONCE encrypted = true }
]
`)

	fs := fstest.MapFS{"secrets.pxf": &fstest.MapFile{Data: []byte(``)}}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrNoDecrypter))
}

func TestNew_RequiresDefaultSource(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "x" path = "x.pxf" for_each = ONCE }]
`)

	sample := dynamicpb.NewMessage(appDesc)

	// No sources at all.
	_, err := resolver.New[*dynamicpb.Message](sample, spec)
	require.Error(t, err)

	// Has a non-default source but no "default".
	_, err = resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("vault", source.FS(fstest.MapFS{})),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"default" source required`)
}

// --- interpolation pipeline ---

func TestResolve_InterpolationAcrossLayers(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
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

	// base declares api_url as a template referencing base_url.
	// dev only overrides base_url; api_url's template re-resolves
	// against the new base_url. This is the user's original ask:
	// "express the root of the content of those variables and for any
	// other environment, override existing variables ... like a
	// templating manipulation."
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "mybigproject.com"
api_url = "api.${base_url}"
feature_flags = ["fast_path"]
`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
base_url = "dev.mybigproject.com"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	get := func(name string) string {
		fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
		return res.Config.Get(fd).String()
	}
	assert.Equal(t, "dev.mybigproject.com", get("base_url"))
	assert.Equal(t, "api.dev.mybigproject.com", get("api_url"),
		"api_url's template re-resolves against dev's base_url")
}

// --- realistic fixture mirroring the user's original example ---

func TestResolve_TheUsersOriginalExample(t *testing.T) {
	// "the main production base url is mybigproject.com but for dev it
	// is dev.mybigproject.com" — the example that motivated the project.
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
default_environment = "dev"
environments = [
  { name = "prod" }
  { name = "sandbox" extends = "prod" }
  { name = "uat"     extends = "sandbox" }
  { name = "dev"     extends = "sandbox" }
]
context_keys = [
  { name = "region" allowed_values = ["us", "eu"] }
]
layers = [
  { id = "base"   path = "base.pxf"                for_each = ONCE  required = true }
  { id = "env"    path = "env/${env}.pxf"          for_each = PER_ENV }
  { id = "region" path = "region/${ctx.region}.pxf" for_each = ONCE }
]
`)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "mybigproject.com"
api_url = "api.mybigproject.com"
db_password = "base-pwd"
`)},
		"env/sandbox.pxf": &fstest.MapFile{Data: []byte(`
base_url = "sandbox.mybigproject.com"
api_url = "api.sandbox.mybigproject.com"
`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
base_url = "dev.mybigproject.com"
api_url = "api.dev.mybigproject.com"
db_password = "dev-pwd"
`)},
		"region/eu.pxf": &fstest.MapFile{Data: []byte(`
api_url = "api.eu.dev.mybigproject.com"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	t.Run("dev with region=eu", func(t *testing.T) {
		res, err := r.Resolve(context.Background(),
			resolver.Request{Environment: "dev", Context: map[string]string{"region": "eu"}})
		require.NoError(t, err)
		defer res.Destroy()

		get := func(name string) string {
			fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
			return res.Config.Get(fd).String()
		}
		assert.Equal(t, "dev.mybigproject.com", get("base_url"))
		assert.Equal(t, "api.eu.dev.mybigproject.com", get("api_url"),
			"region/eu overlay wins for api_url; base_url untouched")

		s := res.Secrets.Get("db_password")
		require.NotNil(t, s)
		buf, _ := s.Open()
		defer buf.Destroy()
		assert.Equal(t, "dev-pwd", string(buf.Bytes()))
	})

	t.Run("uat with no region", func(t *testing.T) {
		res, err := r.Resolve(context.Background(), resolver.Request{Environment: "uat"})
		require.NoError(t, err)
		defer res.Destroy()

		get := func(name string) string {
			fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
			return res.Config.Get(fd).String()
		}
		// uat extends sandbox extends prod. sandbox.pxf sets base_url
		// + api_url; uat.pxf doesn't exist (skipped); base provides
		// db_password.
		assert.Equal(t, "sandbox.mybigproject.com", get("base_url"))
		assert.Equal(t, "api.sandbox.mybigproject.com", get("api_url"))

		s := res.Secrets.Get("db_password")
		require.NotNil(t, s)
		buf, _ := s.Open()
		defer buf.Destroy()
		assert.Equal(t, "base-pwd", string(buf.Bytes()),
			"sandbox/uat don't override db_password; base value wins")
	})

	t.Run("default env (dev) with no context", func(t *testing.T) {
		res, err := r.Resolve(context.Background(), resolver.Request{})
		require.NoError(t, err)
		defer res.Destroy()

		get := func(name string) string {
			fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
			return res.Config.Get(fd).String()
		}
		assert.Equal(t, "dev.mybigproject.com", get("base_url"))
		assert.Equal(t, "api.dev.mybigproject.com", get("api_url"),
			"no region context → region/eu.pxf skipped → dev's api_url wins")
	})
}

// keep the chain import live (referenced via err checks indirectly)
var _ = chain.Expand

// --- concurrency safety ---

func TestResolution_DestroyConcurrent(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
db_password = "shh"
`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)))
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)

	// Many goroutines call Destroy. Without sync.Once, the second call
	// would re-enter the destroy closure and double-Destroy the
	// memguard Enclaves backing the Secrets — memguard panics on that.
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res.Destroy()
		}()
	}
	wg.Wait()

	// Post-Destroy: secret access returns nil cleanly.
	assert.Nil(t, res.Secrets.Get("db_password"))
}

// --- defaults + validation pipeline ---

const annotationsProtoSrcResolver = `
syntax = "proto3";
package pxf;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FieldOptions {
  bool required = 50000;
  string default = 50001;
}
`

const annotatedAppProtoSrc = `
syntax = "proto3";
package annapp.v1;

import "pxf/annotations.proto";

message AnnApp {
  string base_url = 1 [(pxf.required) = true];
  string api_url = 2 [(pxf.default) = "api.${base_url}"];
  string region = 3 [(pxf.default) = "us"];
  string api_key = 4 [(pxf.required) = true];
}
`

func compileAnnotated(t *testing.T) (chainDesc, appDesc protoreflect.MessageDescriptor) {
	t.Helper()
	sources := map[string]string{
		"chain.proto":           chainSpecProtoSrc,
		"app.proto":             annotatedAppProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrcResolver,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "chain.proto", "app.proto")
	require.NoError(t, err)
	rl := files.AsResolver()
	cd, err := rl.FindDescriptorByName("chameleon.v1.ChainSpec")
	require.NoError(t, err)
	ad, err := rl.FindDescriptorByName("annapp.v1.AnnApp")
	require.NoError(t, err)
	return cd.(protoreflect.MessageDescriptor), ad.(protoreflect.MessageDescriptor)
}

func TestResolve_DefaultsThenInterpolation(t *testing.T) {
	chainDesc, appDesc := compileAnnotated(t)

	// base sets only the required fields. api_url and region get
	// their defaults; api_url's default is itself a template that
	// interpolates against base_url.
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "mybigproject.com"
api_key = "k"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	get := func(name string) string {
		fd := res.Config.Descriptor().Fields().ByName(protoreflect.Name(name))
		return res.Config.Get(fd).String()
	}
	assert.Equal(t, "mybigproject.com", get("base_url"))
	assert.Equal(t, "api.mybigproject.com", get("api_url"),
		"api_url default is a template; interpolation resolved it against base_url")
	assert.Equal(t, "us", get("region"), "region got its literal default")
}

func TestResolve_RequiredFieldMissingFails(t *testing.T) {
	chainDesc, appDesc := compileAnnotated(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`)
	fs := fstest.MapFS{
		// api_key is required but never set; base_url is set.
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key")
}

// --- trace plumbing ---

func TestTrace_DisabledByDefault(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)))
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	assert.Nil(t, res.Trace, "Trace must be nil unless WithTrace was passed")
}

func TestTrace_RecordsLayerProvenance(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`)
	fs := fstest.MapFS{
		"base.pxf":     &fstest.MapFile{Data: []byte(`base_url = "base"`)},
		"env/dev.pxf":  &fstest.MapFile{Data: []byte(`api_url = "api.dev"`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace())
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	require.NotNil(t, res.Trace)
	assert.Equal(t, "base", res.Trace.Get("base_url").LayerID)
	assert.Equal(t, "env", res.Trace.Get("api_url").LayerID,
		"dev layer is the highest to mention api_url; trace tracks it")
}

func TestTrace_HighestLayerWins(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`)
	fs := fstest.MapFS{
		"base.pxf":    &fstest.MapFile{Data: []byte(`base_url = "from-base"`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`base_url = "from-dev"`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace())
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	assert.Equal(t, "env", res.Trace.Get("base_url").LayerID,
		"the higher (env) layer overrides base — trace reflects the winner")
}

func TestTrace_FromDefault(t *testing.T) {
	chainDesc, appDesc := compileAnnotated(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
api_key = "k"
`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace())
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	// `region` has (pxf.default) = "us"; no layer set it.
	regionTrace := res.Trace.Get("region")
	assert.True(t, regionTrace.FromDefault, "region should be marked as default-supplied")
	assert.Empty(t, regionTrace.LayerID, "FromDefault entries have empty LayerID")

	// `base_url` was set explicitly; not from default.
	assert.False(t, res.Trace.Get("base_url").FromDefault)
}

func TestTrace_Interpolated(t *testing.T) {
	chainDesc, appDesc := compileAnnotated(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	// api_url's default is "api.${base_url}" — defaults pass writes
	// the template, interpolation rewrites it. Trace records both.
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "example.com"
api_key = "k"
`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace())
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	apiTrace := res.Trace.Get("api_url")
	assert.True(t, apiTrace.FromDefault, "api_url filled by default")
	assert.True(t, apiTrace.Interpolated, "default value was a template that got expanded")
}

func TestTrace_Sensitive(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
db_password = "shh"
`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace())
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	pwTrace := res.Trace.Get("db_password")
	assert.True(t, pwTrace.Sensitive, "pxf.Secret-typed field marked sensitive")
	assert.Equal(t, "base", pwTrace.LayerID)
	assert.False(t, res.Trace.Get("base_url").Sensitive,
		"non-secret fields not marked sensitive")
}

func TestTrace_Paths(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
api_url = "y"
db_password = "p"
`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace())
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	paths := res.Trace.Paths()
	// Sorted alphabetically; should include the three set fields plus
	// `db_password.value` (parser also marks the inner path).
	assert.Contains(t, paths, "base_url")
	assert.Contains(t, paths, "api_url")
	assert.Contains(t, paths, "db_password")
	// Sorted check.
	for i := 1; i < len(paths); i++ {
		assert.LessOrEqual(t, paths[i-1], paths[i])
	}
}

func TestResolve_RequiredSatisfiedAcrossLayers(t *testing.T) {
	chainDesc, appDesc := compileAnnotated(t)

	// base only sets base_url. env layer adds api_key. Required
	// validation must run AFTER merge, not per-layer; otherwise the
	// base parse would error on missing api_key.
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"`)},
		"env/prod.pxf": &fstest.MapFile{Data: []byte(`api_key = "prod-key"`)},
		"env/dev.pxf":  &fstest.MapFile{Data: []byte(`api_key = "dev-key"`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err, "required field provided by higher layer must satisfy validation")
	defer res.Destroy()

	fd := res.Config.Descriptor().Fields().ByName("api_key")
	assert.Equal(t, "dev-key", res.Config.Get(fd).String())
}
