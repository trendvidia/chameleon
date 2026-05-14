// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}
