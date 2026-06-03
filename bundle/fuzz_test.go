// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundle_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/trendvidia/chameleon/bundle"
	"github.com/trendvidia/chameleon/internal/bundler"
)

// FuzzOpen pushes arbitrary bytes through bundle.Open. The
// invariant is: Open must NEVER panic on any input. Returns either a
// *Bundle or a non-nil error. Crashes from the zip parser, the
// chain.pxf parser, or io.ReadAll's growth loop count as fuzz finds.
//
// Bundle.Open ingests untrusted bytes from secret stores / network
// fetches — exactly the kind of boundary where a hostile producer
// can supply hand-crafted input. The two static negative tests
// (TestOpen_RejectsMalformedZip, TestOpen_MissingChainEntry) cover
// known shapes; this harness pressure-tests the rest of the input
// space.
//
// Run with:
//
//	go test ./bundle/ -fuzz=FuzzOpen -fuzztime=30s
func FuzzOpen(f *testing.F) {
	for _, seed := range openSeedCorpus(f) {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Property: never panic. Errors are fine.
		_, _ = bundle.Open(bytes.NewReader(data))
	})
}

// FuzzOpenBase64 pushes arbitrary bytes through bundle.OpenBase64.
// Same property as FuzzOpen — never panic. Exercises the
// base64-decode + zip-open + chain-parse stack as one unit, so a
// regression in any of the three surfaces here.
//
// Run with:
//
//	go test ./bundle/ -fuzz=FuzzOpenBase64 -fuzztime=30s
func FuzzOpenBase64(f *testing.F) {
	for _, seed := range openBase64SeedCorpus(f) {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = bundle.OpenBase64(bytes.NewReader(data))
	})
}

// openSeedCorpus assembles the static seeds for FuzzOpen. The fuzzer
// mutates these to reach interesting regions of the input space —
// good seeds include both well-formed shapes (valid zip + chain) and
// adversarial ones (truncated headers, non-zip junk).
func openSeedCorpus(f *testing.F) [][]byte {
	f.Helper()
	return [][]byte{
		nil,
		{},
		[]byte("not a zip"),
		{0x50, 0x4b, 0x03, 0x04}, // truncated zip local file header
		buildMinimalChainOnlyZip(f),
		buildEmptyZip(f),
		buildZipWithGarbageChain(f),
	}
}

// openBase64SeedCorpus assembles the static seeds for FuzzOpenBase64.
// Each entry pressures a different stage:
//
//   - non-base64 input  → base64 decoder rejects
//   - base64 of "not a zip" → decoder OK, zip parser rejects
//   - base64 of empty zip → decoder + zip OK, chain.pxf missing
//   - base64 of a real chain-only zip → full success path
func openBase64SeedCorpus(f *testing.F) [][]byte {
	f.Helper()
	return [][]byte{
		nil,
		{},
		[]byte("not valid base64!!!@@@"),
		[]byte(base64.StdEncoding.EncodeToString([]byte("not a zip"))),
		[]byte(base64.StdEncoding.EncodeToString(buildEmptyZip(f))),
		[]byte(base64.StdEncoding.EncodeToString(buildMinimalChainOnlyZip(f))),
	}
}

// buildMinimalChainOnlyZip produces a zip containing only chain.pxf
// (a minimal valid PXF chain spec). bundle.Open should succeed on
// this — chain.pxf is the only required entry; audit is optional.
func buildMinimalChainOnlyZip(f *testing.F) []byte {
	f.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(bundler.ChainEntryPath)
	if err != nil {
		f.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(`version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)); err != nil {
		f.Fatalf("zip write chain: %v", err)
	}
	if err := zw.Close(); err != nil {
		f.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// buildEmptyZip is a structurally-valid zip with no entries. The
// chain.pxf parse layer should reject with ErrMissingChain — useful
// seed for the missing-entry path.
func buildEmptyZip(f *testing.F) []byte {
	f.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zw.Close(); err != nil {
		f.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// buildZipWithGarbageChain is a zip whose chain.pxf entry contains
// random non-PXF bytes. Should produce a "parse chain.pxf" error,
// not a panic — exercises the parser error-path.
func buildZipWithGarbageChain(f *testing.F) []byte {
	f.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(bundler.ChainEntryPath)
	if err != nil {
		f.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte("\x00\x01\x02 not a chain spec at all")); err != nil {
		f.Fatalf("zip write garbage: %v", err)
	}
	if err := zw.Close(); err != nil {
		f.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}
