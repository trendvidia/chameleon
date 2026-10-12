// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatal(err)
	}
	const want = "base_url: https://dev.example.com\ndb_password: 25 bytes\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

// TestReadmeQuickStart keeps the README's quick start the code that
// TestRun compiles and runs. The README shows the region between the
// README:quickstart markers in main.go, one indent level out and with
// tabs as four spaces. Its previous quick start was hand-written, called
// a function that did not exist and imported packages it never used.
func TestReadmeQuickStart(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	region := between(t, string(src), "// README:quickstart begin\n", "\t// README:quickstart end\n")
	var want strings.Builder
	for _, line := range strings.SplitAfter(region, "\n") {
		line = strings.TrimPrefix(line, "\t")
		indent := len(line) - len(strings.TrimLeft(line, "\t"))
		want.WriteString(strings.Repeat("    ", indent) + line[indent:])
	}

	block := between(t, string(readme), "<!-- quickstart: examples/buf/main.go -->\n```go\n", "```\n")
	if block != want.String() {
		t.Errorf("README quick start differs from examples/buf/main.go; copy the region between the README:quickstart markers.\nREADME:\n%s\nmain.go:\n%s", block, want.String())
	}
}

func between(t *testing.T, s, begin, end string) string {
	t.Helper()
	_, rest, ok := strings.Cut(s, begin)
	if !ok {
		t.Fatalf("marker %q not found", begin)
	}
	region, _, ok := strings.Cut(rest, end)
	if !ok {
		t.Fatalf("marker %q not found after %q", end, begin)
	}
	return region
}
