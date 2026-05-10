// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package chameleon is the top-level entry point. Most callers reach
// for resolver.New + Resolve directly; this package provides a single
// shortcut, [Load], for the common simple case (one filesystem-backed
// layer source, no sops, no extra options).
//
// For everything else — multiple sources, encrypted layers, trace
// plumbing, request context — go straight to the resolver package.
package chameleon

import (
	"context"
	"io/fs"

	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
	"google.golang.org/protobuf/proto"
)

// Load is a one-call shortcut for the simplest case: a single
// filesystem-rooted layer directory, no encrypted layers, no
// caller-supplied request context, and the parsed ChainSpec already
// in hand.
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
// bufbuild/protocompile, etc.).
//
// For encrypted layers, multiple logical sources, request-context
// values, or trace, use [resolver.New] directly.
func Load[T proto.Message](sample T, spec proto.Message, layersFS fs.FS, env string) (*resolver.Resolution[T], error) {
	r, err := resolver.New[T](sample, spec,
		resolver.WithSource("default", source.FS(layersFS)),
	)
	if err != nil {
		return nil, err
	}
	return r.Resolve(context.Background(), resolver.Request{Environment: env})
}
