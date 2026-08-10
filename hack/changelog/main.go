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
//	go run ./hack/changelog -check                 # validate names, CI gate
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
		// load already rejected every malformed name; reaching here
		// means they are all well-formed.
		fmt.Printf("changelog.d: %d fragment(s), all well-formed\n", len(frags))
		return nil
	}
	if version == "" {
		return fmt.Errorf("-version is required (or pass -check)")
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

	if err := insert(file, section); err != nil {
		return err
	}
	// Only remove fragments once the changelog write has succeeded, so a
	// failure here leaves the source of truth intact rather than
	// half-consumed.
	for _, f := range frags {
		if err := os.Remove(f.Path); err != nil {
			return fmt.Errorf("remove %s: %w", f.Path, err)
		}
	}
	if preamble != "" {
		if err := os.Remove(filepath.Join(dir, preambleName)); err != nil {
			return fmt.Errorf("remove preamble: %w", err)
		}
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
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		// README.md documents the directory; .gitkeep keeps it alive.
		if name == "README.md" {
			continue
		}
		path := filepath.Join(dir, name)
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
	fmt.Fprintf(&b, "## [%s] — %s\n\n", version, date)
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

// insert places section immediately after the Unreleased heading and
// resets that section to the empty marker.
func insert(file, section string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	text := string(raw)
	head := strings.Index(text, unreleasedHeading)
	if head < 0 {
		return fmt.Errorf("%s: no %q heading to insert after", file, unreleasedHeading)
	}
	// The Unreleased body runs to the next top-level release heading.
	rest := text[head+len(unreleasedHeading):]
	next := strings.Index(rest, "\n## ")
	if next < 0 {
		return fmt.Errorf("%s: no release heading after %q", file, unreleasedHeading)
	}
	tail := rest[next+1:]

	var b strings.Builder
	b.WriteString(text[:head])
	b.WriteString(unreleasedHeading)
	b.WriteString("\n\n")
	b.WriteString(emptyMarker)
	b.WriteString("\n\n")
	b.WriteString(section)
	b.WriteString(tail)

	return os.WriteFile(file, []byte(b.String()), 0o644)
}
