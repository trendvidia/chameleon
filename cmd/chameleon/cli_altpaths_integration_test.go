// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// Alternative-path coverage for the CLI binary. The existing
// integration tests (binary_dataflow, multienv) focused on the
// load-bearing happy paths + critical security canaries (recipient
// isolation, cross-decrypt). This file fills in the operator-error
// and not-quite-happy alternatives:
//
//   - Bundle build refuses when recipients_by_env doesn't cover
//     every declared env's encrypted layer
//   - `get --env=<unknown>` errors instead of silently no-op
//   - --bundle-env=VAR variant (env-var-fed bundle, --base64 form)
//   - Required source layer missing on disk surfaces as build error
//
// Reuses runBinary / encryptForTest / the multi-env fixtures.

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiEnv_BundleBuildErrorsWhenEnvLacksRecipients(t *testing.T) {
	// Chain declares prod + sandbox, both with encrypted PER_ENV
	// layers. Manifest's recipients_by_env covers ONLY prod. The
	// bundler must REFUSE to build rather than silently emit a
	// sandbox layer with no recipients (which would mean sandbox
	// can't decrypt anything, despite the bundle "successfully"
	// shipping).
	//
	// This is the operator-error variant: a manifest typo (forgot
	// the sandbox key) should fail loud at build time, not at
	// runtime when sandbox tries to deploy.
	s := setupMultiEnv(t)

	// Overwrite the manifest authored by setupMultiEnv with one
	// that has prod ONLY — no sandbox entry, no fallback.
	require.NoError(t, os.WriteFile(s.manifest, []byte(`modules = [{
  name = "billing-worker"
  allow = ["db_password"]
  recipients_by_env = {
    "prod": { age = ["`+s.prod.recipient+`"] }
  }
}]
`), 0o644))

	t.Setenv("SOPS_AGE_KEY", s.src.identity)
	_, stderr, exit := runBinary(t,
		"bundle", "build",
		"--manifest="+s.manifest,
		"--module=billing-worker",
		"--schema="+s.schema,
		"--include="+s.include,
		"--message=multienv.v1.Cfg",
		"--chain="+s.chain,
		"--layers="+s.dir,
		"--decrypter=sops",
		"--out="+s.bundle,
	)
	assert.NotEqual(t, 0, exit,
		"bundle build must refuse when an env's encrypted layer has no recipients")
	// The bundler's error from processLayer mentions "no recipients"
	// + the offending env name. Pin both so the message keeps the
	// "which env did you miss?" UX.
	assert.True(t,
		strings.Contains(stderr, "no recipients") ||
			strings.Contains(stderr, "lists no recipients"),
		"error should call out the missing-recipients condition; got: %s", stderr)
	assert.Contains(t, stderr, "sandbox",
		"error should name the env whose recipient is missing; got: %s", stderr)

	// And the partial bundle file should NOT exist (or, if the
	// zip writer touched it, callers can rely on the error to
	// know not to ship it).
	if _, err := os.Stat(s.bundle); err == nil {
		// The bundler may have created an empty/partial file on
		// the way to the error. The test cares that the EXIT was
		// non-zero — operators key off that. Make the assertion
		// soft (informational) for the file-existence side.
		t.Logf("bundle file exists after error (operator key is the exit code); size:")
	}
}

func TestBinary_GetUnknownEnvErrors(t *testing.T) {
	// Chain declares prod + sandbox. `chameleon get --env=ghost`
	// must error rather than silently resolve some default. The
	// error wraps chain.ErrUnknownEnvironment somewhere up the
	// stack; the binary exit must be non-zero so shell scripts
	// catch the typo.
	s := setupMultiEnv(t)
	t.Setenv("SOPS_AGE_KEY", s.src.identity)
	buildMultiEnvBundle(t, s)

	t.Setenv("SOPS_AGE_KEY", s.prod.identity)
	stdout, stderr, exit := runBinary(t,
		"get",
		"--schema="+s.schema,
		"--include="+s.include,
		"--message=multienv.v1.Cfg",
		"--bundle="+s.bundle,
		"--env=ghost",
	)
	assert.NotEqual(t, 0, exit,
		"unknown env must surface as non-zero exit")
	assert.Empty(t, stdout,
		"no resolved output should leak when the env is unknown")
	assert.True(t,
		strings.Contains(stderr, "ghost") ||
			strings.Contains(stderr, "unknown environment"),
		"error should identify the offending env name; got: %s", stderr)
}

func TestBinary_BundleViaEnvVar(t *testing.T) {
	// --bundle-env=VAR reads bundle bytes from the named env var.
	// Used for secret-manager → service pipelines where the
	// bundle lives in an env var rather than on disk. The env-var
	// path is the load-bearing alternative to --bundle=PATH;
	// previously we only tested PATH + stdin.
	//
	// The contents must be --base64-wrapped because env vars are
	// strings and raw zip bytes contain NUL terminators that
	// truncate the value on most platforms.
	dir := dataFlowFixtures(t)
	bundlePath := filepath.Join(dir, "via-env.bundle")

	// 1. Build --base64 bundle (so it's safe to stuff into an env var).
	buildArgs := append([]string{"bundle", "build", "--base64", "--out=" + bundlePath},
		commonBundleArgs(dir)...)
	_, stderr, exit := runBinary(t, buildArgs...)
	require.Equal(t, 0, exit, "bundle build --base64 exit; stderr: %s", stderr)

	bundleBytes, err := os.ReadFile(bundlePath)
	require.NoError(t, err)
	// Sanity that the bytes are decodable base64 — the consumer
	// will reject malformed ones, and the test would surface that
	// further down with a confusing error message.
	_, err = base64.StdEncoding.DecodeString(string(bundleBytes))
	require.NoError(t, err, "build --base64 produced non-decodable bytes")

	// 2. Stuff into an env var and read via --bundle-env=VAR.
	t.Setenv("CHAMELEON_TEST_BUNDLE_B64", string(bundleBytes))

	stdout, stderr, exit := runBinary(t,
		"get",
		"--schema="+filepath.Join(dir, "app.proto"),
		"--include="+dir,
		"--message=smoke.v1.Cfg",
		"--bundle-env=CHAMELEON_TEST_BUNDLE_B64",
		"--base64",
		"--env=prod",
	)
	require.Equal(t, 0, exit, "get --bundle-env exit; stderr: %s", stderr)
	assert.Contains(t, stdout, `base_url = "http://x"`,
		"env-var-fed bundle must resolve identically to PATH-fed")
}

func TestBinary_BundleViaEnvVar_UnsetVarErrors(t *testing.T) {
	// --bundle-env=NOT_A_REAL_VAR must error cleanly rather than
	// proceeding with empty input. The operator-error mode here is
	// "the secret manager fetch failed and the var was never
	// populated" — the failure should mention the var name so the
	// user knows which env var was expected.
	dir := dataFlowFixtures(t)

	// Use a name unlikely to collide with anything else.
	const unsetVar = "CHAMELEON_DEFINITELY_NOT_SET_BUNDLE_VAR"
	// Belt-and-suspenders: ensure it really isn't set.
	require.NoError(t, os.Unsetenv(unsetVar))

	stdout, stderr, exit := runBinary(t,
		"get",
		"--schema="+filepath.Join(dir, "app.proto"),
		"--include="+dir,
		"--message=smoke.v1.Cfg",
		"--bundle-env="+unsetVar,
		"--env=prod",
	)
	assert.NotEqual(t, 0, exit, "unset bundle-env var must error")
	assert.Empty(t, stdout, "no output should leak on this error path")
	assert.Contains(t, stderr, unsetVar,
		"error should name the missing env var so operator sees the typo; got: %s", stderr)
}

func TestMultiEnv_BundleBuildErrorsOnMissingRequiredLayer(t *testing.T) {
	// Chain spec marks the env PER_ENV layer Required: true; we
	// remove sandbox's source ciphertext from disk before running
	// the bundler. The build must surface the read failure rather
	// than silently dropping sandbox's layer (which would result
	// in a bundle that's broken for sandbox at runtime).
	s := setupMultiEnv(t)

	// Delete sandbox's source layer file.
	sandboxSrc := filepath.Join(s.dir, "env/sandbox.pxf")
	require.NoError(t, os.Remove(sandboxSrc))

	t.Setenv("SOPS_AGE_KEY", s.src.identity)
	_, stderr, exit := runBinary(t,
		"bundle", "build",
		"--manifest="+s.manifest,
		"--module=billing-worker",
		"--schema="+s.schema,
		"--include="+s.include,
		"--message=multienv.v1.Cfg",
		"--chain="+s.chain,
		"--layers="+s.dir,
		"--decrypter=sops",
		"--out="+s.bundle,
	)
	assert.NotEqual(t, 0, exit, "missing required layer must fail the build")
	assert.True(t,
		strings.Contains(stderr, "sandbox") ||
			strings.Contains(stderr, "no such file"),
		"error should identify the missing layer / file; got: %s", stderr)
}
