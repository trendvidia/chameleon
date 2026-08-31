// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package validate_test

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

	"github.com/trendvidia/chameleon/defaults"
	"github.com/trendvidia/chameleon/validate"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

const annotationsProtoSrc = `
syntax = "proto3";
package pxf;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FieldOptions {
  bool required = 1314;
  string default = 1315;
}
`

const appProtoSrc = `
syntax = "proto3";
package val_test.v1;

import "pxf/annotations.proto";

message AppConfig {
  string base_url = 1 [(pxf.required) = true];
  string api_key = 2 [(pxf.required) = true];
  string optional_thing = 3;
  string defaulted_required = 4 [(pxf.required) = true, (pxf.default) = "fallback"];
  Endpoint primary = 5 [(pxf.required) = true];
  Endpoint optional_endpoint = 6;
}

message Endpoint {
  string host = 1 [(pxf.required) = true];
  int32 port = 2;
}
`

func compileApp(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"app.proto":             appProtoSrc,
		"pxf/annotations.proto": annotationsProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "app.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("val_test.v1.AppConfig")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

func parseInput(t *testing.T, desc protoreflect.MessageDescriptor, input string) (proto.Message, map[string]struct{}) {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	present := make(map[string]struct{})
	for _, p := range res.PresentFields() {
		present[p] = struct{}{}
	}
	return msg, present
}

// --- happy paths ---

func TestValidate_AllRequiredPresent(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, `
base_url = "x"
api_key = "k"
defaulted_required = "y"
primary {
  host = "h"
}
`)
	require.NoError(t, validate.Validate(msg, present))
}

func TestValidate_DefaultSatisfiesRequired(t *testing.T) {
	desc := compileApp(t)
	// defaulted_required has both (pxf.required) and (pxf.default).
	// No layer sets it. Defaults pass fills it and marks present.
	// Validate must accept.
	msg, present := parseInput(t, desc, `
base_url = "x"
api_key = "k"
primary {
  host = "h"
}
`)
	require.NoError(t, defaults.Apply(msg, present))
	require.NoError(t, validate.Validate(msg, present),
		"a (pxf.default) on a (pxf.required) field satisfies the requirement")
}

// --- error paths ---

func TestValidate_MissingTopLevelRequired(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, `
api_key = "k"
defaulted_required = "y"
primary { host = "h" }
`) // base_url missing

	err := validate.Validate(msg, present)
	require.Error(t, err)
	assert.True(t, errors.Is(err, validate.ErrMissingRequired))
	assert.Contains(t, err.Error(), "base_url")
}

func TestValidate_MissingMultipleRequiredListed(t *testing.T) {
	desc := compileApp(t)
	msg, present := parseInput(t, desc, ``) // everything missing

	err := validate.Validate(msg, present)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key")
	assert.Contains(t, err.Error(), "base_url")
	assert.Contains(t, err.Error(), "defaulted_required")
	assert.Contains(t, err.Error(), "primary")
}

func TestValidate_NestedRequiredCheckedWhenParentLive(t *testing.T) {
	desc := compileApp(t)
	// primary is live (has port set) but its required `host` is missing.
	msg, present := parseInput(t, desc, `
base_url = "x"
api_key = "k"
defaulted_required = "y"
primary {
  port = 80
}
`)

	err := validate.Validate(msg, present)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary.host")
}

func TestValidate_NestedRequiredSkippedWhenParentAbsent(t *testing.T) {
	desc := compileApp(t)
	// optional_endpoint is never set; its inner host (required) is not
	// checked because the parent is absent. Top-level primary is the
	// one we have to set to make the test pass at all.
	msg, present := parseInput(t, desc, `
base_url = "x"
api_key = "k"
defaulted_required = "y"
primary { host = "h" }
`)

	require.NoError(t, validate.Validate(msg, present),
		"optional_endpoint.host required check skipped because optional_endpoint is absent")
}

func TestValidate_RequiredNestedMessageItself(t *testing.T) {
	desc := compileApp(t)
	// primary is required as a whole (annotation on the field, not on
	// inner host). Without it, top-level "primary" is missing.
	msg, present := parseInput(t, desc, `
base_url = "x"
api_key = "k"
defaulted_required = "y"
`)

	err := validate.Validate(msg, present)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary")
}

func TestValidate_NilMessage(t *testing.T) {
	require.ErrorContains(t, validate.Validate(nil, nil), "nil message")
}
