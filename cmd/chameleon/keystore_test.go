// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/trendvidia/sops/v3/age/keypb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func TestKeystoreAdd_GeneratesFreshIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)

	var out bytes.Buffer
	err := keystoreAddCmd(&out, []string{"--name=prod"})
	require.NoError(t, err)

	// stdout carries the recipient.
	recipient := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(recipient, "age1"), "expected age1... recipient, got %q", recipient)

	// File written with the named entry.
	store, err := keypb.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, store.Keys, "prod")
	require.Empty(t, store.Default, "default not set unless --default is passed")

	// Identity in the file parses cleanly and round-trips to the
	// printed recipient.
	id, err := age.ParseX25519Identity(store.Keys["prod"])
	require.NoError(t, err)
	assert.Equal(t, recipient, id.Recipient().String())
}

func TestKeystoreAdd_AdoptsProvidedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)

	var out bytes.Buffer
	err := keystoreAddCmd(&out, []string{
		"--name=adopted",
		"--identity=" + rotOldIdentity,
	})
	require.NoError(t, err)

	assert.Equal(t, rotOldRecipient, strings.TrimSpace(out.String()))

	store, err := keypb.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, rotOldIdentity, store.Keys["adopted"])
}

func TestKeystoreAdd_SetsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)

	var out bytes.Buffer
	require.NoError(t, keystoreAddCmd(&out, []string{"--name=first", "--default"}))

	store, err := keypb.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "first", store.Default)
}

func TestKeystoreAdd_AppendsToExistingKeystore(t *testing.T) {
	// Pre-populate keystore with one entry.
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)

	existing := &keypb.AgeKeyFile{
		Default: "old",
		Keys:    map[string]string{"old": rotOldIdentity},
	}
	data, err := pxf.Marshal(existing)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	// Add a new entry; existing one must survive, default preserved.
	var out bytes.Buffer
	require.NoError(t, keystoreAddCmd(&out, []string{"--name=new"}))

	store, err := keypb.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, rotOldIdentity, store.Keys["old"], "previous entry must survive")
	assert.NotEmpty(t, store.Keys["new"], "new entry must be present")
	assert.Equal(t, "old", store.Default, "default must not change without --default")
}

func TestKeystoreAdd_RefusesOverwriteWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)

	require.NoError(t, keystoreAddCmd(&bytes.Buffer{}, []string{"--name=prod"}))

	// Second add with same name should error and leave the file
	// untouched.
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	err = keystoreAddCmd(&bytes.Buffer{}, []string{"--name=prod"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has an entry")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "refused write must not touch the file")
}

func TestKeystoreAdd_ForceOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)

	// First add: adopt rotOldIdentity.
	require.NoError(t, keystoreAddCmd(&bytes.Buffer{}, []string{
		"--name=prod",
		"--identity=" + rotOldIdentity,
	}))

	// Second add with --force: adopt rotNewIdentity under same name.
	var out bytes.Buffer
	require.NoError(t, keystoreAddCmd(&out, []string{
		"--name=prod",
		"--identity=" + rotNewIdentity,
		"--force",
	}))

	store, err := keypb.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, rotNewIdentity, store.Keys["prod"], "force should swap the entry's identity")
	assert.Equal(t, deriveRecipient(t, rotNewIdentity), strings.TrimSpace(out.String()))
}

func TestKeystoreAdd_RejectsNonPxfExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.txt")
	err := keystoreAddCmd(&bytes.Buffer{}, []string{"--name=prod", "--file=" + path})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".pxf")
}

func TestKeystoreAdd_RejectsMalformedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	err := keystoreAddCmd(&bytes.Buffer{}, []string{
		"--name=prod",
		"--file=" + path,
		"--identity=not-a-real-age-key",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse --identity")
}

func TestKeystoreAdd_RequiresName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)
	err := keystoreAddCmd(&bytes.Buffer{}, []string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--name is required")
}

func TestKeystoreAdd_RequiresFileOrEnv(t *testing.T) {
	// Explicitly unset SOPS_AGE_KEY_FILE for this test.
	t.Setenv("SOPS_AGE_KEY_FILE", "")
	err := keystoreAddCmd(&bytes.Buffer{}, []string{"--name=prod"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keystore path required")
}

func TestKeystoreAdd_FileWrittenWith0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)
	require.NoError(t, keystoreAddCmd(&bytes.Buffer{}, []string{"--name=prod"}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	mode := info.Mode().Perm()
	// We set 0o600 on the temp file before rename; Go on macOS /
	// Linux preserves perms across rename. umask interactions are
	// possible but t.TempDir gives us a fresh dir with default
	// perms, and 0o600 should be observable.
	assert.Equal(t, os.FileMode(0o600), mode, "keystore file should be 0o600 (private keys)")
}

func TestKeystoreAdd_ErrorPathLeavesNoTempFile(t *testing.T) {
	// Force an error during identity acquisition (bad --identity),
	// then verify the keystore directory has no leaked .tmp files.
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.pxf")
	err := keystoreAddCmd(&bytes.Buffer{}, []string{
		"--name=prod",
		"--file=" + path,
		"--identity=garbage",
	})
	require.Error(t, err)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "no temp files should leak on error: found %q", e.Name())
	}
}

func TestKeystoreCmd_DispatchesAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.pxf")
	t.Setenv("SOPS_AGE_KEY_FILE", path)
	var out bytes.Buffer
	err := keystoreCmd(&out, []string{"add", "--name=via-dispatch"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(out.String()), "age1"))
}

func TestKeystoreCmd_UnknownSubcommandErrors(t *testing.T) {
	var out bytes.Buffer
	err := keystoreCmd(&out, []string{"frobnicate"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subcommand")
}

func TestKeystoreCmd_NoSubcommandErrors(t *testing.T) {
	var out bytes.Buffer
	err := keystoreCmd(&out, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subcommand required")
}
