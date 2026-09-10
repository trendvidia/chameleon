// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package parse_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/parse"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// secretProtoSrc mirrors trendvidia/protowire @ proto/pxf/secret.proto.
// Compiled inline to keep this test free of cross-repo proto-resolver
// configuration.
const secretProtoSrc = `
syntax = "proto3";
package pxf;
message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`

const appProtoSrc = `
syntax = "proto3";
package app.v1;

import "pxf/secret.proto";

message AppConfig {
  string base_url = 1;
  pxf.Secret db_password = 2;
  Endpoint primary = 3;
  repeated Endpoint replicas = 4;
  map<string, pxf.Secret> tenant_keys = 5;
  repeated pxf.Secret backup_keys = 6;
}

message Endpoint {
  string url = 1;
  pxf.Secret auth_token = 2;
}
`

func compileApp(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"test.proto":       appProtoSrc,
		"pxf/secret.proto": secretProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(sources),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "test.proto")
	require.NoError(t, err)
	d, err := result.AsResolver().FindDescriptorByName("app.v1.AppConfig")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// readField is a test helper to peek at a (possibly cleared) inner
// `value` of a pxf.Secret field after Move.
func readSecretValue(t *testing.T, msg protoreflect.Message, path ...string) string {
	t.Helper()
	cur := msg
	for i, name := range path {
		fd := cur.Descriptor().Fields().ByName(protoreflect.Name(name))
		require.NotNil(t, fd, "field %q not found at depth %d", name, i)
		cur = cur.Get(fd).Message()
	}
	return cur.Get(cur.Descriptor().Fields().ByName("value")).String()
}

// --- core cases ---

func TestMove_TopLevelSecret(t *testing.T) {
	desc := compileApp(t)
	input := `
base_url = "example.com"
db_password = "supersecret"
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	secrets, err := parse.Move(msg)
	require.NoError(t, err)
	defer secrets.Destroy()

	require.Equal(t, 1, secrets.Len())
	s := secrets.Get("db_password")
	require.NotNil(t, s)

	buf, err := s.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "supersecret", string(buf.Bytes()))

	// value cleared on the proto message.
	assert.Equal(t, "", readSecretValue(t, msg.ProtoReflect(), "db_password"))
}

func TestMove_NestedSecret(t *testing.T) {
	desc := compileApp(t)
	input := `
primary {
  url = "https://api.example.com"
  auth_token = "tok-primary"
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	secrets, err := parse.Move(msg)
	require.NoError(t, err)
	defer secrets.Destroy()

	s := secrets.Get("primary.auth_token")
	require.NotNil(t, s)
	buf, err := s.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "tok-primary", string(buf.Bytes()))
}

func TestMove_RepeatedNestedSecret(t *testing.T) {
	desc := compileApp(t)
	input := `
replicas = [
  {
    url = "u1"
    auth_token = "t1"
  }
  {
    url = "u2"
    auth_token = "t2"
  }
]
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	secrets, err := parse.Move(msg)
	require.NoError(t, err)
	defer secrets.Destroy()

	require.Equal(t, 2, secrets.Len())
	for i, want := range []string{"t1", "t2"} {
		path := []string{"replicas[0].auth_token", "replicas[1].auth_token"}[i]
		s := secrets.Get(path)
		require.NotNil(t, s, "path %q missing", path)
		buf, err := s.Open()
		require.NoError(t, err)
		assert.Equal(t, want, string(buf.Bytes()))
		buf.Destroy()
	}
}

func TestMove_RepeatedSecretShorthand(t *testing.T) {
	desc := compileApp(t)
	// repeated pxf.Secret accepts the scalar shorthand thanks to the
	// PXF codec patch we landed earlier.
	input := `
backup_keys = [
  "key-a",
  "key-b"
]
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	secrets, err := parse.Move(msg)
	require.NoError(t, err)
	defer secrets.Destroy()

	require.Equal(t, 2, secrets.Len())
	for i, want := range []string{"key-a", "key-b"} {
		s := secrets.Get([]string{"backup_keys[0]", "backup_keys[1]"}[i])
		require.NotNil(t, s)
		buf, err := s.Open()
		require.NoError(t, err)
		assert.Equal(t, want, string(buf.Bytes()))
		buf.Destroy()
	}
}

func TestMove_MapValueSecret(t *testing.T) {
	desc := compileApp(t)
	input := `
tenant_keys = {
  "acme": "key-a"
  "globex": "key-g"
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	secrets, err := parse.Move(msg)
	require.NoError(t, err)
	defer secrets.Destroy()

	require.Equal(t, 2, secrets.Len())
	sa := secrets.Get(`tenant_keys["acme"]`)
	sg := secrets.Get(`tenant_keys["globex"]`)
	require.NotNil(t, sa)
	require.NotNil(t, sg)

	bufA, err := sa.Open()
	require.NoError(t, err)
	defer bufA.Destroy()
	assert.Equal(t, "key-a", string(bufA.Bytes()))

	bufG, err := sg.Open()
	require.NoError(t, err)
	defer bufG.Destroy()
	assert.Equal(t, "key-g", string(bufG.Bytes()))
}

func TestMove_HintAndFingerprintPreserved(t *testing.T) {
	desc := compileApp(t)
	input := `
db_password {
  value = "x"
  hint = "Postgres primary"
  fingerprint = "sha256:abc"
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	secrets, err := parse.Move(msg)
	require.NoError(t, err)
	defer secrets.Destroy()

	s := secrets.Get("db_password")
	require.NotNil(t, s)
	assert.Equal(t, "Postgres primary", s.Hint())
	assert.Equal(t, "sha256:abc", s.Fingerprint())

	// On the cleared proto message: value gone, metadata preserved.
	pwMsg := msg.ProtoReflect().Get(desc.Fields().ByName("db_password")).Message()
	pd := pwMsg.Descriptor()
	assert.Equal(t, "", pwMsg.Get(pd.Fields().ByName("value")).String())
	assert.Equal(t, "Postgres primary", pwMsg.Get(pd.Fields().ByName("hint")).String())
	assert.Equal(t, "sha256:abc", pwMsg.Get(pd.Fields().ByName("fingerprint")).String())
}

func TestMove_Idempotent(t *testing.T) {
	desc := compileApp(t)
	input := `db_password = "x"`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	first, err := parse.Move(msg)
	require.NoError(t, err)
	defer first.Destroy()
	require.Equal(t, 1, first.Len())

	second, err := parse.Move(msg)
	require.NoError(t, err)
	require.Equal(t, 0, second.Len(), "second pass should find nothing — the first cleared all values")
}

func TestMove_NilMessage(t *testing.T) {
	secrets, err := parse.Move(nil)
	require.NoError(t, err)
	assert.Nil(t, secrets)
}

func TestMove_EmptySecretFieldNotMaterialized(t *testing.T) {
	// A pxf.Secret block with empty value (e.g. only hint set) should
	// not produce a Map entry — there's no plaintext to protect.
	desc := compileApp(t)
	input := `
db_password {
  hint = "placeholder"
}
`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	secrets, err := parse.Move(msg)
	require.NoError(t, err)
	defer secrets.Destroy()

	assert.Equal(t, 0, secrets.Len())
	// hint is preserved on the proto message either way.
	assert.Equal(t, "placeholder", readSecretHint(t, msg.ProtoReflect(), "db_password"))
}

// --- helper ---

func readSecretHint(t *testing.T, msg protoreflect.Message, path ...string) string {
	t.Helper()
	cur := msg
	for _, name := range path {
		fd := cur.Descriptor().Fields().ByName(protoreflect.Name(name))
		require.NotNil(t, fd)
		cur = cur.Get(fd).Message()
	}
	return cur.Get(cur.Descriptor().Fields().ByName("hint")).String()
}
