// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Command changelog assembles the per-change fragments in changelog.d/
// into a release section of CHANGELOG.md.
//
// A fragment is a file named `<id>.<category>.md` whose entire content
// is the entry, verbatim. There is no wrapper format and no escaping:
// the file holds the same markdown that used to be typed straight into
// CHANGELOG.md, so tables, fenced code blocks and multi-paragraph
// entries survive untouched. `id` is the issue or PR number the change
// belongs to (or a short slug when there is none) and orders entries
// within a category; `category` is one of the Keep a Changelog headings
// plus `internal`.
//
// The point is not automation for its own sake — it is that two PRs
// touching CHANGELOG.md conflict, every time, on a file where the
// conflict is pure noise. A fragment per change never conflicts,
// because no two PRs write the same file.
//
// Usage:
//
//	go run ./hack/changelog -version 0.23.0        # fold into CHANGELOG.md
//	go run ./hack/changelog -version 0.23.0 -preview   # print, change nothing
//	go run ./hack/changelog -check                 # validate names + Unreleased, CI gate
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// categories is both the set of valid category slugs and the order
// their headings appear in a release section. Keep a Changelog's order,
// with `internal` last — it is the one heading this repo added, for
// changes that alter no API but that a consumer reads as a confidence
// signal (see the v0.22.1 section).
var categories = []struct{ Slug, Heading string }{
	{"added", "Added"},
	{"changed", "Changed"},
	{"deprecated", "Deprecated"},
	{"removed", "Removed"},
	{"fixed", "Fixed"},
	{"security", "Security"},
	{"internal", "Internal"},
}

const (
	defaultDir  = "changelog.d"
	defaultFile = "CHANGELOG.md"

	// preambleName is the optional release lead paragraph — the prose
	// that sits between the version heading and the first `###`, as in
	// the v0.22.0 and v0.22.1 sections. It is not a fragment: it
	// describes the release rather than one change, so it carries no
	// category and is never bulleted.
	preambleName = "_preamble.md"

	// unreleasedHeading is the anchor the assembled section is inserted
	// after. It stays in the file, emptied, so the next cycle has
	// somewhere to point.
	unreleasedHeading = "## [Unreleased]"

	// emptyMarker is what an Unreleased section says when every
	// fragment has been folded into a release.
	emptyMarker = "_Nothing yet — changes live in [changelog.d/](changelog.d/) until release._"
)

// versionRe is the shape a release heading takes: bare semver, with an
// optional prerelease suffix. The heading never carries the leading `v`
// — the tag is what carries it (docs/RELEASING.md) — and the
// link-reference definitions at the foot of CHANGELOG.md key off the
// bare form. Unchecked, a `v` typed out of tag-shaped habit lands in a
// committed section that is about to be tagged.
var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

type fragment struct {
	Path     string
	ID       string // "134", or a slug like "releasing-doc"
	Num      int    // parsed ID, or -1 when the ID is a slug
	Category string
	Body     string
}

func main() {
	var (
		version = flag.String("version", "", "release version to assemble, e.g. 0.23.0")
		date    = flag.String("date", "", "release date (YYYY-MM-DD); defaults to today")
		dir     = flag.String("dir", defaultDir, "fragment directory")
		file    = flag.String("file", defaultFile, "changelog file to fold into")
		preview = flag.Bool("preview", false, "print the assembled section; change nothing")
		check   = flag.Bool("check", false, "validate fragment names and exit")
	)
	flag.Parse()

	if err := run(*version, *date, *dir, *file, *preview, *check); err != nil {
		fmt.Fprintln(os.Stderr, "changelog:", err)
		os.Exit(1)
	}
}

func run(version, date, dir, file string, preview, check bool) error {
	frags, preamble, err := load(dir)
	if err != nil {
		return err
	}
	if check {
		// load already rejected every malformed name. What it cannot
		// see is an entry written the old way, straight into the
		// Unreleased section — assembly replaces that section wholesale,
		// so such an entry is deleted at release time without a word.
		// Every PR runs this target, which is the cheapest place to
		// catch it.
		if err := checkUnreleased(file, dir); err != nil {
			return err
		}
		fmt.Printf("changelog.d: %d fragment(s), all well-formed; %s [Unreleased] is empty\n", len(frags), file)
		return nil
	}
	if err := checkVersion(version); err != nil {
		return err
	}
	if len(frags) == 0 {
		return fmt.Errorf("no fragments in %s — nothing to release", dir)
	}
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	section := assemble(version, date, preamble, frags)
	if preview {
		fmt.Print(section)
		return nil
	}

	if err := insert(file, dir, version, section); err != nil {
		return err
	}
	// Only remove fragments once the changelog write has succeeded, so a
	// failure there leaves the source of truth intact rather than
	// half-consumed. Past that point every removal is attempted and the
	// failures are reported together: aborting on the first one would
	// leave the operator to discover the rest one re-run at a time, and
	// a re-run is refused now that the section exists.
	var rmErrs []error
	for _, f := range frags {
		if err := os.Remove(f.Path); err != nil {
			rmErrs = append(rmErrs, err)
		}
	}
	if preamble != "" {
		if err := os.Remove(filepath.Join(dir, preambleName)); err != nil {
			rmErrs = append(rmErrs, err)
		}
	}
	if len(rmErrs) > 0 {
		return fmt.Errorf("%s was written, but %d fragment(s) could not be removed — "+
			"their entries are already in the %s section, so delete them by hand rather than re-running: %w",
			file, len(rmErrs), version, errors.Join(rmErrs...))
	}
	fmt.Printf("changelog: folded %d fragment(s) into %s as %s\n", len(frags), file, version)
	return nil
}

// load reads every fragment in dir, rejecting anything whose name does
// not parse. A malformed name is an error rather than a skip: silently
// ignoring a file is how an entry goes missing from a release, which is
// the failure this whole mechanism exists to prevent.
func load(dir string) (frags []fragment, preamble string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", dir, err)
	}
	valid := map[string]bool{}
	for _, c := range categories {
		valid[c.Slug] = true
	}

	for _, e := range entries {
		name := e.Name()
		// README.md documents the directory; dotfiles are tooling and OS
		// noise (.gitkeep keeps the directory alive, .DS_Store just
		// happens). Neither is anybody's attempt at an entry.
		if name == "README.md" || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		// Everything else was meant to be a fragment, so a wrong
		// extension or a subdirectory is an error rather than a skip.
		// Skipping is worse than it looks: CI's gate counts any added
		// file under changelog.d/, so `151.fixed.txt` turns the PR green
		// and is then never assembled into anything.
		if e.IsDir() {
			return nil, "", fmt.Errorf("%s: subdirectories are not read — move the fragment directly into %s", path, dir)
		}
		if !strings.HasSuffix(name, ".md") {
			return nil, "", fmt.Errorf("%s: want <id>.<category>.md — a fragment is markdown and its name must end in .md", path)
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, "", fmt.Errorf("read %s: %w", path, rerr)
		}
		body := strings.TrimRight(string(raw), "\n")
		if strings.TrimSpace(body) == "" {
			return nil, "", fmt.Errorf("%s is empty", path)
		}
		if name == preambleName {
			preamble = body
			continue
		}

		// <id>.<category>.md
		base := strings.TrimSuffix(name, ".md")
		dot := strings.LastIndex(base, ".")
		if dot <= 0 || dot == len(base)-1 {
			return nil, "", fmt.Errorf("%s: want <id>.<category>.md", path)
		}
		id, cat := base[:dot], base[dot+1:]
		if !valid[cat] {
			return nil, "", fmt.Errorf("%s: unknown category %q (want one of %s)", path, cat, slugs())
		}
		num := leadingNum(id)
		frags = append(frags, fragment{Path: path, ID: id, Num: num, Category: cat, Body: body})
	}

	// Numbered fragments ascending, then slugs alphabetically. Issue
	// order is the closest thing to a meaningful sequence available, and
	// it is stable across machines, which alphabetical-by-filename is
	// not once IDs reach three digits.
	sort.SliceStable(frags, func(i, j int) bool {
		a, b := frags[i], frags[j]
		switch {
		case a.Num >= 0 && b.Num >= 0:
			return a.Num < b.Num
		case a.Num >= 0:
			return true
		case b.Num >= 0:
			return false
		default:
			return a.ID < b.ID
		}
	})
	return frags, preamble, nil
}

// leadingNum extracts the issue number from an ID, returning -1 when
// there is none. One issue routinely produces several entries in the
// same category — #130 alone contributed five to the Added section of
// v0.23.0 — so an ID may carry a discriminator after the number
// (`130-crypt`, `130-trace-decrypter`) to keep filenames unique. Those
// still sort under 130, then alphabetically among themselves.
func leadingNum(id string) int {
	end := 0
	for end < len(id) && id[end] >= '0' && id[end] <= '9' {
		end++
	}
	if end == 0 {
		return -1
	}
	// Reject "130x": a digit run followed by anything but a separator is
	// a typo, not a discriminator, and silently treating it as issue 130
	// would file the entry under the wrong number.
	if end < len(id) && id[end] != '-' {
		return -1
	}
	n, err := strconv.Atoi(id[:end])
	if err != nil {
		return -1
	}
	return n
}

// checkVersion rejects anything that is not the bare semver a release
// heading carries, naming the leading `v` case specifically because it
// is the one a maintainer types by reflex — the next release step is
// `git tag v0.23.0`.
func checkVersion(version string) error {
	switch {
	case version == "":
		return fmt.Errorf("-version is required (or pass -check)")
	case versionRe.MatchString(version):
		return nil
	case versionRe.MatchString(strings.TrimPrefix(version, "v")):
		return fmt.Errorf("-version %q: drop the leading \"v\" — the heading is bare (%s); the tag is what carries it",
			version, strings.TrimPrefix(version, "v"))
	default:
		return fmt.Errorf("-version %q: want a bare semver, e.g. 0.23.0", version)
	}
}

func slugs() string {
	out := make([]string, len(categories))
	for i, c := range categories {
		out[i] = c.Slug
	}
	return strings.Join(out, ", ")
}

// assemble renders the release section, headings in canonical order and
// only for categories that actually have fragments.
func assemble(version, date, preamble string, frags []fragment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", versionHeading(version), date)
	if preamble != "" {
		b.WriteString(preamble)
		b.WriteString("\n\n")
	}
	for _, c := range categories {
		var inCat []fragment
		for _, f := range frags {
			if f.Category == c.Slug {
				inCat = append(inCat, f)
			}
		}
		if len(inCat) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", c.Heading)
		for _, f := range inCat {
			b.WriteString(f.Body)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

func versionHeading(version string) string { return "## [" + version + "]" }

// splitUnreleased locates the Unreleased section: everything before its
// heading, the body between that heading and the next release heading,
// and the tail from that release heading onwards.
func splitUnreleased(file, text string) (before, body, tail string, err error) {
	head := strings.Index(text, unreleasedHeading)
	if head < 0 {
		return "", "", "", fmt.Errorf("%s: no %q heading to insert after", file, unreleasedHeading)
	}
	// The Unreleased body runs to the next top-level release heading.
	rest := text[head+len(unreleasedHeading):]
	next := strings.Index(rest, "\n## ")
	if next < 0 {
		return "", "", "", fmt.Errorf("%s: no release heading after %q", file, unreleasedHeading)
	}
	return text[:head], strings.TrimSpace(rest[:next]), rest[next+1:], nil
}

// assertUnreleasedEmpty fails when the Unreleased section holds anything
// but the empty marker. Assembly replaces that section rather than
// merging with it, so an entry typed in the old way is destroyed at
// release time and nothing says so — the same silent loss load() refuses
// for a misnamed file.
func assertUnreleasedEmpty(file, dir, body string) error {
	if body == "" || body == emptyMarker {
		return nil
	}
	return fmt.Errorf("%s: %q is not empty, and assembling a release would discard it. "+
		"Entries live one per file in %s/ now (see %s/README.md) — move this into a fragment:\n\n%s",
		file, unreleasedHeading, dir, dir, body)
}

// checkUnreleased is assertUnreleasedEmpty against the file on disk: the
// -check half of the CI gate, run on every PR.
func checkUnreleased(file, dir string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	_, body, _, err := splitUnreleased(file, string(raw))
	if err != nil {
		return err
	}
	return assertUnreleasedEmpty(file, dir, body)
}

// insert places section immediately after the Unreleased heading and
// resets that section to the empty marker.
func insert(file, dir, version, section string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	text := string(raw)
	// A second run for the same version would append a second section
	// for it rather than replace the first, splitting the release's
	// entries across two identical headings. The realistic way in is a
	// late fragment arriving after assembly, so say what to do about it.
	if strings.Contains(text, "\n"+versionHeading(version)) {
		return fmt.Errorf("%s already has a [%s] section — assemble a version once. "+
			"To fold in a late fragment, restore both sides first (git checkout -- %s %s) and re-run",
			file, version, file, dir)
	}
	before, body, tail, err := splitUnreleased(file, text)
	if err != nil {
		return err
	}
	// -check catches this on every PR; repeat it here because the
	// release path must not be the one that discovers it too late.
	if err := assertUnreleasedEmpty(file, dir, body); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString(before)
	b.WriteString(unreleasedHeading)
	b.WriteString("\n\n")
	b.WriteString(emptyMarker)
	b.WriteString("\n\n")
	b.WriteString(section)
	b.WriteString(tail)

	return os.WriteFile(file, []byte(b.String()), 0o644)
}
