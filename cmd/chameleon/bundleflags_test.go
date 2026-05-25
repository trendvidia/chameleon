// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/internal/bundler"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// buildBundleFor produces the bundle bytes (raw zip) for the
// fixture revealFixture sets up: a tiny schema with one
// non-secret + one Secret, a single-env chain, a plaintext layer.
// Manifest allows only `api_key` so the bundle's narrowed layer
// still carries the secret (the canonical "encrypted in transit
// even though source is plaintext" path is exercised by the
// existing bundle/build_test.go end-to-end).
func buildBundleFor(t *testing.T, dir string, base64 bool) []byte {
	t.Helper()

	// Layer FS — plaintext, single layer.
	layerBytes := []byte(`base_url = "http://x"
api_key = "live-plaintext-from-layer"
`)
	layersFS := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: layerBytes},
	}

	chainSpec := &chameleonv1.ChainSpec{
		Version:      "1.0.0",
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
		Layers: []*chameleonv1.LayerEntry{{
			Id: "base", Path: "base.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Required: true,
		}},
	}
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:  "test",
			Allow: []string{"api_key"},
		}},
	}

	desc, err := compileSchema(filepath.Join(dir, "app.proto"), "smoke.v1.Cfg", []string{dir})
	require.NoError(t, err)

	var buf bytes.Buffer
	build := bundler.Build
	if base64 {
		build = bundler.BuildBase64
	}
	require.NoError(t, build(context.Background(), bundler.BuildOptions{
		Manifest:     manifest,
		Module:       "test",
		Schema:       desc,
		ChainSpec:    chainSpec,
		LayersFS:     layersFS,
		Output:       &buf,
		ManifestName: "test-manifest",
	}))
	return buf.Bytes()
}

// --- reveal via --bundle=FILE -----------------------------------------------

func TestRevealCmd_BundleFile(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.zip")
	require.NoError(t, writeFile(bundlePath, buildBundleFor(t, dir, false /* raw zip */)))

	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--env=prod",
		"--field=api_key",
	})
	require.NoError(t, err)
	assert.Equal(t, "live-plaintext-from-layer", out.String(),
		"bundle input should resolve the same plaintext the loose-file path does")
}

func TestRevealCmd_BundleFileBase64(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.b64")
	require.NoError(t, writeFile(bundlePath, buildBundleFor(t, dir, true /* base64 */)))

	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--base64",
		"--env=prod",
		"--field=api_key",
	})
	require.NoError(t, err)
	assert.Equal(t, "live-plaintext-from-layer", out.String())
}

// --- reveal via --bundle=- (stdin) ------------------------------------------

func TestRevealCmd_BundleStdin(t *testing.T) {
	dir := revealFixture(t)
	stdin := bytes.NewReader(buildBundleFor(t, dir, false))

	var out bytes.Buffer
	err := revealCmd(stdin, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=-",
		"--env=prod",
		"--field=api_key",
	})
	require.NoError(t, err)
	assert.Equal(t, "live-plaintext-from-layer", out.String())
}

// --- reveal via --bundle-env=VAR --------------------------------------------

func TestRevealCmd_BundleEnv(t *testing.T) {
	dir := revealFixture(t)
	// Base64 in env vars is the realistic pattern (env values are
	// strings; binary zip bytes survive but require terminal-safe
	// transport more often than not). Exercise the realistic case.
	t.Setenv("CHAMELEON_TEST_BUNDLE", string(buildBundleFor(t, dir, true)))

	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle-env=CHAMELEON_TEST_BUNDLE",
		"--base64",
		"--env=prod",
		"--field=api_key",
	})
	require.NoError(t, err)
	assert.Equal(t, "live-plaintext-from-layer", out.String())
}

func TestRevealCmd_BundleEnvUnset(t *testing.T) {
	dir := revealFixture(t)
	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle-env=CHAMELEON_THIS_VAR_DOES_NOT_EXIST_XYZZY",
		"--env=prod",
		"--field=api_key",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not set")
}

// --- mutual-exclusion checks ------------------------------------------------

func TestRevealCmd_BundleAndBundleEnvMutuallyExclusive(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.zip")
	require.NoError(t, writeFile(bundlePath, buildBundleFor(t, dir, false)))
	t.Setenv("CHAMELEON_TEST_BUNDLE", "anything")

	err := revealCmd(nil, &bytes.Buffer{}, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--bundle-env=CHAMELEON_TEST_BUNDLE",
		"--env=prod",
		"--field=api_key",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestRevealCmd_BundleAndChainMutuallyExclusive(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.zip")
	require.NoError(t, writeFile(bundlePath, buildBundleFor(t, dir, false)))

	err := revealCmd(nil, &bytes.Buffer{}, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--chain=" + filepath.Join(dir, "chain.pxf"),
		"--env=prod",
		"--field=api_key",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined with bundle inputs")
}

// --- get + explain smoke through --bundle -----------------------------------

// Spot-checks that the same bundle wiring works on the sibling
// resolution subcommands. Full output assertions stay on the
// existing GetCmd_Smoke / ExplainCmd_Smoke; here we only assert
// the bundle path doesn't error.
func TestGetCmd_BundleFile(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.zip")
	require.NoError(t, writeFile(bundlePath, buildBundleFor(t, dir, false)))

	var out bytes.Buffer
	err := getCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--env=prod",
	})
	require.NoError(t, err)
	assert.Contains(t, out.String(), `base_url = "http://x"`,
		"get output via bundle should contain the resolved scalar")
}

func TestExplainCmd_BundleFile(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.zip")
	require.NoError(t, writeFile(bundlePath, buildBundleFor(t, dir, false)))

	var out bytes.Buffer
	err := explainCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--env=prod",
	})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "base_url",
		"explain output via bundle should mention the resolved field")
}

// --- buildBundleSources: chain spec's declared sources -----------------------
//
// Regression tests for the gap where reveal/get/explain with a
// --bundle* input only registered the bundle's FS as "default",
// ignoring everything else the chain spec declared. Chains that use
// an env-source layer (the STEWARD_* runtime-override pattern) would
// previously error with `layer references unregistered source` mid-
// resolve; now they work.

// buildBundleForWithEnvSource produces a bundle whose chain declares
// a `kind=env` source + a layer that consumes it. The env source's
// prefix is "SMOKE_" so a SMOKE_API_KEY env var feeds api_key. The
// base layer carries a default so the env-source override has
// something to win against.
func buildBundleForWithEnvSource(t *testing.T, dir string) []byte {
	t.Helper()

	layersFS := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "http://from-base-layer"
api_key = "from-base-layer"
`)},
	}

	chainSpec := &chameleonv1.ChainSpec{
		Version:      "1.0.0",
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
		Sources: map[string]*chameleonv1.SourceRef{
			"env": {Kind: "env", Options: map[string]string{"prefix": "SMOKE_"}},
		},
		Layers: []*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Required: true},
			{Id: "env-runtime", Path: "(env)", ForEach: chameleonv1.LayerEntry_ONCE, Source: "env"},
		},
	}
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:  "test",
			Allow: []string{"api_key"},
		}},
	}

	desc, err := compileSchema(filepath.Join(dir, "app.proto"), "smoke.v1.Cfg", []string{dir})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, bundler.BuildBase64(context.Background(), bundler.BuildOptions{
		Manifest:     manifest,
		Module:       "test",
		Schema:       desc,
		ChainSpec:    chainSpec,
		LayersFS:     layersFS,
		Output:       &buf,
		ManifestName: "test-manifest",
	}))
	return buf.Bytes()
}

func TestRevealCmd_BundleWithEnvSource(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.b64")
	require.NoError(t, writeFile(bundlePath, buildBundleForWithEnvSource(t, dir)))

	// SMOKE_API_KEY (prefix + UPPER_SNAKE of api_key) overrides the
	// base layer's plaintext value via the env-runtime layer at the
	// top of the chain. Without buildBundleSources registering the
	// env source from the chain spec, reveal would error with
	// `layer references unregistered source`.
	t.Setenv("SMOKE_API_KEY", "from-env-source-override")

	var out bytes.Buffer
	err := revealCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--base64",
		"--env=prod",
		"--field=api_key",
	})
	require.NoError(t, err)
	assert.Equal(t, "from-env-source-override", out.String(),
		"env source must register from chain spec when bundle input is used; "+
			"env-runtime layer should override the base layer value")
}

func TestGetCmd_BundleWithEnvSource(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.b64")
	require.NoError(t, writeFile(bundlePath, buildBundleForWithEnvSource(t, dir)))

	t.Setenv("SMOKE_API_KEY", "from-env-source-override")

	var out bytes.Buffer
	err := getCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--base64",
		"--env=prod",
	})
	require.NoError(t, err,
		"get must not error on chains with env-source layers when bundle input is used")
	// get redacts Secret values, so we can't see the env-source
	// plaintext here. The non-error is the assertion that matters —
	// pre-fix this errored with "unregistered source" before any
	// output was written.
	assert.Contains(t, out.String(), "base_url",
		"output should include the resolved non-secret fields")
}

func TestExplainCmd_BundleWithEnvSource(t *testing.T) {
	dir := revealFixture(t)
	bundlePath := filepath.Join(dir, "bundle.b64")
	require.NoError(t, writeFile(bundlePath, buildBundleForWithEnvSource(t, dir)))

	t.Setenv("SMOKE_API_KEY", "from-env-source-override")

	var out bytes.Buffer
	err := explainCmd(nil, &out, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--base64",
		"--env=prod",
	})
	require.NoError(t, err,
		"explain must not error on chains with env-source layers when bundle input is used")
}

// TestRevealCmd_BundleRejectsEncryptedEnvLayer mirrors the
// buildSources rejection: a layer that references a kind=env source
// MUST NOT carry encrypted=true (sops has nothing to bite on for
// env vars). buildBundleSources should reject it identically.
//
// The bundler may catch this at build time; if not, the run-time
// rejection covers callers who hand us a bundle they didn't build.
func TestRevealCmd_BundleRejectsEncryptedEnvLayer(t *testing.T) {
	dir := revealFixture(t)

	layersFS := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"
api_key = "y"
`)},
	}
	chainSpec := &chameleonv1.ChainSpec{
		Version:      "1.0.0",
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
		Sources: map[string]*chameleonv1.SourceRef{
			"env": {Kind: "env", Options: map[string]string{"prefix": "SMOKE_"}},
		},
		Layers: []*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Required: true},
			{Id: "env-runtime", Path: "(env)", ForEach: chameleonv1.LayerEntry_ONCE, Source: "env", Encrypted: true},
		},
	}
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{Name: "test", Allow: []string{"api_key"}}},
	}
	desc, err := compileSchema(filepath.Join(dir, "app.proto"), "smoke.v1.Cfg", []string{dir})
	require.NoError(t, err)

	var buf bytes.Buffer
	buildErr := bundler.BuildBase64(context.Background(), bundler.BuildOptions{
		Manifest:     manifest,
		Module:       "test",
		Schema:       desc,
		ChainSpec:    chainSpec,
		LayersFS:     layersFS,
		Output:       &buf,
		ManifestName: "test-manifest",
	})
	if buildErr != nil {
		// Bundler caught it first — that's also acceptable; just
		// confirm the message points at the source-kind conflict.
		assert.Contains(t, buildErr.Error(), "env",
			"bundler rejected encrypted-on-env-source layer: %v", buildErr)
		return
	}

	bundlePath := filepath.Join(dir, "bundle.b64")
	require.NoError(t, writeFile(bundlePath, buf.Bytes()))

	err = revealCmd(nil, &bytes.Buffer{}, []string{
		"--schema=" + filepath.Join(dir, "app.proto"),
		"--include=" + dir,
		"--message=smoke.v1.Cfg",
		"--bundle=" + bundlePath,
		"--base64",
		"--env=prod",
		"--field=api_key",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypted=true is meaningless",
		"buildBundleSources must reject encrypted=true on env-source layers")
}

// --- silence unused-import nags --------------------------------------------

var _ = strings.Contains
