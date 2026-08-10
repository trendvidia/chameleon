// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// Multi-environment age round-trip through the CLI binary. The
// in-process TestBuild_EndToEnd_Age in internal/bundler/build_test.go
// covers the per-env recipients path via the Go API; this file does
// the same flow through the actual `chameleon` binary:
//
//     pre-encrypted source layers (one key)
//       -> chameleon bundle build  (re-encrypts per-env)
//         -> chameleon get --env=X (decrypts that env's layer)
//
// Three envs, each with a distinct destination key, prove the
// per-env recipient isolation works end-to-end:
//
//   - prod-key can read prod's layer but NOT sandbox's
//   - sandbox-key can read sandbox's layer but NOT prod's
//   - the shared `base.pxf` (ONCE layer reachable from all envs)
//     is re-encrypted to the UNION of recipients, so any env's key
//     can decrypt its share
//
// Gated behind `-tags integration`. Reuses encryptForTest /
// runBinary helpers from the same package.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envIdentity packages an age identity + recipient for one env in
// the multi-env fixture. Each env gets its own.
type envIdentity struct {
	name      string // env name (prod, sandbox, dev)
	identity  string // AGE-SECRET-KEY-... (caller keeps secret)
	recipient string // age1... (manifest-side)
}

func genEnvIdentity(t *testing.T, name string) envIdentity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	require.NoError(t, err, "generate %s identity", name)
	return envIdentity{
		name:      name,
		identity:  id.String(),
		recipient: id.Recipient().String(),
	}
}

// multiEnvFixtures stamps the test layout on disk and returns the
// directory plus the per-env identities. The source layers are
// encrypted to a separate "src" identity that the bundle build
// step uses via SOPS_AGE_KEY_FILE; the per-env destination
// identities live only in the manifest's recipients_by_env.
type multiEnvSetup struct {
	dir      string
	src      envIdentity
	prod     envIdentity
	sandbox  envIdentity
	bundle   string
	manifest string
	chain    string
	schema   string
	include  string
}

func setupMultiEnv(t *testing.T) multiEnvSetup {
	t.Helper()
	dir := t.TempDir()

	src := genEnvIdentity(t, "src")
	prod := genEnvIdentity(t, "prod")
	sandbox := genEnvIdentity(t, "sandbox")

	// Schema: one scalar + one Secret. The scalar (env_tag) lets us
	// distinguish envs in resolved output without needing decryption
	// for the assertion. The Secret (db_password) exercises the
	// narrow + redact path.
	files := map[string]string{
		"pxf/secret.proto": `syntax = "proto3";
package pxf;
message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`,
		"app.proto": `syntax = "proto3";
package multienv.v1;
import "pxf/secret.proto";
message Cfg {
  string base_url = 1;
  string env_tag = 2;
  pxf.Secret db_password = 3;
}
`,
		"chain.pxf": `version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "sandbox" }
]
layers = [
  { id = "base" path = "base.pxf"        for_each = ONCE    encrypted = true required = true }
  { id = "env"  path = "env/${env}.pxf"  for_each = PER_ENV encrypted = true required = true }
]
`,
		"manifest.pxf": `modules = [{
  name = "billing-worker"
  allow = ["db_password"]
  recipients_by_env = {
    "prod":    { age = ["` + prod.recipient + `"] }
    "sandbox": { age = ["` + sandbox.recipient + `"] }
  }
}]
`,
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	// Encrypt the three source layers to the src recipient. The
	// bundle build step needs to decrypt these (via SOPS_AGE_KEY_FILE
	// pointing at src.identity) before re-encrypting per-env.
	writeEncrypted := func(name string, plain string) {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		ct, err := encryptForTest(t, []byte(plain), src.recipient)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(full, ct, 0o644))
	}
	writeEncrypted("base.pxf", `base_url = "http://shared"
db_password = "ROTATE-ME"
`)
	writeEncrypted("env/prod.pxf", `env_tag = "prod"
`)
	writeEncrypted("env/sandbox.pxf", `env_tag = "sandbox"
`)

	return multiEnvSetup{
		dir:      dir,
		src:      src,
		prod:     prod,
		sandbox:  sandbox,
		bundle:   filepath.Join(dir, "billing-worker.bundle"),
		manifest: filepath.Join(dir, "manifest.pxf"),
		chain:    filepath.Join(dir, "chain.pxf"),
		schema:   filepath.Join(dir, "app.proto"),
		include:  dir,
	}
}

// buildMultiEnvBundle runs `chameleon bundle build` against the
// multi-env fixture with the src key in SOPS_AGE_KEY so the bundler
// can decrypt source layers. Returns the captured stderr for
// diagnostics on failure.
func buildMultiEnvBundle(t *testing.T, s multiEnvSetup) {
	t.Helper()
	t.Setenv("SOPS_AGE_KEY", s.src.identity)
	args := []string{
		"bundle", "build",
		"--manifest=" + s.manifest,
		"--module=billing-worker",
		"--schema=" + s.schema,
		"--include=" + s.include,
		"--message=multienv.v1.Cfg",
		"--chain=" + s.chain,
		"--layers=" + s.dir,
		"--decrypter=sops",
		"--out=" + s.bundle,
	}
	_, stderr, exit := runBinary(t, args...)
	require.Equal(t, 0, exit, "bundle build exit; stderr: %s", stderr)
}

func TestMultiEnv_ProdKeyResolvesProdEnv(t *testing.T) {
	// End-to-end happy path for one env. The bundle re-encrypted
	// env/prod.pxf to prod's recipient; setting SOPS_AGE_KEY to
	// prod's identity must let `get --env=prod` decrypt it and
	// surface the env_tag scalar.
	s := setupMultiEnv(t)
	buildMultiEnvBundle(t, s)

	t.Setenv("SOPS_AGE_KEY", s.prod.identity)
	stdout, stderr, exit := runBinary(t,
		"get",
		"--schema="+s.schema,
		"--include="+s.include,
		"--message=multienv.v1.Cfg",
		"--bundle="+s.bundle,
		"--env=prod",
		"--decrypter=sops",
	)
	require.Equal(t, 0, exit, "get --env=prod with prod-key exit; stderr: %s", stderr)

	// base.pxf (shared) decrypts because base was re-encrypted to
	// the UNION of recipients.
	assert.Contains(t, stdout, `base_url = "http://shared"`,
		"shared base layer must be readable by any env's key")

	// env/prod.pxf decrypts because prod-key matches the per-env
	// recipient on that layer.
	assert.Contains(t, stdout, `env_tag = "prod"`,
		"prod's env-specific scalar must surface in --env=prod")

	// db_password is in the allow list → present in output, but
	// redacted by `get`'s redaction pass.
	assert.Contains(t, stdout, `<redacted>`,
		"allowed secret must be present and redacted")
	assert.NotContains(t, stdout, "ROTATE-ME",
		"plaintext of the source secret must never reach stdout")
}

func TestMultiEnv_SandboxKeyResolvesSandboxEnv(t *testing.T) {
	// Same shape as the prod test, for the sandbox env. Pinning
	// both envs independently catches an asymmetry regression
	// (e.g., the recipient-fan-out picking up only the first map
	// entry).
	s := setupMultiEnv(t)
	buildMultiEnvBundle(t, s)

	t.Setenv("SOPS_AGE_KEY", s.sandbox.identity)
	stdout, stderr, exit := runBinary(t,
		"get",
		"--schema="+s.schema,
		"--include="+s.include,
		"--message=multienv.v1.Cfg",
		"--bundle="+s.bundle,
		"--env=sandbox",
		"--decrypter=sops",
	)
	require.Equal(t, 0, exit, "get --env=sandbox with sandbox-key exit; stderr: %s", stderr)

	assert.Contains(t, stdout, `env_tag = "sandbox"`,
		"sandbox's env-specific scalar must surface in --env=sandbox")
	assert.NotContains(t, stdout, `env_tag = "prod"`,
		"prod's env-specific value must not leak into sandbox resolution")
}

func TestMultiEnv_WrongKeyFailsToDecryptEnvLayer(t *testing.T) {
	// The load-bearing isolation property: sandbox's per-env layer
	// was encrypted ONLY to sandbox's recipient, so prod-key cannot
	// decrypt it. `get --env=sandbox` with prod-key must fail. If it
	// succeeded, the recipient isolation would be broken — a
	// production-critical regression that needs to fail loud.
	s := setupMultiEnv(t)
	buildMultiEnvBundle(t, s)

	t.Setenv("SOPS_AGE_KEY", s.prod.identity)
	// --decrypter=sops is load-bearing here, not decoration: without
	// it the CLI wires resolver.WithoutDecrypter and this test passes
	// on the non-zero exit from "decryption disabled" — proving
	// nothing about recipient isolation, which is the whole point.
	stdout, stderr, exit := runBinary(t,
		"get",
		"--schema="+s.schema,
		"--include="+s.include,
		"--message=multienv.v1.Cfg",
		"--bundle="+s.bundle,
		"--env=sandbox",
		"--decrypter=sops",
	)
	assert.NotEqual(t, 0, exit,
		"prod-key must not decrypt sandbox's per-env layer")
	assert.NotContains(t, stderr, "decryption disabled",
		"the failure must come from the key, not from a resolver with no decrypter")
	assert.Empty(t, stdout,
		"no resolution should be printed on decrypt failure")
	// The sops error wording varies; assert that some decrypt-
	// related stderr surfaced rather than pinning the exact phrase.
	assert.True(t,
		strings.Contains(stderr, "decrypt") ||
			strings.Contains(stderr, "no key could decrypt"),
		"stderr should identify the decrypt failure, got: %s", stderr)
}

func TestMultiEnv_SharedBaseLayerEncryptedToBothRecipients(t *testing.T) {
	// The ONCE-shared base.pxf is reachable from both prod and
	// sandbox, so it must be re-encrypted to the UNION of both
	// envs' recipients. This is the recipient-union path inside
	// unionRecipients, exercised at the binary boundary by running
	// `get --env=X` with BOTH keys independently and asserting
	// `base_url` surfaces in each. (The per-env layer assertions
	// live in the two tests above.)
	s := setupMultiEnv(t)
	buildMultiEnvBundle(t, s)

	for _, env := range []struct {
		name     string
		identity string
	}{
		{"prod", s.prod.identity},
		{"sandbox", s.sandbox.identity},
	} {
		t.Run(env.name, func(t *testing.T) {
			t.Setenv("SOPS_AGE_KEY", env.identity)
			stdout, stderr, exit := runBinary(t,
				"get",
				"--schema="+s.schema,
				"--include="+s.include,
				"--message=multienv.v1.Cfg",
				"--bundle="+s.bundle,
				"--env="+env.name,
				"--decrypter=sops",
			)
			require.Equal(t, 0, exit, "%s exit; stderr: %s", env.name, stderr)
			assert.Contains(t, stdout, `base_url = "http://shared"`,
				"shared base layer must decrypt for %s (UNION recipient)", env.name)
		})
	}
}
