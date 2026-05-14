// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"os"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

// Analyzer reports reads / writes of a pxf.Secret's `Value` field. The
// chameleon parse.Move pass clears Value after extracting the
// plaintext into a memguard Enclave; reads return "" and writes leave
// the runtime view untouched (parse.Move clears them again on the
// next Resolve). Either is almost always a bug — the typed accessor
// emitted by protoc-gen-chameleon-go is the supported reads-path.
var Analyzer = &analysis.Analyzer{
	Name: "chameleonlint",
	Doc: "flags reads / writes of pxfv1.Secret.Value " +
		"(or structurally equivalent Secret types). " +
		"Value is empty after chameleon.parse.Move runs; use the " +
		"typed accessor (e.g. AppConfigSecret_<Field>(res.Secrets)) or " +
		"res.Secrets.Get(\"<path>\") instead.",
	Run: runLint,
}

// lintCmd implements `chameleon lint <pkg-pattern>...`. The minimal
// driver — load packages, run Analyzer, print diagnostics — gives us
// help/usage text owned by the subcommand instead of singlechecker's
// flat "<Analyzer.Name>: ..." banner. Returns the process exit code:
// 0 = clean, 1 = diagnostics emitted or load/analysis error, 2 = bad
// flags. main exits with this value.
func lintCmd(out, errOut *os.File, args []string) int {
	fs := flag.NewFlagSet("chameleon lint", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon lint — flag reads / writes of pxfv1.Secret.Value.

Value is cleared by chameleon.parse.Move once the plaintext is
extracted into a memguard Enclave. Reads return ""; writes leave the
runtime view untouched (parse.Move clears them again on the next
Resolve). Use the typed accessor (e.g.
AppConfigSecret_<Field>(res.Secrets)) or
res.Secrets.Get("<dotted.path>") to reach the live plaintext.

Usage:
    chameleon lint <package>...

Examples:
    chameleon lint ./...
    chameleon lint github.com/acme/svc/internal/config`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		fs.Usage()
		return 2
	}

	cfg := &packages.Config{Mode: packages.LoadAllSyntax}
	initial, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintln(errOut, "chameleon lint:", err)
		return 1
	}
	if n := packages.PrintErrors(initial); n > 0 {
		return 1
	}
	if len(initial) == 0 {
		fmt.Fprintln(errOut, "chameleon lint: no packages matched")
		return 1
	}

	graph, err := checker.Analyze([]*analysis.Analyzer{Analyzer}, initial, nil)
	if err != nil {
		fmt.Fprintln(errOut, "chameleon lint:", err)
		return 1
	}
	if err := graph.PrintText(out, -1); err != nil {
		fmt.Fprintln(errOut, "chameleon lint:", err)
		return 1
	}
	for act := range graph.All() {
		if act.IsRoot && len(act.Diagnostics) > 0 {
			return 1
		}
	}
	return 0
}

func runLint(pass *analysis.Pass) (interface{}, error) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Value" {
				return true
			}
			named := receiverNamed(pass.TypesInfo, sel.X)
			if named == nil {
				return true
			}
			if !isPxfSecret(named) {
				return true
			}
			pass.Reportf(sel.Sel.Pos(),
				"pxfv1.Secret.Value is cleared by chameleon.parse.Move and reads return \"\"; "+
					"use the typed accessor (e.g. AppConfigSecret_<Field>(res.Secrets)) or "+
					"res.Secrets.Get(\"<dotted.path>\") to access the plaintext via memguard")
			return true
		})
	}
	return nil, nil
}

// receiverNamed returns the named type underlying the receiver of a
// selector expression, peeling pointers and aliases. Returns nil if
// the receiver isn't a named struct.
func receiverNamed(info *types.Info, x ast.Expr) *types.Named {
	if info == nil {
		return nil
	}
	tv, ok := info.Types[x]
	if !ok {
		return nil
	}
	t := tv.Type
	// Peel pointers; chameleon-side Secret is usually accessed through
	// a pointer (cfg.DbPassword *pxfv1.Secret).
	for {
		ptr, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	return named
}

// isPxfSecret reports whether t names the pxf.Secret Go binding. Two
// matching strategies, ORed:
//
//  1. Import-path match against known canonical locations. Robust but
//     bound to the import paths we know about.
//
//  2. Structural fallback: a type named `Secret` in a package named
//     `pxfv1` with `Value`, `Hint`, `Fingerprint` string fields.
//     Catches vendored / re-generated copies that don't sit at the
//     canonical import path but follow the standard `go_package`
//     convention (`pkg/pxf;pxfv1`).
func isPxfSecret(t *types.Named) bool {
	obj := t.Obj()
	if obj == nil || obj.Name() != "Secret" {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	// Canonical paths.
	switch pkg.Path() {
	case
		"github.com/trendvidia/protowire/proto/pxf",
		"github.com/trendvidia/protowire-go/proto/pxf/v1":
		return true
	}
	// Structural fallback. Requires:
	//   - package name "pxfv1" (standard go_package convention).
	//   - struct underlying.
	//   - string fields Value, Hint, Fingerprint.
	if pkg.Name() != "pxfv1" {
		return false
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	have := map[string]bool{}
	n := st.NumFields()
	for i := range n {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		if b, ok := f.Type().(*types.Basic); ok && b.Kind() == types.String {
			have[f.Name()] = true
		}
	}
	return have["Value"] && have["Hint"] && have["Fingerprint"]
}
