// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/sops/v4/age/keypb"
)

// writeKeystoreAtomic + loadOrInitKeystore error-path coverage. The
// existing keystore_test.go covers the happy paths exhaustively, but
// the atomic-write contract's failure modes (mkdir error, create
// error, rename error) and the keystore-load error branch (malformed
// PXF) were silent at 45.2% / 62.5%.

func TestWriteKeystoreAtomic_MkdirParentBlocked(t *testing.T) {
	// If a path component on the way to the target is a regular
	// file instead of a directory, MkdirAll fails. The atomic
	// writer must surface the error wrapped with "mkdir" so the
	// operator sees the stage; no temp file is left behind.
	root := t.TempDir()
	// Create a regular file where a directory should be.
	conflict := filepath.Join(root, "iam-a-file")
	require.NoError(t, os.WriteFile(conflict, []byte("not a dir"), 0o600))

	// Now ask the writer to create a file under that "directory".
	target := filepath.Join(conflict, "keys.pxf")
	err := writeKeystoreAtomic(target, &keypb.AgeKeyFile{Keys: map[string]string{"k": "v"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mkdir",
		"error should identify the mkdir stage; got: %v", err)
}

func TestWriteKeystoreAtomic_RenameOverDirectoryFails(t *testing.T) {
	// If the destination path is itself a directory, the rename
	// from temp file → target should fail. Some platforms refuse
	// to rename a regular file over an existing directory; we
	// exploit that to drive the rename error branch.
	dir := t.TempDir()
	target := filepath.Join(dir, "keys.pxf")
	// Create a directory at the target path.
	require.NoError(t, os.MkdirAll(target, 0o700))

	err := writeKeystoreAtomic(target, &keypb.AgeKeyFile{Keys: map[string]string{"k": "v"}})
	require.Error(t, err)
	// The error should surface from the rename stage. Different
	// platforms produce different wording, so the assertion is
	// the broader fact that the write failed; the bytes haven't
	// landed at `target` (it's still a directory).
	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir(), "target should still be a directory; write must not have overwritten it")
}

func TestWriteKeystoreAtomic_LeavesNoTempFileOnMkdirFailure(t *testing.T) {
	// The error-cleanup is load-bearing: a leaked temp file would
	// accumulate over time on a busy operator workstation. Verify
	// no `.keys-*.pxf.tmp` files survive an mkdir failure.
	root := t.TempDir()
	conflict := filepath.Join(root, "iam-a-file")
	require.NoError(t, os.WriteFile(conflict, []byte("not a dir"), 0o600))

	target := filepath.Join(conflict, "keys.pxf")
	_ = writeKeystoreAtomic(target, &keypb.AgeKeyFile{Keys: map[string]string{"k": "v"}})

	// Walk the root looking for any .tmp file.
	root_entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, e := range root_entries {
		assert.NotContains(t, e.Name(), ".tmp",
			"no .tmp file should be left behind, found: %s", e.Name())
	}
}

func TestWriteKeystoreAtomic_CreatesParentDirectoriesAsNeeded(t *testing.T) {
	// Happy path that's not in keystore_test.go: target path with
	// nested non-existent directories. MkdirAll(0o700) should
	// create them, and the final file should land at the target.
	root := t.TempDir()
	target := filepath.Join(root, "deep", "nested", "keys.pxf")
	store := &keypb.AgeKeyFile{
		Default: "k",
		Keys:    map[string]string{"k": rotOldIdentity},
	}
	require.NoError(t, writeKeystoreAtomic(target, store))

	// File exists at the target.
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.False(t, info.IsDir())

	// Parent dir mode should be 0o700 (private). On macOS umask
	// can interact, so assert the bits we control rather than an
	// exact equality.
	parent, err := os.Stat(filepath.Dir(target))
	require.NoError(t, err)
	if parent.Mode().Perm()&0o077 != 0 {
		t.Errorf("parent dir mode %o leaks bits to group/other; want only owner bits", parent.Mode().Perm())
	}
}

func TestLoadOrInitKeystore_MalformedFileSurfaces(t *testing.T) {
	// loadOrInitKeystore returns an empty store ONLY for the
	// "missing file" case. A garbled .pxf must surface the parse
	// error rather than silently emit an empty store — otherwise
	// the subsequent write would clobber whatever the operator
	// intended to keep.
	path := filepath.Join(t.TempDir(), "keys.pxf")
	require.NoError(t, os.WriteFile(path, []byte("\x00\x01\x02 not a valid pxf keystore"), 0o600))

	_, err := loadOrInitKeystore(path)
	require.Error(t, err)
	// Don't pin specific wording — the sops fork's parser owns it.
	// The point is the function did NOT swallow the error.
	if strings.Contains(err.Error(), "no such file") {
		t.Errorf("malformed PXF must NOT be treated as missing-file; got: %v", err)
	}
}

func TestLoadOrInitKeystore_MissingFileReturnsEmptyStore(t *testing.T) {
	// Existing happy path made explicit: a path that doesn't exist
	// returns an empty store + nil error so the caller proceeds to
	// add the first entry.
	path := filepath.Join(t.TempDir(), "never-created.pxf")
	store, err := loadOrInitKeystore(path)
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.Empty(t, store.Default)
	assert.NotNil(t, store.Keys, "Keys map should be allocated for the caller to write into")
}

func TestLoadOrInitKeystore_ReadsExistingStore(t *testing.T) {
	// Mirror of the missing-file test for the present-file branch.
	// keystore_test.go exercises this via keystoreAddCmd's
	// append-to-existing flow, but a direct unit test pins the
	// helper's contract independent of the cmd wrapper.
	path := filepath.Join(t.TempDir(), "existing.pxf")
	original := &keypb.AgeKeyFile{
		Default: "ops",
		Keys:    map[string]string{"ops": rotOldIdentity, "prod": rotNewIdentity},
	}
	require.NoError(t, writeKeystoreAtomic(path, original))

	got, err := loadOrInitKeystore(path)
	require.NoError(t, err)
	assert.Equal(t, "ops", got.Default)
	assert.Equal(t, rotOldIdentity, got.Keys["ops"])
	assert.Equal(t, rotNewIdentity, got.Keys["prod"])
}
