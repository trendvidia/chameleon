// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// runPluginOnSource drives generate() against a caller-provided proto
// source. Returns whatever generate() reported via the plugin
// response — including the error string the protogen runtime
// surfaces when generate() returns an error. The caller asserts on
// either resp.GetError() (for the error path) or resp.GetFile() (for
// the success path).
func runPluginOnSource(t *testing.T, protoSrc string, withResolved bool) *pluginpb.CodeGeneratorResponse {
	t.Helper()
	prev := pluginState.withResolved
	pluginState.withResolved = withResolved
	t.Cleanup(func() { pluginState.withResolved = prev })

	sources := map[string]string{
		"sample.proto":     protoSrc,
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

	// generate() returns an error; protogen routes it onto the
	// response's Error field. We surface both — the caller decides.
	if genErr := generate(plugin); genErr != nil {
		// Mimic protogen's ParamFunc behavior — when generate
		// returns, protogen would set Error on Response. Do the same
		// so existing test patterns keep working.
		errStr := genErr.Error()
		return &pluginpb.CodeGeneratorResponse{Error: &errStr}
	}
	return plugin.Response()
}

// TestReservedNameCollision_FieldNamedSecrets — a user proto with a
// non-Secret field literally named "secrets" generates a
// `Secrets() string` field getter on the wrapper, which would
// shadow the wrapper's `Secrets() *secret.Map` method and fail at
// the consumer's go build. Validate that the plugin fails fast
// with a clear error.
func TestReservedNameCollision_FieldNamedSecrets(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret db_password = 1;
  string secrets = 2;
}
`
	resp := runPluginOnSource(t, src, true /*withResolved*/)
	require.NotEmpty(t, resp.GetError(), "plugin must error on reserved-name collision")
	assert.Contains(t, resp.GetError(), "Secrets",
		"error should mention the colliding method name")
	assert.Contains(t, resp.GetError(), "AppConfig",
		"error should mention the offending message")
}

// TestReservedNameCollision_ResetAndStringHandledByProtogen — fields
// named `reset` and `string` ARE risks for the wrapper's
// proto.Message methods, but protogen pre-mangles their GoName to
// `Reset_` / `String_` (it knows about the proto.Message interface).
// Regression guard: if a future protogen version stops mangling
// those, our validator needs to extend its FromGoName reserved set.
func TestReservedNameCollision_ResetAndStringHandledByProtogen(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret pw = 1;
  bool reset = 2;
  string string = 3;
}
`
	resp := runPluginOnSource(t, src, true)
	require.Empty(t, resp.GetError(),
		"protogen mangles `reset`/`string` GoNames; no collision should fire. "+
			"If this test fails, protogen behavior changed — extend "+
			"reservedWrapperMethodNames_FromGoName to cover Reset / String.")
}

// TestReservedNameCollision_SecretNamedReset — a pxf.Secret literally
// named `reset` produces wrapperMethodName="Reset" via the
// proto-path-based emit (NOT through protogen's mangling). This
// would collide with the wrapper's Reset() proto.Message
// delegation. The validator's FromPath reserved set must catch it.
func TestReservedNameCollision_SecretNamedReset(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret reset = 1;
}
`
	resp := runPluginOnSource(t, src, true)
	require.NotEmpty(t, resp.GetError())
	assert.Contains(t, resp.GetError(), "Reset")
	assert.Contains(t, resp.GetError(), "proto.Message")
}

// TestReservedNameCollision_SecretNamedString — same as the Reset
// case but for the String() proto.Message method.
func TestReservedNameCollision_SecretNamedString(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret string = 1;
}
`
	resp := runPluginOnSource(t, src, true)
	require.NotEmpty(t, resp.GetError())
	assert.Contains(t, resp.GetError(), "String")
}

// TestReservedNameCollision_FieldNamedProtoReflect — `proto_reflect`
// PascalCases to "ProtoReflect", colliding with the wrapper's
// ProtoReflect() proto.Message method.
func TestReservedNameCollision_FieldNamedProtoReflect(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret pw = 1;
  string proto_reflect = 2;
}
`
	resp := runPluginOnSource(t, src, true)
	require.NotEmpty(t, resp.GetError())
	assert.Contains(t, resp.GetError(), "ProtoReflect")
}

// TestReservedNameCollision_SecretPathEqualsReserved — a pxf.Secret
// at path "secrets" (PascalCased → "Secrets") collides with the
// wrapper's Secrets() accessor. Covers the Secret-method emit
// branch, distinct from the field-getter branch in earlier tests.
func TestReservedNameCollision_SecretPathEqualsReserved(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret secrets = 1;
}
`
	resp := runPluginOnSource(t, src, true)
	require.NotEmpty(t, resp.GetError())
	assert.Contains(t, resp.GetError(), "Secrets")
}

// TestReservedNameCollision_DoesNotFireWithoutResolved — without
// `with_resolved=true`, the wrapper isn't emitted and the
// reserved-name set doesn't apply. The function-form accessors
// can use any path/field name. Regression guard against the
// validator firing on the non-wrapper path.
func TestReservedNameCollision_DoesNotFireWithoutResolved(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret pw = 1;
  string secrets = 2;
  bool reset = 3;
}
`
	resp := runPluginOnSource(t, src, false /*withResolved=off*/)
	require.Empty(t, resp.GetError(),
		"reserved-name check applies only to with_resolved mode; got error: %s",
		resp.GetError())
	require.Len(t, resp.GetFile(), 1, "function-form sidecar should still emit")
}

// TestReservedNameCollision_PluginErrorMentionsRemediation — the
// error message should tell the user what to do (rename or opt
// out), not just describe the collision.
func TestReservedNameCollision_PluginErrorMentionsRemediation(t *testing.T) {
	src := `syntax = "proto3";
package mypkg.v1;
option go_package = "example.com/myrepo/mypkg/v1;mypkgv1";

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret pw = 1;
  string secrets = 2;
}
`
	resp := runPluginOnSource(t, src, true)
	errStr := resp.GetError()
	require.NotEmpty(t, errStr)
	// User-facing remediation: rename or opt out.
	assert.Contains(t, errStr, "rename")
	assert.Contains(t, errStr, "with_resolved")
}

// Sanity: ensure the unit test file compiles even when none of the
// imports are touched by the test bodies above.
var _ = fmt.Errorf
