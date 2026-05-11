// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"errors"
	"strings"
	"testing"
)

func TestSplit_NoHeader(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace_only", "  \n\n  "},
		{"comments_only", "# hi\n// also\n/* block */"},
		{"plain_body", `base_url = "x"`},
		{"at_type", "@type Foo\nx = 1"},
		{"unrelated_directive", "@something else"},
		{"headers_not_header", "@headers Foo { id = \"x\" }"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := Split([]byte(c.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m != nil {
				t.Fatalf("expected no match, got %+v", m)
			}
		})
	}
}

func TestSplit_Basic(t *testing.T) {
	in := `@header chameleon.v1.LayerHeader
{
  id = "base"
}
base_url = "x"
`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected match")
	}
	if m.Type != "chameleon.v1.LayerHeader" {
		t.Errorf("type = %q, want chameleon.v1.LayerHeader", m.Type)
	}
	if !strings.Contains(string(m.Inner), `id = "base"`) {
		t.Errorf("inner missing id assignment: %q", m.Inner)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), `base_url = "x"`) {
		t.Errorf("rest = %q", m.Rest)
	}
}

func TestSplit_LeadingTrivia(t *testing.T) {
	in := `# comment one
// comment two
/* block
   comment */

@header X {
  id = "x"
}
y = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || m.Type != "X" {
		t.Fatalf("got %+v", m)
	}
}

func TestSplit_BraceInString(t *testing.T) {
	in := `@header X {
  name = "a }} brace"
  other = "{ also }"
}
trailing = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected match")
	}
	if !strings.Contains(string(m.Inner), `"a }} brace"`) {
		t.Errorf("inner did not preserve string with braces: %q", m.Inner)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), `trailing = 1`) {
		t.Errorf("rest = %q", m.Rest)
	}
}

func TestSplit_EscapedQuote(t *testing.T) {
	in := `@header X {
  name = "with \"quote\" and }"
}
ok = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), "ok = 1") {
		t.Fatalf("rest = %q", m.Rest)
	}
}

func TestSplit_TripleQuoted(t *testing.T) {
	in := `@header X {
  body = """
  this } has } braces
  and { too }
  """
}
after = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected match")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), "after = 1") {
		t.Errorf("rest = %q", m.Rest)
	}
}

func TestSplit_BytesLiteral(t *testing.T) {
	in := `@header X {
  blob = b"aGVsbG8="
}
next = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), "next = 1") {
		t.Fatalf("got %+v", m)
	}
}

func TestSplit_BraceInLineComment(t *testing.T) {
	in := `@header X {
  # inline { } braces in comment
  id = "x"
  // also { } here
}
done = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), "done = 1") {
		t.Fatalf("got %+v", m)
	}
}

func TestSplit_BraceInBlockComment(t *testing.T) {
	in := `@header X {
  /* nested } and { braces */
  id = "x"
}
end = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), "end = 1") {
		t.Fatalf("got %+v", m)
	}
}

func TestSplit_NestedBlocks(t *testing.T) {
	in := `@header X {
  inner {
    deep { x = 1 }
  }
}
tail = 1`
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil || !strings.HasPrefix(strings.TrimSpace(string(m.Rest)), "tail = 1") {
		t.Fatalf("got %+v", m)
	}
}

func TestSplit_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"missing_type", "@header { id = \"x\" }", ErrSyntax},
		{"missing_brace", "@header X", ErrSyntax},
		{"missing_close", "@header X {\n  id = \"x\"\n", ErrUnterminated},
		{"unterminated_string", `@header X { name = "open`, ErrUnterminated},
		{"unterminated_triple", `@header X { name = """open`, ErrUnterminated},
		{"unterminated_block_comment", "@header X { /* open\n id = 1\n", ErrUnterminated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Split([]byte(c.in))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v; want %v", err, c.want)
			}
		})
	}
}

func TestSplit_EmptyRest(t *testing.T) {
	in := "@header X { id = \"x\" }"
	m, err := Split([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected match")
	}
	if len(m.Rest) != 0 {
		t.Errorf("rest = %q, want empty", m.Rest)
	}
}
