// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chameleon_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// What a plaintext consumer carries is a public contract, and it is not
// one the type checker enforces. A single `import
// "github.com/trendvidia/chameleon/sops"` added to resolver.go compiles
// fine and silently puts every cloud KMS SDK into every consumer's
// binary; a single import of the sops *module* from any package or test
// in this module puts them back into every consumer's module graph.
// Both happened up to v0.22.1 — 2.7 MB → 68 MB on a program resolving
// two plaintext layers, and 35 → 347 modules in its `go list -m all`
// (trendvidia/chameleon#130).
//
// So the graphs themselves are the assertions. These tests read the
// real package graph (`go list -deps`) and the real module
// requirements (`go mod edit -json`), and fail if key management
// reappears in either.

// plaintextPath is every package a consumer can reach without opting
// into decryption: the chameleon.Load shortcut, the resolver, and the
// passes it runs. Deliberately excludes bundle / internal/bundler
// (bundling re-encrypts, so a caller supplies a backend on purpose)
// and cmd (binaries, not consumer surface).
var plaintextPath = []string{
	"github.com/trendvidia/chameleon",
	"github.com/trendvidia/chameleon/chain",
	"github.com/trendvidia/chameleon/crypt",
	"github.com/trendvidia/chameleon/defaults",
	"github.com/trendvidia/chameleon/interpolate",
	"github.com/trendvidia/chameleon/merge",
	"github.com/trendvidia/chameleon/parse",
	"github.com/trendvidia/chameleon/resolver",
	"github.com/trendvidia/chameleon/secret",
	"github.com/trendvidia/chameleon/source",
	"github.com/trendvidia/chameleon/validate",
	"github.com/trendvidia/chameleon/wkt",
}

// keyManagement is the weight #130 was about. Prefixes, matched
// against every package in the graph and every module in the
// requirements.
var keyManagement = []struct{ prefix, what string }{
	{"github.com/trendvidia/chameleon/sops", "chameleon's sops bridge"},
	{"github.com/trendvidia/sops/", "the sops library"},
	{"github.com/getsops/", "upstream sops"},
	{"github.com/aws/", "the AWS SDK"},
	{"cloud.google.com/", "the Google Cloud SDK"},
	{"github.com/Azure/", "the Azure SDK"},
	{"github.com/hashicorp/vault", "the Vault API"},
}

// goCmd runs a go subcommand in dir and returns stdout, skipping the
// test if there is no toolchain to run.
func goCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH; cannot inspect the module graph")
	}
	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = dir
	// stdout only. The go command writes progress to stderr — a
	// toolchain switch alone emits "go: downloading go1.x.y" — and
	// both callers below parse what comes back: one as JSON, one as a
	// whitespace-separated package list. Folding stderr in turns a
	// benign notice into "invalid character 'g'", or into a package
	// name that no prefix check ever matches.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "go %s in %q failed: %s", strings.Join(args, " "), dir, stderr.String())
	return string(out)
}

// deps returns the full package graph of pkgs as seen from the module
// rooted at dir.
func deps(t *testing.T, dir string, pkgs ...string) []string {
	t.Helper()
	lines := strings.Fields(goCmd(t, dir, append([]string{"list", "-deps"}, pkgs...)...))
	require.NotEmpty(t, lines, "go list -deps returned nothing — the check below would pass vacuously")
	return lines
}

func TestPlaintextPathLinksNoKeyManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	for _, pkg := range deps(t, ".", plaintextPath...) {
		for _, km := range keyManagement {
			if strings.HasPrefix(pkg, km.prefix) {
				t.Errorf("plaintext path links %s (%s).\n"+
					"Decryption is opt-in at link time: the resolver takes crypt.Decrypter and a\n"+
					"backend registers itself (see chameleon/crypt). Importing an implementation\n"+
					"from any package above puts key management in every consumer's binary —\n"+
					"trendvidia/chameleon#130.", pkg, km.what)
			}
		}
	}
}

func TestLibraryModuleRequiresNoKeyManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go mod edit")
	}

	// Package-level cleanliness is not enough: a *test* in this module
	// that imports the sops module would keep the whole KMS tree in
	// this go.mod, and a consumer's `go list -m all` reports our
	// requirements whether or not their build reaches the code. That
	// is why chameleon/sops and cmd/chameleon are separate modules.
	var mod struct {
		Require []struct {
			Path     string
			Indirect bool
		}
	}
	require.NoError(t, json.Unmarshal([]byte(goCmd(t, ".", "mod", "edit", "-json")), &mod))
	require.NotEmpty(t, mod.Require, "go mod edit -json listed no requirements — check would pass vacuously")

	for _, r := range mod.Require {
		for _, km := range keyManagement {
			if strings.HasPrefix(r.Path, km.prefix) {
				t.Errorf("library go.mod requires %s (%s).\n"+
					"Every consumer's `go list -m all`, dependency review and SBOM inherits this\n"+
					"module's requirements. Anything needing a backend belongs in the sops or\n"+
					"cmd/chameleon module — trendvidia/chameleon#130.", r.Path, km.what)
			}
		}
	}
}

func TestKeyManagementCheckCatchesTheSopsModule(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	// Negative control. Both tests above pass trivially if the prefix
	// list stops matching anything real (a module path bump, say), so
	// point the same check at the module that must fail it. Runs
	// inside sops/ so it holds with or without the workspace.
	var hits int
	for _, pkg := range deps(t, "sops", "./...") {
		for _, km := range keyManagement {
			if strings.HasPrefix(pkg, km.prefix) {
				hits++
			}
		}
	}
	require.NotZero(t, hits,
		"chameleon/sops must trip the key-management check; if it no longer does, "+
			"the prefixes in keyManagement are stale and the tests above prove nothing")
}
