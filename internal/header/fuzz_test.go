// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"testing"
)

// FuzzParse exercises [Parse] against arbitrary bytes. The function
// reads what the PXF lexer collected as the body of a
// `@header chameleon.v1.LayerHeader { ... }` directive — bytes the
// author wrote in their PXF source. They're attacker-influenced for
// any pipeline that loads chain layers from a network or untrusted
// filesystem, so Parse must:
//
//   - Never panic, regardless of input shape.
//   - Return an error (not a partial Parsed) for malformed input.
//   - Honor the chameleon.v1.LayerHeader schema; never expose fields
//     outside it.
//
// On success, the test also runs Check against a fixed Ref to
// exercise the post-Parse code path; that must not panic either,
// though it may report a mismatch error (the seed Ref is chosen so
// some seeds succeed-and-match while others succeed-and-mismatch).
//
// The seed corpus mixes legitimate headers, header-shaped inputs
// with weird-but-valid content, and adversarial fragments (unclosed
// strings, invalid UTF-8, unknown fields, deeply nested
// constructions). go fuzz will then mutate from these.
func FuzzParse(f *testing.F) {
	seeds := [][]byte{
		// Empty bytes — three-state semantics: no fields set.
		[]byte(""),
		// Minimal valid header.
		[]byte(`id = "base"`),
		// All known fields populated.
		[]byte(`id = "base"
environment = "prod"
encrypted = true
notes = "rotated 2026-05"
content_sha256 = "sha256:abc"`),
		// Just a comment — PXF allows leading comments.
		[]byte(`# only a comment`),
		// Whitespace-only.
		[]byte("   \n  \t  \n"),
		// Field absent from the LayerHeader schema — Parse should
		// surface this as a decode error, NOT silently accept.
		[]byte(`unknown_field = "x"`),
		// Quoted strings with embedded escapes.
		[]byte(`notes = "line1\nline2"`),
		// Empty string assignments.
		[]byte(`id = ""
environment = ""`),
		// Boolean values both ways.
		[]byte(`encrypted = false`),
		// Unicode in notes — PXF strings are UTF-8.
		[]byte(`notes = "résumé"`),
		// Repeated assignment of the same field — last write wins,
		// per proto semantics.
		[]byte(`id = "a"
id = "b"`),
		// Malformed: unterminated string.
		[]byte(`notes = "unterminated`),
		// Malformed: missing `=`.
		[]byte(`id "base"`),
		// Malformed: bare RHS, no key.
		[]byte(`= "lhs missing"`),
		// Malformed: lone closing brace.
		[]byte(`}`),
		// Malformed: directive-shaped content inside a body.
		[]byte(`@type = "evil"`),
		// Invalid UTF-8 bytes in a string.
		[]byte(`notes = "` + "\xff\xfe" + `"`),
		// Very long string field — bounded path through the parser.
		[]byte(`notes = "` + string(make([]byte, 1<<10)) + `"`),
		// Deeply repeated assignments.
		[]byte(`id = "a"
id = "b"
id = "c"
id = "d"`),
		// Just a sub-block — LayerHeader has no message-typed
		// fields, so this should error cleanly.
		[]byte(`sub { foo = "bar" }`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	// Fixed Ref used for the Check call on successful parses. Some
	// seeds will agree, others won't — both branches are exercised
	// without panicking.
	ref := Ref{EntryID: "base", Env: "prod", Encrypted: true}

	f.Fuzz(func(t *testing.T, body []byte) {
		// Property 1: Parse must never panic for any input.
		p, err := Parse(body)
		if err != nil {
			// Error path is allowed; ensure no panic when callers
			// inspect the error string.
			_ = err.Error()
			return
		}
		// Property 2: a successful Parse returns a non-nil result.
		if p == nil {
			t.Fatalf("Parse returned (nil, nil) for body %q — invariant violation", body)
		}
		// Property 3: Check must not panic regardless of whether
		// the header matches ref. It may return an error (mismatch)
		// — that's fine.
		_ = p.Check(ref)
	})
}
