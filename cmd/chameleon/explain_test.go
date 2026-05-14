// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/chameleon/source"
)

// TestCompileSchema_OnlyWKTs exercises the no-includes path: a
// schema importing only Google well-known types (handled by
// protocompile.WithStandardImports) resolves without --include.
// Smoke-test for the refactor — the SourceResolver shift from
// in-memory map to ImportPaths must not regress the common case.
func TestCompileSchema_OnlyWKTs(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "app.proto")
	write(t, schema, `
syntax = "proto3";
package app.v1;
import "google/protobuf/duration.proto";
message Config {
    string name = 1;
    google.protobuf.Duration timeout = 2;
}
`)
	desc, err := compileSchema(schema, "app.v1.Config", nil)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if string(desc.Name()) != "Config" {
		t.Errorf("got descriptor %q, want Config", desc.Name())
	}
}

// TestCompileSchema_TransitiveImportNeedsInclude verifies the
// motivating bug: a schema that imports a custom proto fails to
// compile without --include, succeeds with it.
func TestCompileSchema_TransitiveImportNeedsInclude(t *testing.T) {
	root := t.TempDir()

	// External proto sits at <root>/lib/widget/v1/widget.proto.
	libDir := filepath.Join(root, "lib")
	widgetPath := filepath.Join(libDir, "widget", "v1", "widget.proto")
	write(t, widgetPath, `
syntax = "proto3";
package widget.v1;
message Widget {
    string id = 1;
}
`)

	// Consumer schema imports it by package-rooted path.
	schemaDir := filepath.Join(root, "app")
	schemaPath := filepath.Join(schemaDir, "config.proto")
	write(t, schemaPath, `
syntax = "proto3";
package app.v1;
import "widget/v1/widget.proto";
message Config {
    widget.v1.Widget primary = 1;
}
`)

	// Without --include: compile must fail with a path-resolution error.
	if _, err := compileSchema(schemaPath, "app.v1.Config", nil); err == nil {
		t.Fatal("compile unexpectedly succeeded without --include")
	} else if !strings.Contains(err.Error(), "widget/v1/widget.proto") {
		t.Errorf("error doesn't mention missing import: %v", err)
	}

	// With --include pointing at lib/: compile succeeds.
	desc, err := compileSchema(schemaPath, "app.v1.Config", []string{libDir})
	if err != nil {
		t.Fatalf("compile failed with --include: %v", err)
	}
	if string(desc.Name()) != "Config" {
		t.Errorf("got descriptor %q, want Config", desc.Name())
	}
}

// TestCompileSchema_MissingSchema yields a clean error rather than
// the deeper protocompile message a non-existent schema would
// otherwise produce inside the import-search loop.
func TestCompileSchema_MissingSchema(t *testing.T) {
	_, err := compileSchema("/no/such/path.proto", "app.v1.Config", nil)
	if err == nil {
		t.Fatal("compile unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "read schema") {
		t.Errorf("error doesn't surface the early-return path: %v", err)
	}
}

// TestBuildSources_AutoRegistersUndeclaredLayerSources is the
// motivating bug: a chain spec references source names (here:
// "base", "secrets") via layers without declaring them in the
// spec.sources map. The old code only registered "default", so
// resolver.New refused to start with
//   `layer references unregistered source: source="secrets"`.
// Fix: every layer-referenced name not in spec.sources gets
// auto-registered at layersDir.
func TestBuildSources_AutoRegistersUndeclaredLayerSources(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "marker.txt"), "ok")

	spec := &chameleonv1.ChainSpec{
		Layers: []*chameleonv1.LayerEntry{
			{Id: "base", Source: "base", Path: "marker.txt"},
			{Id: "secrets-dev", Source: "secrets", Path: "marker.txt"},
		},
	}
	// chainDir is unused on this path (no declared sources); pass
	// the temp dir as a sentinel value to be sure pass 2 ignores it.
	got, err := buildSources(spec, t.TempDir(), dir)
	if err != nil {
		t.Fatalf("buildSources: %v", err)
	}
	for _, name := range []string{"base", "secrets"} {
		src, ok := got[name]
		if !ok {
			t.Errorf("source %q missing from result; got keys %v", name, keysOf(got))
			continue
		}
		assertSourceRootedAt(t, src, "marker.txt")
	}
}

// TestBuildSources_DeclaredRelativeRootAnchorsAtChainDir is the
// motivating fix for the website/config bug report:
//   chain.pxf declares `root = "./layers"` next to itself, and the
//   user passes `--layers layers`. The author wrote `./layers`
//   meaning "relative to chain.pxf", not "relative to whatever
//   --layers was set to". Anchoring at chainDir resolves correctly;
//   anchoring at layersDir double-joined to `layers/layers/...`
//   and produced ErrRequiredLayerMissing.
func TestBuildSources_DeclaredRelativeRootAnchorsAtChainDir(t *testing.T) {
	chainDir := t.TempDir()
	write(t, filepath.Join(chainDir, "layers", "base.pxf"), "x: 1")

	// layersDir is a DIFFERENT directory — the test fails if
	// buildSources joins onto layersDir instead of chainDir.
	layersDir := t.TempDir()

	spec := &chameleonv1.ChainSpec{
		Sources: map[string]*chameleonv1.SourceRef{
			"default": {Kind: "fs", Options: map[string]string{"root": "./layers"}},
		},
	}
	got, err := buildSources(spec, chainDir, layersDir)
	if err != nil {
		t.Fatalf("buildSources: %v", err)
	}
	src, ok := got["default"]
	if !ok {
		t.Fatalf("source %q missing; got keys %v", "default", keysOf(got))
	}
	assertSourceRootedAt(t, src, "base.pxf")
}

// TestBuildSources_DeclaredFSAbsoluteRootHonored verifies that an
// absolute options["root"] is used verbatim, not re-rooted under
// chainDir or layersDir.
func TestBuildSources_DeclaredFSAbsoluteRootHonored(t *testing.T) {
	chainDir := t.TempDir()  // unrelated
	layersDir := t.TempDir() // unrelated
	sourceDir := t.TempDir()
	write(t, filepath.Join(sourceDir, "abs.pxf"), "x: 1")

	spec := &chameleonv1.ChainSpec{
		Sources: map[string]*chameleonv1.SourceRef{
			"elsewhere": {Kind: "fs", Options: map[string]string{"root": sourceDir}},
		},
	}
	got, err := buildSources(spec, chainDir, layersDir)
	if err != nil {
		t.Fatalf("buildSources: %v", err)
	}
	assertSourceRootedAt(t, got["elsewhere"], "abs.pxf")
}

// TestBuildSources_DeclaredEmptyRootFallsBackToLayersDir verifies
// the convenience case: a declared `{ kind = "fs" }` with no root
// uses --layers, matching the pre-spec-sources single-source
// invocation shape (so adding a sources block isn't a regression
// for users who just want to label their default source).
func TestBuildSources_DeclaredEmptyRootFallsBackToLayersDir(t *testing.T) {
	chainDir := t.TempDir() // empty, unused
	layersDir := t.TempDir()
	write(t, filepath.Join(layersDir, "x.pxf"), "x: 1")

	spec := &chameleonv1.ChainSpec{
		Sources: map[string]*chameleonv1.SourceRef{
			"default": {Kind: "fs"},
		},
	}
	got, err := buildSources(spec, chainDir, layersDir)
	if err != nil {
		t.Fatalf("buildSources: %v", err)
	}
	assertSourceRootedAt(t, got["default"], "x.pxf")
}

// TestBuildSources_UnknownKindErrors guards the strict-on-declared
// path: a spec that explicitly declares a non-fs source kind must
// fail rather than silently registering something wrong.
func TestBuildSources_UnknownKindErrors(t *testing.T) {
	spec := &chameleonv1.ChainSpec{
		Sources: map[string]*chameleonv1.SourceRef{
			"vault": {Kind: "hashicorp-vault"},
		},
	}
	_, err := buildSources(spec, t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected error for unknown source kind")
	}
	if !strings.Contains(err.Error(), `kind "hashicorp-vault"`) {
		t.Errorf("error doesn't mention the offending kind: %v", err)
	}
}

// TestBuildSources_EmptyLayerSourceMapsToDefault: a layer with
// Source="" should register as "default" — the historical convention
// the old hardcoded-WithSource("default", ...) path encoded.
func TestBuildSources_EmptyLayerSourceMapsToDefault(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "x.pxf"), "x: 1")

	spec := &chameleonv1.ChainSpec{
		Layers: []*chameleonv1.LayerEntry{
			{Id: "implicit", Source: "", Path: "x.pxf"},
		},
	}
	got, err := buildSources(spec, t.TempDir(), dir)
	if err != nil {
		t.Fatalf("buildSources: %v", err)
	}
	if _, ok := got["default"]; !ok {
		t.Errorf("expected key %q; got %v", "default", keysOf(got))
	}
}

// TestBuildSources_DeclaredOverridesAuto: when a name appears in
// BOTH spec.sources (with a custom root) AND a layer's source, the
// declared root wins. Without this, a spec author could declare an
// fs source pointing somewhere specific and have it silently re-
// rooted by the auto-registration pass.
func TestBuildSources_DeclaredOverridesAuto(t *testing.T) {
	layersDir := t.TempDir()
	write(t, filepath.Join(layersDir, "wrong.pxf"), "x: 1")

	declaredRoot := t.TempDir()
	write(t, filepath.Join(declaredRoot, "right.pxf"), "x: 1")

	spec := &chameleonv1.ChainSpec{
		Sources: map[string]*chameleonv1.SourceRef{
			"shared": {Kind: "fs", Options: map[string]string{"root": declaredRoot}},
		},
		Layers: []*chameleonv1.LayerEntry{
			{Id: "uses-shared", Source: "shared", Path: "right.pxf"},
		},
	}
	got, err := buildSources(spec, t.TempDir(), layersDir)
	if err != nil {
		t.Fatalf("buildSources: %v", err)
	}
	src := got["shared"]
	// Must resolve declared-root file:
	assertSourceRootedAt(t, src, "right.pxf")
	// Must NOT resolve layersDir file:
	if _, err := src.Stat(context.Background(), "wrong.pxf"); err == nil {
		info, _ := src.Stat(context.Background(), "wrong.pxf")
		if info.Exists {
			t.Errorf("declared source unexpectedly sees layersDir file %q — auto-registration overrode declared root", "wrong.pxf")
		}
	}
}

// TestBuildSources_EmptySpecRegistersDefault: belt-and-suspenders —
// an entirely empty chain spec still produces a non-empty source
// map so callers can surface a more useful "spec has no layers"
// error from the resolver rather than "unregistered source".
func TestBuildSources_EmptySpecRegistersDefault(t *testing.T) {
	got, err := buildSources(&chameleonv1.ChainSpec{}, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("buildSources: %v", err)
	}
	if _, ok := got["default"]; !ok || len(got) != 1 {
		t.Errorf("expected single %q key; got %v", "default", keysOf(got))
	}
}

// TestSelectDecrypter_None: the default flag value disables
// decryption — encrypted layers will surface ErrNoDecrypter, which
// is the explicit "you forgot to wire something up" signal.
func TestSelectDecrypter_None(t *testing.T) {
	for _, v := range []string{"", "none"} {
		dec, err := selectDecrypter(v)
		if err != nil {
			t.Errorf("selectDecrypter(%q): %v", v, err)
		}
		if dec != nil {
			t.Errorf("selectDecrypter(%q) returned non-nil: %T", v, dec)
		}
	}
}

// TestSelectDecrypter_Sops returns a real chsops decrypter. We
// don't drive it through an actual decrypt (env-dependent + slow);
// the contract is "non-nil and implements Decrypter."
func TestSelectDecrypter_Sops(t *testing.T) {
	dec, err := selectDecrypter("sops")
	if err != nil {
		t.Fatalf("selectDecrypter: %v", err)
	}
	if dec == nil {
		t.Fatal("sops decrypter unexpectedly nil")
	}
}

// TestSelectDecrypter_UnknownErrors: a typo'd flag must error
// rather than silently falling back to "none". Misspelling
// --decrypter=spos shouldn't quietly disable decryption.
func TestSelectDecrypter_UnknownErrors(t *testing.T) {
	_, err := selectDecrypter("vault")
	if err == nil {
		t.Fatal("expected error for unknown decrypter")
	}
	if !strings.Contains(err.Error(), `"vault"`) {
		t.Errorf("error doesn't mention the offending value: %v", err)
	}
}

// assertSourceRootedAt opens a known file via src.Open and fails if
// it cannot be read. The only black-box way to verify a Source's
// FS-root from outside the source package.
func assertSourceRootedAt(t *testing.T, src source.Source, file string) {
	t.Helper()
	rc, err := src.Open(context.Background(), file)
	if err != nil {
		t.Errorf("source.Open(%q): %v", file, err)
		return
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); err != nil {
		t.Errorf("read %q: %v", file, err)
	}
}

func keysOf(m map[string]source.Source) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}
