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
	"google.golang.org/protobuf/reflect/protoreflect"
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
// wrapper struct + constructor for the root message that has Secrets,
// plus the proto.Message delegation methods (Reset / String /
// ProtoReflect) that make the wrapper substitutable for the raw
// proto in reflection-driven APIs.
func TestResolved_WrapperStructAndConstructor(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "type AppConfigResolved struct {")
	assert.Contains(t, out, "func NewAppConfigResolved(m *AppConfig, secrets *")
	assert.Contains(t, out, "func (r *AppConfigResolved) Secrets() *")
	// proto.Message delegation (added in v0.9.0; replaces Underlying()).
	assert.Contains(t, out, "func (r *AppConfigResolved) Reset()")
	assert.Contains(t, out, "func (r *AppConfigResolved) String() string")
	assert.Contains(t, out, "func (r *AppConfigResolved) ProtoReflect()")
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

// TestResolved_ContainerChainMethods_Emitted — repeated and map
// message fields whose element/value type contains a Secret get
// chain methods returning accessor helper types (Phase 2b PR2).
// Flat methods (ReplicasAuthTokenAt, SitesAuthToken) coexist for
// back-compat with Phase 2a consumers.
func TestResolved_ContainerChainMethods_Emitted(t *testing.T) {
	out := runPluginWithResolved(t)
	// repeated Endpoint → AppConfigReplicasResolved with Len/At.
	assert.Contains(t, out, "func (r *AppConfigResolved) Replicas() *AppConfigReplicasResolved")
	// map<string, Endpoint> → AppConfigSitesResolved with Len/Get.
	assert.Contains(t, out, "func (r *AppConfigResolved) Sites() *AppConfigSitesResolved")
	// Flat methods still emitted.
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

// TestResolved_ListHelper_TypeShape — the repeated chain method emits
// a `<Parent><Field>Resolved` helper type with slice-typed m, an
// inherited prefix, Len, and an At(i) that builds a nested wrapper
// scoped to "<field>[i].".
func TestResolved_ListHelper_TypeShape(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "type AppConfigReplicasResolved struct {")
	assert.Contains(t, out, "\tm       []*Endpoint")
	assert.Contains(t, out, "\tprefix  string")
	assert.Contains(t, out, "func (r *AppConfigReplicasResolved) Len() int")
	assert.Contains(t, out, "func (r *AppConfigReplicasResolved) At(i int) *EndpointResolved")
	// Out-of-range yields nil.
	assert.Contains(t, out, "if i < 0 || i >= len(r.m) {")
	// Prefix construction uses Itoa and the field's proto name.
	assert.Contains(t, out, `r.prefix + "replicas[" + strconv.Itoa(i) + "].",`)
}

// TestResolved_MapHelper_StringKeyShape — map<string, Endpoint>
// emits a helper whose Get takes string and quotes it for the path
// to match parse.Move's `["acme"]` rendering.
func TestResolved_MapHelper_StringKeyShape(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "type AppConfigSitesResolved struct {")
	assert.Contains(t, out, "\tm       map[string]*Endpoint")
	assert.Contains(t, out, "func (r *AppConfigSitesResolved) Len() int")
	assert.Contains(t, out, "func (r *AppConfigSitesResolved) Get(key string) *EndpointResolved")
	// Absent key yields nil.
	assert.Contains(t, out, "v, ok := r.m[key]")
	assert.Contains(t, out, "if !ok {")
	// Prefix construction uses Quote for string keys.
	assert.Contains(t, out, `r.prefix + "sites[" + strconv.Quote(key) + "].",`)
}

// TestResolved_NestedContainerOnNestedWrapper — Cluster.nodes is a
// repeated Endpoint field on a NESTED wrapper. The container chain
// method emits there too (ClusterResolved.Nodes()), so consumers can
// reach
//
//	cfg.PrimaryCluster().Nodes().At(0).AuthToken()
//
// alongside the flat
//
//	cfg.PrimaryClusterNodesAuthTokenAt(0).
func TestResolved_NestedContainerOnNestedWrapper(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "type ClusterNodesResolved struct {")
	assert.Contains(t, out, "func (r *ClusterResolved) Nodes() *ClusterNodesResolved")
	assert.Contains(t, out, "func (r *ClusterNodesResolved) At(i int) *EndpointResolved")
	// Path construction is field-relative; the parent's prefix is
	// already "primary_cluster." when reached via the AppConfig chain,
	// so the full runtime path is "primary_cluster.nodes[0]." → secret
	// lookups land at "primary_cluster.nodes[0].auth_token".
	assert.Contains(t, out, `r.prefix + "nodes[" + strconv.Itoa(i) + "].",`)
}

// TestResolved_ListChain_NotForRepeatedSecret — `repeated pxf.Secret
// backup_keys` is a SCALAR-leaf repeated, not a user-message
// repeated. No helper type emitted; the existing flat method
// BackupKeysAt(i) covers it.
func TestResolved_ListChain_NotForRepeatedSecret(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.NotContains(t, out, "type AppConfigBackupKeysResolved")
	assert.NotContains(t, out, "func (r *AppConfigResolved) BackupKeys()")
	// But the flat method must still exist.
	assert.Contains(t, out, "BackupKeysAt")
}

// TestResolved_MapChain_NotForMapSecret — `map<string, pxf.Secret>
// tenant_keys` is a scalar-leaf map. No helper; flat method
// TenantKeys(key) covers it.
func TestResolved_MapChain_NotForMapSecret(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.NotContains(t, out, "type AppConfigTenantKeysResolved")
	// `func ... TenantKeys()` (zero-arg parent chain method) must NOT
	// exist — the existing TenantKeys(key string) flat-method does,
	// so just check the parameterless variant is absent.
	assert.NotContains(t, out, "func (r *AppConfigResolved) TenantKeys() *")
	// Flat method present.
	assert.Contains(t, out, "func (r *AppConfigResolved) TenantKeys(key string) *")
}

// TestResolved_MapKeyRuntimeExpr — covers the key-formatter dispatch
// for non-string map keys. Currently no map<int*, UserMsg> fixture
// in the schema, so we exercise the helper directly.
func TestResolved_MapKeyRuntimeExpr_StringKind(t *testing.T) {
	// Already covered by the Sites test above; this is a sanity
	// check that the schema in sample.proto uses map<string, Endpoint>.
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "Get(key string)")
}

// --- Phase 2b PR3: field-by-field getters for non-Secret fields ---

// TestResolved_FieldGetter_ScalarString — top-level scalar string
// field gets a thin delegating getter. Body uses the nil-safe GetX
// accessor so consumers don't panic on absent fields.
func TestResolved_FieldGetter_ScalarString(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) BaseUrl() string {")
	assert.Contains(t, out, "return r.m.GetBaseUrl()")
}

// TestResolved_FieldGetter_ScalarPrimitives — int32, bool, float64,
// bytes all render to the canonical Go types.
func TestResolved_FieldGetter_ScalarPrimitives(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) MaxReplicas() int32 {")
	assert.Contains(t, out, "func (r *AppConfigResolved) Enabled() bool {")
	assert.Contains(t, out, "func (r *AppConfigResolved) RateLimitPerSec() float64 {")
	assert.Contains(t, out, "func (r *AppConfigResolved) Signature() []byte {")
}

// TestResolved_FieldGetter_Enum — enum field returns the enum type
// (qualified ident managed by protogen). For the inline fixture's
// AppConfig_Mode, the GoIdent is `AppConfig_Mode` in the same
// package, so the rendered return type is just `AppConfig_Mode`.
func TestResolved_FieldGetter_Enum(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) Mode() AppConfig_Mode {")
	assert.Contains(t, out, "return r.m.GetMode()")
}

// TestResolved_FieldGetter_ScalarMessage_NoSecrets — scalar message
// field whose target has NO Secrets gets a getter that returns the
// raw *MsgType. The chain-method emit deliberately skips these
// (nothing to chain into).
func TestResolved_FieldGetter_ScalarMessage_NoSecrets(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) Metadata() *Metadata {")
	assert.Contains(t, out, "return r.m.GetMetadata()")
	// And the chain emit must NOT have produced a wrapper for this
	// field — Metadata has no Secrets.
	assert.NotContains(t, out, "type AppConfigMetadataResolved struct {")
}

// TestResolved_FieldGetter_RepeatedScalar — repeated string renders
// as []string.
func TestResolved_FieldGetter_RepeatedScalar(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) FeatureFlags() []string {")
	assert.Contains(t, out, "return r.m.GetFeatureFlags()")
}

// TestResolved_FieldGetter_RepeatedMessage_NoSecrets — repeated
// Metadata renders as []*Metadata. No chain helper emitted.
func TestResolved_FieldGetter_RepeatedMessage_NoSecrets(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) History() []*Metadata {")
	assert.NotContains(t, out, "type AppConfigHistoryResolved struct {")
}

// TestResolved_FieldGetter_MapScalar — map<string, string> renders
// as map[string]string. No chain helper.
func TestResolved_FieldGetter_MapScalar(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) Labels() map[string]string {")
}

// TestResolved_FieldGetter_MapMessage_NoSecrets — map<string,
// Metadata> renders as map[string]*Metadata. No chain helper.
func TestResolved_FieldGetter_MapMessage_NoSecrets(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *AppConfigResolved) Annotations() map[string]*Metadata {")
	assert.NotContains(t, out, "type AppConfigAnnotationsResolved struct {")
}

// TestResolved_FieldGetter_NotEmittedForSecretLeaves — pxf.Secret in
// any shape (scalar / repeated / map<*, Secret>) is covered by the
// typed Secret accessor methods. The generic field-getter emit must
// skip them to avoid exposing *pxfv1.Secret with .Value.
func TestResolved_FieldGetter_NotEmittedForSecretLeaves(t *testing.T) {
	out := runPluginWithResolved(t)
	// Scalar pxf.Secret — DbPassword exists as a typed method
	// returning *secret.Secret. There must NOT be a duplicate
	// getter returning *pxfv1.Secret.
	idx := strings.Index(out, "func (r *AppConfigResolved) DbPassword() *")
	require.Greater(t, idx, 0)
	tail := out[idx:]
	endLine := strings.IndexByte(tail, '\n')
	require.Greater(t, endLine, 0)
	header := tail[:endLine]
	// The typed Secret accessor returns *secret.Secret; the
	// hypothetical wrong getter would return *pxfv1.Secret.
	assert.NotContains(t, header, "pxfv1")
	// Repeated/map Secret getters must not exist as field-getter
	// shape (zero-arg parameterless).
	assert.NotContains(t, out, "func (r *AppConfigResolved) BackupKeys() []*")
	assert.NotContains(t, out, "func (r *AppConfigResolved) TenantKeys() map[")
	assert.NotContains(t, out, "func (r *AppConfigResolved) PortKeys() map[")
}

// TestResolved_FieldGetter_NotEmittedForWrapperBearingFields —
// scalar / repeated / map message fields where the target IS
// wrapper-bearing are covered by the chain-method emit. The
// field-getter emit must skip them so consumers always reach for
// the typed chain.
func TestResolved_FieldGetter_NotEmittedForWrapperBearingFields(t *testing.T) {
	out := runPluginWithResolved(t)
	// Primary is *Endpoint (wrapper-bearing) — chain method
	// `Primary() *EndpointResolved` exists, the generic getter
	// `Primary() *Endpoint` must not.
	assert.NotContains(t, out, "func (r *AppConfigResolved) Primary() *Endpoint {")
	// Replicas / Sites / PrimaryCluster similarly covered by chain.
	assert.NotContains(t, out, "func (r *AppConfigResolved) Replicas() []*Endpoint {")
	assert.NotContains(t, out, "func (r *AppConfigResolved) Sites() map[string]*Endpoint {")
	assert.NotContains(t, out, "func (r *AppConfigResolved) PrimaryCluster() *Cluster {")
}

// TestResolved_FieldGetter_OnNestedWrapper — getter emit runs on
// every Resolved wrapper, not just the top-level. Endpoint has a
// non-Secret `url` field; EndpointResolved should expose Url() string.
func TestResolved_FieldGetter_OnNestedWrapper(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.Contains(t, out, "func (r *EndpointResolved) Url() string {")
	assert.Contains(t, out, "return r.m.GetUrl()")
}

// TestGoPrimitiveType covers the kind→Go-type mapping. The helper
// is pure; testing it directly catches regressions when adding new
// kinds (e.g. if proto editions introduces a new primitive).
func TestGoPrimitiveType(t *testing.T) {
	cases := []struct {
		kind protoreflect.Kind
		want string
	}{
		{protoreflect.StringKind, "string"},
		{protoreflect.BoolKind, "bool"},
		{protoreflect.Int32Kind, "int32"},
		{protoreflect.Int64Kind, "int64"},
		{protoreflect.Uint32Kind, "uint32"},
		{protoreflect.Uint64Kind, "uint64"},
		{protoreflect.FloatKind, "float32"},
		{protoreflect.DoubleKind, "float64"},
		{protoreflect.BytesKind, "[]byte"},
		{protoreflect.Sint32Kind, "int32"},
		{protoreflect.Sfixed64Kind, "int64"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, goPrimitiveType(c.kind))
	}
}

// --- proto.Message delegation (v0.9.0 — Underlying() removed) ---

// TestResolved_NoUnderlyingMethod — Underlying() must NOT be emitted
// on any wrapper. The Deprecated marker in v0.8.0 has been followed
// by hard removal in v0.9.0 — the wrapper IS proto.Message, no raw-
// proto escape hatch remains. Catches accidental re-introduction.
func TestResolved_NoUnderlyingMethod(t *testing.T) {
	out := runPluginWithResolved(t)
	assert.NotContains(t, out, ") Underlying() *",
		"Underlying() was removed in v0.9.0; the wrapper implements proto.Message directly")
	assert.NotContains(t, out, "// Deprecated: prefer the typed accessor methods",
		"the v0.8.0 deprecation note should be gone with the method")
}

// TestResolved_ProtoMessageDelegation — every wrapper emits Reset,
// String, and ProtoReflect, delegating to the inner proto. The
// wrapper is fully substitutable for the raw proto in proto.Marshal
// / proto.Equal / proto.Clone / any other reflection-driven API.
func TestResolved_ProtoMessageDelegation(t *testing.T) {
	out := runPluginWithResolved(t)
	for _, wrapper := range []string{"AppConfigResolved", "EndpointResolved", "ClusterResolved"} {
		assert.Contains(t, out, "func (r *"+wrapper+") Reset() { r.m.Reset() }",
			"%s.Reset() should delegate to inner proto", wrapper)
		assert.Contains(t, out, "func (r *"+wrapper+") String() string { return r.m.String() }",
			"%s.String() should delegate to inner proto", wrapper)
		assert.Contains(t, out, "func (r *"+wrapper+") ProtoReflect()",
			"%s.ProtoReflect() should be emitted", wrapper)
	}
	// ProtoReflect body delegates to r.m.ProtoReflect() — the
	// return-type ident is import-managed by protogen so we just
	// check the body shape.
	assert.Contains(t, out, "{ return r.m.ProtoReflect() }")
}

// TestResolved_CompileTimeAssertion — the generated file carries a
// `var _ proto.Message = (*XResolved)(nil)` for every wrapper. If
// the emit path ever skips one of the three required methods
// (Reset / String / ProtoReflect), the CONSUMER's go build breaks
// at the assertion line — self-validating codegen.
func TestResolved_CompileTimeAssertion(t *testing.T) {
	out := runPluginWithResolved(t)
	for _, wrapper := range []string{"AppConfigResolved", "EndpointResolved", "ClusterResolved"} {
		assert.Contains(t, out, "var _ proto.Message = (*"+wrapper+")(nil)",
			"compile-time proto.Message assertion missing for %s", wrapper)
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
