// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// writeUserConfig drops a $HOME/.chameleon-shaped PXF document into
// a temp file and points CHAMELEON_USER_CONFIG at it for the rest
// of the test. Returns the path so tests can assert against it in
// error messages.
func writeUserConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".chameleon")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	t.Setenv("CHAMELEON_USER_CONFIG", path)
	return path
}

// TestLoadUserConfig_Missing — absent file is not an error, the
// loader returns (nil, nil) so callers fall back to CLI flags.
func TestLoadUserConfig_Missing(t *testing.T) {
	t.Setenv("CHAMELEON_USER_CONFIG", filepath.Join(t.TempDir(), "nope"))
	cfg, err := loadUserConfig()
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

// TestLoadUserConfig_Parse — a well-formed PXF document parses into
// a UserConfig with the expected fields.
func TestLoadUserConfig_Parse(t *testing.T) {
	writeUserConfig(t, `@type chameleon.v1.UserConfig

default_app = "steward"

apps = {
  "steward": {
    root      = "/abs/proj"
    schema    = "proto/app.proto"
    include   = ["proto"]
    message   = "steward.config.v1.AppConfig"
    chain     = "config/chain.pxf"
    layers    = "config/layers"
    env       = "dev"
    decrypter = "sops"
  }
}
`)
	cfg, err := loadUserConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "steward", cfg.GetDefaultApp())
	app := cfg.GetApps()["steward"]
	require.NotNil(t, app)
	assert.Equal(t, "/abs/proj", app.GetRoot())
	assert.Equal(t, "config/chain.pxf", app.GetChain())
	assert.Equal(t, "sops", app.GetDecrypter())
	assert.Equal(t, []string{"proto"}, app.GetInclude())
}

// TestLoadUserConfig_ParseError — a malformed PXF document surfaces
// a parse error rather than silently yielding an empty config.
func TestLoadUserConfig_ParseError(t *testing.T) {
	writeUserConfig(t, `not a valid pxf document {{{`)
	_, err := loadUserConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// --- selectApp ---

// TestSelectApp_ExplicitNameWins — --app=foo always picks
// apps["foo"], even when default_app is set to something else.
func TestSelectApp_ExplicitNameWins(t *testing.T) {
	cfg := &chameleonv1.UserConfig{
		DefaultApp: "alpha",
		Apps: map[string]*chameleonv1.AppConfig{
			"alpha": {Root: "/a"},
			"beta":  {Root: "/b"},
		},
	}
	app, err := selectApp(cfg, "beta")
	require.NoError(t, err)
	assert.Equal(t, "/b", app.GetRoot())
}

// TestSelectApp_DefaultAppFallback — no --app, default_app set,
// multi-app config: pick default_app.
func TestSelectApp_DefaultAppFallback(t *testing.T) {
	cfg := &chameleonv1.UserConfig{
		DefaultApp: "alpha",
		Apps: map[string]*chameleonv1.AppConfig{
			"alpha": {Root: "/a"},
			"beta":  {Root: "/b"},
		},
	}
	app, err := selectApp(cfg, "")
	require.NoError(t, err)
	assert.Equal(t, "/a", app.GetRoot())
}

// TestSelectApp_SingleAppShortcut — exactly one app in the config,
// no --app, no default_app: use the lone entry.
func TestSelectApp_SingleAppShortcut(t *testing.T) {
	cfg := &chameleonv1.UserConfig{
		Apps: map[string]*chameleonv1.AppConfig{
			"only": {Root: "/o"},
		},
	}
	app, err := selectApp(cfg, "")
	require.NoError(t, err)
	assert.Equal(t, "/o", app.GetRoot())
}

// TestSelectApp_AmbiguousNoApp — multi-app, no --app, no
// default_app: error listing available apps. The user must
// disambiguate.
func TestSelectApp_AmbiguousNoApp(t *testing.T) {
	cfg := &chameleonv1.UserConfig{
		Apps: map[string]*chameleonv1.AppConfig{
			"alpha": {Root: "/a"},
			"beta":  {Root: "/b"},
		},
	}
	_, err := selectApp(cfg, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alpha, beta")
}

// TestSelectApp_UnknownApp — --app=bogus errors with the available
// list. Doesn't fall back to default_app — the user explicitly
// asked for "bogus" and the right move is to surface the typo.
func TestSelectApp_UnknownApp(t *testing.T) {
	cfg := &chameleonv1.UserConfig{
		DefaultApp: "alpha",
		Apps: map[string]*chameleonv1.AppConfig{
			"alpha": {Root: "/a"},
		},
	}
	_, err := selectApp(cfg, "bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"bogus"`)
	assert.Contains(t, err.Error(), "alpha")
}

// TestSelectApp_NilConfig — no config file present (loader
// returned nil). With no --app: nil app, no error. With --app:
// error since the user clearly expected a config.
func TestSelectApp_NilConfig(t *testing.T) {
	app, err := selectApp(nil, "")
	require.NoError(t, err)
	assert.Nil(t, app)

	_, err = selectApp(nil, "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no")
}

// --- resolveAppPath / applyAppDefaults ---

// TestResolveAppPath — relative paths join app.Root; absolute
// paths pass through; empty passes through.
func TestResolveAppPath(t *testing.T) {
	app := &chameleonv1.AppConfig{Root: "/proj"}
	assert.Equal(t, "/proj/sub/file", resolveAppPath(app, "sub/file"))
	assert.Equal(t, "/abs/elsewhere", resolveAppPath(app, "/abs/elsewhere"))
	assert.Equal(t, "", resolveAppPath(app, ""))
}

// TestApplyAppDefaults_FillsEmptyFlags — every empty flag pointer
// gets filled from the AppConfig; path-typed fields are resolved
// against app.Root.
func TestApplyAppDefaults_FillsEmptyFlags(t *testing.T) {
	app := &chameleonv1.AppConfig{
		Root:      "/proj",
		Schema:    "app.proto",
		Include:   []string{"proto"},
		Message:   "x.Y",
		Chain:     "chain.pxf",
		Layers:    "layers",
		Env:       "dev",
		Decrypter: "sops",
	}
	var (
		schema, message, chainPath, layersDir, env, decrypter string
		includePaths                                          []string
	)
	applyAppDefaults(nil, app, &schema, &message, &chainPath, &layersDir, &env, &decrypter, &includePaths)

	assert.Equal(t, "/proj/app.proto", schema)
	assert.Equal(t, "x.Y", message)
	assert.Equal(t, "/proj/chain.pxf", chainPath)
	assert.Equal(t, "/proj/layers", layersDir)
	assert.Equal(t, "dev", env)
	assert.Equal(t, "sops", decrypter)
	assert.Equal(t, []string{"/proj/proto"}, includePaths)
}

// TestApplyAppDefaults_CLIWins — non-empty flag pointers are never
// overwritten. The load-bearing precedence rule.
func TestApplyAppDefaults_CLIWins(t *testing.T) {
	app := &chameleonv1.AppConfig{
		Root:    "/proj",
		Schema:  "app.proto",
		Message: "x.Y",
		Chain:   "chain.pxf",
		Env:     "dev",
	}
	schema := "/cli/schema.proto"
	message := "explicit.Msg"
	chainPath := "/cli/chain.pxf"
	layersDir := "/cli/layers"
	env := "prod"
	decrypter := "none"
	includePaths := []string{"/cli/proto"}

	applyAppDefaults(nil, app, &schema, &message, &chainPath, &layersDir, &env, &decrypter, &includePaths)

	assert.Equal(t, "/cli/schema.proto", schema, "schema CLI override preserved")
	assert.Equal(t, "explicit.Msg", message)
	assert.Equal(t, "/cli/chain.pxf", chainPath)
	assert.Equal(t, "/cli/layers", layersDir)
	assert.Equal(t, "prod", env)
	assert.Equal(t, []string{"/cli/proto"}, includePaths)
	// --decrypter has a Go default of "none"; the merge treats
	// "none" as "not set" so the config's sops would win — but
	// this app has no decrypter, so "none" sticks.
	assert.Equal(t, "none", decrypter)
}

// TestApplyAppDefaults_LayersDotIsTreatedAsUnset — --layers has a
// Go default of ".", which would otherwise look "set" and shadow
// the config's layers field. The merge promotes "." to "fall
// back to config" so the common no-flag invocation works.
func TestApplyAppDefaults_LayersDotIsTreatedAsUnset(t *testing.T) {
	app := &chameleonv1.AppConfig{Root: "/proj", Layers: "config/layers"}
	layersDir := "."
	var (
		schema, message, chainPath, env, decrypter string
		includePaths                               []string
	)
	applyAppDefaults(nil, app, &schema, &message, &chainPath, &layersDir, &env, &decrypter, &includePaths)
	assert.Equal(t, "/proj/config/layers", layersDir,
		"--layers default of \".\" should be overridden by the AppConfig")
}

// TestApplyAppDefaults_NilApp — a nil app passes through every
// flag pointer unchanged. Callers (each Cmd) get to pass the
// result of selectApp unconditionally without a nil-check.
func TestApplyAppDefaults_NilApp(t *testing.T) {
	schema, message, chainPath, layersDir, env, decrypter := "x", "y", "z", "/l", "e", "d"
	includePaths := []string{"i"}
	applyAppDefaults(nil, nil, &schema, &message, &chainPath, &layersDir, &env, &decrypter, &includePaths)
	assert.Equal(t, "x", schema)
	assert.Equal(t, "y", message)
	assert.Equal(t, "z", chainPath)
	assert.Equal(t, "/l", layersDir)
	assert.Equal(t, "e", env)
	assert.Equal(t, "d", decrypter)
	assert.Equal(t, []string{"i"}, includePaths)
}
