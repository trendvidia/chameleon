// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

//go:build integration

// End-to-end protoc-plugin smoke. Builds the protoc-gen-chameleon-go
// binary, runs the real `protoc` against a small .proto fixture, and
// asserts the sidecar file lands on disk and parses as valid Go.
//
// Why: the in-process tests in gen_test.go and gen_resolved_test.go
// drive the generator via the protogen Go API directly — they assert
// generated code shape exhaustively, but never exercise the
// `protoc --plugin=` invocation contract (CodeGeneratorRequest /
// CodeGeneratorResponse framing, stdin/stdout binary protocol).
// A regression in main.go's plugin entry — or a breaking change in
// the protogen library's framing handling — would slip past every
// existing test.
//
// Gated behind `-tags integration` so it stays out of the default
// `go test ./...` path (it `go build`s a binary and spawns protoc).
//
// Skips automatically when `protoc` is not on PATH so contributors
// without protoc installed don't see spurious failures.
//
// Run with:
//
//	go test -tags=integration ./cmd/protoc-gen-chameleon-go/...
package main

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	pluginBinPath  string
	pluginBinBuilt sync.Once
	pluginBinErr   error
)

// buildPluginBinary compiles protoc-gen-chameleon-go into a temp
// directory shared across this test process. Reused via sync.Once so
// the build runs once even across subtests.
func buildPluginBinary(t *testing.T) string {
	t.Helper()
	pluginBinBuilt.Do(func() {
		dir, err := os.MkdirTemp("", "protoc-gen-chameleon-go-*")
		if err != nil {
			pluginBinErr = err
			return
		}
		// protoc expects the binary to be named protoc-gen-<NAME>
		// where NAME matches the --<NAME>_out flag. The plugin
		// flag is --chameleon-go_out, so the binary must be
		// protoc-gen-chameleon-go on PATH.
		bin := filepath.Join(dir, "protoc-gen-chameleon-go")
		cmd := exec.Command("go", "build", "-o", bin, ".")
		out, err := cmd.CombinedOutput()
		if err != nil {
			pluginBinErr = err
			t.Logf("go build output:\n%s", out)
			return
		}
		pluginBinPath = bin
	})
	if pluginBinErr != nil {
		t.Fatalf("build protoc-gen-chameleon-go: %v", pluginBinErr)
	}
	return pluginBinPath
}

// requireProtoc skips the test if `protoc` isn't on PATH. Lets
// contributors run integration tests without installing protoc.
func requireProtoc(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("protoc")
	if err != nil {
		t.Skip("protoc not on PATH; skipping real-protoc plugin smoke")
	}
	return p
}

// repoRoot resolves the repo root so the test can reference the
// examples/buf .proto fixtures by absolute path. The test runs with
// cwd inside cmd/protoc-gen-chameleon-go, so go two levels up.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}

func TestPluginViaProtoc_GeneratesSidecarFile(t *testing.T) {
	protocPath := requireProtoc(t)
	binPath := buildPluginBinary(t)

	outDir := t.TempDir()
	root := repoRoot(t)
	protoPath := filepath.Join(root, "examples", "buf", "proto")
	target := "myapp/v1/config.proto"

	cmd := exec.Command(protocPath,
		"--proto_path="+protoPath,
		"--plugin=protoc-gen-chameleon-go="+binPath,
		"--chameleon-go_out="+outDir,
		"--chameleon-go_opt=paths=source_relative",
		target,
	)
	combined, err := cmd.CombinedOutput()
	require.NoError(t, err, "protoc invocation failed: %s", combined)

	// The plugin emits <basename>.chameleon.go alongside the
	// source .proto (paths=source_relative is set).
	sidecar := filepath.Join(outDir, "myapp", "v1", "config.chameleon.go")
	info, err := os.Stat(sidecar)
	require.NoError(t, err, "sidecar file %s should exist after protoc run", sidecar)
	assert.Greater(t, info.Size(), int64(0), "sidecar must be non-empty")

	// Parse the generated file as Go syntax. A protogen output
	// regression that produces malformed Go would surface here as
	// a parse error rather than a downstream compile failure in
	// some unrelated consumer.
	body, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	fset := token.NewFileSet()
	_, err = parser.ParseFile(fset, sidecar, body, parser.ParseComments)
	require.NoError(t, err, "generated sidecar must parse as valid Go")

	src := string(body)

	// Spot-check the canonical accessor names the plugin's package
	// doc promises. AppConfig has:
	//   - db_password           → AppConfigSecret_DbPassword
	//   - primary.auth_token    → AppConfigSecret_Primary_AuthToken
	//   - backup_keys (repeated)→ AppConfigSecret_BackupKeys
	//   - tenant_keys (map)     → AppConfigSecret_TenantKeys
	for _, want := range []string{
		"AppConfigSecret_DbPassword",
		"AppConfigSecret_Primary_AuthToken",
		"AppConfigSecret_BackupKeys",
		"AppConfigSecret_TenantKeys",
	} {
		assert.Contains(t, src, want,
			"sidecar should declare %s", want)
	}

	// Header sanity: the generator marker is what `go generate`
	// tools and code-search heuristics rely on to identify
	// generated code.
	assert.Contains(t, src, "Code generated by protoc-gen-chameleon-go",
		"generator marker comment should be present")
	assert.Contains(t, src, "DO NOT EDIT",
		"DO NOT EDIT marker should be present")
}

func TestPluginViaProtoc_NoSecretsNoSidecar(t *testing.T) {
	// A .proto without any pxf.Secret field should NOT produce a
	// sidecar. The plugin's main loop checks len(sites) == 0 and
	// skips file generation entirely. This test pins that behavior
	// through the real protoc invocation so a regression that
	// accidentally emits an empty sidecar surfaces here.
	protocPath := requireProtoc(t)
	binPath := buildPluginBinary(t)

	outDir := t.TempDir()
	protoDir := filepath.Join(t.TempDir(), "proto")
	require.NoError(t, os.MkdirAll(protoDir, 0o755))
	plain := filepath.Join(protoDir, "plain.proto")
	require.NoError(t, os.WriteFile(plain, []byte(`syntax = "proto3";
package plain.v1;
option go_package = "example.com/plain;plainv1";
message Nothing {
  string url = 1;
  int32 count = 2;
}
`), 0o644))

	cmd := exec.Command(protocPath,
		"--proto_path="+protoDir,
		"--plugin=protoc-gen-chameleon-go="+binPath,
		"--chameleon-go_out="+outDir,
		"--chameleon-go_opt=paths=source_relative",
		"plain.proto",
	)
	combined, err := cmd.CombinedOutput()
	require.NoError(t, err, "protoc invocation failed: %s", combined)

	sidecar := filepath.Join(outDir, "plain.chameleon.go")
	_, err = os.Stat(sidecar)
	assert.True(t, os.IsNotExist(err),
		"no sidecar should exist for a secret-less .proto; got: %v", err)
}

func TestPluginViaProtoc_WithResolvedOption(t *testing.T) {
	// `--chameleon-go_opt=with_resolved=true` opts into the
	// AppConfigResolved wrapper. Pin that the option survives the
	// protoc parameter pipeline (it's parsed in params.go from the
	// CodeGeneratorRequest.parameter field) and ends up gated on
	// generation.
	protocPath := requireProtoc(t)
	binPath := buildPluginBinary(t)

	outDir := t.TempDir()
	root := repoRoot(t)
	protoPath := filepath.Join(root, "examples", "buf", "proto")
	target := "myapp/v1/config.proto"

	cmd := exec.Command(protocPath,
		"--proto_path="+protoPath,
		"--plugin=protoc-gen-chameleon-go="+binPath,
		"--chameleon-go_out="+outDir,
		"--chameleon-go_opt=paths=source_relative,with_resolved=true",
		target,
	)
	combined, err := cmd.CombinedOutput()
	require.NoError(t, err, "protoc invocation failed: %s", combined)

	sidecar := filepath.Join(outDir, "myapp", "v1", "config.chameleon.go")
	body, err := os.ReadFile(sidecar)
	require.NoError(t, err)

	src := string(body)
	// The wrapper type uses the pattern <Root>Resolved per the
	// plugin doc. AppConfig → AppConfigResolved.
	assert.Contains(t, src, "AppConfigResolved",
		"with_resolved=true should emit the AppConfigResolved wrapper type")
}

func TestPluginViaProtoc_UnknownOptionTolerated(t *testing.T) {
	// params.go is intentionally permissive on unknown keys — see
	// the comment at params.go:48-51. The documented contract is
	// that future protogen options + not-yet-implemented chameleon
	// keys should be silent no-ops, not build breakers. This test
	// pins that contract through the real protoc invocation so a
	// regression to "fail on unknown" surfaces here.
	protocPath := requireProtoc(t)
	binPath := buildPluginBinary(t)

	outDir := t.TempDir()
	root := repoRoot(t)
	protoPath := filepath.Join(root, "examples", "buf", "proto")

	cmd := exec.Command(protocPath,
		"--proto_path="+protoPath,
		"--plugin=protoc-gen-chameleon-go="+binPath,
		"--chameleon-go_out="+outDir,
		"--chameleon-go_opt=paths=source_relative,this_option_does_not_exist=yes",
		"myapp/v1/config.proto",
	)
	combined, err := cmd.CombinedOutput()
	require.NoError(t, err,
		"unknown options must be tolerated per params.go's documented permissive contract; got: %s", combined)

	// Sidecar should still emit normally — unknown options shouldn't
	// suppress generation.
	sidecar := filepath.Join(outDir, "myapp", "v1", "config.chameleon.go")
	_, err = os.Stat(sidecar)
	require.NoError(t, err, "sidecar should still emit with an unknown option present")
}
