// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/chain"
	chsops "github.com/trendvidia/chameleon/sops"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// TestMigrateOne_PlaintextLayer round-trips a plaintext layer through
// migrate — read, parse, apply add-field, format, write — and asserts
// the new field is present in the resulting file.
func TestMigrateOne_PlaintextLayer(t *testing.T) {
	dir := t.TempDir()
	path := "base.pxf"
	full := filepath.Join(dir, path)
	require.NoError(t, os.WriteFile(full, []byte(`base_url = "x"`+"\n"), 0o644))

	ref := chain.LayerRef{
		EntryID:   "base",
		Path:      path,
		Required:  true,
		Encrypted: false,
	}
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: true}, raw: "true"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, false /*dryRun*/, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateChanged, status)
	assert.Contains(t, out.String(), "CHANGED")

	updated, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Contains(t, string(updated), "base_url")
	assert.Contains(t, string(updated), "feature = true")
}

// TestMigrateOne_DryRunDoesNotWrite verifies dry-run prints WOULD-CHANGE
// and leaves the file untouched.
func TestMigrateOne_DryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := "base.pxf"
	full := filepath.Join(dir, path)
	original := []byte(`base_url = "x"` + "\n")
	require.NoError(t, os.WriteFile(full, original, 0o644))

	ref := chain.LayerRef{EntryID: "base", Path: path, Required: true}
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: true}, raw: "true"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, true /*dryRun*/, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateChanged, status)
	assert.Contains(t, out.String(), "WOULD-CHANGE")
	assert.Contains(t, out.String(), "before:")
	assert.Contains(t, out.String(), "after:")

	unchanged, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Equal(t, original, unchanged, "dry-run must not write the file")
}

// TestMigrateOne_IdempotentSkip — the field already exists; status
// reports UNCHANGED.
func TestMigrateOne_IdempotentSkip(t *testing.T) {
	dir := t.TempDir()
	path := "base.pxf"
	full := filepath.Join(dir, path)
	require.NoError(t, os.WriteFile(full, []byte("feature = true\n"), 0o644))

	ref := chain.LayerRef{EntryID: "base", Path: path, Required: true}
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: false}, raw: "false"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, false, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateUnchanged, status)
	assert.Contains(t, out.String(), "UNCHANGED")
}

// TestMigrateOne_OptionalMissingSkipped — missing optional file
// reports SKIP, not error.
func TestMigrateOne_OptionalMissingSkipped(t *testing.T) {
	dir := t.TempDir()
	ref := chain.LayerRef{EntryID: "env", Path: "env/dev.pxf", Required: false}
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: true}, raw: "true"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, false, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateSkipped, status)
	assert.Contains(t, out.String(), "SKIP")
}

// TestMigrateOne_RequiredMissingErrors — missing required file is a
// hard error.
func TestMigrateOne_RequiredMissingErrors(t *testing.T) {
	dir := t.TempDir()
	ref := chain.LayerRef{EntryID: "base", Path: "missing.pxf", Required: true}
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: true}, raw: "true"}

	var out bytes.Buffer
	_, err := migrateOne(context.Background(), &out, ref, dir, tf, false, chsops.Default())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "read"), "got %v", err)
}

// TestMigrateOne_NonDefaultSourceSkipped — sources other than
// "default" are skipped for now (no source registry plumbing here).
func TestMigrateOne_NonDefaultSourceSkipped(t *testing.T) {
	dir := t.TempDir()
	ref := chain.LayerRef{EntryID: "secrets", Path: "x.pxf", Required: false, Source: "secrets"}
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: true}, raw: "true"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, false, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateSkipped, status)
	assert.Contains(t, out.String(), "non-default source")
}

// TestMigrateOne_DeleteField_Toplevel — pipeline-level smoke for the
// delete-field transform on a plaintext layer.
func TestMigrateOne_DeleteField_Toplevel(t *testing.T) {
	dir := t.TempDir()
	path := "base.pxf"
	full := filepath.Join(dir, path)
	require.NoError(t, os.WriteFile(full, []byte("feature = true\nkeep = 1\n"), 0o644))

	ref := chain.LayerRef{EntryID: "base", Path: path, Required: true}
	tf := &deleteField{path: "feature"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, false, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateChanged, status)

	updated, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.NotContains(t, string(updated), "feature")
	assert.Contains(t, string(updated), "keep = 1")
}

// TestMigrateOne_RenameField_Nested — pipeline-level smoke for the
// rename-field transform on a nested path.
func TestMigrateOne_RenameField_Nested(t *testing.T) {
	dir := t.TempDir()
	path := "base.pxf"
	full := filepath.Join(dir, path)
	require.NoError(t, os.WriteFile(full, []byte("db {\n  user = \"alice\"\n}\n"), 0o644))

	ref := chain.LayerRef{EntryID: "base", Path: path, Required: true}
	tf := &renameField{from: "db.user", to: "db.username"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, false, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateChanged, status)

	updated, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Contains(t, string(updated), `username = "alice"`)
	assert.NotContains(t, string(updated), `user = "alice"`)
}

// TestMigrateOne_EncryptedLayer_AgeRoundTrip — encrypts a PXF fixture
// with age, runs migrate to add a field, and verifies the resulting
// file decrypts back with the new field present. Exercises the full
// sops decrypt + transform + sops encrypt-with-same-keys pipeline
// against a real (local-crypto) encrypted layer.
func TestMigrateOne_EncryptedLayer_AgeRoundTrip(t *testing.T) {
	const (
		recipient = "age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun"
		identity  = "AGE-SECRET-KEY-1G0Q5K9TV4REQ3ZSQRMTMG8NSWQGYT0T7TZ33RAZEE0GZYVZN0APSU24RK7"
	)
	t.Setenv("SOPS_AGE_KEY", identity)

	dir := t.TempDir()
	path := "base.pxf"
	full := filepath.Join(dir, path)

	// Build an encrypted fixture: encrypt a small PXF payload via
	// sops encrypt with the test age recipient.
	plain := []byte("base_url = \"x\"\n")
	ciph, err := encryptForTest(t, plain, recipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(full, ciph, 0o644))

	ref := chain.LayerRef{
		EntryID:   "base",
		Path:      path,
		Required:  true,
		Encrypted: true,
	}
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: true}, raw: "true"}

	var out bytes.Buffer
	status, err := migrateOne(context.Background(), &out, ref, dir, tf, false /*dryRun*/, chsops.Default())
	require.NoError(t, err)
	assert.Equal(t, migrateChanged, status)

	// Re-read the file; it should still be encrypted (sops metadata block present).
	rewritten, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Contains(t, string(rewritten), "sops {", "rewritten file should still carry sops metadata block")

	// Decrypt and confirm the new field is in the plaintext.
	decrypted, err := decryptForTest(t, rewritten)
	require.NoError(t, err)
	assert.Contains(t, string(decrypted), "base_url")
	assert.Contains(t, string(decrypted), "feature = true")
}
