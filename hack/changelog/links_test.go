// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"os"
	"strings"
	"testing"
)

// The link block is derived, so its rules are pinned once here rather
// than re-read off the rendered file at each release: Unreleased
// compares the latest tag with HEAD, each release with the one before,
// and the first release links to its tag page (#184).
func TestLinkBlockShape(t *testing.T) {
	got := linkBlock([]string{"0.23.0", "0.22.1", "0.1.0"})
	want := "[Unreleased]: " + repoURL + "/compare/v0.23.0...HEAD\n" +
		"[0.23.0]: " + repoURL + "/compare/v0.22.1...v0.23.0\n" +
		"[0.22.1]: " + repoURL + "/compare/v0.1.0...v0.22.1\n" +
		"[0.1.0]: " + repoURL + "/releases/tag/v0.1.0\n"
	if got != want {
		t.Errorf("linkBlock =\n%s\nwant\n%s", got, want)
	}
	if linkBlock(nil) != "" {
		t.Error("no headings must produce no block")
	}
}

// Assembling a release moves [Unreleased] to the new tag and adds the
// new version's compare line; the older lines are regenerated, not
// preserved, so a stale one cannot ride along.
func TestRunMaintainsLinkBlock(t *testing.T) {
	dir, file := fixture(t, map[string]string{"1.added.md": "- one\n"})
	if err := run("0.23.0", "2026-01-01", dir, file, false, false); err != nil {
		t.Fatalf("run() = %v", err)
	}
	raw, _ := os.ReadFile(file)
	text := string(raw)
	for _, want := range []string{
		"[Unreleased]: " + repoURL + "/compare/v0.23.0...HEAD",
		"[0.23.0]: " + repoURL + "/compare/v0.22.1...v0.23.0",
		"[0.22.1]: " + repoURL + "/releases/tag/v0.22.1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("after assembly the file lacks %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "compare/v0.22.1...HEAD") {
		t.Error("the old [Unreleased] line survived assembly")
	}
	// The block is the tail of the file and nothing follows it.
	if !strings.HasSuffix(text, "/releases/tag/v0.22.1\n") {
		t.Errorf("link block is not the tail of the file:\n%s", text[max(0, len(text)-200):])
	}
	if err := run("", "", dir, file, false, true); err != nil {
		t.Errorf("-check after assembly = %v, want nil", err)
	}
}

// -check refuses a block that does not match the headings — the exact
// drift #184 found: [Unreleased] still naming a tag eleven releases old
// and newer sections with no line at all — and says what to run.
func TestCheckRefusesStaleLinks(t *testing.T) {
	dir, file := fixture(t, nil)
	raw, _ := os.ReadFile(file)
	stale := strings.Replace(string(raw), "compare/v0.22.1...HEAD", "compare/v0.13.0...HEAD", 1)
	if err := os.WriteFile(file, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run("", "", dir, file, false, true)
	if err == nil || !strings.Contains(err.Error(), "link-reference block does not match") || !strings.Contains(err.Error(), "changelog-relink") {
		t.Fatalf("run(-check) = %v, want a link-block mismatch naming the repair target", err)
	}
	if err := relink(file); err != nil {
		t.Fatalf("relink() = %v", err)
	}
	if err := run("", "", dir, file, false, true); err != nil {
		t.Errorf("-check after relink = %v, want nil", err)
	}
}

// A file with no block at all gets one appended; the prose above it is
// untouched.
func TestRelinkAppendsWhenMissing(t *testing.T) {
	dir, file := fixture(t, nil)
	raw, _ := os.ReadFile(file)
	body, block := splitLinkBlock(string(raw))
	if block == "" {
		t.Fatal("fixture has no link block to remove")
	}
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := relink(file); err != nil {
		t.Fatalf("relink() = %v", err)
	}
	after, _ := os.ReadFile(file)
	gotBody, gotBlock := splitLinkBlock(string(after))
	if gotBody != body {
		t.Errorf("relink changed the prose:\n%s\nwant\n%s", gotBody, body)
	}
	if gotBlock != linkBlock([]string{"0.22.1"}) {
		t.Errorf("appended block = %q", gotBlock)
	}
	_ = dir
}
