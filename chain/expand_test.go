// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// chainSpecProtoSrc is a stripped-down ChainSpec schema — drops the
// (pxf.required) annotations to keep the test free of the protowire
// annotations.proto dependency. Field shapes match
// chameleon/v1/chain.proto.
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
  string key_name = 4;
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

func compileChainSpec(t testing.TB) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{"chain.proto": chainSpecProtoSrc}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	result, err := comp.Compile(context.Background(), "chain.proto")
	require.NoError(t, err)
	d, err := result.AsResolver().FindDescriptorByName("chameleon.v1.ChainSpec")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// parseSpec compiles a PXF chain spec literal into a dynamic ChainSpec
// message. Tests use this to build up specs as PXF text rather than
// hand-constructing protoreflect graphs.
func parseSpec(t testing.TB, desc protoreflect.MessageDescriptor, input string) proto.Message {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(input), msg))
	return msg
}

// --- happy paths ---

func TestExpand_SingleEnvOnceLayer(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  {
    id = "base"
    path = "base.pxf"
    for_each = ONCE
  }
]
`)
	refs, err := chain.Expand(spec, "prod", nil)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "base", refs[0].EntryID)
	assert.Equal(t, "base.pxf", refs[0].Path)
	assert.Equal(t, "prod", refs[0].Env)
	assert.Equal(t, "default", refs[0].Source)
}

func TestExpand_PerEnvWalksAncestryRootToLeaf(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "sandbox" extends = "prod" }
  { name = "uat"     extends = "sandbox" }
]
layers = [
  {
    id = "env"
    path = "env/${env}.pxf"
    for_each = PER_ENV
  }
]
`)
	refs, err := chain.Expand(spec, "uat", nil)
	require.NoError(t, err)
	require.Len(t, refs, 3, "PER_ENV should expand to one ref per ancestor")
	assert.Equal(t, "env/prod.pxf", refs[0].Path)
	assert.Equal(t, "prod", refs[0].Env)
	assert.Equal(t, "env/sandbox.pxf", refs[1].Path)
	assert.Equal(t, "sandbox", refs[1].Env)
	assert.Equal(t, "env/uat.pxf", refs[2].Path)
	assert.Equal(t, "uat", refs[2].Env)
}

func TestExpand_ContextDrivenLayer(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
context_keys = [
  { name = "region" }
]
layers = [
  { id = "base"   path = "base.pxf"                for_each = ONCE }
  { id = "region" path = "region/${ctx.region}.pxf" for_each = ONCE }
]
`)

	t.Run("region present → both layers materialize", func(t *testing.T) {
		refs, err := chain.Expand(spec, "prod", map[string]string{"region": "eu"})
		require.NoError(t, err)
		require.Len(t, refs, 2)
		assert.Equal(t, "region/eu.pxf", refs[1].Path)
	})

	t.Run("region absent → optional layer skipped", func(t *testing.T) {
		refs, err := chain.Expand(spec, "prod", nil)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, "base", refs[0].EntryID)
	})
}

func TestExpand_DefaultEnvironment(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
default_environment = "dev"
environments = [{ name = "dev" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	refs, err := chain.Expand(spec, "", nil)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "dev", refs[0].Env)
}

func TestExpand_ContextDefaultApplied(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
context_keys = [
  { name = "region" default = "us" }
]
layers = [{ id = "r" path = "region/${ctx.region}.pxf" for_each = ONCE }]
`)
	refs, err := chain.Expand(spec, "prod", nil)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "region/us.pxf", refs[0].Path)
}

func TestExpand_AllowedValuesAccepted(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
context_keys = [
  { name = "region" allowed_values = ["us", "eu", "ap"] }
]
layers = [{ id = "r" path = "region/${ctx.region}.pxf" for_each = ONCE }]
`)
	refs, err := chain.Expand(spec, "prod", map[string]string{"region": "eu"})
	require.NoError(t, err)
	require.Len(t, refs, 1)
}

func TestExpand_SourceDefaulting(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base"     path = "base.pxf"     for_each = ONCE }
  { id = "secrets"  path = "secrets.pxf"  for_each = ONCE  source = "vault" }
]
`)
	refs, err := chain.Expand(spec, "prod", nil)
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, "default", refs[0].Source)
	assert.Equal(t, "vault", refs[1].Source)
}

func TestExpand_EncryptedFlagPropagates(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "secrets" path = "secrets.pxf" for_each = ONCE encrypted = true }
]
`)
	refs, err := chain.Expand(spec, "prod", nil)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.True(t, refs[0].Encrypted)
}

// --- error paths ---

func TestExpand_UnknownEnvironment(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	_, err := chain.Expand(spec, "ghost", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, chain.ErrUnknownEnvironment))
}

func TestExpand_NoEnvAndNoDefault(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	_, err := chain.Expand(spec, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no environment")
}

func TestExpand_CyclicAncestry(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [
  { name = "a" extends = "b" }
  { name = "b" extends = "a" }
]
layers = [{ id = "x" path = "x.pxf" for_each = ONCE }]
`)
	_, err := chain.Expand(spec, "a", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, chain.ErrCyclicAncestry))
}

func TestExpand_ExtendsUndeclared(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [
  { name = "child" extends = "ghost" }
]
layers = [{ id = "x" path = "x.pxf" for_each = ONCE }]
`)
	_, err := chain.Expand(spec, "child", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extends undeclared")
}

func TestExpand_RequiredContextMissing(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
context_keys = [{ name = "tenant" required = true }]
layers = [{ id = "x" path = "x.pxf" for_each = ONCE }]
`)
	_, err := chain.Expand(spec, "prod", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, chain.ErrMissingRequiredContext))
}

func TestExpand_ContextValueNotAllowed(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
context_keys = [{ name = "region" allowed_values = ["us", "eu"] }]
layers = [{ id = "x" path = "x.pxf" for_each = ONCE }]
`)
	_, err := chain.Expand(spec, "prod", map[string]string{"region": "mars"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, chain.ErrContextValueNotAllowed))
}

func TestExpand_UnknownContextKeyStrict(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
context_keys = [{ name = "region" }]
layers = [{ id = "x" path = "x.pxf" for_each = ONCE }]
`)
	_, err := chain.Expand(spec, "prod", map[string]string{"regions": "eu"}) // typo
	require.Error(t, err)
	assert.True(t, errors.Is(err, chain.ErrUnknownContextKey))
}

func TestExpand_AllowUnknownContextKeys(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "x" path = "x.pxf" for_each = ONCE }]
`)
	_, err := chain.Options{AllowUnknownContextKeys: true}.Expand(
		spec, "prod", map[string]string{"region": "eu"})
	require.NoError(t, err)
}

func TestExpand_RequiredLayerUnresolved(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
context_keys = [{ name = "region" }]
layers = [
  { id = "r" path = "region/${ctx.region}.pxf" for_each = ONCE required = true }
]
`)
	_, err := chain.Expand(spec, "prod", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, chain.ErrUnresolvedRequiredLayer))
}

func TestExpand_NotAChainSpec(t *testing.T) {
	// Compile any other message type and pass it to Expand.
	const otherSrc = `
syntax = "proto3";
package other.v1;
message NotASpec { string s = 1; }
`
	sources := map[string]string{"o.proto": otherSrc}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	res, err := comp.Compile(context.Background(), "o.proto")
	require.NoError(t, err)
	desc := res[0].Messages().ByName("NotASpec")
	bad := dynamicpb.NewMessage(desc)

	_, err = chain.Expand(bad, "prod", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected chameleon.v1.ChainSpec")
}

// --- end-to-end shape ---

func TestExpand_RealisticChain(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
default_environment = "dev"
environments = [
  { name = "prod" }
  { name = "sandbox" extends = "prod" }
  { name = "uat"     extends = "sandbox" }
  { name = "dev"     extends = "sandbox" }
]
context_keys = [
  { name = "region" allowed_values = ["us", "eu", "ap"] }
  { name = "tenant" }
]
layers = [
  { id = "base"    path = "base.pxf"                       for_each = ONCE    required = true }
  { id = "env"     path = "env/${env}.pxf"                 for_each = PER_ENV }
  { id = "region"  path = "region/${ctx.region}.pxf"       for_each = ONCE }
  { id = "tenant"  path = "tenant/${ctx.tenant}/o.pxf"     for_each = ONCE }
  { id = "secrets" path = "${env}.sops.pxf"                for_each = ONCE   encrypted = true   source = "secrets" }
]
`)

	refs, err := chain.Expand(spec, "uat", map[string]string{
		"region": "eu",
		"tenant": "acme",
	})
	require.NoError(t, err)

	// Expected materialization order:
	//   base.pxf
	//   env/prod.pxf, env/sandbox.pxf, env/uat.pxf
	//   region/eu.pxf
	//   tenant/acme/o.pxf
	//   uat.sops.pxf  (encrypted, source=secrets)
	require.Len(t, refs, 7)

	want := []struct {
		id   string
		path string
		env  string
	}{
		{"base", "base.pxf", "uat"},
		{"env", "env/prod.pxf", "prod"},
		{"env", "env/sandbox.pxf", "sandbox"},
		{"env", "env/uat.pxf", "uat"},
		{"region", "region/eu.pxf", "uat"},
		{"tenant", "tenant/acme/o.pxf", "uat"},
		{"secrets", "uat.sops.pxf", "uat"},
	}
	for i, w := range want {
		assert.Equal(t, w.id, refs[i].EntryID, "ref[%d].EntryID", i)
		assert.Equal(t, w.path, refs[i].Path, "ref[%d].Path", i)
		assert.Equal(t, w.env, refs[i].Env, "ref[%d].Env", i)
	}
	assert.True(t, refs[6].Encrypted)
	assert.Equal(t, "secrets", refs[6].Source)
}
