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
// a no-param method named after the PascalCase path.
func TestResolved_TopLevelScalarMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) DbPassword() *")
	assert.Contains(t, out, `r.secrets.Get("db_password")`)
}

// TestResolved_NestedScalarMethod — nested Secret yields a PascalCase
// dotted method name and the dotted path body.
func TestResolved_NestedScalarMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) PrimaryAuthToken() *")
	assert.Contains(t, out, `r.secrets.Get("primary.auth_token")`)
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
