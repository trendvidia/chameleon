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
//   - pxf/         — copy of pxf/annotations.proto from
//     github.com/trendvidia/protos, compiled for codegen only. Its Go
//     is github.com/trendvidia/protos/pxf; generating it here would
//     register the file twice (#210).
package proto
