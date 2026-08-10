// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// annotationsSrc is the minimal pxf extension file the fixtures import.
// Written into each test's own tree so `import "pxf/annotations.proto"`
// resolves against the schema's own directory, the way a real repo
// vendors it.
const annotationsSrc = `
syntax = "proto3";
package pxf;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FieldOptions {
  bool required = 50000;
  string default = 50001;
  string key = 50002;
}
`

func TestCheckSchema_CleanSchemaPasses(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	schema := filepath.Join(dir, "app.proto")
	write(t, schema, `
syntax = "proto3";
package app.v1;
import "pxf/annotations.proto";
message Config {
  string base_url = 1 [(pxf.default) = "example.com"];
}
`)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + schema}); err != nil {
		t.Fatalf("clean schema rejected: %v", err)
	}
	if got := out.String(); !strings.HasPrefix(got, "OK\t") {
		t.Errorf("output %q does not start with OK", got)
	}
}

func TestCheckSchema_ViolationIsReportedAndFails(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	schema := filepath.Join(dir, "app.proto")
	write(t, schema, `
syntax = "proto3";
package app.v1;
import "pxf/annotations.proto";
message Config {
  repeated string tags = 1 [(pxf.default) = "ignored"];
}
`)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + schema})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	got := out.String()
	for _, want := range []string{
		`app.proto: field "app.v1.Config.tags"`,
		`invalid (pxf.default) = "ignored"`,
		"one literal cannot denote a list",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\ngot: %s", want, got)
		}
	}
}

// The case the command exists for: the schema handed in is itself
// clean, and the violation lives in a file it imports. Nothing that
// resolves a layer would tell an author of common.proto that they had
// broken every consumer downstream.
func TestCheckSchema_ImportedFileViolationNamesTheImport(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	write(t, filepath.Join(dir, "common.proto"), `
syntax = "proto3";
package common.v1;
import "pxf/annotations.proto";
message Unreferenced {
  repeated string labels = 1 [(pxf.default) = "nope"];
}
`)
	schema := filepath.Join(dir, "config.proto")
	write(t, schema, `
syntax = "proto3";
package myapp.v1;
import "common.proto";
message AppConfig {
  string base_url = 1;
}
`)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + schema})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	got := out.String()
	if !strings.Contains(got, `common.proto: field "common.v1.Unreferenced.labels"`) {
		t.Errorf("violation must name the declaring file, not the one passed in\ngot: %s", got)
	}
	if strings.Contains(got, "config.proto:") {
		t.Errorf("config.proto is conformant and must not be blamed\ngot: %s", got)
	}
}

func TestCheckSchema_MessageFlagAssertsExistence(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	schema := filepath.Join(dir, "app.proto")
	write(t, schema, `
syntax = "proto3";
package app.v1;
message Config { string base_url = 1; }
`)
	t.Run("present", func(t *testing.T) {
		var out bytes.Buffer
		if err := checkSchemaCmd(&out, []string{"--schema=" + schema, "--message=app.v1.Config"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		var out bytes.Buffer
		err := checkSchemaCmd(&out, []string{"--schema=" + schema, "--message=app.v1.Nope"})
		if err == nil || !strings.Contains(err.Error(), `find message "app.v1.Nope"`) {
			t.Fatalf("got %v, want a find-message error", err)
		}
	})
}

// --message must not change the verdict: the bind-time rules are a
// property of the file closure, so naming a conformant message in a
// non-conformant file still fails.
func TestCheckSchema_MessageFlagDoesNotNarrowTheCheck(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	schema := filepath.Join(dir, "app.proto")
	write(t, schema, `
syntax = "proto3";
package app.v1;
import "pxf/annotations.proto";
message Fine { string base_url = 1; }
message Broken { repeated string tags = 1 [(pxf.default) = "ignored"]; }
`)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + schema, "--message=app.v1.Fine"})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want the file's violation reported regardless of --message", err)
	}
	if !strings.Contains(out.String(), "app.v1.Broken.tags") {
		t.Errorf("expected Broken's violation\ngot: %s", out.String())
	}
}

func TestCheckSchema_SchemaFlagRequired(t *testing.T) {
	var out bytes.Buffer
	err := checkSchemaCmd(&out, nil)
	if err == nil || !strings.Contains(err.Error(), "--schema is required") {
		t.Fatalf("got %v, want --schema required", err)
	}
}

func TestCheckSchema_MissingFile(t *testing.T) {
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=/no/such/schema.proto"})
	if err == nil || !strings.Contains(err.Error(), "read schema") {
		t.Fatalf("got %v, want a read-schema error", err)
	}
}

// The OK line reports the closure size, and the diamond — A imports B
// and C, both importing D — must count D once. A wrong count here is
// the signal an author reads to notice --include is misconfigured, so
// it has to be trustworthy.
func TestCheckSchema_ClosureSizeDedupesTheDiamond(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d.proto"), `
syntax = "proto3";
package d.v1;
message D { string x = 1; }
`)
	for _, n := range []string{"b", "c"} {
		write(t, filepath.Join(dir, n+".proto"), `
syntax = "proto3";
package `+n+`.v1;
import "d.proto";
message `+strings.ToUpper(n)+` { d.v1.D d = 1; }
`)
	}
	schema := filepath.Join(dir, "a.proto")
	write(t, schema, `
syntax = "proto3";
package a.v1;
import "b.proto";
import "c.proto";
message A { b.v1.B b = 1; c.v1.C c = 2; }
`)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + schema}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// a + b + c + d = 4, with d counted once.
	if !strings.Contains(out.String(), "4 file(s) checked") {
		t.Errorf("want 4 files in the closure\ngot: %s", out.String())
	}
}
