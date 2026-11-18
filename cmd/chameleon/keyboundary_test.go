// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"github.com/trendvidia/sops/v4/age/keypb"
)

// keyBoundaryChain is the README's per-environment key example with the
// README's recommended layout: prod and sandbox name different keys,
// dev inherits sandbox's, and the secrets layer is an encrypted PER_ENV
// entry.
const keyBoundaryChain = `version = "1.0.0"
environments = [
  { name = "prod"    key_name = "prod-key" }
  { name = "sandbox" extends = "prod"    key_name = "sandbox-key" }
  { name = "dev"     extends = "sandbox" }
]
layers = [
  { id = "base"    path = "base.pxf"           for_each = ONCE required = true }
  { id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true }
]
`

// TestReveal_DevResolvesWithOnlyItsOwnKey is the end-to-end pin for
// #236. Each secrets file is encrypted to its own environment's age
// key, and the process holds only sandbox-key, which dev inherits.
// Before #236 resolving dev also decrypted secrets/prod.pxf and failed
// for want of prod-key; a process that succeeded had to hold prod's
// key. Now dev never reaches prod's file: it resolves with its own key
// and gets its own value, and prod's secret is nowhere in the output.
func TestReveal_DevResolvesWithOnlyItsOwnKey(t *testing.T) {
	prodID, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	sandboxID, err := age.GenerateX25519Identity()
	require.NoError(t, err)

	dir := t.TempDir()
	writeFixture(t, dir, map[string]string{
		"pxf/secret.proto": `syntax = "proto3";
package pxf;
message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`,
		"app.proto": `syntax = "proto3";
package smoke.v1;
import "pxf/secret.proto";
message Cfg {
  string base_url = 1;
  pxf.Secret api_key = 2;
}
`,
		"chain.pxf": keyBoundaryChain,
		"base.pxf":  "base_url = \"http://x\"\n",
	})
	encryptLayer := func(rel, plain string, recipient *age.X25519Recipient) {
		t.Helper()
		ct, err := encryptForTest(t, []byte(plain), recipient.String())
		require.NoError(t, err)
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, ct, 0o644))
	}
	encryptLayer("secrets/prod.pxf", "api_key = \"prod-only-secret\"\n", prodID.Recipient())
	encryptLayer("secrets/sandbox.pxf", "api_key = \"sandbox-secret\"\n", sandboxID.Recipient())
	encryptLayer("secrets/dev.pxf", "api_key = \"dev-secret\"\n", sandboxID.Recipient())

	// The process's only key: sandbox-key. HOME is isolated so the
	// default keys.pxf cannot supply another one.
	keyFile, err := pxf.Marshal(&keypb.AgeKeyFile{Keys: map[string]string{"sandbox-key": sandboxID.String()}})
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "keys.pxf")
	require.NoError(t, os.WriteFile(keyPath, keyFile, 0o600))
	t.Setenv("HOME", t.TempDir())
	unsetForTest(t, "SOPS_AGE_KEY", "SOPS_AGE_KEY_CMD", "SOPS_AGE_SSH_PRIVATE_KEY_FILE", "SOPS_AGE_SSH_PRIVATE_KEY_CMD")
	t.Setenv("SOPS_AGE_KEY_FILE", keyPath)

	reveal := func(env string) (string, error) {
		var out bytes.Buffer
		err := revealCmd(nil, &out, []string{
			"--schema=" + filepath.Join(dir, "app.proto"),
			"--include=" + dir,
			"--message=smoke.v1.Cfg",
			"--chain=" + filepath.Join(dir, "chain.pxf"),
			"--layers=" + dir,
			"--env=" + env,
			"--field=api_key",
			"--decrypter=sops",
		})
		return out.String(), err
	}

	got, err := reveal("dev")
	require.NoError(t, err, "dev must resolve holding only its own (inherited) key")
	assert.Equal(t, "dev-secret", got)

	got, err = reveal("sandbox")
	require.NoError(t, err)
	assert.Equal(t, "sandbox-secret", got)

	// The control: prod's layer is real ciphertext the process cannot
	// open, so the boundary is what kept dev and sandbox away from it.
	_, err = reveal("prod")
	require.Error(t, err, "prod needs prod-key, which this process does not hold")
}

// unsetForTest removes each variable for the test and restores it after.
// t.Setenv(name, "") is not the same thing: the sops age key source
// treats a set-but-empty SOPS_AGE_KEY_CMD as a command to run.
func unsetForTest(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "") // registers the restore; then remove it outright
		require.NoError(t, os.Unsetenv(name))
	}
}
