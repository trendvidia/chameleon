// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Command chameleon-lint flags reads / writes of the public
// `Value string` field on pxfv1.Secret (and structurally-equivalent
// Secret types) in consumer code. After chameleon's parse.Move runs,
// that field is empty — code like `if cfg.X.Value == "" { panic }`
// silently deploys a worker that panics on every real secret.
//
// Usage:
//
//	chameleon-lint ./...
//
// The binary uses golang.org/x/tools/go/analysis. Integrates with
// golangci-lint via the custom-analyzer mechanism or runs standalone.
package main

import "golang.org/x/tools/go/analysis/singlechecker"

func main() {
	singlechecker.Main(Analyzer)
}
