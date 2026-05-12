// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package interpolate_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/interpolate"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// chainProtoSource builds a proto file source whose message has n
// string fields named f0 ... f(n-1). Used by the depth-limit tests
// to construct linear `${...}` chains of arbitrary depth without
// hand-rolling 65 field declarations.
func chainProtoSource(n int) string {
	var b strings.Builder
	b.WriteString(`syntax = "proto3";
package depth_test.v1;
message Chain {
`)
	for i := range n {
		fmt.Fprintf(&b, "  string f%d = %d;\n", i, i+1)
	}
	b.WriteString("}\n")
	return b.String()
}

// compileChain compiles chainProtoSource(n) and returns the
// `Chain` MessageDescriptor.
func compileChain(t *testing.T, n int) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"chain.proto": chainProtoSource(n),
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(sources)},
		),
	}
	files, err := comp.Compile(context.Background(), "chain.proto")
	require.NoError(t, err)
	d, err := files.AsResolver().FindDescriptorByName("depth_test.v1.Chain")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// buildLinearChain produces PXF source where f0 = "${f1}", ...,
// f(n-2) = "${f(n-1)}", f(n-1) = "leaf". Resolving f0 walks the
// full chain.
func buildLinearChain(n int) string {
	var b strings.Builder
	for i := range n - 1 {
		fmt.Fprintf(&b, "f%d = \"${f%d}\"\n", i, i+1)
	}
	fmt.Fprintf(&b, "f%d = \"leaf\"\n", n-1)
	return b.String()
}

// parseChain compiles a Chain proto with `n` fields, decodes the
// linear-chain PXF, and returns the message ready for interpolate.
func parseChain(t *testing.T, n int) proto.Message {
	t.Helper()
	desc := compileChain(t, n)
	msg := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(buildLinearChain(n)), msg))
	return msg
}

// TestInterpolate_DepthBelowLimit — a chain of length 50 (well under
// MaxResolveDepth=64) resolves cleanly. The leaf value propagates
// up the chain.
func TestInterpolate_DepthBelowLimit(t *testing.T) {
	msg := parseChain(t, 50)
	require.NoError(t, interpolate.Apply(msg))

	fd := msg.ProtoReflect().Descriptor().Fields().ByName("f0")
	assert.Equal(t, "leaf", msg.ProtoReflect().Get(fd).String(),
		"50-deep chain should resolve all the way to leaf")
}

// TestInterpolate_DepthAtLimitBoundary locks in the exact threshold.
//
// Chain mechanics: with n fields f0..f(n-1), f0→f1→…→f(n-2)="${f(n-1)}",
// f(n-1)="leaf". interpolate.Apply enters via walker.field on f0,
// calling expand(value, nil); the initial resolve sees stack=[]. Each
// recursive resolve grows the stack by one. The terminal leaf field
// is reached when stack length equals n-2 (resolves count from f1 to
// f(n-1), inclusive of the leaf). The check `len(stack) >= 64`
// triggers when that stack length first hits 64 — which is n-1 = 64
// for the first failure, i.e. n = 65 still succeeds (max stack = 63
// at the leaf), n = 66 trips on resolve(f65) with stack len 64.
func TestInterpolate_DepthAtLimitBoundary(t *testing.T) {
	t.Run("n_65_succeeds_at_boundary", func(t *testing.T) {
		msg := parseChain(t, 65)
		err := interpolate.Apply(msg)
		require.NoError(t, err, "chain of n=65 max-stack=63 should be under MaxResolveDepth=64")
	})
	t.Run("n_66_trips_just_over", func(t *testing.T) {
		msg := parseChain(t, 66)
		err := interpolate.Apply(msg)
		require.Error(t, err)
		assert.True(t, errors.Is(err, interpolate.ErrDepthExceeded),
			"chain of n=66 max-stack=64 should be errors.Is(ErrDepthExceeded); got %v", err)
		assert.NotErrorIs(t, err, interpolate.ErrCycle,
			"depth-exceeded is a SEPARATE failure mode from cycles")
	})
}

// TestInterpolate_DepthExceededDiagnostic — the error wraps the
// limit and the chain prefix so consumers can identify which chain
// blew up. Note: the initial walker.field call passes stack=nil,
// so f0 (the entry field) does NOT appear in the error trace;
// only f1 through f(N-1) — the ones added during recursive
// resolve — do.
func TestInterpolate_DepthExceededDiagnostic(t *testing.T) {
	msg := parseChain(t, 100)
	err := interpolate.Apply(msg)
	require.Error(t, err)
	require.True(t, errors.Is(err, interpolate.ErrDepthExceeded))

	errStr := err.Error()
	assert.Contains(t, errStr, "limit 64", "error should name MaxResolveDepth")
	assert.Contains(t, errStr, "64 frames", "error should name how many frames the chain reached")
	// Verify a representative on-stack field appears so consumers
	// can correlate against their schema.
	assert.Contains(t, errStr, "f1")
	assert.Contains(t, errStr, "f63")
	assert.Contains(t, errStr, "f65", "the path that tripped the limit should appear")
}

// TestInterpolate_DepthLimitExposedAsConst — sanity check that the
// public MaxResolveDepth constant is what we documented.
func TestInterpolate_DepthLimitExposedAsConst(t *testing.T) {
	assert.Equal(t, 64, interpolate.MaxResolveDepth,
		"MaxResolveDepth is part of the public API; bumping requires a CHANGELOG note")
}

// TestInterpolate_CycleAndDepthAreDistinct — a true cycle (f0 → f1
// → f0) trips ErrCycle, NOT ErrDepthExceeded. Lock in the
// precedence: cycle detection runs after the depth check in
// resolve, but the cycle case keeps the stack short so the depth
// check doesn't fire first.
func TestInterpolate_CycleAndDepthAreDistinct(t *testing.T) {
	desc := compileChain(t, 2) // fields f0, f1
	msg := dynamicpb.NewMessage(desc)
	require.NoError(t, pxf.Unmarshal([]byte(`f0 = "${f1}"
f1 = "${f0}"
`), msg))

	err := interpolate.Apply(msg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, interpolate.ErrCycle))
	assert.False(t, errors.Is(err, interpolate.ErrDepthExceeded),
		"a 2-cycle should NOT trip the depth limit (stack stays at len 2)")
}
