// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
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

// The closure is what is checked, so --message has to resolve over the
// closure too: a message declared in an imported common.proto exists as
// far as this command is concerned, and pinning it in CI is exactly the
// shared-schema case the command is for.
func TestCheckSchema_MessageFlagResolvesThroughImports(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "common.proto"), `
syntax = "proto3";
package common.v1;
message Shared { string tenant = 1; }
`)
	schema := filepath.Join(dir, "config.proto")
	write(t, schema, `
syntax = "proto3";
package myapp.v1;
import "common.proto";
message AppConfig { common.v1.Shared shared = 1; }
`)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + schema, "--message=common.v1.Shared"}); err != nil {
		t.Fatalf("message declared in an imported file rejected: %v", err)
	}
	if !strings.HasPrefix(out.String(), "OK\t") {
		t.Errorf("output %q does not start with OK", out.String())
	}
}

// A bad --message must not swallow the violation report: the report is
// the command's primary output, and a CI job that mistypes a message
// name should still be told what else is wrong.
func TestCheckSchema_MessageErrorDoesNotSuppressViolations(t *testing.T) {
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
	err := checkSchemaCmd(&out, []string{"--schema=" + schema, "--message=app.v1.Typo"})
	if err == nil || !strings.Contains(err.Error(), `find message "app.v1.Typo"`) {
		t.Fatalf("got %v, want a find-message error", err)
	}
	if !strings.Contains(out.String(), `field "app.v1.Config.tags"`) {
		t.Errorf("violation must still be reported\ngot: %s", out.String())
	}
	// …and the success line must not be, since neither check passed.
	if strings.Contains(out.String(), "OK\t") {
		t.Errorf("unexpected OK line\ngot: %s", out.String())
	}
}

// --include is the only way to check a schema whose imports live
// elsewhere, which is the normal shape of a schema repo. Both roots are
// needed here, so this also pins that the flag is repeatable.
func TestCheckSchema_IncludeResolvesImportsOutsideTheSchemaDir(t *testing.T) {
	dir := t.TempDir()
	annDir := filepath.Join(dir, "ann")
	libDir := filepath.Join(dir, "lib")
	write(t, filepath.Join(annDir, "pxf", "annotations.proto"), annotationsSrc)
	write(t, filepath.Join(libDir, "common.proto"), `
syntax = "proto3";
package common.v1;
import "pxf/annotations.proto";
message Shared { string tenant = 1 [(pxf.default) = "acme"]; }
`)
	schema := filepath.Join(dir, "schema", "config.proto")
	write(t, schema, `
syntax = "proto3";
package myapp.v1;
import "common.proto";
message AppConfig { common.v1.Shared shared = 1; }
`)

	t.Run("without include the import does not resolve", func(t *testing.T) {
		var out bytes.Buffer
		err := checkSchemaCmd(&out, []string{"--schema=" + schema})
		if err == nil || !strings.Contains(err.Error(), "compile") {
			t.Fatalf("got %v, want a compile error for the unresolvable import", err)
		}
	})

	t.Run("with both roots it compiles and checks the closure", func(t *testing.T) {
		var out bytes.Buffer
		if err := checkSchemaCmd(&out, []string{
			"--schema=" + schema,
			"--include=" + libDir,
			"--include=" + annDir,
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// config + common + pxf/annotations + descriptor.proto.
		if !strings.Contains(out.String(), "4 file(s) checked") {
			t.Errorf("want the 4-file closure\ngot: %s", out.String())
		}
	})
}

// Flag parsing stops at the first positional argument, so accepting one
// would silently drop every flag after it. Rejecting is the only way a
// caller learns that `--schema=a.proto b.proto` checked one file.
func TestCheckSchema_RejectsPositionalArguments(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "app.proto")
	write(t, schema, `
syntax = "proto3";
package app.v1;
message Config { string base_url = 1; }
`)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + schema, "other.proto", "--include=./lib"})
	if err == nil || !strings.Contains(err.Error(), `unexpected argument "other.proto"`) {
		t.Fatalf("got %v, want the extra argument rejected", err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing should be checked\ngot: %s", out.String())
	}
}

// A directory is the likely wrong --schema, since per-directory
// checking is the shape a schema repo wants. The up-front stat exists
// so it does not surface as protocompile's "is a directory".
func TestCheckSchema_DirectorySchemaIsRejected(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + dir})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("got %v, want a not-a-regular-file error", err)
	}
}

// A placeholder stands in for an import that did not resolve. Upstream's
// validation walk skips it, so counting it here would put a file in the
// OK line that ValidateFile never inspected.
func TestClosureSize_SkipsPlaceholders(t *testing.T) {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("app.proto"),
		Syntax:     proto.String("proto3"),
		Package:    proto.String("app.v1"),
		Dependency: []string{"missing.proto"},
	}
	fd, err := (protodesc.FileOptions{AllowUnresolvable: true}).New(fdp, nil)
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	if imps := fd.Imports(); imps.Len() != 1 || !imps.Get(0).IsPlaceholder() {
		t.Fatalf("fixture should carry one placeholder import")
	}
	if got := closureSize(fd); got != 1 {
		t.Errorf("closureSize = %d, want 1 (the placeholder must not be counted)", got)
	}
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
