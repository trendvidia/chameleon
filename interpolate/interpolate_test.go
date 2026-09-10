// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package interpolate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/interpolate"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const secretProtoSrc = `
syntax = "proto3";
package pxf;
message Secret { string value = 1; string hint = 2; string fingerprint = 3; }
`

const appProtoSrc = `
syntax = "proto3";
package interp_test.v1;

import "pxf/secret.proto";

message AppConfig {
  string base_url = 1;
  string api_url = 2;
  string admin_url = 3;
  Endpoint primary = 4;
  repeated string mirror_urls = 5;
  map<string, string> labels = 6;
  pxf.Secret db_password = 7;
}

message Endpoint {
  string host = 1;
  string url = 2;
}
`

func compileApp(tb testing.TB) protoreflect.MessageDescriptor {
	tb.Helper()
	sources := map[string]string{
		"app.proto":        appProtoSrc,
		"pxf/secret.proto": secretProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "app.proto")
	require.NoError(tb, err)
	d, err := files.AsResolver().FindDescriptorByName("interp_test.v1.AppConfig")
	require.NoError(tb, err)
	return d.(protoreflect.MessageDescriptor)
}

func parse(tb testing.TB, desc protoreflect.MessageDescriptor, input string) proto.Message {
	tb.Helper()
	msg := dynamicpb.NewMessage(desc)
	require.NoError(tb, pxf.Unmarshal([]byte(input), msg))
	return msg
}

func get(t *testing.T, msg proto.Message, path ...string) string {
	t.Helper()
	cur := msg.ProtoReflect()
	for i, name := range path {
		fd := cur.Descriptor().Fields().ByName(protoreflect.Name(name))
		require.NotNil(t, fd)
		if i == len(path)-1 {
			return cur.Get(fd).String()
		}
		cur = cur.Get(fd).Message()
	}
	return ""
}

// --- happy paths ---

func TestApply_BasicSubstitution(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "mybigproject.com"
api_url = "api.${base_url}"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "mybigproject.com", get(t, msg, "base_url"))
	assert.Equal(t, "api.mybigproject.com", get(t, msg, "api_url"))
}

func TestApply_TheUsersOriginalExample(t *testing.T) {
	desc := compileApp(t)
	// "production base url is mybigproject.com but for dev it is
	// dev.mybigproject.com" — solved by overriding base_url and
	// letting api_url's template re-resolve.
	msg := parse(t, desc, `
base_url = "dev.mybigproject.com"
api_url = "api.${base_url}"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "api.dev.mybigproject.com", get(t, msg, "api_url"))
}

func TestApply_MultipleReferencesInOneString(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "mybigproject.com"
api_url = "https://${base_url}/v1/${admin_url}"
admin_url = "admin"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "https://mybigproject.com/v1/admin", get(t, msg, "api_url"))
}

func TestApply_TransitiveReference(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "mybigproject.com"
api_url = "api.${base_url}"
admin_url = "admin.${api_url}"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "admin.api.mybigproject.com", get(t, msg, "admin_url"))
}

func TestApply_NestedMessageReference(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
primary {
  host = "api.example.com"
  url = "https://${primary.host}/v1"
}
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "https://api.example.com/v1", get(t, msg, "primary", "url"))
}

func TestApply_TemplateInRepeatedField(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "mybigproject.com"
mirror_urls = ["a.${base_url}", "b.${base_url}"]
`)
	require.NoError(t, interpolate.Apply(msg))

	r := msg.ProtoReflect()
	fd := r.Descriptor().Fields().ByName("mirror_urls")
	list := r.Get(fd).List()
	require.Equal(t, 2, list.Len())
	assert.Equal(t, "a.mybigproject.com", list.Get(0).String())
	assert.Equal(t, "b.mybigproject.com", list.Get(1).String())
}

func TestApply_TemplateInMapValue(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "mybigproject.com"
labels = {
  "primary": "https://${base_url}"
  "static": "no-template-here"
}
`)
	require.NoError(t, interpolate.Apply(msg))

	r := msg.ProtoReflect()
	fd := r.Descriptor().Fields().ByName("labels")
	m := r.Get(fd).Map()
	assert.Equal(t, "https://mybigproject.com",
		m.Get(protoreflect.ValueOfString("primary").MapKey()).String())
	assert.Equal(t, "no-template-here",
		m.Get(protoreflect.ValueOfString("static").MapKey()).String())
}

func TestApply_NoTemplatesIsNoOp(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "mybigproject.com"
api_url = "api.mybigproject.com"
`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "mybigproject.com", get(t, msg, "base_url"))
	assert.Equal(t, "api.mybigproject.com", get(t, msg, "api_url"))
}

func TestApply_EmptyFieldResolvesToEmptyString(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `api_url = "prefix-${admin_url}-suffix"`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "prefix--suffix", get(t, msg, "api_url"))
}

func TestApply_LiteralDollarSignSurvives(t *testing.T) {
	desc := compileApp(t)
	// PXF preserves the bytes verbatim. "$5.99" has no "${" so it's
	// a no-op for the interpolator.
	msg := parse(t, desc, `base_url = "$5.99 special"`)
	require.NoError(t, interpolate.Apply(msg))
	assert.Equal(t, "$5.99 special", get(t, msg, "base_url"))
}

// --- secrets are skipped ---

func TestApply_SecretValueNotInterpolated(t *testing.T) {
	desc := compileApp(t)
	// db_password.value contains a string that LOOKS like a template,
	// but since we skip WKT subtrees, it stays literal.
	msg := parse(t, desc, `
base_url = "mybigproject.com"
db_password {
  value = "literal-${base_url}"
}
`)
	require.NoError(t, interpolate.Apply(msg))
	pwField := desc.Fields().ByName("db_password")
	pwMsg := msg.ProtoReflect().Get(pwField).Message()
	assert.Equal(t, "literal-${base_url}",
		pwMsg.Get(pwMsg.Descriptor().Fields().ByName("value")).String(),
		"interpolator must not touch pxf.Secret.value")
}

func TestApply_CannotReferenceSecretValue(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
db_password { value = "shh" }
api_url = "auth=${db_password.value}"
`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrInvalidReference),
		"references through pxf.Secret must be rejected")
}

// --- error paths ---

func TestApply_CycleDetected(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "${api_url}"
api_url = "${base_url}"
`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrCycle))
}

func TestApply_SelfReferenceIsCycle(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `base_url = "${base_url}"`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrCycle))
}

func TestApply_LongerCycleDetected(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `
base_url = "${api_url}"
api_url = "${admin_url}"
admin_url = "${base_url}"
`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrCycle))
}

func TestApply_UnknownReferenceErrors(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `api_url = "${nonexistent_field}"`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrUnknownReference))
}

func TestApply_MalformedTemplateErrors(t *testing.T) {
	desc := compileApp(t)
	msg := parse(t, desc, `api_url = "broken: ${unclosed"`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrMalformedTemplate))
}

func TestApply_DescendIntoNonMessageErrors(t *testing.T) {
	desc := compileApp(t)
	// base_url is a string scalar; can't descend into it.
	msg := parse(t, desc, `
base_url = "x"
api_url = "${base_url.nope}"
`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrInvalidReference))
}

func TestApply_TerminatingOnNonStringErrors(t *testing.T) {
	// Reference a message-typed field as a leaf (admin_url chain
	// resolves to a Endpoint, which isn't a string).
	desc := compileApp(t)
	msg := parse(t, desc, `
primary { host = "x" url = "y" }
api_url = "${primary}"
`)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrInvalidReference))
}

// --- nil ---

func TestApply_NilMessage(t *testing.T) {
	err := interpolate.Apply(nil)
	require.Error(t, err)
}
