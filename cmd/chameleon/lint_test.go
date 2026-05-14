// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer_FlagsBadPatterns(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "badpkg")
}

func TestAnalyzer_GoodPatternsClean(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "goodpkg")
}

// TestAnalyzer_GeneratedFilesSkipped exercises the "skip files with
// the `// Code generated ... DO NOT EDIT.` header" rule. The genpkg
// fixture reads `pxfv1.Secret.Value` directly — the kind of read
// the linter would otherwise flag — but the file carries the
// standard generated-file marker so the analyzer must short-circuit
// before walking the AST. analysistest.Run fails the test if any
// diagnostic fires; an empty `// want` set on every line is the
// implicit expectation.
func TestAnalyzer_GeneratedFilesSkipped(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "genpkg")
}
