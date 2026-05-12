// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package source_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/source"
)

// fixtureFS returns a small filesystem used across the tests. base.pxf
// has known contents; nested/dir.pxf exercises a non-trivial path.
func fixtureFS() fstest.MapFS {
	return fstest.MapFS{
		"base.pxf":       &fstest.MapFile{Data: []byte("base_url = \"x\"\n")},
		"nested/dir.pxf": &fstest.MapFile{Data: []byte("nested = true\n")},
	}
}

// TestFS_Open_Success — the happy path: an existing path opens, returns
// a reader whose bytes match the fixture.
func TestFS_Open_Success(t *testing.T) {
	src := source.FS(fixtureFS())
	rc, err := src.Open(context.Background(), "base.pxf")
	require.NoError(t, err)
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "base_url = \"x\"\n", string(got))
}

// TestFS_Open_NotFound_WrapsErrNotFound — a missing reference returns
// an error that errors.Is identifies as ErrNotFound. This is the
// load-bearing contract: the resolver checks errors.Is for ErrNotFound
// to decide whether to skip an optional layer.
func TestFS_Open_NotFound_WrapsErrNotFound(t *testing.T) {
	src := source.FS(fixtureFS())
	_, err := src.Open(context.Background(), "missing.pxf")
	require.Error(t, err)
	assert.True(t, errors.Is(err, source.ErrNotFound),
		"errors.Is must match source.ErrNotFound; got %v", err)
	// Error string should mention the bad reference so logs are
	// actionable.
	assert.Contains(t, err.Error(), "missing.pxf")
}

// TestFS_Open_GenericErrorNotWrappedAsNotFound — an underlying
// non-ErrNotExist error must NOT match ErrNotFound. Regression guard
// against accidental over-wrapping that would silently turn permission
// errors into "skip this layer."
func TestFS_Open_GenericErrorNotWrappedAsNotFound(t *testing.T) {
	src := source.FS(brokenFS{err: errors.New("permission denied")})
	_, err := src.Open(context.Background(), "any.pxf")
	require.Error(t, err)
	assert.False(t, errors.Is(err, source.ErrNotFound),
		"non-ErrNotExist errors must not match ErrNotFound; got %v", err)
	assert.Contains(t, err.Error(), "permission denied")
}

// TestFS_Open_ContextCancelled — a cancelled context aborts before
// the fs.FS is consulted. Lets the resolver respect ctx deadlines
// during high-volume layer loads.
func TestFS_Open_ContextCancelled(t *testing.T) {
	src := source.FS(fixtureFS())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := src.Open(ctx, "base.pxf")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled),
		"cancelled ctx must surface as context.Canceled; got %v", err)
}

// TestFS_Stat_Exists — Stat on an existing reference returns
// Exists:true and the file size.
func TestFS_Stat_Exists(t *testing.T) {
	src := source.FS(fixtureFS())
	info, err := src.Stat(context.Background(), "base.pxf")
	require.NoError(t, err)
	assert.True(t, info.Exists)
	assert.Equal(t, int64(len("base_url = \"x\"\n")), info.Size)
}

// TestFS_Stat_Missing — Stat on a missing reference returns
// Info{Exists:false, Size:-1} with NO error. The resolver uses this
// to short-circuit optional missing layers without a full Open.
func TestFS_Stat_Missing(t *testing.T) {
	src := source.FS(fixtureFS())
	info, err := src.Stat(context.Background(), "absent.pxf")
	require.NoError(t, err, "missing-file Stat must NOT error — resolver checks Info.Exists")
	assert.False(t, info.Exists)
	assert.Equal(t, int64(-1), info.Size)
}

// TestFS_Stat_GenericError — non-ErrNotExist errors from the FS
// propagate as a Stat error (not as Info{Exists:false}).
func TestFS_Stat_GenericError(t *testing.T) {
	src := source.FS(brokenFS{err: errors.New("io fail")})
	_, err := src.Stat(context.Background(), "any.pxf")
	require.Error(t, err)
	assert.False(t, errors.Is(err, source.ErrNotFound))
	assert.Contains(t, err.Error(), "io fail")
}

// TestFS_Stat_ContextCancelled — same ctx guard as Open.
func TestFS_Stat_ContextCancelled(t *testing.T) {
	src := source.FS(fixtureFS())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := src.Stat(ctx, "base.pxf")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

// TestFS_Open_NestedPath — non-trivial paths route through fs.FS
// unchanged.
func TestFS_Open_NestedPath(t *testing.T) {
	src := source.FS(fixtureFS())
	rc, err := src.Open(context.Background(), "nested/dir.pxf")
	require.NoError(t, err)
	defer rc.Close()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "nested = true\n", string(got))
}

// brokenFS is an fs.FS that returns a caller-supplied error from every
// operation. Lets us exercise the "non-ErrNotExist underlying error"
// branches of FS.Open and FS.Stat.
type brokenFS struct{ err error }

func (b brokenFS) Open(name string) (fs.File, error) {
	return nil, b.err
}
