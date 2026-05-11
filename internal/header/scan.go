// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package header parses the optional `@header chameleon.v1.LayerHeader
// { ... }` preamble that may appear at the top of a layer file before
// the schema-typed body.
//
// protowire-go's PXF lexer only recognizes the `@type` directive; any
// other `@<name>` returns ILLEGAL. Until protowire-go grows generic
// `@<directive>` grammar (tracked in chameleon ROADMAP.md), chameleon
// peels the header block off the byte stream itself and hands the
// remainder to `pxf.UnmarshalFull` against the consumer's schema. The
// scanner here mirrors PXF tokenization rules for strings, comments,
// and escapes so braces inside string literals or comments don't fool
// the brace matcher.
package header

import (
	"errors"
	"fmt"
)

var (
	// ErrUnterminated indicates the header block is missing its closing
	// `}`, or a string/comment inside the block is unterminated.
	ErrUnterminated = errors.New("header: unterminated block")
	// ErrSyntax indicates a malformed @header preamble: missing type
	// name, missing `{`, etc.
	ErrSyntax = errors.New("header: syntax error")
)

// Match describes a successful `@header` extraction.
type Match struct {
	// Type is the type name from `@header <type>`, e.g.
	// "chameleon.v1.LayerHeader".
	Type string
	// Inner is the bytes BETWEEN the opening `{` and the matching `}`
	// (both exclusive). Suitable for direct PXF unmarshal.
	Inner []byte
	// Rest is data with the entire `@header ... { ... }` preamble
	// removed — the schema-typed body the caller should parse against
	// the consumer's message.
	Rest []byte
}

// Split scans the first non-trivia token in data. If it's `@header`,
// the function extracts the full `@header <type> { ... }` preamble and
// returns a *Match. Otherwise it returns (nil, nil) and the caller
// should parse data as-is.
//
// An error is returned only when `@header` is present but malformed:
// missing type name, missing brace, unterminated string/comment, etc.
//
// Whitespace and `# ... ` / `// ...` / `/* ... */` comments before
// `@header` are allowed and skipped. The scanner mirrors PXF
// tokenization rules so `}` inside `"..."` / `"""..."""` / `b"..."` /
// comments does not close the block prematurely.
func Split(data []byte) (*Match, error) {
	i := skipTrivia(data, 0)
	if i >= len(data) || data[i] != '@' {
		return nil, nil
	}

	const tag = "header"
	tagEnd := i + 1 + len(tag)
	if tagEnd > len(data) || string(data[i+1:tagEnd]) != tag {
		return nil, nil
	}
	if tagEnd < len(data) && isIdentPart(data[tagEnd]) {
		return nil, nil
	}

	// Type name follows after at least one space/tab/newline/comment.
	k := skipTrivia(data, tagEnd)
	if k == tagEnd {
		return nil, fmt.Errorf("%w: expected whitespace after @header", ErrSyntax)
	}
	typeStart := k
	for k < len(data) && isIdentPart(data[k]) {
		k++
	}
	if k == typeStart {
		return nil, fmt.Errorf("%w: expected type name after @header", ErrSyntax)
	}
	typeName := string(data[typeStart:k])

	k = skipTrivia(data, k)
	if k >= len(data) || data[k] != '{' {
		return nil, fmt.Errorf("%w: expected '{' after @header %s", ErrSyntax, typeName)
	}
	innerStart := k + 1
	end, err := findClosingBrace(data, innerStart)
	if err != nil {
		return nil, err
	}
	return &Match{
		Type:  typeName,
		Inner: data[innerStart:end],
		Rest:  data[end+1:],
	}, nil
}

// skipTrivia advances past PXF whitespace (` `, `\t`, `\r`, `\n`) and
// comments (`#` line, `//` line, `/* */` block). Returns the new
// offset.
func skipTrivia(data []byte, i int) int {
	for i < len(data) {
		ch := data[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n':
			i++
		case ch == '#':
			i = skipToEOL(data, i+1)
		case ch == '/' && i+1 < len(data) && data[i+1] == '/':
			i = skipToEOL(data, i+2)
		case ch == '/' && i+1 < len(data) && data[i+1] == '*':
			j := i + 2
			closed := false
			for j+1 < len(data) {
				if data[j] == '*' && data[j+1] == '/' {
					j += 2
					closed = true
					break
				}
				j++
			}
			if !closed {
				return len(data)
			}
			i = j
		default:
			return i
		}
	}
	return i
}

func skipToEOL(data []byte, i int) int {
	for i < len(data) && data[i] != '\n' {
		i++
	}
	return i
}

// findClosingBrace finds the `}` that matches an already-consumed `{`.
// start is the byte index AFTER the opening brace; the returned index
// is the byte index OF the matching close brace. Brace counting
// respects strings, bytes literals, and comments.
func findClosingBrace(data []byte, start int) (int, error) {
	depth := 1
	i := start
	for i < len(data) {
		ch := data[i]
		switch {
		case ch == '{':
			depth++
			i++
		case ch == '}':
			depth--
			if depth == 0 {
				return i, nil
			}
			i++
		case ch == '"':
			j, err := skipString(data, i)
			if err != nil {
				return 0, err
			}
			i = j
		case ch == 'b' && i+1 < len(data) && data[i+1] == '"':
			j, err := skipBytesLit(data, i)
			if err != nil {
				return 0, err
			}
			i = j
		case ch == '#':
			i = skipToEOL(data, i+1)
		case ch == '/' && i+1 < len(data) && data[i+1] == '/':
			i = skipToEOL(data, i+2)
		case ch == '/' && i+1 < len(data) && data[i+1] == '*':
			j := i + 2
			closed := false
			for j+1 < len(data) {
				if data[j] == '*' && data[j+1] == '/' {
					j += 2
					closed = true
					break
				}
				j++
			}
			if !closed {
				return 0, fmt.Errorf("%w: unterminated block comment", ErrUnterminated)
			}
			i = j
		default:
			i++
		}
	}
	return 0, fmt.Errorf("%w: missing '}'", ErrUnterminated)
}

// skipString advances past a "..." or """...""" string literal,
// returning the byte index immediately after the closing quote(s).
// Single-quoted strings honor `\<x>` escape sequences (so `\"` does
// not end the string). Triple-quoted strings are scanned literally
// until the next `"""` — matching the PXF lexer's behavior.
func skipString(data []byte, i int) (int, error) {
	if i+2 < len(data) && data[i+1] == '"' && data[i+2] == '"' {
		j := i + 3
		for j+2 < len(data) {
			if data[j] == '"' && data[j+1] == '"' && data[j+2] == '"' {
				return j + 3, nil
			}
			j++
		}
		return 0, fmt.Errorf("%w: unterminated triple-quoted string", ErrUnterminated)
	}
	j := i + 1
	for j < len(data) {
		ch := data[j]
		if ch == '\\' {
			if j+1 >= len(data) {
				return 0, fmt.Errorf("%w: trailing backslash in string", ErrSyntax)
			}
			j += 2
			continue
		}
		if ch == '"' {
			return j + 1, nil
		}
		if ch == '\n' {
			return 0, fmt.Errorf("%w: newline in single-quoted string", ErrSyntax)
		}
		j++
	}
	return 0, fmt.Errorf("%w: unterminated string", ErrUnterminated)
}

// skipBytesLit advances past a b"..." literal. No backslash escapes
// inside; ends at the first `"` (matching the PXF lexer).
func skipBytesLit(data []byte, i int) (int, error) {
	j := i + 2 // past `b"`
	for j < len(data) {
		ch := data[j]
		if ch == '"' {
			return j + 1, nil
		}
		if ch == '\n' {
			return 0, fmt.Errorf("%w: newline in bytes literal", ErrSyntax)
		}
		j++
	}
	return 0, fmt.Errorf("%w: unterminated bytes literal", ErrUnterminated)
}

func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9') || ch == '.'
}
