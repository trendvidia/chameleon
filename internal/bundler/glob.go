// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import "strings"

// matchGlob reports whether a schema path matches a manifest pattern.
// Patterns are dot-separated segments with two wildcards:
//
//   - `*`  matches exactly one segment (any non-empty name, no dots).
//   - `**` matches zero or more segments. Must appear as a whole
//     segment — `db.**` and `**.token` are valid; `pre**fix` is not.
//
// Examples (pattern → path → match):
//
//	"db_password"        "db_password"                 → true
//	"primary.*"          "primary.auth_token"          → true
//	"primary.*"          "primary.endpoint.host"       → false
//	"primary.**"         "primary.endpoint.host"       → true
//	"**"                 "anything.at.any.depth"       → true
//	"**.token"           "auth.session.token"          → true
//	"sites.*.auth_token" "sites.us_east.auth_token"    → true (segment glob spans the map placeholder)
//
// The schema-path form (no `[i]` / `[key]` brackets) means a `*`
// segment in a pattern matches what would be `[key]` or `[i]` at
// runtime — manifest authors filter at the field level, never per
// runtime-index.
func matchGlob(pattern, path string) bool {
	patSegs := strings.Split(pattern, ".")
	pathSegs := strings.Split(path, ".")
	return matchSegments(patSegs, pathSegs)
}

// matchSegments is a recursive segment-by-segment matcher. The
// recursion only branches on `**`; for typical patterns (no `**` or
// a single trailing `**`) it runs in linear time over the longer
// of the two segment slices.
func matchSegments(pat, path []string) bool {
	for len(pat) > 0 {
		seg := pat[0]
		if seg == "**" {
			// Greedy: try to match the rest of the pattern against
			// every suffix of path, including the empty suffix.
			rest := pat[1:]
			for i := 0; i <= len(path); i++ {
				if matchSegments(rest, path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		if seg != "*" && seg != path[0] {
			return false
		}
		pat = pat[1:]
		path = path[1:]
	}
	return len(path) == 0
}

// validGlob reports whether pattern is syntactically well-formed.
// Catches the two common authoring mistakes — `pre**fix` (`**` not
// a whole segment) and the empty pattern. Returns nil on success.
//
// Note for editors: do not write the empty pattern as a pair of
// backticks here. gofmt's doc-comment printer rewrites a backtick
// pair (and a pair of single quotes) into Unicode curly quotes, so
// the example silently turns into punctuation on the next `make fmt`.
func validGlob(pattern string) error {
	if pattern == "" {
		return errEmptyPattern
	}
	for _, seg := range strings.Split(pattern, ".") {
		if seg == "" {
			return errEmptySegment
		}
		if seg == "*" || seg == "**" {
			continue
		}
		if strings.Contains(seg, "*") {
			return errStarInSegment
		}
	}
	return nil
}

type globError string

func (e globError) Error() string { return string(e) }

const (
	errEmptyPattern  globError = "empty pattern"
	errEmptySegment  globError = "empty segment (consecutive dots or leading/trailing dot)"
	errStarInSegment globError = "`*` must be a whole segment — use `*` or `**`, not mixed with literal chars"
)
