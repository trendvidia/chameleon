// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package source_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/source"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// envFixtureProto exercises every shape the env source needs to
// handle: scalar string / bool / int32 / int64 / float64 / bytes,
// well-known leaves (pxf.Secret, google.protobuf.Duration), nested
// singular message (so we get a dotted path with two segments), and
// a repeated field (which env must silently ignore).
const envFixtureProto = `
syntax = "proto3";
package env_test.v1;

import "pxf/secret.proto";
import "google/protobuf/duration.proto";

message AppConfig {
  string model           = 1;
  bool   debug           = 2;
  int32  max_tokens      = 3;
  int64  big_number      = 4;
  double ratio           = 5;
  bytes  envelope_key    = 6;
  pxf.Secret api_key     = 7;
  google.protobuf.Duration timeout = 8;
  Nested nested          = 9;
  repeated string tags   = 10;
}

message Nested {
  string  inner_value    = 1;
  pxf.Secret inner_secret = 2;
}
`

const envFixtureSecretProto = `
syntax = "proto3";
package pxf;

message Secret {
  string value = 1;
}
`

// compileEnvFixture compiles the inline proto fixture and returns
// the AppConfig descriptor. Mirrors validate_test's compileApp.
func compileEnvFixture(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"app.proto":        envFixtureProto,
		"pxf/secret.proto": envFixtureSecretProto,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "app.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("env_test.v1.AppConfig")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// readAll opens an env source and parses the synthetic PXF document
// back into a dynamicpb.Message, returning it for field-by-field
// assertions. Round-trips through the same codec a real layer would.
func readAll(t *testing.T, src source.Source, desc protoreflect.MessageDescriptor) *dynamicpb.Message {
	t.Helper()
	rc, err := src.Open(context.Background(), "ignored")
	require.NoError(t, err)
	defer rc.Close()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	out := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal(body, out))
	return out
}

func TestEnvSource_NoEnvVarsEmitsEmptyMessage(t *testing.T) {
	// With nothing set, the env source must still produce a valid,
	// empty PXF document that the resolver merges as a no-op. The
	// alternative (returning ErrNotFound or an error) would make env
	// layers a special case in the chain expand path.
	desc := compileEnvFixture(t)
	src := source.Env(desc)
	msg := readAll(t, src, desc)

	gotAny := false
	msg.ProtoReflect().Range(func(_ protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		gotAny = true
		return false
	})
	assert.False(t, gotAny, "unset env should yield zero populated fields")
}

func TestEnvSource_StringField(t *testing.T) {
	desc := compileEnvFixture(t)
	t.Setenv("MODEL", "claude-sonnet-4-6")
	msg := readAll(t, source.Env(desc), desc)
	fd := desc.Fields().ByName("model")
	assert.Equal(t, "claude-sonnet-4-6", msg.ProtoReflect().Get(fd).String())
}

func TestEnvSource_PrefixStripsAndRequires(t *testing.T) {
	desc := compileEnvFixture(t)
	// Without prefix: MODEL alone matches.
	// With prefix STEWARD_: MODEL is ignored; STEWARD_MODEL matches.
	t.Setenv("MODEL", "wrong-no-prefix")
	t.Setenv("STEWARD_MODEL", "right-with-prefix")

	msg := readAll(t, source.Env(desc, source.WithPrefix("STEWARD_")), desc)
	fd := desc.Fields().ByName("model")
	assert.Equal(t, "right-with-prefix", msg.ProtoReflect().Get(fd).String())
}

func TestEnvSource_ScalarCoercion(t *testing.T) {
	desc := compileEnvFixture(t)
	t.Setenv("DEBUG", "true")
	t.Setenv("MAX_TOKENS", "8192")
	t.Setenv("BIG_NUMBER", "9007199254740993") // > 2^53; needs int64
	t.Setenv("RATIO", "0.75")
	t.Setenv("ENVELOPE_KEY", "aGVsbG8=") // base64("hello")

	msg := readAll(t, source.Env(desc), desc).ProtoReflect()

	assert.True(t, msg.Get(desc.Fields().ByName("debug")).Bool())
	assert.Equal(t, int32(8192), int32(msg.Get(desc.Fields().ByName("max_tokens")).Int()))
	assert.Equal(t, int64(9007199254740993), msg.Get(desc.Fields().ByName("big_number")).Int())
	assert.InEpsilon(t, 0.75, msg.Get(desc.Fields().ByName("ratio")).Float(), 1e-9)
	assert.Equal(t, []byte("hello"), msg.Get(desc.Fields().ByName("envelope_key")).Bytes())
}

func TestEnvSource_SecretLeaf(t *testing.T) {
	// pxf.Secret is a message but env source treats it as a scalar:
	// API_KEY → fills Secret.value with the raw string. The resolver's
	// OnSecretField hook then routes those bytes through memguard.
	desc := compileEnvFixture(t)
	t.Setenv("API_KEY", "sk-test-12345")
	msg := readAll(t, source.Env(desc), desc).ProtoReflect()

	apiKeyFd := desc.Fields().ByName("api_key")
	secret := msg.Get(apiKeyFd).Message()
	valueFd := secret.Descriptor().Fields().ByName("value")
	assert.Equal(t, "sk-test-12345", secret.Get(valueFd).String())
}

func TestEnvSource_Duration(t *testing.T) {
	desc := compileEnvFixture(t)
	t.Setenv("TIMEOUT", "120s")
	msg := readAll(t, source.Env(desc), desc).ProtoReflect()

	timeoutFd := desc.Fields().ByName("timeout")
	dur := msg.Get(timeoutFd).Message()
	seconds := dur.Get(dur.Descriptor().Fields().ByName("seconds")).Int()
	assert.Equal(t, int64(120), seconds)
}

func TestEnvSource_NestedMessageDescent(t *testing.T) {
	// Dotted path nested.inner_value → NESTED_INNER_VALUE. Confirms
	// the recursive walk handles two-segment paths and the partial
	// parent message is materialised only when a child is populated.
	desc := compileEnvFixture(t)
	t.Setenv("NESTED_INNER_VALUE", "via-env")
	t.Setenv("NESTED_INNER_SECRET", "secret-via-env")

	msg := readAll(t, source.Env(desc), desc).ProtoReflect()
	nestedFd := desc.Fields().ByName("nested")
	nested := msg.Get(nestedFd).Message()

	innerFd := nested.Descriptor().Fields().ByName("inner_value")
	assert.Equal(t, "via-env", nested.Get(innerFd).String())

	innerSecretFd := nested.Descriptor().Fields().ByName("inner_secret")
	innerSecret := nested.Get(innerSecretFd).Message()
	valueFd := innerSecret.Descriptor().Fields().ByName("value")
	assert.Equal(t, "secret-via-env", innerSecret.Get(valueFd).String())
}

func TestEnvSource_NestedNotMaterialisedWhenNoChildSet(t *testing.T) {
	// If no env var matches any field under nested.*, the env source
	// must not emit an empty nested {} block — that would write zero
	// over a higher-precedence layer's populated nested. (Empty
	// nested in PXF parses as "nested is set to its zero value,"
	// which the merger treats as a real value.)
	desc := compileEnvFixture(t)
	t.Setenv("MODEL", "something") // populate a sibling so the doc isn't empty

	msg := readAll(t, source.Env(desc), desc).ProtoReflect()
	nestedFd := desc.Fields().ByName("nested")
	assert.False(t, msg.Has(nestedFd), "nested must not appear when no env contributed to it")
}

func TestEnvSource_RepeatedFieldIgnored(t *testing.T) {
	// Repeated fields are out of scope for v1 — env vars are flat.
	// The walker must skip them without erroring.
	desc := compileEnvFixture(t)
	t.Setenv("TAGS", "this-should-not-land")

	msg := readAll(t, source.Env(desc), desc).ProtoReflect()
	tagsFd := desc.Fields().ByName("tags")
	assert.Equal(t, 0, msg.Get(tagsFd).List().Len())
}

func TestEnvSource_CoercionFailureLoud(t *testing.T) {
	// A garbage value for a typed field must surface at Open time,
	// not silently drop the override. Silent drops hide typos until
	// the misconfigured field is actually consumed downstream.
	cases := []struct {
		envVar string
		envVal string
		field  string // for error-substring assertion
	}{
		{"DEBUG", "not-a-bool", "debug"},
		{"MAX_TOKENS", "abc", "max_tokens"},
		{"BIG_NUMBER", "1.5", "big_number"},
		{"ENVELOPE_KEY", "not-valid-base64!!!", "envelope_key"},
		{"TIMEOUT", "ten-seconds", "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.envVar, func(t *testing.T) {
			desc := compileEnvFixture(t)
			t.Setenv(tc.envVar, tc.envVal)
			_, err := source.Env(desc).Open(context.Background(), "ignored")
			require.Error(t, err, "expected loud failure for %s=%q", tc.envVar, tc.envVal)
			assert.Contains(t, err.Error(), tc.field, "error should cite the field path")
		})
	}
}

func TestEnvSource_StatAlwaysExists(t *testing.T) {
	desc := compileEnvFixture(t)
	info, err := source.Env(desc).Stat(context.Background(), "ignored")
	require.NoError(t, err)
	assert.True(t, info.Exists, "env source is conceptually always present")
}
