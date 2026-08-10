// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chameleon_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What a plaintext consumer links is a public contract, and it is not
// one the type checker enforces: a single `import
// "github.com/trendvidia/chameleon/sops"` added to resolver.go compiles
// fine and silently puts every cloud KMS SDK into every consumer's
// binary. That is exactly what happened up to v0.22.1 — 2.7 MB → 68 MB
// on a program resolving two plaintext layers
// (trendvidia/chameleon#130).
//
// So the graph itself is the assertion. These tests shell out to `go
// list -deps`, which reports the real package graph the linker will
// see, and fail if a key-management dependency reappears on the
// plaintext path.

// modulePath is this module, and the prefix every package below is
// filtered against.
const modulePath = "github.com/trendvidia/chameleon"

// plaintextPath returns every package a consumer can reach without
// opting into decryption: the chameleon.Load shortcut, the resolver,
// the passes it runs, the generated protos, and the bundle reader
// (opening a bundle is a read, and its output feeds straight into
// chameleon.Load).
//
// Derived from `go list ./...` rather than written out, because a
// hand-kept list is guarded only by remembering to edit it: a public
// package added next month that imports chameleon/sops for one type is
// the exact mistake this file exists to catch, and it would sail past
// a literal that never mentions it. Only three things are subtracted,
// each for a reason that will not change:
//
//   - internal/* — not consumer surface, and internal/bundler is the
//     *write* side, which links a backend on purpose (bundling
//     re-encrypts).
//   - cmd/* — binaries. `chameleon bundle` decrypts by design.
//   - chameleon/sops — the backend itself. It is what the negative
//     control below points the same check at.
func plaintextPath(t *testing.T) []string {
	t.Helper()

	var out []string
	for _, pkg := range list(t, "./...") {
		switch {
		case pkg == selfPrefix,
			strings.HasPrefix(pkg, modulePath+"/internal/"),
			strings.HasPrefix(pkg, modulePath+"/cmd/"):
			continue
		}
		out = append(out, pkg)
	}

	// Derivation means the list can go wrong quietly — a filter that
	// over-matches leaves a shorter set that still passes. Assert the
	// two endpoints of the plaintext path are in it: the entry point
	// consumers call and the package that regressed in #130.
	for _, must := range []string{modulePath, modulePath + "/resolver", modulePath + "/bundle"} {
		require.Contains(t, out, must, "derived plaintext set lost a package it must cover")
	}
	return out
}

// selfPrefix is chameleon's own bridge package — the one entry in
// keyManagement that is in this module. The negative control below
// skips it; see the comment there.
const selfPrefix = "github.com/trendvidia/chameleon/sops"

// keyManagement is the weight #130 was about. Prefixes, matched
// against every package in the graph.
var keyManagement = []struct{ prefix, what string }{
	{selfPrefix, "chameleon's sops bridge"},
	{"github.com/trendvidia/sops/", "the sops library"},
	{"github.com/getsops/", "upstream sops"},
	{"github.com/aws/", "the AWS SDK"},
	{"cloud.google.com/", "the Google Cloud SDK"},
	{"github.com/Azure/", "the Azure SDK"},
	{"github.com/hashicorp/vault", "the Vault API"},
}

// deps returns the full package graph of pkgs, as `go list -deps` sees
// it.
func deps(t *testing.T, pkgs ...string) []string {
	t.Helper()
	return list(t, append([]string{"-deps"}, pkgs...)...)
}

// list runs `go list` with args and returns the package names it
// printed.
func list(t *testing.T, args ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH; cannot inspect the package graph")
	}

	// stdout only: `go list` writes module-download progress ("go:
	// downloading github.com/aws/aws-sdk-go-v2 v1.30.0") to stderr on
	// a cold cache, and folding that in would feed the checks below
	// package names that are not in the graph at all.
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "go list failed: %s", stderr.String())

	lines := strings.Fields(string(out))
	require.NotEmpty(t, lines, "go list returned nothing — the check below would pass vacuously")
	return lines
}

func TestPlaintextPathLinksNoKeyManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	for _, pkg := range deps(t, plaintextPath(t)...) {
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

func TestKeyManagementCheckCatchesTheSopsPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	// Negative control. The test above passes trivially if the prefix
	// list stops matching anything real (a module path bump, say), so
	// point the same check at the package that must fail it.
	//
	// Counts hits on the *upstream* prefixes only. `go list -deps X`
	// includes X itself, so counting every prefix would score a hit on
	// keyManagement[0] ("chameleon/sops") unconditionally and the
	// control could never fail — which is the exact failure mode it
	// exists to rule out.
	hits := map[string]int{}
	for _, pkg := range deps(t, "github.com/trendvidia/chameleon/sops") {
		for _, km := range keyManagement {
			if km.prefix == selfPrefix {
				continue
			}
			if strings.HasPrefix(pkg, km.prefix) {
				hits[km.prefix]++
			}
		}
	}

	// Every upstream prefix must match something: chameleon/sops
	// reaches all of them (the sops library pulls each KMS backend in
	// unconditionally), so a prefix that matches nothing is a stale
	// prefix, and a stale prefix silently weakens the plaintext-path
	// test above.
	for _, km := range keyManagement {
		if km.prefix == selfPrefix {
			continue
		}
		assert.NotZerof(t, hits[km.prefix],
			"chameleon/sops links no package under %q (%s), so that prefix matches nothing and "+
				"TestPlaintextPathLinksNoKeyManagement is not actually checking for it — "+
				"the module path probably moved", km.prefix, km.what)
	}
}
