// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// repoURL is the compare-link base. It is a constant rather than read from
// git because the links are for a reader on GitHub, not for whoever ran
// the tool, and the remote of a fork or a mirror is the wrong answer.
const repoURL = "https://github.com/trendvidia/chameleon"

// headingRe matches a release heading and captures the bare version.
var headingRe = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)\]`)

// linkRe matches one line of the link-reference block at the foot of the
// file: `[Unreleased]: url` or `[x.y.z]: url`.
var linkRe = regexp.MustCompile(`^\[(Unreleased|[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)\]: \S+$`)

// releasedVersions lists the release headings in file order, newest
// first — the order the link block mirrors.
func releasedVersions(text string) []string {
	matches := headingRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// linkBlock is the Keep a Changelog link-reference block for versions
// (newest first): Unreleased compares the latest release with HEAD, each
// release compares with the one before it, and the first release, having
// nothing before it, links to its tag page.
func linkBlock(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Unreleased]: %s/compare/v%s...HEAD\n", repoURL, versions[0])
	// Each release compares against the next older one; the oldest has
	// nothing to compare against and links to its tag page.
	for i := 0; i+1 < len(versions); i++ {
		fmt.Fprintf(&b, "[%s]: %s/compare/v%s...v%s\n", versions[i], repoURL, versions[i+1], versions[i])
	}
	first := versions[len(versions)-1]
	fmt.Fprintf(&b, "[%s]: %s/releases/tag/v%s\n", first, repoURL, first)
	return b.String()
}

// splitLinkBlock separates the body of the file from its trailing
// link-reference block: the run of matching lines at the end of the file,
// blank lines around it included. A file with no block splits into
// (text, "").
func splitLinkBlock(text string) (body, block string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && linkRe.MatchString(lines[start-1]) {
		start--
	}
	if start == end {
		return text, ""
	}
	// Absorb the blank line(s) that separate the block from the prose,
	// so the body ends at the last line of content.
	bodyEnd := start
	for bodyEnd > 0 && strings.TrimSpace(lines[bodyEnd-1]) == "" {
		bodyEnd--
	}
	return strings.Join(lines[:bodyEnd], "\n") + "\n", strings.Join(lines[start:end], "\n") + "\n"
}

// withLinkBlock returns text with its link block replaced by the one the
// headings imply — appended if the file had none.
func withLinkBlock(text string) string {
	body, _ := splitLinkBlock(text)
	block := linkBlock(releasedVersions(text))
	if block == "" {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\n" + block
}

// checkLinks fails when the link block on disk is not the one the
// headings imply, naming the first line that differs. The block is
// derived, so any drift is a stale or hand-edited line, and the fix is
// always the same: `go run ./hack/changelog -relink`.
func checkLinks(file, text string) error {
	_, got := splitLinkBlock(text)
	want := linkBlock(releasedVersions(text))
	if got == want {
		return nil
	}
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		g, w := "", ""
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			return fmt.Errorf("%s: link-reference block does not match the release headings (line %d of the block):\n  have: %q\n  want: %q\n"+
				"The block is derived from the `## [x.y.z]` headings; run `make changelog-relink` to regenerate it",
				file, i+1, g, w)
		}
	}
	return nil
}

// relink rewrites the link block on disk from the headings — the repair
// path for a file that predates the tool maintaining it.
func relink(file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	text := string(raw)
	out := withLinkBlock(text)
	if out == text {
		fmt.Printf("changelog: link block already matches %d release heading(s)\n", len(releasedVersions(text)))
		return nil
	}
	if err := os.WriteFile(file, []byte(out), 0o644); err != nil {
		return err
	}
	fmt.Printf("changelog: rewrote the link block for %d release heading(s)\n", len(releasedVersions(text)))
	return nil
}
