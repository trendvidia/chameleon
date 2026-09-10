// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package chameleon is the top-level entry point. Most callers reach
// for resolver.New + Resolve directly; this package provides a single
// shortcut, [Load], for the common simple case (one filesystem-backed
// layer source, no extra options).
//
// For everything else — multiple sources, trace plumbing, request
// context — go straight to the resolver package.
package chameleon

import (
	"context"
	"io/fs"

	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
	"google.golang.org/protobuf/proto"
)

// Load is a one-call shortcut for the simplest case: a single
// filesystem-rooted layer directory, no caller-supplied request
// context, and the parsed ChainSpec already in hand.
//
// Encrypted layers work here too, without a decrypter argument, as
// long as the build links a backend — blank-import
// chameleon/sops (or any package that registers one; see the crypt
// package doc). Without one, an encrypted layer fails the resolve
// with resolver.ErrNoDecrypter.
//
// That auto-selection is a behavior change, and Load is the entry
// point that cannot decline it: up to v0.22.1 an encrypted layer
// always failed here, because Load passes no Decrypter. It now
// succeeds whenever *anything* in the module graph registers a
// backend, which a transitive dependency can do without the calling
// program's knowledge. Load takes no options, so there is no
// [resolver.WithoutDecrypter] to reach for — a caller who needs
// encrypted layers to stay refused has to build the Resolver
// directly:
//
//	r, err := resolver.New[T](sample, spec,
//	    resolver.WithSource("default", source.FS(layersFS)),
//	    resolver.WithoutDecrypter(),
//	)
//
// Equivalent to:
//
//	r, err := resolver.New[T](sample, spec,
//	    resolver.WithSource("default", source.FS(layersFS)),
//	)
//	if err != nil { return nil, err }
//	return r.Resolve(context.Background(), resolver.Request{Environment: env})
//
// `layersFS` can be any fs.FS — `os.DirFS("./layers")` for local files,
// `embed.FS` for compiled-in layers, fstest.MapFS for unit tests.
//
// The caller still owns the parsed ChainSpec — chameleon doesn't
// prescribe how to load it (file, embed, dynamic compile via
// trendvidia/protocompile, etc.).
//
// For multiple logical sources, request-context values, trace, an
// explicitly constructed Decrypter, or no decrypter at all, use
// [resolver.New] directly.
func Load[T proto.Message](sample T, spec proto.Message, layersFS fs.FS, env string) (*resolver.Resolution[T], error) {
	r, err := resolver.New[T](sample, spec,
		resolver.WithSource("default", source.FS(layersFS)),
	)
	if err != nil {
		return nil, err
	}
	return r.Resolve(context.Background(), resolver.Request{Environment: env})
}
