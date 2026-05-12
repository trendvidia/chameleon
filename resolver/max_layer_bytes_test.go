// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package resolver_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
)

// minimalChain is the shortest chain spec that loads exactly one
// layer. Reused across the WithMaxLayerBytes tests so each test
// varies only the layer payload size + the option.
func minimalChain(t *testing.T) (proto.Message, fstest.MapFS) {
	t.Helper()
	chainDesc, _ := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	return spec, fstest.MapFS{}
}

// TestWithMaxLayerBytes_DefaultAllowsTypical — no option set, the
// 10 MiB default applies, a tiny fixture loads fine.
func TestWithMaxLayerBytes_DefaultAllowsTypical(t *testing.T) {
	_, appDesc := compileAll(t)
	spec, _ := minimalChain(t)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"` + "\n")},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)))
	require.NoError(t, err)
	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()
}

// TestWithMaxLayerBytes_RejectsOversize — a custom cap below the
// payload size triggers ErrLayerTooLarge with diagnostic context.
func TestWithMaxLayerBytes_RejectsOversize(t *testing.T) {
	_, appDesc := compileAll(t)
	spec, _ := minimalChain(t)
	// 200-byte layer payload. Cap at 100 → reject.
	bigPayload := `base_url = "` + strings.Repeat("x", 180) + `"` + "\n"
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(bigPayload)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithMaxLayerBytes(100))
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrLayerTooLarge),
		"err should be errors.Is(ErrLayerTooLarge); got %v", err)
	assert.Contains(t, err.Error(), "base", "should name the layer id")
	assert.Contains(t, err.Error(), "base.pxf", "should name the layer path")
	assert.Contains(t, err.Error(), "100", "should name the active limit")
}

// TestWithMaxLayerBytes_BoundaryExact — a layer EQUAL to the cap is
// accepted; one byte over rejects. Locks in the boundary semantics.
func TestWithMaxLayerBytes_BoundaryExact(t *testing.T) {
	_, appDesc := compileAll(t)
	spec, _ := minimalChain(t)
	// Construct a payload of exactly 64 bytes.
	exact := `base_url = "` + strings.Repeat("x", 64-len(`base_url = ""`+"\n")) + `"` + "\n"
	require.Equal(t, 64, len(exact))

	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(exact)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithMaxLayerBytes(64))
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err, "payload exactly at limit should be accepted")
	res.Destroy()

	// Same payload + a single trailing newline (= 65 bytes) with cap
	// 64 must reject.
	fs["base.pxf"] = &fstest.MapFile{Data: []byte(exact + "\n")}
	r2, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithMaxLayerBytes(64))
	require.NoError(t, err)

	_, err = r2.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrLayerTooLarge))
}

// TestWithMaxLayerBytes_ZeroFallsBackToDefault — passing 0 means
// "use default" (the prior behavior); a 200-byte payload still
// loads fine since default is 10 MiB.
func TestWithMaxLayerBytes_ZeroFallsBackToDefault(t *testing.T) {
	_, appDesc := compileAll(t)
	spec, _ := minimalChain(t)
	payload := `base_url = "` + strings.Repeat("x", 180) + `"` + "\n"
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(payload)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithMaxLayerBytes(0))
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()
}

// TestWithMaxLayerBytes_NegativeErrors — a negative value is a
// programming error; New refuses it at construction.
func TestWithMaxLayerBytes_NegativeErrors(t *testing.T) {
	_, appDesc := compileAll(t)
	spec, _ := minimalChain(t)
	sample := dynamicpb.NewMessage(appDesc)
	_, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fstest.MapFS{})),
		resolver.WithMaxLayerBytes(-1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithMaxLayerBytes")
	assert.Contains(t, err.Error(), "-1")
}

// TestWithMaxLayerBytes_MaxInt64DisablesCap — a sufficiently large
// cap effectively disables the check. Documented escape hatch for
// trusted Sources with genuinely large layers.
func TestWithMaxLayerBytes_MaxInt64DisablesCap(t *testing.T) {
	_, appDesc := compileAll(t)
	spec, _ := minimalChain(t)
	payload := `base_url = "` + strings.Repeat("x", 1024) + `"` + "\n"
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(payload)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithMaxLayerBytes(math.MaxInt64))
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()
}
