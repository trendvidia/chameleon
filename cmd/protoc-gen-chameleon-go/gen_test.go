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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

const secretProtoSrc = `
syntax = "proto3";
package pxf;
option go_package = "github.com/trendvidia/protowire/proto/pxf;pxfv1";
message Secret { string value = 1; string hint = 2; string fingerprint = 3; }
`

const sampleProtoSrc = `
syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  string base_url = 1;
  pxf.Secret db_password = 2;
  Endpoint primary = 3;
  repeated pxf.Secret backup_keys = 4;
  map<string, pxf.Secret> tenant_keys = 5;
  map<int32, pxf.Secret> port_keys = 6;

  // Recursion cases — the original sidecar plugin skipped these.
  repeated Endpoint replicas = 7;             // repeated user-msg with inner Secret
  map<string, Endpoint> sites = 8;            // map<K, user-msg> with inner Secret
  Cluster primary_cluster = 9;                // nested user-msg with deeper containers

  // Non-Secret field shapes (Phase 2b PR3 — field-by-field getters).
  // These should each generate a thin pass-through getter on
  // AppConfigResolved so consumers can stop reaching for Underlying().
  int32 max_replicas = 10;                    // scalar primitive (int)
  bool enabled = 11;                          // scalar primitive (bool)
  double rate_limit_per_sec = 12;             // scalar primitive (float)
  bytes signature = 13;                       // bytes
  Mode mode = 14;                             // scalar enum
  repeated string feature_flags = 15;         // repeated primitive
  map<string, string> labels = 16;            // map<*, primitive>
  Metadata metadata = 17;                     // scalar message, no Secret inside
  repeated Metadata history = 18;             // repeated message, no Secret
  map<string, Metadata> annotations = 19;     // map<*, msg>, no Secret

  enum Mode {
    MODE_UNSPECIFIED = 0;
    MODE_FAST = 1;
    MODE_SAFE = 2;
  }
}

message Endpoint {
  string url = 1;
  pxf.Secret auth_token = 2;
  repeated pxf.Secret backup_tokens = 3;      // repeated Secret inside a repeated user-msg
}

message Cluster {
  repeated Endpoint nodes = 1;                // two-deep: AppConfig.primary_cluster.nodes[i].auth_token
}

// Metadata is intentionally Secret-free; reference points for the
// PR3 field-getter emit on non-wrapper-bearing message types.
message Metadata {
  string version = 1;
  int32 generation = 2;
}
`

// runPlugin drives the generate() function against an in-memory
// CodeGeneratorRequest built from a small set of inline protos. Returns
// the generated sidecar file content (single file expected).
func runPlugin(t *testing.T) string {
	t.Helper()

	// Compile both sources via protocompile.
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

	// Convert each protocompile File into a FileDescriptorProto for
	// the CodeGeneratorRequest. Protogen needs all transitive deps
	// included so it can resolve type references.
	var fdProtos []*descriptorpb.FileDescriptorProto
	for _, f := range files {
		fdProtos = append(fdProtos, protodesc.ToFileDescriptorProto(f))
	}
	// Standard protobuf imports (descriptor.proto etc.) need to be
	// reachable too; protocompile's WithStandardImports plus
	// RangeFiles covers this.

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

func TestGenerate_TopLevelSecret(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_DbPassword(s ")
	assert.Contains(t, out, `s.Get("db_password")`)
}

func TestGenerate_NestedSecret(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_Primary_AuthToken(s ")
	assert.Contains(t, out, `s.Get("primary.auth_token")`)
}

func TestGenerate_RepeatedSecret(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_BackupKeys(s ")
	assert.Contains(t, out, ", i int)")
	assert.Contains(t, out, `s.Get("backup_keys[" + `)
}

func TestGenerate_MapStringSecret(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_TenantKeys(s ")
	assert.Contains(t, out, ", key string)")
	// String key path uses strconv.Quote at runtime to match the
	// `["acme"]` format parse.Move emits.
	assert.Contains(t, out, "Quote(key)")
}

func TestGenerate_MapInt32Secret(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_PortKeys(s ")
	assert.Contains(t, out, ", key int32)")
	assert.Contains(t, out, "FormatInt(int64(key), 10)")
}

func TestGenerate_NoSidecarForFilesWithoutSecrets(t *testing.T) {
	// A schema with no pxf.Secret produces no sidecar file at all.
	const noSecretSrc = `
syntax = "proto3";
package other.v1;
option go_package = "example.com/other/v1;otherv1";

message Plain {
  string name = 1;
  int32 count = 2;
}
`
	sources := map[string]string{"other.proto": noSecretSrc}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "other.proto")
	require.NoError(t, err)
	var fdProtos []*descriptorpb.FileDescriptorProto
	for _, f := range files {
		fdProtos = append(fdProtos, protodesc.ToFileDescriptorProto(f))
	}
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"other.proto"},
		ProtoFile:      fdProtos,
	}
	plugin, err := protogen.Options{}.New(req)
	require.NoError(t, err)
	require.NoError(t, generate(plugin))
	resp := plugin.Response()
	assert.Empty(t, resp.GetFile(), "no sidecar should be emitted when no Secrets are present")
}

func TestGenerate_HeaderAndImports(t *testing.T) {
	out := runPlugin(t)
	assert.True(t, strings.HasPrefix(out, "// Code generated"))
	assert.Contains(t, out, "package mypkgv1")
	// Imports should be auto-managed by protogen — verify the runtime
	// secret package is referenced.
	assert.Contains(t, out, "github.com/trendvidia/chameleon/secret")
}

// --- recursion: containers of user-messages with inner Secrets ---

func TestGenerate_RepeatedUserMessageWithInnerSecret(t *testing.T) {
	out := runPlugin(t)
	// repeated Endpoint replicas; Endpoint has pxf.Secret auth_token.
	// Should emit AppConfigSecret_Replicas_AuthToken(s, i int).
	assert.Contains(t, out, "func AppConfigSecret_Replicas_AuthToken(s *")
	assert.Contains(t, out, ", i int)")
	// Path expression assembly: "replicas[" + Itoa(i) + "].auth_token"
	assert.Contains(t, out, `"replicas["`)
	assert.Contains(t, out, `Itoa(i)`)
	assert.Contains(t, out, `"].auth_token"`)
}

func TestGenerate_RepeatedUserMessageWithInnerRepeatedSecret(t *testing.T) {
	out := runPlugin(t)
	// repeated Endpoint replicas; Endpoint has repeated pxf.Secret backup_tokens.
	// Should emit AppConfigSecret_Replicas_BackupTokens(s, i, j int).
	assert.Contains(t, out, "func AppConfigSecret_Replicas_BackupTokens(s *")
	assert.Contains(t, out, "i int, j int)")
	// Path: "replicas[" + Itoa(i) + "].backup_tokens[" + Itoa(j) + "]"
	assert.Contains(t, out, `"replicas["`)
	assert.Contains(t, out, `"].backup_tokens["`)
	assert.Contains(t, out, `"]"`)
}

func TestGenerate_MapUserMessageWithInnerSecret(t *testing.T) {
	out := runPlugin(t)
	// map<string, Endpoint> sites; Endpoint has pxf.Secret auth_token.
	// Should emit AppConfigSecret_Sites_AuthToken(s, key string).
	assert.Contains(t, out, "func AppConfigSecret_Sites_AuthToken(s *")
	assert.Contains(t, out, ", key string)")
	// Path: "sites[" + Quote(key) + "].auth_token"
	assert.Contains(t, out, `"sites["`)
	assert.Contains(t, out, `Quote(key)`)
	assert.Contains(t, out, `"].auth_token"`)
}

func TestGenerate_NestedThenRepeated(t *testing.T) {
	out := runPlugin(t)
	// AppConfig.primary_cluster.nodes[i].auth_token —
	// AppConfig has Cluster primary_cluster (scalar nested).
	// Cluster has repeated Endpoint nodes (repeated user-msg).
	// Endpoint has Secret auth_token.
	// Expected: AppConfigSecret_PrimaryCluster_Nodes_AuthToken(s, i int).
	assert.Contains(t, out, "func AppConfigSecret_PrimaryCluster_Nodes_AuthToken(s *")
	assert.Contains(t, out, ", i int)")
	// Path: "primary_cluster.nodes[" + Itoa(i) + "].auth_token"
	assert.Contains(t, out, `"primary_cluster.nodes["`)
}

func TestGenerate_DocPathRendered(t *testing.T) {
	out := runPlugin(t)
	// Doc comments use the "[i]" / "[key]" markers so the path reads
	// the same way parse.Move's runtime path does.
	assert.Contains(t, out, `"replicas[i].auth_token"`)
	assert.Contains(t, out, `"sites[key].auth_token"`)
	assert.Contains(t, out, `"replicas[i].backup_tokens[j]"`)
}

// --- pure-logic units (no plugin run) ---

func TestSiteFuncName(t *testing.T) {
	cases := []struct {
		root  string
		names []string
		want  string
	}{
		{"AppConfig", []string{"db_password"}, "AppConfigSecret_DbPassword"},
		{"AppConfig", []string{"primary", "auth_token"}, "AppConfigSecret_Primary_AuthToken"},
		{"App", []string{"a_b_c"}, "AppSecret_ABC"},
		{"App", []string{"x"}, "AppSecret_X"},
	}
	for _, c := range cases {
		path := make([]pathStep, len(c.names))
		for i, n := range c.names {
			path[i] = pathStep{Name: n, Container: containerScalar}
		}
		s := secretSite{
			Root: &protogen.Message{GoIdent: protogen.GoIdent{GoName: c.root}},
			Path: path,
		}
		assert.Equal(t, c.want, siteFuncName(s))
	}
}

func TestToCamel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"db_password", "DbPassword"},
		{"foo", "Foo"},
		{"a_b_c", "ABC"},
		{"already_pascal", "AlreadyPascal"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, toCamel(c.in), "in=%q", c.in)
	}
}

// keep proto imported (used implicitly by the request construction
// helpers; lint-friendly anchor).
var _ = proto.Marshal
