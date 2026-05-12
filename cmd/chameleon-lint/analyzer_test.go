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
