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
	"github.com/trendvidia/chameleon/secret"
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

func compileAll(tb testing.TB) (chainDesc, appDesc protoreflect.MessageDescriptor) {
	tb.Helper()
	sources := map[string]string{
		"chain.proto":      chainSpecProtoSrc,
		"pxf/secret.proto": pxfSecretProtoSrc,
		"app.proto":        appProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(),
		"chain.proto", "app.proto")
	require.NoError(tb, err)

	rl := files.AsResolver()
	cd, err := rl.FindDescriptorByName("chameleon.v1.ChainSpec")
	require.NoError(tb, err)
	ad, err := rl.FindDescriptorByName("app.v1.AppConfig")
	require.NoError(tb, err)
	return cd.(protoreflect.MessageDescriptor), ad.(protoreflect.MessageDescriptor)
}

func parseSpec(tb testing.TB, desc protoreflect.MessageDescriptor, input string) proto.Message {
	tb.Helper()
	msg := dynamicpb.NewMessage(desc)
	require.NoError(tb, pxf.Unmarshal([]byte(input), msg))
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
		"base.pxf":     &fstest.MapFile{Data: []byte(`base_url = "base.example.com"`)},
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

// TestResolve_HookRoutedSecret_ProtoMessageLeftEmpty — confirms the
// scalar-shorthand secret reaches the Map via the per-decode
// OnSecretField hook AND leaves Secret.value untouched on the proto
// message. The hook is the load-bearing thing; if it were not wired,
// the proto message would carry the plaintext (the v0.6.x behavior)
// and the test would observe "supersecret" on the message even
// though the Map also has it.
func TestResolve_HookRoutedSecret_ProtoMessageLeftEmpty(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`db_password = "supersecret"`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	// Map has the secret (via the hook, not via Move's walk — Move
	// would have skipped because Secret.value is empty on the message).
	s := res.Secrets.Get("db_password")
	require.NotNil(t, s)
	buf, err := s.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "supersecret", string(buf.Bytes()))

	// Critically: Secret.value on the merged proto message must be
	// empty — confirming the hook took the bytes during decode and
	// the standard string-field assignment was bypassed.
	pwField := res.Config.Descriptor().Fields().ByName("db_password")
	pwMsg := res.Config.Get(pwField).Message()
	pwValue := pwMsg.Get(pwMsg.Descriptor().Fields().ByName("value")).String()
	assert.Equal(t, "", pwValue, "hook must have taken the value during decode; proto message should be empty")
}

// TestResolve_NullOverridesHookEntry — base layer writes a scalar-
// shorthand secret (hook routes it into the Map mid-decode); a
// higher layer explicitly nulls the field. Merge clears the Secret
// from dst, MoveInto's orphan-cleanup pass detects the missing path,
// and the consumer must NOT see the lower layer's value.
//
// Without orphan cleanup, the hook entry would outlive its null
// override — a real security regression (consumer fetches a secret
// the merged config disowned).
func TestResolve_NullOverridesHookEntry(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base"    path = "base.pxf"             for_each = ONCE required = true }
  { id = "env-pwd" path = "env/${env}.pwd.pxf"  for_each = ONCE }
]
`)
	fs := fstest.MapFS{
		"base.pxf":        &fstest.MapFile{Data: []byte(`db_password = "base-secret"`)},
		"env/dev.pwd.pxf": &fstest.MapFile{Data: []byte(`db_password = null`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	assert.Nil(t, res.Secrets.Get("db_password"),
		"null override must drop the hook entry; otherwise consumers can fetch a secret the merged config nulled")
	assert.Equal(t, 0, res.Secrets.Len())
}

// TestResolve_BlockFormWithoutValueClearsHookEntry — companion to
// TestResolve_NullOverridesHookEntry. base layer writes
// `db_password = "base-secret"` (scalar shorthand, hook routes the
// value); dev layer writes `db_password { hint = "rotated" }` (block
// form, NO value subfield). WKT atomic-merge says dev's block
// replaces base's Secret wholesale — the prior value must be
// dropped per the v0.11 clearing semantic.
//
// Without this, the hook-routed base value would survive dev's
// "block-form replacement" and the consumer would see a secret the
// merged config never intended to keep.
func TestResolve_BlockFormWithoutValueClearsHookEntry(t *testing.T) {
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base"    path = "base.pxf"            for_each = ONCE required = true }
  { id = "env-pwd" path = "env/${env}.pwd.pxf" for_each = ONCE }
]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`db_password = "base-secret"`)},
		// dev's block form: hint only, no value. Atomic-merge clear.
		"env/dev.pwd.pxf": &fstest.MapFile{Data: []byte(`db_password { hint = "rotated 2026" }`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	assert.Nil(t, res.Secrets.Get("db_password"),
		"block-form-without-value is a wholesale replace; base's hook entry must be cleared")
	assert.Equal(t, 0, res.Secrets.Len())
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
	require.ErrorContains(t, err, "no sources registered")

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

// TestResolver_ConcurrentResolve backs the godoc claim at
// resolver.go:82 — "Resolver is safe for concurrent Resolve calls;
// each call produces an independent Resolution." Without this test
// the claim is asserted but unverified, which is risky to lock in
// at v1.0.
//
// N goroutines call r.Resolve against the same *Resolver. Each must:
//   - return a non-nil *Resolution with no error;
//   - observe the SAME field values (the underlying spec + FS are
//     unchanged across calls);
//   - hold a SEPARATE secret.Map (per-Resolve allocation, no aliasing).
//
// Under `go test -race`, this test would flag any unsynchronized
// shared state on the Resolver itself (e.g. a cached descriptor
// without sync.Once, mutable fields not protected by a mutex).
func TestResolver_ConcurrentResolve(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base"  path = "base.pxf"           for_each = ONCE required = true }
  { id = "env"   path = "env/${env}.pxf"     for_each = PER_ENV }
]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "base.example.com"
api_url = "api.base.example.com"
db_password = "base-pw"
feature_flags = ["fast_path"]
`)},
		"env/prod.pxf": &fstest.MapFile{Data: []byte(`
base_url = "prod.example.com"
`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
base_url = "dev.example.com"
db_password = "dev-pw"
feature_flags = ["fast_path", "new_dashboard"]
`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)))
	require.NoError(t, err)

	const n = 64
	results := make([]*resolver.Resolution[*dynamicpb.Message], n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			// Alternate environments so the resolver exercises both
			// branches of the chain. Race detection on shared state
			// (caches, presence unions, OTel instrument writes) fires
			// regardless of which env is requested; mixing them
			// stresses the merge / interpolate paths too.
			env := "prod"
			if i%2 == 1 {
				env = "dev"
			}
			res, err := r.Resolve(context.Background(), resolver.Request{Environment: env})
			results[i] = res
			errs[i] = err
		}(i)
	}
	wg.Wait()
	// Schedule Destroy on every successful Resolution before any
	// assertions, so a panicking assert doesn't leak Enclaves.
	t.Cleanup(func() {
		for _, res := range results {
			if res != nil {
				res.Destroy()
			}
		}
	})

	// All goroutines succeeded.
	for i, err := range errs {
		require.NoError(t, err, "goroutine %d", i)
		require.NotNil(t, results[i], "goroutine %d returned nil Resolution", i)
	}

	// Resolutions are independent — pairwise-distinct secret.Map
	// pointers. The Config message pointer also differs per Resolve
	// (each call gets a fresh proto allocation).
	seenSecrets := make(map[*secret.Map]int)
	for i, res := range results {
		if existing, ok := seenSecrets[res.Secrets]; ok {
			t.Errorf("goroutine %d shares Secrets map with goroutine %d (aliased Resolution)", i, existing)
		}
		seenSecrets[res.Secrets] = i
	}

	// Field values are consistent per environment across all
	// goroutines that requested that env. base_url and feature_flags
	// differ between prod and dev; we verify each env's resolutions
	// agree internally and disagree with the other env's.
	getBaseURL := func(res *resolver.Resolution[*dynamicpb.Message]) string {
		fd := res.Config.Descriptor().Fields().ByName("base_url")
		return res.Config.Get(fd).String()
	}
	getSecret := func(res *resolver.Resolution[*dynamicpb.Message], path string) string {
		s := res.Secrets.Get(path)
		require.NotNil(t, s, "secret %q missing in resolution", path)
		buf, err := s.Open()
		require.NoError(t, err)
		defer buf.Destroy()
		return string(buf.Bytes())
	}

	for i, res := range results {
		env := "prod"
		if i%2 == 1 {
			env = "dev"
		}
		switch env {
		case "prod":
			assert.Equal(t, "prod.example.com", getBaseURL(res),
				"goroutine %d (env=prod): base_url drift", i)
			assert.Equal(t, "base-pw", getSecret(res, "db_password"),
				"goroutine %d (env=prod): db_password drift", i)
		case "dev":
			assert.Equal(t, "dev.example.com", getBaseURL(res),
				"goroutine %d (env=dev): base_url drift", i)
			assert.Equal(t, "dev-pw", getSecret(res, "db_password"),
				"goroutine %d (env=dev): db_password drift", i)
		}
	}
}

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
	for range 32 {
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
import "google/protobuf/duration.proto";

message AnnApp {
  string base_url = 1 [(pxf.required) = true];
  string api_url = 2 [(pxf.default) = "api.${base_url}"];
  string region = 3 [(pxf.default) = "us"];
  string api_key = 4 [(pxf.required) = true];
  google.protobuf.Duration shutdown_grace = 5 [(pxf.default) = "250ms"];
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

// TestResolve_WKTDefault is the end-to-end shape of issue #129: a
// (pxf.default) on a google.protobuf.Duration field must survive the
// full pipeline, not resolve to a zero Duration.
func TestResolve_WKTDefault(t *testing.T) {
	chainDesc, appDesc := compileAnnotated(t)
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
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
api_key = "k"
`)},
		// prod leaves shutdown_grace to the schema default; dev overrides.
		"env/prod.pxf": &fstest.MapFile{Data: []byte(``)},
		"env/dev.pxf":  &fstest.MapFile{Data: []byte(`shutdown_grace = 1s`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace())
	require.NoError(t, err)

	grace := func(t *testing.T, cfg *dynamicpb.Message) (secs, nanos int64) {
		t.Helper()
		fd := cfg.Descriptor().Fields().ByName("shutdown_grace")
		require.True(t, cfg.Has(fd), "shutdown_grace must be set")
		d := cfg.Get(fd).Message()
		return d.Get(d.Descriptor().Fields().ByName("seconds")).Int(),
			d.Get(d.Descriptor().Fields().ByName("nanos")).Int()
	}

	t.Run("default applies when no layer sets it", func(t *testing.T) {
		res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
		require.NoError(t, err)
		defer res.Destroy()

		secs, nanos := grace(t, res.Config)
		assert.Equal(t, int64(0), secs)
		assert.Equal(t, int64(250_000_000), nanos, "(pxf.default) = 250ms must reach the Duration")
		assert.True(t, res.Trace.Get("shutdown_grace").FromDefault)
	})

	t.Run("layer value wins over the default", func(t *testing.T) {
		res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
		require.NoError(t, err)
		defer res.Destroy()

		secs, nanos := grace(t, res.Config)
		assert.Equal(t, int64(1), secs)
		assert.Equal(t, int64(0), nanos)
		trace := res.Trace.Get("shutdown_grace")
		assert.False(t, trace.FromDefault)
		assert.Equal(t, "env", trace.LayerID)
	})
}

// TestResolve_UnsupportedDefaultErrorIsNotDoubledPrefixed pins the
// operator-facing shape of the new defaults rejection. defaults.Apply
// already prefixes "defaults: ", so the resolver wraps with a bare
// "resolver: " — otherwise the error reads "resolver: defaults:
// defaults: …" and anyone grepping logs for the documented text
// misses it.
func TestResolve_UnsupportedDefaultErrorIsNotDoubledPrefixed(t *testing.T) {
	const badAppSrc = `
syntax = "proto3";
package badapp.v1;

import "pxf/annotations.proto";

message BadApp {
  string base_url = 1;
  Inner inner = 2 [(pxf.default) = "nope"];
}

message Inner { string x = 1; }
`
	sources := map[string]string{
		"chain.proto":           chainSpecProtoSrc,
		"app.proto":             badAppSrc,
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
	ad, err := rl.FindDescriptorByName("badapp.v1.BadApp")
	require.NoError(t, err)

	spec := parseSpec(t, cd.(protoreflect.MessageDescriptor), `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"`)},
	}
	sample := dynamicpb.NewMessage(ad.(protoreflect.MessageDescriptor))
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)))
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolver: defaults: inner:")
	assert.Contains(t, err.Error(), "badapp.v1.Inner")
	assert.NotContains(t, err.Error(), "defaults: defaults:",
		"the pass name must appear once, not twice")
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
		"base.pxf":    &fstest.MapFile{Data: []byte(`base_url = "base"`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`api_url = "api.dev"`)},
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
		"base.pxf":     &fstest.MapFile{Data: []byte(`base_url = "x"`)},
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

// --- @header preamble ---

// headerSpec returns a minimal one-base-one-env chain spec parsed
// against the test chainDesc. Sources used: "default" for plaintext,
// "secrets" for an encrypted layer.
func headerSpec(t *testing.T, chainDesc protoreflect.MessageDescriptor, encrypted bool) proto.Message {
	t.Helper()
	encField := ""
	if encrypted {
		encField = "encrypted = true source = \"secrets\""
	}
	return parseSpec(t, chainDesc, `
version = "1.0.0"
default_environment = "dev"
environments = [
  { name = "prod" }
  { name = "dev" extends = "prod" }
]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true `+encField+` }
  { id = "env"  path = "env/${env}.pxf" for_each = PER_ENV }
]
`)
}

func TestResolve_HeaderMatches(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := headerSpec(t, chainDesc, false)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
@header chameleon.v1.LayerHeader
{
  id = "base"
  encrypted = false
}
base_url = "x.com"
`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
@header chameleon.v1.LayerHeader
{
  id = "env"
  environment = "dev"
}
base_url = "dev.x.com"
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

	fd := res.Config.Descriptor().Fields().ByName("base_url")
	assert.Equal(t, "dev.x.com", res.Config.Get(fd).String(),
		"body parses normally after @header is peeled off")
}

func TestResolve_HeaderIDMismatch(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := headerSpec(t, chainDesc, false)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
@header chameleon.v1.LayerHeader
{
  id = "wrong-id"
}
base_url = "x.com"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@header.id", "error mentions the offending field")
	assert.Contains(t, err.Error(), "wrong-id")
}

func TestResolve_HeaderEnvironmentMismatch(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := headerSpec(t, chainDesc, false)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x.com"`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`
@header chameleon.v1.LayerHeader
{
  environment = "prod"
}
base_url = "dev.x.com"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@header.environment")
}

func TestResolve_HeaderEncryptedMismatch(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := headerSpec(t, chainDesc, false) // chain entry is NOT encrypted

	// File asserts encrypted=true while the chain entry is plaintext.
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
@header chameleon.v1.LayerHeader
{
  encrypted = true
}
base_url = "x.com"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@header.encrypted")
}

func TestResolve_HeaderWrongType(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := headerSpec(t, chainDesc, false)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
@header some.other.Header
{
  id = "base"
}
base_url = "x.com"
`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chameleon.v1.LayerHeader")
}

func TestResolve_HeaderOptionalAbsent(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := headerSpec(t, chainDesc, false)

	// No @header at all — resolves cleanly, no assertion run.
	fs := fstest.MapFS{
		"base.pxf":    &fstest.MapFile{Data: []byte(`base_url = "x.com"`)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(`base_url = "dev.x.com"`)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)
	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	fd := res.Config.Descriptor().Fields().ByName("base_url")
	assert.Equal(t, "dev.x.com", res.Config.Get(fd).String())
}

// --- WKT clobber warning ---

// clobberSpec returns a minimal chain spec with two layers (base, env)
// for clobber-detection tests.
func clobberSpec(t *testing.T, chainDesc protoreflect.MessageDescriptor) proto.Message {
	t.Helper()
	return parseSpec(t, chainDesc, `
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
}

// TestClobber_RecordedOnTrace — env layer drops base's hint; the
// resolver's Trace should carry one ClobberRecord attributing the
// loss to the env layer.
func TestClobber_RecordedOnTrace(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := clobberSpec(t, chainDesc)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(
			`db_password { value = "x"  hint = "Postgres primary" }` + "\n",
		)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(
			`db_password = "y"` + "\n",
		)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace(),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	require.NotNil(t, res.Trace)
	require.Len(t, res.Trace.ClobberedMetadata, 1, "expected one clobber recorded")
	rec := res.Trace.ClobberedMetadata[0]
	assert.Equal(t, "db_password", rec.Path)
	assert.Equal(t, "pxf.Secret", rec.Type)
	assert.Equal(t, []string{"hint"}, rec.LostFields)
	assert.Equal(t, "env", rec.ClobberingLayerID)
}

// TestClobber_StrictModeErrors — same setup as above, but
// WithStrictMetadata upgrades the warning to a hard resolve error.
func TestClobber_StrictModeErrors(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := clobberSpec(t, chainDesc)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(
			`db_password { value = "x"  hint = "Postgres primary" }` + "\n",
		)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(
			`db_password = "y"` + "\n",
		)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithStrictMetadata(),
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.Error(t, err, "strict mode must abort resolution on metadata clobber")
	assert.Contains(t, err.Error(), "clobbered metadata")
	assert.Contains(t, err.Error(), "db_password")
	assert.Contains(t, err.Error(), "hint")
}

// TestClobber_NoneWhenUpperPreservesMetadata — env layer re-declares
// the hint, no clobber.
func TestClobber_NoneWhenUpperPreservesMetadata(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := clobberSpec(t, chainDesc)

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(
			`db_password { value = "x"  hint = "Postgres primary" }` + "\n",
		)},
		"env/dev.pxf": &fstest.MapFile{Data: []byte(
			`db_password { value = "y"  hint = "Postgres primary" }` + "\n",
		)},
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace(),
	)
	require.NoError(t, err)
	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "dev"})
	require.NoError(t, err)
	defer res.Destroy()

	require.NotNil(t, res.Trace)
	assert.Empty(t, res.Trace.ClobberedMetadata, "upper layer preserved metadata; no clobber expected")
}
