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

// A directory --schema is the schema-repo shape (#153) and is now
// searched rather than rejected — but an empty one is still a mistake,
// and has to say so rather than reporting "OK, 0 files".
func TestCheckSchema_EmptyDirectorySchemaIsRejected(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + dir})
	if err == nil || !strings.Contains(err.Error(), "no .proto files found") {
		t.Fatalf("got %v, want an empty-directory error", err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing should be reported\ngot: %s", out.String())
	}
}

// A placeholder stands in for an import that did not resolve. Upstream's
// validation walk skips it, so counting it here would put a file in the
// OK line that ValidateFile never inspected.
func TestCollectClosure_SkipsPlaceholders(t *testing.T) {
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
	seen := map[string]struct{}{}
	collectClosure(fd, seen)
	if got := len(seen); got != 1 {
		t.Errorf("closure size = %d, want 1 (the placeholder must not be counted)", got)
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

// --- multi-root (#153) ---

// schemaRepo stamps the shape the multi-root form exists for: a repo
// that owns several .proto files, all importing one shared common
// file, with the pxf annotations vendored where a real one puts them.
// broken decides whether common.proto carries the violation.
func schemaRepo(t *testing.T, broken bool) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	commonField := `string label = 1;`
	if broken {
		commonField = `repeated string labels = 1 [(pxf.default) = "nope"];`
	}
	write(t, filepath.Join(dir, "common", "v1", "common.proto"), `
syntax = "proto3";
package common.v1;
import "pxf/annotations.proto";
message Shared {
  `+commonField+`
}
`)
	for _, n := range []string{"a", "b", "c"} {
		write(t, filepath.Join(dir, "svc", n+".proto"), `
syntax = "proto3";
package svc.`+n+`.v1;
import "common/v1/common.proto";
message Cfg { common.v1.Shared shared = 1; }
`)
	}
	return dir
}

// The case #153 was filed for. Three roots import one bad
// common.proto; the shell loop it replaces recompiles the closure
// three times and prints the same violation three times. One run, one
// line — with the roots that reach it, which is the part the
// repetition was carrying.
func TestCheckSchema_SharedViolationReportedOnceWithItsRoots(t *testing.T) {
	dir := schemaRepo(t, true)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "svc"),
		"--include=" + dir,
	})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	if !strings.Contains(err.Error(), "1 violation(s)") {
		t.Errorf("the summary counts deduplicated violations, got %v", err)
	}
	got := out.String()
	if n := strings.Count(got, "common.v1.Shared.labels"); n != 1 {
		t.Errorf("violation printed %d times, want 1\ngot: %s", n, got)
	}
	if !strings.Contains(got, "(via a.proto, b.proto, c.proto)") {
		t.Errorf("the reaching roots are the blast radius and must be named\ngot: %s", got)
	}
}

// A directory --schema is searched recursively, and the files it finds
// are named the way their siblings import them — protoc's -I contract.
// A name that did not match would make the same file compile twice,
// once as a root and once as another root's import, and report its
// violations under both names.
func TestCheckSchema_DirectorySearchIsRecursiveAndProtocNamed(t *testing.T) {
	dir := schemaRepo(t, true)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + dir})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	got := out.String()
	// common/v1/common.proto is nested two levels down and is itself a
	// root here (the walk finds it), so it is named relative to the
	// directory that was searched.
	if !strings.Contains(got, "common/v1/common.proto: field") {
		t.Errorf("violation should be reported under the protoc-style name\ngot: %s", got)
	}
	if n := strings.Count(got, "common.v1.Shared.labels"); n != 1 {
		t.Errorf("violation printed %d times, want 1\ngot: %s", n, got)
	}
}

// The OK line's two counts answer different questions — how much was
// asked for, and how much was covered. #148 made the file count
// load-bearing as the cheapest signal that --include is wrong; with
// several roots the covered count alone cannot carry that, because it
// no longer says how many roots produced it.
func TestCheckSchema_MultiRootOKLineReportsBothCounts(t *testing.T) {
	dir := schemaRepo(t, false)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "svc"),
		"--include=" + dir,
	}); err != nil {
		t.Fatalf("clean repo rejected: %v", err)
	}
	got := out.String()
	// 3 roots; closure is a, b, c, common.proto, annotations.proto and
	// descriptor.proto — the shared files counted once.
	if !strings.Contains(got, "OK\t3 root(s)\t6 file(s) checked") {
		t.Errorf("want both counts on the OK line\ngot: %q", got)
	}
}

// The single-file form's output is unchanged by all of the above: one
// root still prints the path the operator typed, not a count.
func TestCheckSchema_SingleRootOKLineIsUnchanged(t *testing.T) {
	dir := schemaRepo(t, false)
	schema := filepath.Join(dir, "svc", "a.proto")
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + schema, "--include=" + dir}); err != nil {
		t.Fatalf("clean schema rejected: %v", err)
	}
	if want := "OK\t" + schema + "\t4 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// --schema is repeatable, which is what a shell glob expands into.
func TestCheckSchema_SchemaFlagIsRepeatable(t *testing.T) {
	dir := schemaRepo(t, false)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "svc", "a.proto"),
		"--schema=" + filepath.Join(dir, "svc", "b.proto"),
		"--include=" + dir,
	}); err != nil {
		t.Fatalf("two roots rejected: %v", err)
	}
	if !strings.Contains(out.String(), "OK\t2 root(s)") {
		t.Errorf("want 2 roots\ngot: %q", out.String())
	}
}

// Naming the same file twice — a directory plus one of its members,
// or a repeated flag — is one root. Otherwise its violations would be
// reported twice and the root count would overstate what was asked.
func TestCheckSchema_SameFileNamedTwiceIsOneRoot(t *testing.T) {
	dir := schemaRepo(t, true)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "common"),
		"--schema=" + filepath.Join(dir, "common", "v1", "common.proto"),
		"--include=" + dir,
	})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	if !strings.Contains(err.Error(), "1 violation(s)") {
		t.Errorf("the same file named twice must not double-report, got %v", err)
	}
	if n := strings.Count(out.String(), "common.v1.Shared.labels"); n != 1 {
		t.Errorf("violation printed %d times, want 1\ngot: %s", n, out.String())
	}
}

// Two different files that would compile under one name is not
// something to resolve silently: protocompile would find whichever
// import path came first and check it twice, leaving the other
// unchecked while the run reported two roots.
func TestCheckSchema_NameCollisionAcrossRootsIsRejected(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	for _, n := range []string{"x", "y"} {
		write(t, filepath.Join(dir, n, "config.proto"), `
syntax = "proto3";
package `+n+`.v1;
message Cfg { string a = 1; }
`)
	}
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "x", "config.proto"),
		"--schema=" + filepath.Join(dir, "y", "config.proto"),
	})
	if err == nil || !strings.Contains(err.Error(), `both compile as "config.proto"`) {
		t.Fatalf("got %v, want the collision rejected", err)
	}
	if !strings.Contains(err.Error(), "--include") {
		t.Errorf("the error must name the fix, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing should be checked\ngot: %s", out.String())
	}
}

// --message is asked once of everything that was checked, not once per
// root: with several roots the message need only exist somewhere in
// the closure they span. Asking per root would fail every root that
// does not happen to import the file declaring it.
func TestCheckSchema_MessageIsAssertedAcrossTheUnionClosure(t *testing.T) {
	dir := schemaRepo(t, false)
	var out bytes.Buffer
	// svc.b.v1.Cfg is declared in b.proto, which a.proto never imports.
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "svc"),
		"--include=" + dir,
		"--message=svc.b.v1.Cfg",
	}); err != nil {
		t.Fatalf("message declared in one root must satisfy the assertion: %v", err)
	}

	out.Reset()
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "svc"),
		"--include=" + dir,
		"--message=svc.d.v1.Nope",
	})
	if err == nil || !strings.Contains(err.Error(), `find message "svc.d.v1.Nope"`) {
		t.Fatalf("got %v, want the missing message reported", err)
	}
}

// The offending file is named at the head of every violation line, so
// listing it again among the roots that reach it reads as though
// something else were meant. A root that is only guilty of its own
// violation gets no annotation at all.
func TestCheckSchema_RootIsNotListedAsReachingItself(t *testing.T) {
	dir := schemaRepo(t, true)
	// Two roots so the multi-root annotation is on: the broken file
	// itself, and one importer.
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "common", "v1", "common.proto"),
		"--schema=" + filepath.Join(dir, "svc", "a.proto"),
		"--include=" + dir,
	})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	got := strings.TrimSpace(out.String())
	if !strings.HasSuffix(got, "(via svc/a.proto)") {
		t.Errorf("want only the importing root listed\ngot: %s", got)
	}

	// And with the broken file as the only root among several that
	// reach it — here the other root is clean and imports nothing —
	// there is nothing to annotate.
	write(t, filepath.Join(dir, "svc", "standalone.proto"), `
syntax = "proto3";
package svc.standalone.v1;
message Cfg { string a = 1; }
`)
	out.Reset()
	err = checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "common", "v1", "common.proto"),
		"--schema=" + filepath.Join(dir, "svc", "standalone.proto"),
		"--include=" + dir,
	})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	if strings.Contains(out.String(), "(via") {
		t.Errorf("a root's own violation needs no via-list\ngot: %s", out.String())
	}
}
