// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chameleon_test

import (
	"os/exec"
	"strings"
	"testing"

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

// plaintextPath is every package a consumer can reach without opting
// into decryption: the chameleon.Load shortcut, the resolver, and the
// passes it runs. Deliberately excludes bundle / internal/bundler
// (bundling re-encrypts, so it links a backend on purpose) and cmd
// (binaries, not consumer surface).
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
// against every package in the graph.
var keyManagement = []struct{ prefix, what string }{
	{"github.com/trendvidia/chameleon/sops", "chameleon's sops bridge"},
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
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH; cannot inspect the package graph")
	}

	args := append([]string{"list", "-deps"}, pkgs...)
	out, err := exec.CommandContext(t.Context(), "go", args...).CombinedOutput()
	require.NoError(t, err, "go list -deps failed: %s", out)

	lines := strings.Fields(string(out))
	require.NotEmpty(t, lines, "go list -deps returned nothing — the check below would pass vacuously")
	return lines
}

func TestPlaintextPathLinksNoKeyManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}

	for _, pkg := range deps(t, plaintextPath...) {
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
	var hits int
	for _, pkg := range deps(t, "github.com/trendvidia/chameleon/sops") {
		for _, km := range keyManagement {
			if strings.HasPrefix(pkg, km.prefix) {
				hits++
			}
		}
	}
	require.NotZero(t, hits,
		"chameleon/sops must trip the key-management check; if it no longer does, "+
			"the prefixes in keyManagement are stale and the plaintext-path test above proves nothing")
}
