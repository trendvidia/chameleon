// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chameleon_test

import (
	"encoding/json"
	"os"
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
// The same is true one level up. A *test* in this module that imports
// the sops module would keep the whole KMS tree in this go.mod, and a
// consumer's `go list -m all`, dependency review and SBOM inherit our
// requirements whether or not their build reaches the code: 347 modules
// for a plaintext consumer, against 35 now that chameleon/sops and
// cmd/chameleon are modules of their own.
//
// So the graphs themselves are the assertion. These tests shell out to
// `go list -deps` for the package graph the linker will see and to `go
// mod edit -json` for the requirements consumers inherit, and fail if
// key management reappears in either.
//
// Key management is the first of two weights pinned here. The second is
// a protobuf compiler (#137, below), which got onto the plaintext path
// by a different mechanism and is checked against the package graph
// only — unlike key management it is a legitimate requirement of this
// go.mod, because the tests in this repo compile schemas from source.
// Each check is paired with a negative control that points it at a
// module which must fail it, so a prefix that stops matching anything
// real cannot leave the check passing vacuously.

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
// a literal that never mentions it. Only four things are subtracted,
// each for a reason that will not change:
//
//   - internal/* — not consumer surface, and internal/bundler is the
//     *write* side, which takes a backend from its caller on purpose
//     (bundling re-encrypts).
//   - hack/* — repo-local maintainer tooling (the changelog assembler),
//     run at a terminal and reachable by nobody downstream. Without this
//     filter, a release helper that imports chameleon/sops — an ordinary
//     thing for one to do — reds this test with a message about what
//     consumers link.
//   - cmd/* — binaries. `chameleon bundle` decrypts by design.
//   - chameleon/sops — the backend itself. It is what the negative
//     control below points the same check at.
//
// Since v0.23.0 the last two are also module boundaries — chameleon/sops
// and cmd/chameleon are separate modules, so `go list ./...` here does
// not reach them at all. The filters stay: they cost nothing, they keep
// this test working if a package ever moves back, and cmd/
// protoc-gen-chameleon-go is still in this module and still a binary.
func plaintextPath(t *testing.T) []string {
	t.Helper()

	var out []string
	for _, pkg := range list(t, "./...") {
		switch {
		case pkg == selfPrefix,
			strings.HasPrefix(pkg, modulePath+"/internal/"),
			strings.HasPrefix(pkg, modulePath+"/hack/"),
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

// deps returns the full package graph of pkgs in this module, as `go
// list -deps` sees it.
func deps(t *testing.T, pkgs ...string) []string {
	t.Helper()
	return depsIn(t, ".", pkgs...)
}

// depsIn is deps for a package graph rooted in another module of this
// repo. Needed since v0.23.0: chameleon/sops is its own module, so
// `go list -deps github.com/trendvidia/chameleon/sops` from here does
// not resolve, with or without the workspace.
func depsIn(t *testing.T, dir string, pkgs ...string) []string {
	t.Helper()
	return listIn(t, dir, append([]string{"-deps"}, pkgs...)...)
}

// list runs `go list` in this module with args and returns the package
// names it printed.
func list(t *testing.T, args ...string) []string {
	t.Helper()
	return listIn(t, ".", args...)
}

// listIn runs `go list` with args in dir.
func listIn(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH; cannot inspect the package graph")
	}

	// stdout only: `go list` writes module-download progress ("go:
	// downloading github.com/aws/aws-sdk-go-v2 v1.30.0") to stderr on
	// a cold cache, and folding that in would feed the checks below
	// package names that are not in the graph at all.
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "go list in %q failed: %s", dir, stderr.String())

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

func TestLibraryModuleRequiresNoKeyManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go mod edit")
	}

	// Package-level cleanliness is not enough. `go mod tidy` records a
	// requirement for every module providing a transitively imported
	// package — tests included — so one test here importing
	// chameleon/sops would put the whole KMS tree back in this go.mod,
	// and from there into every consumer's `go list -m all`. That is
	// what makes chameleon/sops and cmd/chameleon separate modules.
	//
	// Reads the requirements rather than the file text: go.mod's own
	// comments name the sops fork, and a substring check would trip
	// over prose.
	var mod struct {
		Require []struct {
			Path     string
			Indirect bool
		}
	}
	require.NoError(t, json.Unmarshal([]byte(goModJSON(t)), &mod))
	require.NotEmpty(t, mod.Require, "go mod edit -json listed no requirements — check would pass vacuously")

	for _, r := range mod.Require {
		for _, km := range keyManagement {
			if strings.HasPrefix(r.Path, km.prefix) {
				t.Errorf("library go.mod requires %s (%s).\n"+
					"Every consumer's `go list -m all`, dependency review and SBOM inherits this\n"+
					"module's requirements, reached or not. Anything needing a backend belongs in\n"+
					"the sops or cmd/chameleon module — trendvidia/chameleon#130.", r.Path, km.what)
			}
		}
	}
}

// goModJSON returns this module's go.mod as JSON. Separate from list()
// because `go mod edit` takes no package arguments and its output is a
// document, not a line per package.
func goModJSON(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH; cannot inspect the module graph")
	}
	// stdout only, for the same reason list() reads stdout: the go
	// command writes progress to stderr — a toolchain switch alone
	// emits "go: downloading go1.x.y" — and folding it in turns a
	// benign notice into `invalid character 'g'` from the parser
	// below, which reads as a broken test rather than its real signal.
	cmd := exec.CommandContext(t.Context(), "go", "mod", "edit", "-json")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "go mod edit -json failed: %s", stderr.String())
	return string(out)
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
	for _, pkg := range depsIn(t, "sops", "./...") {
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

// protoCompiler is the second thing the plaintext path must not link,
// for the same reason as keyManagement but by a different mechanism
// (trendvidia/chameleon#137). Key management got in through an import
// of an *implementation*; this got in through compiling proto source at
// *runtime* — internal/header built the chameleon.v1.LayerHeader
// descriptor from an inline copy of the message, which put a protobuf
// compiler (13 packages, ~3 MB) in every binary that resolves a
// plaintext layer, to parse a five-field message the generated bindings
// already describe.
//
// A compiler belongs on the path that compiles user-supplied schemas —
// `chameleon explain`, in the cmd/chameleon module, which the negative
// control below points at. It does not belong on the resolve path.
//
// Unlike keyManagement there is no go.mod counterpart: protocompile is
// a direct requirement of this module and stays one, because tests
// throughout the repo (resolver, merge, wkt, chain, validate, and
// internal/header's own descriptor_test.go) build schemas from source.
// The package graph is what separates a test-only compiler from one a
// consumer links, so the package graph is the only assertion available.
//
// The compiler is the trendvidia fork of protocompile, which is also
// what protowire-go compiles its fixtures with; upstream
// bufbuild/protocompile left this repository's graphs entirely, and
// TestNoUpstreamProtoCompilerAnywhere below keeps it out.
const protoCompiler = "github.com/trendvidia/protocompile"

func TestPlaintextPathLinksNoProtoCompiler(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	for _, pkg := range deps(t, plaintextPath(t)...) {
		if strings.HasPrefix(pkg, protoCompiler) {
			t.Errorf("plaintext path links %s.\n"+
				"Descriptors for chameleon's own protos come from the generated bindings in\n"+
				"proto/chameleon/v1, which the resolve path links either way. Compiling proto\n"+
				"source at runtime to describe a message we already generate costs every\n"+
				"consumer a compiler — trendvidia/chameleon#137.", pkg)
		}
	}
}

func TestProtoCompilerCheckCatchesTheCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	// Negative control, for the reason spelled out in
	// TestKeyManagementCheckCatchesTheSopsPackage: the test above
	// passes vacuously if the prefix stops matching anything real, so
	// point the same check at the module that must fail it.
	// `chameleon explain` compiles user-supplied schemas, which is what
	// protocompile is for.
	var found bool
	for _, pkg := range depsIn(t, "cmd/chameleon", "./...") {
		if strings.HasPrefix(pkg, protoCompiler) {
			found = true
			break
		}
	}
	assert.Truef(t, found,
		"cmd/chameleon links no package under %q, so that prefix matches nothing and "+
			"TestPlaintextPathLinksNoProtoCompiler is not actually checking for it — "+
			"the module path probably moved", protoCompiler)
}

// TestNoUpstreamProtoCompilerAnywhere pins the other half of the compiler
// story (#178). TestPlaintextPathLinksNoProtoCompiler says a consumer's
// build reaches no compiler; this says that the one compiler this
// repository does carry is the trendvidia fork, in every module, and
// that upstream bufbuild/protocompile appears in no module's build list
// — so a consumer's `go list -m all`, dependency review and SBOM name one
// protocompile, not two.
//
// `go list -m all` is read per module with the workspace off, because
// that is the graph a consumer of that module resolves; under go.work the
// three directories would all answer with the workspace's union.
func TestNoUpstreamProtoCompilerAnywhere(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}
	const upstream = "github.com/bufbuild/protocompile"

	// Every module's build list names the fork: the library requires it
	// for its tests, and sops and cmd/chameleon require the library. That
	// is the positive control — a prefix that matched nothing real would
	// otherwise pass the upstream check vacuously.
	for _, dir := range []string{".", "sops", "cmd/chameleon"} {
		t.Run(dir, func(t *testing.T) {
			mods := modulesIn(t, dir)
			require.NotEmpty(t, mods, "go list -m all listed nothing — check would pass vacuously")

			var fork bool
			for _, mod := range mods {
				path := strings.Fields(mod)[0]
				if path == upstream || strings.HasPrefix(path, upstream+"/") {
					t.Errorf("%s: build list names %s.\n"+
						"protowire-go compiles with the trendvidia fork of protocompile and so does\n"+
						"this repository; upstream must not come back through a require or a\n"+
						"transitive dependency — trendvidia/chameleon#178.", dir, mod)
				}
				if path == protoCompiler {
					fork = true
				}
			}
			assert.True(t, fork, "%s: build list does not name %s — the check above matched nothing real", dir, protoCompiler)
		})
	}
}

// modulesIn returns `go list -m all` for the module rooted at dir, with
// the workspace disabled so each module answers for itself.
func modulesIn(t *testing.T, dir string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH; cannot inspect the build list")
	}
	cmd := exec.CommandContext(t.Context(), "go", "list", "-m", "all")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "go list -m all in %s failed: %s", dir, stderr.String())
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}
