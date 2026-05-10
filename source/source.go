// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package source defines the byte-producing abstraction for layer
// files. A Source is content-agnostic: it knows how to fetch bytes for
// a given reference, but it does not parse PXF, decrypt sops payloads,
// or reason about layers.
//
// Sources are registered by logical name in the resolver; the chain
// spec maps those names to backing implementations. Built-in: filesystem
// (fs.FS-backed) — works with embed.FS, os.DirFS, and fstest.MapFS.
//
// The sops integration is a Decrypter, not a Source — decryption sits
// between the Source and the parser, so any Source can supply
// ciphertext.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

// ErrNotFound is returned by [Source.Open] when the reference does not
// resolve to an existing artifact. The resolver uses [errors.Is] to
// distinguish "skipped" (optional layer missing) from real I/O failures.
var ErrNotFound = errors.New("source: not found")

// Source produces bytes for a layer reference. Implementations must be
// safe for concurrent use — the resolver may load layers in parallel.
//
// The reference grammar is source-specific. The resolver passes the
// expanded path (with ${env} and ${ctx.*} already substituted) verbatim.
type Source interface {
	// Open returns a reader for ref. Caller closes. Returns ErrNotFound
	// (wrapped) if ref does not resolve; the resolver will treat that
	// as "skip this layer" for non-required entries.
	Open(ctx context.Context, ref string) (io.ReadCloser, error)

	// Stat reports whether ref exists. Lets the resolver short-circuit
	// optional missing layers without paying for a full open.
	Stat(ctx context.Context, ref string) (Info, error)
}

// Info is the result of [Source.Stat].
type Info struct {
	Exists bool
	Size   int64 // -1 if unknown
}

// FS adapts an fs.FS into a Source. Reference is the fs path, rooted
// at the FS root. Suitable for embed.FS, os.DirFS, fstest.MapFS, and
// anything else that implements fs.FS.
//
//	src := source.FS(os.DirFS("/etc/myapp/layers"))
func FS(fsys fs.FS) Source {
	return &fsSource{fsys: fsys}
}

type fsSource struct{ fsys fs.FS }

func (s *fsSource) Open(ctx context.Context, ref string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.fsys.Open(ref)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		return nil, fmt.Errorf("source/fs: open %s: %w", ref, err)
	}
	return f, nil
}

func (s *fsSource) Stat(ctx context.Context, ref string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	fi, err := fs.Stat(s.fsys, ref)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Info{Exists: false, Size: -1}, nil
		}
		return Info{}, fmt.Errorf("source/fs: stat %s: %w", ref, err)
	}
	return Info{Exists: true, Size: fi.Size()}, nil
}
