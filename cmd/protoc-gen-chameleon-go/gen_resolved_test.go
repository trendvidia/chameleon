// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// runPluginWithResolved drives generate() with pluginState.withResolved
// set, then restores. Returns the generated sidecar content.
func runPluginWithResolved(t *testing.T) string {
	t.Helper()
	prev := pluginState.withResolved
	pluginState.withResolved = true
	t.Cleanup(func() { pluginState.withResolved = prev })

	sources := map[string]string{
		"sample.proto":     sampleProtoSrc,
		"pxf/secret.proto": secretProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "sample.proto", "pxf/secret.proto")
	require.NoError(t, err)
	var fdProtos []*descriptorpb.FileDescriptorProto
	for _, f := range files {
		fdProtos = append(fdProtos, protodesc.ToFileDescriptorProto(f))
	}
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"sample.proto"},
		ProtoFile:      fdProtos,
	}
	plugin, err := protogen.Options{}.New(req)
	require.NoError(t, err)
	require.NoError(t, generate(plugin))
	resp := plugin.Response()
	require.NotNil(t, resp)
	require.Empty(t, resp.GetError(), "plugin error: %s", resp.GetError())
	require.Len(t, resp.GetFile(), 1, "expected one sidecar file")
	return resp.GetFile()[0].GetContent()
}

// TestResolved_WrapperStructAndConstructor — the opt-in flag emits the
// wrapper struct + constructor for the root message that has Secrets.
func TestResolved_WrapperStructAndConstructor(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "type AppConfigResolved struct {")
	assert.Contains(t, out, "func NewAppConfigResolved(m *AppConfig, secrets *")
	assert.Contains(t, out, "func (r *AppConfigResolved) Underlying() *AppConfig")
	assert.Contains(t, out, "func (r *AppConfigResolved) Secrets() *")
}

// TestResolved_DefaultOff — without the flag, no wrapper is emitted.
// Guards against the wrapper accidentally becoming a default that
// breaks existing consumers' generated files.
func TestResolved_DefaultOff(t *testing.T) {
	out := runPlugin(t) // default state: withResolved = false
	assert.NotContains(t, out, "AppConfigResolved")
	assert.NotContains(t, out, "NewAppConfigResolved")
}

// TestResolved_TopLevelScalarMethod — top-level scalar Secret yields
// a no-param method named after the PascalCase path. Body uses
// `r.prefix + ...` so the same method works correctly when the
// wrapper is reached via a parent's chained accessor.
func TestResolved_TopLevelScalarMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) DbPassword() *")
	assert.Contains(t, out, `r.secrets.Get(r.prefix + "db_password")`)
}

// TestResolved_NestedScalarMethod — nested Secret yields a PascalCase
// dotted method name and the dotted path body.
func TestResolved_NestedScalarMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) PrimaryAuthToken() *")
	assert.Contains(t, out, `r.secrets.Get(r.prefix + "primary.auth_token")`)
}

// TestResolved_RepeatedSecretMethod — repeated Secret leaf gets an
// "At" suffix to signal the method takes an index parameter.
func TestResolved_RepeatedSecretMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) BackupKeysAt(i int) *")
	assert.Contains(t, out, `"backup_keys["`)
	assert.Contains(t, out, `Itoa(i)`)
	assert.Contains(t, out, `"]"`)
}

// TestResolved_MapStringSecretMethod — map<string, Secret> gets a
// `key K`-typed param; no "At" suffix (the key param already reads
// as the indexer).
func TestResolved_MapStringSecretMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) TenantKeys(key string) *")
	assert.Contains(t, out, `"tenant_keys["`)
	assert.Contains(t, out, `Quote(key)`)
}

// TestResolved_MapInt32SecretMethod — map<int32, Secret>: key kind
// determines the param Go type and runtime formatter.
func TestResolved_MapInt32SecretMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) PortKeys(key int32) *")
	assert.Contains(t, out, `"port_keys["`)
	assert.Contains(t, out, `FormatInt(int64(key), 10)`)
}

// TestResolved_NestedRepeatedSecretMethod —
// repeated Endpoint replicas, each with pxf.Secret auth_token, becomes
// `ReplicasAuthTokenAt(i int) *secret.Secret`. "At" suffix because a
// repeated container appears anywhere in the path, even though the
// trailing Secret is scalar.
func TestResolved_NestedRepeatedSecretMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) ReplicasAuthTokenAt(i int) *")
	assert.Contains(t, out, `"replicas["`)
	assert.Contains(t, out, `"].auth_token"`)
}

// TestResolved_OriginalFunctionsAlsoEmitted — wrapper mode is additive;
// the function-form accessors stay so callers that prefer the
// standalone shape don't lose them.
func TestResolved_OriginalFunctionsAlsoEmitted(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func AppConfigSecret_DbPassword(s ")
	assert.Contains(t, out, "func AppConfigSecret_TenantKeys(s ")
}

// TestResolved_DocCommentMentionsPath — each wrapper method's godoc
// names the dotted path so users reading the godoc can correlate it
// with what `chameleon explain` prints.
func TestResolved_DocCommentMentionsPath(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, `at PXF path "db_password"`)
	assert.Contains(t, out, `at PXF path "primary.auth_token"`)
}

// TestWrapperMethodName covers the naming convention as a pure unit so
// future emit-time changes don't accidentally break the public API
// shape. All four container kinds + nested combinations.
func TestWrapperMethodName(t *testing.T) {
	cases := []struct {
		name string
		path []pathStep
		want string
	}{
		{
			name: "top_level_scalar",
			path: []pathStep{{Name: "db_password", Container: containerScalar}},
			want: "DbPassword",
		},
		{
			name: "nested_scalar",
			path: []pathStep{
				{Name: "primary", Container: containerScalar},
				{Name: "auth_token", Container: containerScalar},
			},
			want: "PrimaryAuthToken",
		},
		{
			name: "repeated_secret_at_suffix",
			path: []pathStep{{Name: "backup_keys", Container: containerRepeated}},
			want: "BackupKeysAt",
		},
		{
			name: "map_secret_no_suffix",
			path: []pathStep{{Name: "tenant_keys", Container: containerMap}},
			want: "TenantKeys",
		},
		{
			name: "repeated_msg_with_inner_scalar_secret_at_suffix",
			path: []pathStep{
				{Name: "replicas", Container: containerRepeated},
				{Name: "auth_token", Container: containerScalar},
			},
			want: "ReplicasAuthTokenAt",
		},
		{
			name: "map_msg_with_inner_scalar_secret_no_suffix",
			path: []pathStep{
				{Name: "sites", Container: containerMap},
				{Name: "auth_token", Container: containerScalar},
			},
			want: "SitesAuthToken",
		},
		{
			name: "repeated_msg_with_inner_repeated_secret_at_suffix_once",
			path: []pathStep{
				{Name: "replicas", Container: containerRepeated},
				{Name: "backup_tokens", Container: containerRepeated},
			},
			want: "ReplicasBackupTokensAt",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, wrapperMethodName(c.path))
		})
	}
}

// TestResolved_PrefixFieldOnWrapper — every wrapper has a prefix
// field for chained-access support. Top-level construction sets it
// to "" via the NewXResolved constructor; nested wrappers set it
// when chained off a parent's method.
func TestResolved_PrefixFieldOnWrapper(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "type AppConfigResolved struct {")
	assert.Contains(t, out, "\tprefix  string")
	// Constructor sets the unexported prefix to its zero value (empty
	// string) implicitly — Go's composite-literal default. We don't
	// emit `prefix: ""` explicitly to keep the constructor body
	// minimal. Just assert the constructor body shape.
	assert.Contains(t, out, "return &AppConfigResolved{m: m, secrets: secrets}")
}

// TestResolved_ChainMethod_ScalarMessageField — for a scalar-message
// field whose target is wrapper-bearing, the parent gets a chain
// method that returns the nested wrapper with an extended prefix.
func TestResolved_ChainMethod_ScalarMessageField(t *testing.T) {
	out := runPluginWithResolved(t)
	// AppConfig.primary is a scalar Endpoint field, Endpoint has Secrets.
	assert.Contains(t, out, "func (r *AppConfigResolved) Primary() *EndpointResolved {")
	assert.Contains(t, out, "\t\tm:       r.m.GetPrimary(),")
	assert.Contains(t, out, "\t\tsecrets: r.secrets,")
	assert.Contains(t, out, "\t\tprefix:  r.prefix + \"primary.\",")
}

// TestResolved_ChainMethod_NestedThroughCluster — AppConfig has
// primary_cluster (scalar Cluster, where Cluster has Secrets only
// transitively through repeated nodes). The chain method still
// emits because Cluster is wrapper-bearing.
func TestResolved_ChainMethod_NestedThroughCluster(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) PrimaryCluster() *ClusterResolved {")
	assert.Contains(t, out, "\t\tprefix:  r.prefix + \"primary_cluster.\",")
}

// TestResolved_ChainMethod_NotEmittedForContainers — repeated /
// map<*, msg> fields do NOT get chain methods in PR1. Container
// chained access (Replicas().At(i).AuthToken()) requires accessor
// helper types and lands in a follow-up. The flat methods on the
// parent (ReplicasAuthTokenAt) still cover those paths.
func TestResolved_ChainMethod_NotEmittedForContainers(t *testing.T) {
	out := runPluginWithResolved(t)
	// `replicas` is repeated Endpoint — must NOT have a chain method.
	assert.NotContains(t, out, "func (r *AppConfigResolved) Replicas() *")
	// `sites` is map<string, Endpoint> — must NOT have a chain method.
	assert.NotContains(t, out, "func (r *AppConfigResolved) Sites() *")
	// But the flat methods must still be present for those paths.
	assert.Contains(t, out, "ReplicasAuthTokenAt")
	assert.Contains(t, out, "SitesAuthToken")
}

// TestResolved_ChainMethod_NotEmittedForSecretField — pxf.Secret is
// itself a message field. Its containing Resolved wrapper has a
// typed method already (DbPassword); a chain method here would be
// nonsense (the Secret has no inner Secrets).
func TestResolved_ChainMethod_NotEmittedForSecretField(t *testing.T) {
	out := runPluginWithResolved(t)
	// Must not emit a chain method for db_password (which is scalar
	// pxf.Secret on AppConfig). pxf.Secret is the leaf, not a
	// wrapper-bearing message in the chameleon sense.
	assert.NotContains(t, out, "func (r *AppConfigResolved) DbPassword() *Secret {")
	// The Secret access method exists and returns *secret.Secret.
	assert.Contains(t, out, "func (r *AppConfigResolved) DbPassword() *")
	assert.Contains(t, out, "r.secrets.Get(r.prefix + \"db_password\")")
}

// TestResolved_NestedWrapperMethodsUsePrefix — the EndpointResolved
// wrapper's own Secret methods reference r.prefix in the body, so
// chain access (cfg.Primary().AuthToken()) and direct construction
// (NewEndpointResolved with prefix="") produce the right paths.
func TestResolved_NestedWrapperMethodsUsePrefix(t *testing.T) {
	out := runPluginWithResolved(t)
	// EndpointResolved.AuthToken's body: r.secrets.Get(r.prefix + "auth_token")
	idx := strings.Index(out, "func (r *EndpointResolved) AuthToken()")
	require.Greater(t, idx, 0, "EndpointResolved should have an AuthToken method")
	tail := out[idx:]
	end := strings.Index(tail, "}")
	require.Greater(t, end, 0)
	body := tail[:end]
	assert.Contains(t, body, "r.secrets.Get(r.prefix + \"auth_token\")")
}

// TestMessageContainsSecret covers the small recursive helper that
// decides whether a message gets a chain method targeted at it. The
// helper is purely descriptor-driven; pure-Go tests would need a
// fixture, so we exercise it through the codegen end-to-end here.
// (Direct unit testing is constrained by protogen requiring a real
// CodeGeneratorRequest.)
func TestMessageContainsSecret_ViaCodegen(t *testing.T) {
	out := runPluginWithResolved(t)
	// Endpoint contains a Secret directly → wrapper-bearing → chain
	// method emitted on AppConfigResolved.Primary().
	assert.Contains(t, out, "Primary() *EndpointResolved")
	// Cluster contains Secrets only through repeated nodes → still
	// wrapper-bearing → chain method emitted on
	// AppConfigResolved.PrimaryCluster().
	assert.Contains(t, out, "PrimaryCluster() *ClusterResolved")
}

// TestResolved_GeneratedCodeShape — assert the wrapper block sits
// AFTER the function-form accessors in the file. Avoids surprising
// diff for consumers running `gofmt -d` on regenerated files.
func TestResolved_GeneratedCodeShape(t *testing.T) {
	out := runPluginWithResolved(t)
	idxFuncs := strings.Index(out, "func AppConfigSecret_DbPassword")
	idxWrap := strings.Index(out, "type AppConfigResolved struct")
	require.Greater(t, idxFuncs, 0)
	require.Greater(t, idxWrap, 0)
	assert.Less(t, idxFuncs, idxWrap,
		"wrapper block should come after the function-form accessors")
}
