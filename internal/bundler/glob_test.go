// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"errors"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		// exact matches
		{"literal exact", "db_password", "db_password", true},
		{"literal mismatch", "db_password", "db_username", false},
		{"literal nested exact", "primary.auth_token", "primary.auth_token", true},
		{"literal-too-short loses to longer path", "primary", "primary.auth_token", false},
		{"literal-too-long loses to shorter path", "primary.auth_token", "primary", false},

		// single-segment wildcard
		{"single-* matches one segment", "primary.*", "primary.auth_token", true},
		{"single-* refuses zero segments", "primary.*", "primary", false},
		{"single-* refuses multiple segments", "primary.*", "primary.endpoint.host", false},
		{"top-level *", "*", "db_password", true},
		{"top-level * refuses depth", "*", "primary.auth_token", false},

		// double-segment wildcard
		{"** matches any depth", "**", "anything.at.any.depth", true},
		{"** matches single segment", "**", "db_password", true},
		{"primary.** matches one level", "primary.**", "primary.auth_token", true},
		{"primary.** matches deep", "primary.**", "primary.endpoint.host", true},
		{"primary.** matches zero (just primary)", "primary.**", "primary", true},
		{"** at end matches suffix-empty", "db.**", "db", true},

		// ** in the middle / start
		{"**.token matches suffix", "**.token", "auth.session.token", true},
		{"**.token matches single", "**.token", "token", true},
		{"a.**.b matches", "a.**.b", "a.x.y.b", true},
		{"a.**.b matches with zero between", "a.**.b", "a.b", true},
		{"a.**.b refuses non-bookend", "a.**.b", "a.x.y.c", false},

		// mixed wildcards and literal segments
		{"sites.*.auth_token", "sites.*.auth_token", "sites.us_east.auth_token", true},
		{"sites.*.auth_token rejects wrong leaf", "sites.*.auth_token", "sites.us_east.api_key", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchGlob(tc.pattern, tc.path)
			if got != tc.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestValidGlob(t *testing.T) {
	good := []string{"*", "**", "db", "db.*", "db.**", "**.token", "a.**.b", "sites.*.auth_token"}
	for _, p := range good {
		t.Run("good/"+p, func(t *testing.T) {
			if err := validGlob(p); err != nil {
				t.Errorf("validGlob(%q) = %v, want nil", p, err)
			}
		})
	}
	bad := []struct {
		pattern string
		want    error
	}{
		{"", errEmptyPattern},
		{"db..token", errEmptySegment},
		{".db", errEmptySegment},
		{"db.", errEmptySegment},
		{"pre**fix", errStarInSegment},
		{"db.foo*", errStarInSegment},
		{"*bar", errStarInSegment},
	}
	for _, tc := range bad {
		t.Run("bad/"+tc.pattern, func(t *testing.T) {
			err := validGlob(tc.pattern)
			if !errors.Is(err, tc.want) {
				t.Errorf("validGlob(%q) = %v, want %v", tc.pattern, err, tc.want)
			}
		})
	}
}
