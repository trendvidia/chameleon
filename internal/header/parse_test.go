// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"strings"
	"testing"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func TestParse_Empty(t *testing.T) {
	p, err := Parse([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{EntryID: "base", Env: "dev", Encrypted: false}); err != nil {
		t.Fatalf("empty header should pass any Ref check, got %v", err)
	}
}

func TestParse_AllFields(t *testing.T) {
	in := `
id = "base"
environment = "dev"
encrypted = false
notes = "the base layer"
content_sha256 = "abc123"
`
	p, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{EntryID: "base", Env: "dev", Encrypted: false}); err != nil {
		t.Fatalf("Check failed: %v", err)
	}
}

func TestCheck_IDMismatch(t *testing.T) {
	p, err := Parse([]byte(`id = "env"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = p.Check(Ref{EntryID: "base"})
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("expected id mismatch error, got %v", err)
	}
}

func TestCheck_EnvironmentMismatch(t *testing.T) {
	p, err := Parse([]byte(`environment = "prod"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = p.Check(Ref{Env: "dev"})
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("expected environment mismatch error, got %v", err)
	}
}

func TestCheck_EncryptedMismatch(t *testing.T) {
	p, err := Parse([]byte(`encrypted = true`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = p.Check(Ref{Encrypted: false})
	if err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("expected encrypted mismatch error, got %v", err)
	}
}

func TestCheck_EncryptedFalseExplicit(t *testing.T) {
	// `encrypted = false` is *present* (three-state). If the chain
	// entry is encrypted=true, this must fail — proving we use Result
	// presence, not the zero value.
	p, err := Parse([]byte(`encrypted = false`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{Encrypted: true}); err == nil {
		t.Fatal("expected mismatch error for encrypted=false vs Ref.Encrypted=true")
	}
}

func TestCheck_AbsentFieldNotChecked(t *testing.T) {
	// If author omitted `environment`, the check ignores it regardless
	// of Ref.Env.
	p, err := Parse([]byte(`id = "base"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{EntryID: "base", Env: "anything"}); err != nil {
		t.Fatalf("Check should ignore absent environment, got %v", err)
	}
}

// HandleDirectives tests. The function was at 0% direct coverage —
// exercised only indirectly via the resolver's loadAndParse flow,
// which means a regression in the "unknown directive" / "wrong type"
// branches would not have surfaced in the package's own test suite.

func TestHandleDirectives_NoDirectives(t *testing.T) {
	// Empty directive slice — the most common case (a layer file
	// with no @header preamble). Must return nil without touching
	// the descriptor cache or any parse machinery.
	if err := HandleDirectives(nil, Ref{EntryID: "base", Env: "dev"}); err != nil {
		t.Errorf("HandleDirectives(nil, ...) = %v, want nil", err)
	}
	if err := HandleDirectives([]pxf.Directive{}, Ref{}); err != nil {
		t.Errorf("HandleDirectives([], ...) = %v, want nil", err)
	}
}

func TestHandleDirectives_WellFormedHeaderPasses(t *testing.T) {
	// A single @header that matches the Ref. Pins the happy path:
	// parse → check → return nil.
	dirs := []pxf.Directive{{
		Name: DirectiveName,
		Type: TypeName,
		Body: []byte(`id = "base"
environment = "dev"
encrypted = false
`),
	}}
	if err := HandleDirectives(dirs, Ref{EntryID: "base", Env: "dev", Encrypted: false}); err != nil {
		t.Errorf("well-formed header should pass; got %v", err)
	}
}

func TestHandleDirectives_UnknownDirectiveRejected(t *testing.T) {
	// chameleon owns the @<name> vocabulary. Anything other than
	// @header must be a hard error — silently accepting unknown
	// directives would let typos like @heaer / @headr through
	// without warning, defeating the point of the preamble.
	dirs := []pxf.Directive{{
		Name: "bogus",
		Type: TypeName,
		Body: []byte(``),
	}}
	err := HandleDirectives(dirs, Ref{})
	if err == nil {
		t.Fatal("expected error for unknown directive name")
	}
	if !strings.Contains(err.Error(), "unknown directive") {
		t.Errorf("error should identify the unknown-directive case, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should echo the offending name, got: %v", err)
	}
}

func TestHandleDirectives_WrongTypeRejected(t *testing.T) {
	// @header is bound to chameleon.v1.LayerHeader. A `@header
	// some.other.Type { ... }` block is structurally suspicious —
	// the consumer may have copy-pasted a different schema's
	// directive. Hard-fail rather than guess.
	dirs := []pxf.Directive{{
		Name: DirectiveName,
		Type: "some.other.Type",
		Body: []byte(``),
	}}
	err := HandleDirectives(dirs, Ref{})
	if err == nil {
		t.Fatal("expected error for @header with non-LayerHeader type")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Errorf("error should identify the type mismatch, got: %v", err)
	}
}

func TestHandleDirectives_ParseFailureSurfaces(t *testing.T) {
	// A @header body that's not valid PXF must propagate as a
	// "parse" error — surfacing the underlying decoder message so
	// the operator sees WHICH line / token offended.
	dirs := []pxf.Directive{{
		Name: DirectiveName,
		Type: TypeName,
		Body: []byte(`id = "missing close quote`),
	}}
	err := HandleDirectives(dirs, Ref{})
	if err == nil {
		t.Fatal("expected error for malformed @header body")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should identify the parse failure, got: %v", err)
	}
}

func TestHandleDirectives_CheckFailureSurfaces(t *testing.T) {
	// A @header that parses cleanly but contradicts the Ref must
	// surface the field-level mismatch — the Check call's error
	// wording carries which field disagreed.
	dirs := []pxf.Directive{{
		Name: DirectiveName,
		Type: TypeName,
		Body: []byte(`id = "wrong-id"`),
	}}
	err := HandleDirectives(dirs, Ref{EntryID: "base"})
	if err == nil {
		t.Fatal("expected mismatch error for @header.id vs Ref.EntryID")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error should mention the id mismatch, got: %v", err)
	}
}

func TestHandleDirectives_FirstErrorStopsIteration(t *testing.T) {
	// Two directives: the first one bad, the second well-formed.
	// HandleDirectives must return on the first error rather than
	// concatenating — keeps the error UX simple (one error per
	// invocation) and avoids hiding the root cause behind a wall of
	// downstream noise.
	dirs := []pxf.Directive{
		{Name: "unknown1", Type: "", Body: []byte(``)},
		{Name: DirectiveName, Type: TypeName, Body: []byte(`id = "base"`)},
	}
	err := HandleDirectives(dirs, Ref{EntryID: "base"})
	if err == nil {
		t.Fatal("expected error from first unknown directive")
	}
	if !strings.Contains(err.Error(), "unknown1") {
		t.Errorf("error should identify the FIRST offending directive, got: %v", err)
	}
}
