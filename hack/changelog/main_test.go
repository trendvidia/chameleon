// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// The tool rewrites a tracked file and deletes the fragments it consumed,
// which is destructive enough that its edge cases want pinning rather than
// re-deriving by hand at release time. Each test builds a throwaway
// changelog.d/ and CHANGELOG.md in t.TempDir() and drives run() the way
// the Makefile targets do.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skeleton is the smallest CHANGELOG.md with the shape insert() needs:
// an Unreleased section holding the empty marker, then a release heading
// for it to insert in front of.
const skeleton = `# Changelog

## [Unreleased]

` + emptyMarker + `

## [0.22.1] — 2026-06-03

### Changed

- **An older entry** that must survive untouched.

[Unreleased]: https://github.com/trendvidia/chameleon/compare/v0.22.1...HEAD
[0.22.1]: https://github.com/trendvidia/chameleon/releases/tag/v0.22.1
`

// fixture writes fragments into a fresh dir and returns (dir, file).
// Contents map filename → body; the changelog starts as skeleton.
func fixture(t *testing.T, contents map[string]string) (dir, file string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "changelog.d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range contents {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file = filepath.Join(root, "CHANGELOG.md")
	if err := os.WriteFile(file, []byte(skeleton), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestLoadRejectsRatherThanSkips is the invariant load()'s doc comment
// states: a file someone meant as an entry never gets passed over in
// silence, because CI's "did this PR add a fragment?" gate counts the
// file either way and the release would just drop it.
func TestLoadRejectsRatherThanSkips(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents map[string]string
		wantErr  string
	}{
		{
			name:     "wrong extension",
			contents: map[string]string{"151-oops.fixed.txt": "- entry\n"},
			wantErr:  "must end in .md",
		},
		{
			name:     "fragment in a subdirectory",
			contents: map[string]string{"sub/151.fixed.md": "- entry\n"},
			wantErr:  "subdirectories are not read",
		},
		{
			name:     "unknown category",
			contents: map[string]string{"151.Fixed.md": "- entry\n"},
			wantErr:  `unknown category "Fixed"`,
		},
		{
			name:     "no category at all",
			contents: map[string]string{"151.md": "- entry\n"},
			wantErr:  "want <id>.<category>.md",
		},
		{
			name:     "empty fragment",
			contents: map[string]string{"151.fixed.md": "\n  \n"},
			wantErr:  "is empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, file := fixture(t, tc.contents)
			// -check is the CI gate: it has to be the thing that fails.
			err := run("", "", dir, file, false, true)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("run(-check) = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestLoadSkipsNonEntries: README.md documents the directory and dotfiles
// are tooling noise. Erroring on .DS_Store would red `make ci` on a Mac.
func TestLoadSkipsNonEntries(t *testing.T) {
	dir, file := fixture(t, map[string]string{
		"README.md":      "# changelog.d\n",
		".gitkeep":       "",
		".DS_Store":      "\x00binary junk",
		"151.fixed.md":   "- **A fix** that counts.\n",
		"_preamble.md":   "Patch release — nothing but the fix below.\n",
		"152.fixed.md":   "- **Another fix** that counts.\n",
		"153.changed.md": "- **A change** that counts.\n",
	})
	if err := run("", "", dir, file, false, true); err != nil {
		t.Fatalf("run(-check) = %v, want nil", err)
	}
	frags, preamble, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 3 {
		t.Errorf("load() returned %d fragments, want 3", len(frags))
	}
	if !strings.HasPrefix(preamble, "Patch release") {
		t.Errorf("preamble = %q, want the _preamble.md body", preamble)
	}
}

func TestLeadingNum(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want int
	}{
		{"130", 130},
		{"130-crypt-package", 130},
		{"7", 7},
		{"130x", -1},          // typo, not a discriminator
		{"releasing-doc", -1}, // a slug
		{"-130", -1},
		{"", -1},
		{"99999999999999999999-overflow", -1}, // Atoi fails; not silently 0
	} {
		if got := leadingNum(tc.id); got != tc.want {
			t.Errorf("leadingNum(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}
}

// TestAssembleOrdering pins both orderings the README promises: canonical
// Keep a Changelog headings, and numbered IDs ascending ahead of slugs.
func TestAssembleOrdering(t *testing.T) {
	dir, _ := fixture(t, map[string]string{
		"releasing-doc.added.md": "- slug entry\n",
		"130-b.added.md":         "- one three zero b\n",
		"130-a.added.md":         "- one three zero a\n",
		"99.added.md":            "- ninety nine\n",
		"140.fixed.md":           "- a fix\n",
		"141.changed.md":         "- a change\n",
		"142.internal.md":        "- an internal note\n",
	})
	frags, preamble, err := load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := assemble("0.23.0", "2026-01-01", preamble, frags)

	if !strings.HasPrefix(got, "## [0.23.0] — 2026-01-01\n") {
		t.Errorf("section starts %q, want the version heading", strings.SplitN(got, "\n", 2)[0])
	}
	wantOrder := []string{
		"### Added", "- ninety nine", "- one three zero a", "- one three zero b", "- slug entry",
		"### Changed", "- a change",
		"### Fixed", "- a fix",
		"### Internal", "- an internal note",
	}
	var at int
	for _, want := range wantOrder {
		i := strings.Index(got[at:], want)
		if i < 0 {
			t.Fatalf("assembled section is missing %q or has it out of order:\n%s", want, got)
		}
		at += i + len(want)
	}
	// Headings with no fragments are omitted rather than left empty.
	for _, absent := range []string{"### Deprecated", "### Removed", "### Security"} {
		if strings.Contains(got, absent) {
			t.Errorf("assembled section contains %q with no fragments under it", absent)
		}
	}
}

func TestCheckVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		wantErr string
	}{
		{"0.23.0", ""},
		{"1.0.0-rc.1", ""},
		{"", "-version is required"},
		{"v0.23.0", `drop the leading "v"`},
		{"0.23", "want a bare semver"},
		{"next", "want a bare semver"},
	} {
		err := checkVersion(tc.version)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("checkVersion(%q) = %v, want nil", tc.version, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("checkVersion(%q) = %v, want an error containing %q", tc.version, err, tc.wantErr)
		}
	}
}

// TestRunFoldsAndConsumes is the happy path: the section lands under
// Unreleased, Unreleased is reset, older releases are untouched, and the
// fragments are gone.
func TestRunFoldsAndConsumes(t *testing.T) {
	dir, file := fixture(t, map[string]string{
		"151.added.md": "- **A new thing** with a\n\n  second paragraph.\n",
		"_preamble.md": "Minor release — one new thing.\n",
	})
	if err := run("0.23.0", "2026-01-01", dir, file, false, false); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}

	got := read(t, file)
	for _, want := range []string{
		"## [Unreleased]\n\n" + emptyMarker,
		"## [0.23.0] — 2026-01-01",
		"Minor release — one new thing.",
		"- **A new thing** with a\n\n  second paragraph.",
		"## [0.22.1] — 2026-06-03",
		"- **An older entry** that must survive untouched.",
		// The link block is regenerated from the headings on every
		// assembly (#184): the new release compares against the previous
		// one, and the first release keeps its tag-page link.
		"[0.23.0]: " + repoURL + "/compare/v0.22.1...v0.23.0",
		"[0.22.1]: " + repoURL + "/releases/tag/v0.22.1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("assembled CHANGELOG.md is missing %q:\n%s", want, got)
		}
	}
	if i, j := strings.Index(got, "## [0.23.0]"), strings.Index(got, "## [0.22.1]"); i > j {
		t.Error("0.23.0 section landed after 0.22.1; newest release must come first")
	}
	for _, name := range []string{"151.added.md", "_preamble.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived assembly (stat err = %v), want it consumed", name, err)
		}
	}
}

// TestRunRefusesDuplicateVersion: re-running for an already-assembled
// version used to append a second identical heading and split the
// release's entries between the two.
func TestRunRefusesDuplicateVersion(t *testing.T) {
	dir, file := fixture(t, map[string]string{"151.added.md": "- first pass\n"})
	if err := run("0.23.0", "2026-01-01", dir, file, false, false); err != nil {
		t.Fatalf("first run() = %v, want nil", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "152.added.md"), []byte("- a late fragment\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := run("0.23.0", "2026-01-01", dir, file, false, false)
	if err == nil || !strings.Contains(err.Error(), "already has a [0.23.0] section") {
		t.Fatalf("second run() = %v, want a refusal naming the existing section", err)
	}
	if got := strings.Count(read(t, file), "## [0.23.0]"); got != 1 {
		t.Errorf("CHANGELOG.md has %d [0.23.0] headings, want 1", got)
	}
	// The refusal must not consume the late fragment either.
	if _, err := os.Stat(filepath.Join(dir, "152.added.md")); err != nil {
		t.Errorf("late fragment was removed by a run that failed: %v", err)
	}
}

// TestUnreleasedBodyIsNeverDiscarded covers the entry someone writes the
// old way, straight into CHANGELOG.md: assembly replaces that section
// wholesale, so it has to be refused rather than deleted in silence.
func TestUnreleasedBodyIsNeverDiscarded(t *testing.T) {
	const strayEntry = "- **A hand-written entry** added the old way."
	stray := func(t *testing.T) (dir, file string) {
		t.Helper()
		dir, file = fixture(t, map[string]string{"151.added.md": "- a fragment\n"})
		body := strings.Replace(skeleton, emptyMarker, "### Fixed\n\n"+strayEntry, 1)
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, file
	}

	t.Run("check", func(t *testing.T) {
		dir, file := stray(t)
		err := run("", "", dir, file, false, true)
		if err == nil || !strings.Contains(err.Error(), "is not empty") {
			t.Fatalf("run(-check) = %v, want an error about the non-empty Unreleased section", err)
		}
	})

	t.Run("assemble", func(t *testing.T) {
		dir, file := stray(t)
		err := run("0.23.0", "2026-01-01", dir, file, false, false)
		if err == nil || !strings.Contains(err.Error(), "is not empty") {
			t.Fatalf("run() = %v, want a refusal rather than a silent discard", err)
		}
		if got := read(t, file); !strings.Contains(got, strayEntry) {
			t.Error("the hand-written entry was destroyed by a run that failed")
		}
		if _, err := os.Stat(filepath.Join(dir, "151.added.md")); err != nil {
			t.Errorf("fragment was consumed by a run that failed: %v", err)
		}
	})
}

func TestPreviewChangesNothing(t *testing.T) {
	dir, file := fixture(t, map[string]string{"151.added.md": "- a fragment\n"})
	before := read(t, file)
	if err := run("0.23.0", "2026-01-01", dir, file, true, false); err != nil {
		t.Fatalf("run(-preview) = %v, want nil", err)
	}
	if read(t, file) != before {
		t.Error("-preview rewrote CHANGELOG.md")
	}
	if _, err := os.Stat(filepath.Join(dir, "151.added.md")); err != nil {
		t.Errorf("-preview consumed a fragment: %v", err)
	}
}

func TestRunErrors(t *testing.T) {
	t.Run("no fragments", func(t *testing.T) {
		dir, file := fixture(t, nil)
		err := run("0.23.0", "", dir, file, false, false)
		if err == nil || !strings.Contains(err.Error(), "nothing to release") {
			t.Fatalf("run() = %v, want a nothing-to-release error", err)
		}
	})

	t.Run("missing fragment directory", func(t *testing.T) {
		_, file := fixture(t, nil)
		err := run("", "", filepath.Join(t.TempDir(), "absent"), file, false, true)
		if err == nil || !strings.Contains(err.Error(), "read ") {
			t.Fatalf("run(-check) = %v, want a read error", err)
		}
	})

	t.Run("no Unreleased heading", func(t *testing.T) {
		dir, file := fixture(t, map[string]string{"151.added.md": "- a fragment\n"})
		if err := os.WriteFile(file, []byte("# Changelog\n\n## [0.22.1] — 2026-06-03\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := run("0.23.0", "", dir, file, false, false)
		if err == nil || !strings.Contains(err.Error(), "no \"## [Unreleased]\" heading") {
			t.Fatalf("run() = %v, want a missing-anchor error", err)
		}
	})

	t.Run("no release heading after Unreleased", func(t *testing.T) {
		dir, file := fixture(t, map[string]string{"151.added.md": "- a fragment\n"})
		if err := os.WriteFile(file, []byte("# Changelog\n\n## [Unreleased]\n\n"+emptyMarker+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := run("0.23.0", "", dir, file, false, false)
		if err == nil || !strings.Contains(err.Error(), "no release heading after") {
			t.Fatalf("run() = %v, want a missing-tail error", err)
		}
	})
}

// TestRepoFragmentsAreWellFormed runs the CI gate against the real
// changelog.d/ and CHANGELOG.md, so a fragment added in a PR that never
// runs `make changelog-check` locally still fails somewhere.
func TestRepoFragmentsAreWellFormed(t *testing.T) {
	root := filepath.Join("..", "..")
	if err := run("", "", filepath.Join(root, defaultDir), filepath.Join(root, defaultFile), false, true); err != nil {
		t.Fatalf("the repo's own fragments do not validate: %v", err)
	}
}
