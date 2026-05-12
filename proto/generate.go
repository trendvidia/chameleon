// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Regenerate chameleon's own Go bindings:
//
//	cd proto && buf generate
//
// or:
//
//	go generate ./proto
//
//go:generate buf generate

// Package proto holds chameleon's own .proto definitions and their
// generated Go bindings. Subdirectories:
//
//   - chameleon/v1/ — ChainSpec, LayerHeader, LayerEntry, etc.
//   - pxf/         — vendored copy of pxf/annotations.proto for codegen
//     (the runtime extension implementation lives in
//     protowire-go's encoding/pxf package, not here).
package proto
