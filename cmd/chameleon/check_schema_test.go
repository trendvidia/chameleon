// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	// Named the way the repo's own files import them: --include=<dir>
	// is the -I root, so it outranks the searched directory, and
	// "svc/a.proto" is the name a sibling would write. This is the line
	// the README and the changelog fragment quote.
	if !strings.Contains(got, "(via svc/a.proto, svc/b.proto, svc/c.proto)") {
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
// asked for, and how much was covered. #144 made the file count
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

// --- review of #158 ---

// An empty --schema value is not "no --schema": it resolves to the
// working directory, which is a directory, which is now searched. The
// unset-variable spelling `--schema=$DIR` has to stay the error it was
// rather than become a green run over every .proto in the tree.
func TestCheckSchema_EmptySchemaValueIsRejected(t *testing.T) {
	dir := schemaRepo(t, true)
	t.Chdir(dir)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema="})
	if err == nil {
		t.Fatalf("an empty --schema checked the working directory instead of failing\ngot: %s", out.String())
	}
	if !strings.Contains(err.Error(), "-schema") {
		t.Errorf("the error must name the flag, got %v", err)
	}
	if strings.Contains(out.String(), "OK") {
		t.Errorf("nothing should be checked\ngot: %s", out.String())
	}
}

// Same for --include: an empty value is the working directory, which
// would silently become an import root and rename every root under it.
func TestCheckSchema_EmptyIncludeValueIsRejected(t *testing.T) {
	dir := schemaRepo(t, false)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + filepath.Join(dir, "svc", "a.proto"), "--include="})
	if err == nil || !strings.Contains(err.Error(), "-include") {
		t.Fatalf("got %v, want an empty --include rejected", err)
	}
}

// --schema=./proto/svc --include=./proto is the narrowing shape: check
// the files I own, resolve imports the way they are written. The root
// names have to come from the --include, because that is what a
// sibling's `import "svc/base.proto"` resolves to — naming them
// relative to the searched directory instead compiles base.proto twice,
// once as the root "base.proto" and once as the import "svc/base.proto",
// and the run dies on duplicate symbols without reporting anything.
func TestCheckSchema_DirSchemaUnderIncludeUsesIncludeRelativeNames(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	write(t, filepath.Join(dir, "svc", "base.proto"), `
syntax = "proto3";
package svc.v1;
import "pxf/annotations.proto";
message Base { repeated string labels = 1 [(pxf.default) = "nope"]; }
`)
	write(t, filepath.Join(dir, "svc", "a.proto"), `
syntax = "proto3";
package svc.v1;
import "svc/base.proto";
message Cfg { Base b = 1; }
`)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "svc"),
		"--include=" + dir,
	})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations (a sibling import must not compile the file twice)", err)
	}
	got := out.String()
	if !strings.Contains(got, "svc/base.proto: field") {
		t.Errorf("want the -I-relative name, which is how a.proto imports it\ngot: %s", got)
	}
	if n := strings.Count(got, "svc.v1.Base.labels"); n != 1 {
		t.Errorf("violation printed %d times, want 1\ngot: %s", n, got)
	}
}

// Naming is decided by how many distinct roots there are, so it has to
// be decided after duplicates are folded together: an accidentally
// repeated flag is still one root and must report under one name.
func TestCheckSchema_RepeatedIdenticalFlagKeepsTheSingleRootName(t *testing.T) {
	dir := schemaRepo(t, true)
	target := filepath.Join(dir, "common", "v1", "common.proto")

	var once, twice bytes.Buffer
	if err := checkSchemaCmd(&once, []string{"--schema=" + target, "--include=" + dir}); !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	if err := checkSchemaCmd(&twice, []string{"--schema=" + target, "--schema=" + target, "--include=" + dir}); !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	if once.String() != twice.String() {
		t.Errorf("naming one root twice changed the report\nonce:  %s\ntwice: %s", once.String(), twice.String())
	}
}

// pxf.ValidateFile documents its result as sorted by declaring file;
// the single-root form inherited that, and folding roots together must
// not trade it for "whichever root got there first".
func TestCheckSchema_ViolationsStaySortedByFileAcrossRoots(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	for _, n := range []string{"zz", "aa"} {
		write(t, filepath.Join(dir, n+".proto"), `
syntax = "proto3";
package `+n+`.v1;
import "pxf/annotations.proto";
message Cfg { repeated string tags = 1 [(pxf.default) = "nope"]; }
`)
	}
	var out bytes.Buffer
	// Named worst-case first: zz.proto is asked for before aa.proto.
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "zz.proto"),
		"--schema=" + filepath.Join(dir, "aa.proto"),
		"--include=" + dir,
	})
	if !errors.Is(err, errSchemaViolations) {
		t.Fatalf("got %v, want errSchemaViolations", err)
	}
	aa, zz := strings.Index(out.String(), "aa.proto:"), strings.Index(out.String(), "zz.proto:")
	if aa < 0 || zz < 0 {
		t.Fatalf("want a violation from each root\ngot: %s", out.String())
	}
	if aa > zz {
		t.Errorf("violations must be sorted by declaring file, not by root order\ngot: %s", out.String())
	}
}

// An import root is a directory, so a file is never inside itself. An
// --include that names a .proto by mistake would otherwise become that
// file's naming prefix and ask protocompile for a file called ".".
func TestCheckSchema_IncludeNamingAFileIsNotAPrefix(t *testing.T) {
	dir := schemaRepo(t, false)
	a := filepath.Join(dir, "svc", "a.proto")
	search, err := resolveSchemaRoots(
		[]string{a, filepath.Join(dir, "svc", "b.proto")},
		[]string{a},
		nil,
	)
	if err != nil {
		t.Fatalf("resolveSchemaRoots: %v", err)
	}
	for _, r := range search.roots {
		if r.name == "." || r.name == "" {
			t.Errorf("root %s got the degenerate name %q", r.path, r.name)
		}
	}
}

// A directory that is both the --schema and the --include — the form
// the README and the changelog recommend — must be searched once, not
// handed to protocompile twice.
func TestCheckSchema_ImportPathsAreDeduplicatedAcrossFlags(t *testing.T) {
	dir := schemaRepo(t, false)
	search, err := resolveSchemaRoots([]string{dir}, []string{dir, dir}, nil)
	if err != nil {
		t.Fatalf("resolveSchemaRoots: %v", err)
	}
	if len(search.importPaths) != 1 || search.importPaths[0] != dir {
		t.Errorf("importPaths = %v, want exactly [%s]", search.importPaths, dir)
	}
}

// The invocation the README's "Checking a whole schema repo" section
// prints, with the counts it claims.
func TestCheckSchema_WholeRepoFormMatchesTheDocumentedOutput(t *testing.T) {
	dir := schemaRepo(t, false)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--include=" + dir}); err != nil {
		t.Fatalf("clean repo rejected: %v", err)
	}
	// 5 roots: pxf/annotations.proto, common/v1/common.proto and the
	// three svc files. 6 files: those plus descriptor.proto.
	if want := "OK\t5 root(s)\t6 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// --- --exclude (#159) ---

// vendoredRepo is the tree #159 was filed for: the schema repo's own
// pxf/annotations.proto, plus a byte-identical vendored copy under
// third_party/. Both declare the same extensions, so compiling both as
// roots is a duplicate-symbol failure however identical they are.
func vendoredRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	write(t, filepath.Join(dir, "third_party", "pxf", "annotations.proto"), annotationsSrc)
	write(t, filepath.Join(dir, "svc", "a.proto"), `
syntax = "proto3";
package svc.a.v1;
import "pxf/annotations.proto";
message Cfg { string base_url = 1 [(pxf.default) = "x"]; }
`)
	return dir
}

// The motivating case. Without --exclude the directory form cannot be
// used at all on this tree; with it, the vendored copy stops being a
// root and the run answers the question that was asked.
func TestCheckSchema_ExcludePrunesAVendoredDuplicate(t *testing.T) {
	dir := vendoredRepo(t)
	args := []string{"--schema=" + dir, "--include=" + dir}

	var out bytes.Buffer
	err := checkSchemaCmd(&out, args)
	if err == nil || !strings.Contains(err.Error(), "already defined") {
		t.Fatalf("got %v, want the duplicate-symbol failure #159 describes", err)
	}

	out.Reset()
	if err := checkSchemaCmd(&out, append(slices.Clone(args), "--exclude=third_party")); err != nil {
		t.Fatalf("--exclude=third_party should make this tree checkable: %v", err)
	}
	// pxf/annotations.proto and svc/a.proto are roots; the closure adds
	// descriptor.proto. The vendored copy is neither a root nor
	// imported by one.
	if want := "OK\t2 root(s)\t3 file(s) checked\t1 skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Excluding a name prunes it at whatever depth it appears, which is
// what makes a subtree exclusion writable without a `**`: the walk
// stops at the directory rather than the pattern spanning the path.
func TestCheckSchema_ExcludeMatchesOneElementAtAnyDepth(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "pxf", "annotations.proto"), annotationsSrc)
	write(t, filepath.Join(dir, "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)
	// Nested three deep, under a name that appears nowhere near the root.
	write(t, filepath.Join(dir, "svc", "deep", "vendor", "dup.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=vendor"}); err != nil {
		t.Fatalf("nested vendor/ should have been pruned: %v", err)
	}
	if !strings.Contains(out.String(), "2 root(s)") {
		t.Errorf("want the two surviving roots\ngot: %q", out.String())
	}

	// A pattern with a separator in it cannot match an element, so it
	// is rejected rather than silently matching nothing.
	out.Reset()
	err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=svc/deep/vendor"})
	if err == nil || !strings.Contains(err.Error(), "cannot contain") {
		t.Fatalf("got %v, want a path-separator rejection", err)
	}
}

// Within one element the usual filepath.Match syntax applies.
func TestCheckSchema_ExcludeMatchesWildcardsWithinAnElement(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)
	write(t, filepath.Join(dir, "sub", "a_test.proto"), `
syntax = "proto3";
package atest.v1;
message ATest { string x = 1; }
`)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=*_test.proto"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// One root survives, and the line still says so rather than
	// switching to the single-file form: --schema named a directory,
	// and what the operator asked for does not move under a pattern
	// (#166). The skipped count is why the narrowing is visible at all.
	if want := "OK\t1 root(s)\t1 file(s) checked\t1 skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	// A malformed pattern fails the run, naming the flag and the
	// pattern, rather than quietly matching nothing. Both spellings are
	// pinned because they fail in different places: "[bad" is bad from
	// its first character, while "a_test*[0-9" only gives itself away
	// once a name gets past the leading chunk — filepath.Match stops
	// scanning where a chunk fails, so Match("a_test*[0-9", "x") is
	// (false, nil) and no single probe at flag-parse time can tell.
	for _, pattern := range []string{"[bad", "a_test*[0-9"} {
		out.Reset()
		err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=" + pattern})
		if err == nil || !strings.Contains(err.Error(), "-exclude") || !strings.Contains(err.Error(), pattern) {
			t.Errorf("--exclude=%s: got %v, want the bad pattern reported by name", pattern, err)
		}
	}
}

// --exclude is repeatable — the flag help, the zsh completion and the
// fish completion all say so. Pinned because collapsing it to a single
// value (one fs.String, say) would keep every other test in this file
// green while honouring only the last flag.
func TestCheckSchema_ExcludeIsRepeatable(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "keep.proto"), `
syntax = "proto3";
package keep.v1;
message Keep { string x = 1; }
`)
	write(t, filepath.Join(dir, "vendor", "a.proto"), `
syntax = "proto3";
package vend.v1;
message A { string x = 1; }
`)
	write(t, filepath.Join(dir, "third_party", "b.proto"), `
syntax = "proto3";
package tp.v1;
message B { string x = 1; }
`)

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + dir, "--exclude=vendor", "--exclude=third_party",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both pruned, so the one surviving root takes the single-root line.
	if want := "OK\t1 root(s)\t1 file(s) checked\t2 skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q (only the last --exclude was honoured?)", out.String(), want)
	}
}

// An --exclude that prunes every root has to say so. Reporting "no
// .proto files found under this directory" sends the operator off to
// debug a path that is plainly full of them — the same misdiagnosis
// the walk root is already protected from.
func TestCheckSchema_ExcludeThatPrunesEverythingBlamesThePattern(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sub", "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)

	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=*.proto"})
	if err == nil || !strings.Contains(err.Error(), "--exclude pruned all 1 .proto file(s)") {
		t.Fatalf("got %v, want the pattern blamed rather than the directory", err)
	}

	// A directory that really holds nothing still blames the directory:
	// the patterns are innocent there.
	out.Reset()
	err = checkSchemaCmd(&out, []string{"--schema=" + t.TempDir(), "--exclude=*.proto"})
	if err == nil || !strings.Contains(err.Error(), "no .proto files found") {
		t.Fatalf("got %v, want the empty-directory error", err)
	}
}

// Decision 2 of #159: --exclude prunes roots, not import paths. The
// vendored tree stays importable — which is the point, since the files
// you own may legitimately import it.
func TestCheckSchema_ExcludeDoesNotAffectInclude(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "third_party", "common.proto"), `
syntax = "proto3";
package common.v1;
message Shared { string tenant = 1; }
`)
	write(t, filepath.Join(dir, "app.proto"), `
syntax = "proto3";
package app.v1;
import "third_party/common.proto";
message Cfg { common.v1.Shared shared = 1; }
`)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + dir, "--include=" + dir, "--exclude=third_party",
	}); err != nil {
		t.Fatalf("an excluded file must stay importable: %v", err)
	}
	// One root (app.proto); the closure still counts the import it
	// resolved out of the excluded directory.
	if want := "OK\t1 root(s)\t2 file(s) checked\t1 skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Decision 4 of #159: naming a file explicitly is the more specific
// instruction, so --exclude does not apply to it — nor to the --schema
// directory itself, which would otherwise read as "check nothing".
func TestCheckSchema_ExcludeDoesNotApplyToWhatSchemaNames(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "own.proto"), `
syntax = "proto3";
package own.v1;
message Own { string x = 1; }
`)
	write(t, filepath.Join(dir, "third_party", "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)

	// The load-bearing shape is one run that both searches a directory
	// and names a file inside the subtree that search prunes: only the
	// walk is filtered, so the file the operator pointed at survives.
	// An --exclude applied to the assembled candidate list instead
	// would pass a test that never enters the walk at all, and drop the
	// second root here. Both orders, since first mention decides how a
	// duplicate is named.
	for name, order := range map[string][]string{
		"file named directly, after the directory":  {"--schema=" + dir, "--schema=" + filepath.Join(dir, "third_party", "a.proto")},
		"file named directly, before the directory": {"--schema=" + filepath.Join(dir, "third_party", "a.proto"), "--schema=" + dir},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			args := append(slices.Clone(order), "--include="+dir, "--exclude=third_party")
			if err := checkSchemaCmd(&out, args); err != nil {
				t.Fatalf("an explicitly named schema must still be checked: %v", err)
			}
			if want := "OK\t2 root(s)\t2 file(s) checked\n"; out.String() != want {
				t.Errorf("output = %q, want %q", out.String(), want)
			}
		})
	}

	t.Run("the searched directory itself", func(t *testing.T) {
		var out bytes.Buffer
		if err := checkSchemaCmd(&out, []string{
			"--schema=" + filepath.Join(dir, "third_party"),
			"--exclude=third_party",
		}); err != nil {
			t.Fatalf("excluding the directory just named must not empty the search: %v", err)
		}
		if !strings.HasPrefix(out.String(), "OK\t") {
			t.Errorf("want the directory searched\ngot: %q", out.String())
		}
	})
}

// Decision 3 of #159: no defaults. A conventionally-named directory is
// walked like any other until someone asks for it to be skipped —
// dot-directories included, which is what a walk that "helpfully"
// skipped hidden entries would get wrong. That vendor/ and
// third_party/ are not skipped implicitly is already what the first
// half of TestCheckSchema_ExcludePrunesAVendoredDuplicate asserts, so
// this pins the names that test does not use rather than paying for
// the duplicate-symbol compile a second time.
func TestCheckSchema_NothingIsExcludedByDefault(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "own.proto"), `
syntax = "proto3";
package own.v1;
message Own { string x = 1; }
`)
	write(t, filepath.Join(dir, "vendor", "v.proto"), `
syntax = "proto3";
package vend.v1;
message V { string x = 1; }
`)
	write(t, filepath.Join(dir, ".hidden", "h.proto"), `
syntax = "proto3";
package hidden.v1;
message H { string x = 1; }
`)

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + dir}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t3 root(s)\t3 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q (vendor/ and .hidden/ are roots until excluded)", out.String(), want)
	}
}

// An empty --exclude is the same class of mistake as an empty --schema:
// `--exclude=$SKIP` with SKIP unset would otherwise be a pattern that
// matches an empty name and quietly does nothing.
func TestCheckSchema_EmptyExcludeIsRejected(t *testing.T) {
	dir := schemaRepo(t, false)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude="})
	if err == nil || !strings.Contains(err.Error(), "empty pattern") {
		t.Fatalf("got %v, want an empty-pattern rejection", err)
	}
}

// TestCheckSchema_MalformedExcludeMatchingNothingIsAccepted pins the
// one case the match-site syntax check cannot reach, as a documented
// divergence rather than an assumption.
//
// filepath.Match stops scanning at the first chunk that fails the
// name, so a malformed pattern is only diagnosed once something in the
// tree matches everything before the bad syntax. "vendor*[0-9" is
// caught in a tree containing a "vendor" element and accepted in one
// that does not.
//
// That is sound for the run in front of you — a pattern whose leading
// chunk matches nothing could not have pruned anything however it was
// completed — but it is not sound across trees: the same CI invocation
// starts failing the day someone adds a directory whose name matches
// the leading chunk. Catching it up front needs a standalone validator
// for Match's syntax, which means reimplementing its scanner; a fixed
// probe name provably cannot do it.
//
// If this test starts failing because the pattern is now rejected up
// front, that is an improvement — delete it.
func TestCheckSchema_MalformedExcludeMatchingNothingIsAccepted(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)

	// Caught: the tree has a "vendor" element, so Match gets past the
	// first chunk and reaches the unterminated class.
	write(t, filepath.Join(dir, "vendor", "b.proto"), `
syntax = "proto3";
package b.v1;
message B { string x = 1; }
`)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=vendor*[0-9"})
	if err == nil || !strings.Contains(err.Error(), "syntax error in pattern") {
		t.Fatalf("got %v, want the malformed pattern reported", err)
	}

	// Not caught: nothing in this tree matches the leading chunk, so
	// Match never scans as far as the bad syntax. The run proceeds and
	// the pattern prunes nothing — which is what it would have done
	// well-formed, hence no wrong answer, only a deferred complaint.
	out.Reset()
	if err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=zzznotpresent*[0-9"}); err != nil {
		t.Fatalf("divergence changed: got %v, want the pattern accepted", err)
	}
	if !strings.Contains(out.String(), "2 root(s)") {
		t.Errorf("nothing should have been pruned\ngot: %q", out.String())
	}
}

// --- the OK line's skipped count and format rule (#166) ---

// The format follows what --schema NAMED, not how many roots came out
// of it. Before #166 a pattern that narrowed a directory search to one
// root flipped the line to the single-file form, so a CI job matching
// `N root(s)` stopped matching at exactly the moment a pattern
// over-pruned — the failure it was watching for.
func TestCheckSchema_OKLineShapeFollowsWhatSchemaNamed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "keep.proto"), `
syntax = "proto3";
package keep.v1;
message K { string x = 1; }
`)
	write(t, filepath.Join(dir, "vendor", "drop.proto"), `
syntax = "proto3";
package drop.v1;
message D { string x = 1; }
`)

	t.Run("directory narrowed to one root keeps the root(s) form", func(t *testing.T) {
		var out bytes.Buffer
		if err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=vendor"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "OK\t1 root(s)\t1 file(s) checked\t1 skipped\n"; out.String() != want {
			t.Errorf("output = %q, want %q", out.String(), want)
		}
	})

	t.Run("one file named directly keeps the single-file form", func(t *testing.T) {
		schema := filepath.Join(dir, "keep.proto")
		var out bytes.Buffer
		if err := checkSchemaCmd(&out, []string{"--schema=" + schema}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "OK\t" + schema + "\t1 file(s) checked\n"; out.String() != want {
			t.Errorf("output = %q, want %q", out.String(), want)
		}
	})

	t.Run("two files named directly use the root(s) form", func(t *testing.T) {
		var out bytes.Buffer
		if err := checkSchemaCmd(&out, []string{
			"--schema=" + filepath.Join(dir, "keep.proto"),
			"--schema=" + filepath.Join(dir, "vendor", "drop.proto"),
		}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "OK\t2 root(s)\t2 file(s) checked\n"; out.String() != want {
			t.Errorf("output = %q, want %q", out.String(), want)
		}
	})
}

// The count is of files the run did not look at, not of files the
// patterns matched. A file pruned from the walk and then named directly
// by another --schema is checked, and counting it would report a gap
// that does not exist.
func TestCheckSchema_SkippedCountExcludesFilesCheckedAnotherWay(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "keep.proto"), `
syntax = "proto3";
package keep.v1;
message K { string x = 1; }
`)
	for _, n := range []string{"a", "b"} {
		write(t, filepath.Join(dir, "vendor", n+".proto"), `
syntax = "proto3";
package `+n+`.v1;
message M`+n+` { string x = 1; }
`)
	}

	// Both vendor files pruned, neither named: 2 skipped.
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + dir, "--exclude=vendor"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t1 root(s)\t1 file(s) checked\t2 skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}

	// One of them named directly: pruned from the walk, checked anyway,
	// so only the other is skipped.
	out.Reset()
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + dir,
		"--schema=" + filepath.Join(dir, "vendor", "a.proto"),
		"--exclude=vendor",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t2 root(s)\t2 file(s) checked\t1 skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A run that excludes nothing prints exactly the line it always did —
// the count appears only when there is something to report, so the
// documented output is unchanged for everyone not using --exclude.
func TestCheckSchema_NoSkippedFieldWithoutExclusions(t *testing.T) {
	dir := schemaRepo(t, false)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + filepath.Join(dir, "svc"), "--include=" + dir}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t3 root(s)\t6 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	// And an --exclude that matches nothing is the same: no gap, no field.
	out.Reset()
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "svc"), "--include=" + dir, "--exclude=nothing-matches-this",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t3 root(s)\t6 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// --- following symlinks (#167) ---

// linkedRepo builds the layout #167 was filed for: a directory the
// operator names, with a subtree symlinked into it from outside.
func linkedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "proto", "top.proto"), `
syntax = "proto3";
package top.v1;
message T { string x = 1; }
`)
	write(t, filepath.Join(dir, "elsewhere", "ext", "e.proto"), `
syntax = "proto3";
package ext.v1;
message E { string x = 1; }
`)
	if err := os.Symlink(filepath.Join(dir, "elsewhere", "ext"), filepath.Join(dir, "proto", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return dir
}

// The motivating case. Before #167 the linked subtree was not searched
// and the run said OK over a narrower set than the operator named,
// with the file count the only hint.
func TestCheckSchema_FollowsSymlinkedDirectories(t *testing.T) {
	dir := linkedRepo(t)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + filepath.Join(dir, "proto")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t2 root(s)\t2 file(s) checked\t1 linked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Decision 3 of #167: a symlinked .proto file, not only a directory.
func TestCheckSchema_FollowsSymlinkedFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "proto", "top.proto"), `
syntax = "proto3";
package top.v1;
message T { string x = 1; }
`)
	write(t, filepath.Join(dir, "outside", "e.proto"), `
syntax = "proto3";
package ext.v1;
message E { string x = 1; }
`)
	if err := os.Symlink(filepath.Join(dir, "outside", "e.proto"), filepath.Join(dir, "proto", "e.proto")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + filepath.Join(dir, "proto")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t2 root(s)\t2 file(s) checked\t1 linked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A link pointing at an ancestor is the reason WalkDir does not follow
// by default. The search must terminate, and must not report the files
// it already walked a second time.
func TestCheckSchema_SymlinkCycleTerminates(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "proto")
	write(t, filepath.Join(root, "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	done := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		if err := checkSchemaCmd(&out, []string{"--schema=" + root}); err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- out.String()
	}()
	select {
	case got := <-done:
		if want := "OK\t1 root(s)\t1 file(s) checked\n"; got != want {
			t.Errorf("output = %q, want %q (the cycle must add nothing)", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("search did not terminate on a symlink cycle")
	}
}

// One file, one name. A file reachable both directly and through a
// link must be compiled once: twice would be two descriptors for one
// file, which is the duplicate-symbol failure the naming rules exist
// to prevent.
func TestCheckSchema_FileReachableTwiceIsCheckedOnce(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "proto")
	write(t, filepath.Join(root, "real", "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + root}); err != nil {
		t.Fatalf("the same file under two paths must not collide: %v", err)
	}
	// One root, and no "linked" — the direct path is walked first, so
	// the file is not reached by crossing a link.
	if want := "OK\t1 root(s)\t1 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// --exclude tests the link's own name, which is what the operator sees
// in the tree — not the name of whatever it points at.
func TestCheckSchema_ExcludeMatchesTheLinkName(t *testing.T) {
	dir := linkedRepo(t)
	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "proto"), "--exclude=linked",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t1 root(s)\t1 file(s) checked\t1 skipped\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A dangling link names nothing. It must not fail the run, and must
// not be counted as anything.
func TestCheckSchema_DanglingSymlinkIsSkipped(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "proto")
	write(t, filepath.Join(root, "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)
	if err := os.Symlink(filepath.Join(dir, "no", "such", "target.proto"), filepath.Join(root, "b.proto")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + root}); err != nil {
		t.Fatalf("a dangling link must not fail the run: %v", err)
	}
	if want := "OK\t1 root(s)\t1 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// The real path has to win the name globally, not just against a link
// sitting in the same directory. Here the link is one level down in a
// sibling that sorts FIRST, so a search that split real-before-linked
// per directory reaches zzz through aaa/l before it reaches zzz
// itself: the file is then named "aaa/l/x.proto" while y.proto's
// import of "zzz/x.proto" compiles a second copy, and the run dies on
// duplicate symbols.
func TestCheckSchema_RealPathWinsOverALinkInAnEarlierSibling(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "proto")
	write(t, filepath.Join(root, "zzz", "x.proto"), `
syntax = "proto3";
package zzz.v1;
message X { string x = 1; }
`)
	write(t, filepath.Join(root, "zzz", "y.proto"), `
syntax = "proto3";
package zzz.v1;
import "zzz/x.proto";
message Y { X x = 1; }
`)
	if err := os.MkdirAll(filepath.Join(root, "aaa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "zzz"), filepath.Join(root, "aaa", "l")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + root}); err != nil {
		t.Fatalf("the link must not take the name off the real path: %v", err)
	}
	// No "linked": both files lie in the tree's real paths, whatever
	// order the walk happens to meet them in.
	if want := "OK\t2 root(s)\t2 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// One file, one root, across ARGUMENTS as well as within one search:
// two --schema directories each linked to the same shared tree reach
// the same file under two names, and compiling both is the
// duplicate-symbol failure the naming rules exist to prevent.
func TestCheckSchema_OneFileThroughTwoArgumentsIsOneRoot(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "shared", "s.proto"), `
syntax = "proto3";
package shared.v1;
message S { string x = 1; }
`)
	for _, d := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "shared"), filepath.Join(dir, "a", "l")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "shared"), filepath.Join(dir, "b", "m")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "a"),
		"--schema=" + filepath.Join(dir, "b"),
	})
	if err != nil {
		t.Fatalf("two links to one file must not become two roots: %v", err)
	}
	// One linked file, counted once — not once per argument that
	// crossed a link to it.
	if want := "OK\t1 root(s)\t1 file(s) checked\t1 linked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// The same directory twice is one search's worth of everything,
// including the linked count: a field that can exceed the file count
// is not a field anything can parse.
func TestCheckSchema_RepeatedSchemaDoesNotDoubleCountLinked(t *testing.T) {
	dir := linkedRepo(t)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "proto"),
		"--schema=" + filepath.Join(dir, "proto"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t2 root(s)\t2 file(s) checked\t1 linked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Naming a file directly is a route to it that crosses no link, so it
// cancels the linked count a directory search claimed for the same
// file — the same resolution `skipped` already gets.
func TestCheckSchema_NamingALinkedFileDirectlyCancelsLinked(t *testing.T) {
	dir := linkedRepo(t)
	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "proto"),
		"--schema=" + filepath.Join(dir, "elsewhere", "ext", "e.proto"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t2 root(s)\t2 file(s) checked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Excluding a directory can leave a link as the only route into it.
// The file is then CHECKED, under the link's name — so reporting it as
// skipped as well would have the OK line claim a file the run did look
// at was one it did not.
func TestCheckSchema_ExcludeDoesNotSkipWhatALinkStillReaches(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "proto")
	write(t, filepath.Join(root, "real", "a.proto"), `
syntax = "proto3";
package a.v1;
message A { string x = 1; }
`)
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var out bytes.Buffer
	if err := checkSchemaCmd(&out, []string{"--schema=" + root, "--exclude=real"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t1 root(s)\t1 file(s) checked\t1 linked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Both trailing fields on one line, in the documented order.
func TestCheckSchema_SkippedAndLinkedCompose(t *testing.T) {
	dir := linkedRepo(t)
	write(t, filepath.Join(dir, "proto", "vendor", "v.proto"), `
syntax = "proto3";
package vendor.v1;
message V { string x = 1; }
`)

	var out bytes.Buffer
	err := checkSchemaCmd(&out, []string{
		"--schema=" + filepath.Join(dir, "proto"), "--exclude=vendor",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "OK\t2 root(s)\t2 file(s) checked\t1 skipped\t1 linked\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}
