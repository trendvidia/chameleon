// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"testing/fstest"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// BuildBase64 is a one-line wrapper that piped Build's output
// through base64.NewEncoder + Close. The 0% coverage left these
// invariants unpinned: nil-Output gating, the close-error surfacing,
// and the round-trip with bundle.OpenBase64. This file covers them
// without dragging the heavy encrypt fixtures into the picture by
// running an unencrypted layer through the pipeline.

// buildMinimalUnencryptedBundle drives BuildBase64 with a tiny
// unencrypted-PXF fixture. Used as the success-path fixture below.
func buildMinimalUnencryptedBundle(t *testing.T, ctx context.Context, output *bytes.Buffer) error {
	t.Helper()
	// The layer below is plaintext, so nothing here encrypts and no
	// Decrypter / Encrypter is needed. The manifest still carries a
	// recipient to stay well-formed; its value never leaves the
	// manifest, so a literal is as good as a generated key.
	const publicKey = "age1testrecipient000000000000000000000000000000000000000000000"

	schema := buildTestDescriptor(t)
	chainSpec := &chameleonv1.ChainSpec{
		Version:      "1.0.0",
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
		Layers: []*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Required: true},
		},
	}
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name: "test",
			Recipients: &chameleonv1.BundleManifest_RecipientSet{
				Age: []string{publicKey},
			},
		}},
	}
	// Plaintext layer — buildTestDescriptor's AppConfig fields are
	// all Secret-bearing or nested messages, so an empty body is
	// the simplest valid payload here.
	layersFS := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(``)},
	}

	return BuildBase64(ctx, BuildOptions{
		Manifest:  manifest,
		Module:    "test",
		Schema:    schema,
		ChainSpec: chainSpec,
		LayersFS:  layersFS,
		Output:    output,
	})
}

func TestBuildBase64_NilOutputFailsFast(t *testing.T) {
	// BuildBase64 must reject a nil Output before constructing the
	// base64 encoder — otherwise it would panic inside the encoder
	// constructor at runtime. The error wording is the documented
	// gate ("output is required").
	err := BuildBase64(context.Background(), BuildOptions{
		// All other fields intentionally left zero to prove the gate
		// fires first.
	})
	if err == nil {
		t.Fatal("expected error for nil Output, got nil")
	}
	if !strings.Contains(err.Error(), "output is required") {
		t.Errorf("error should identify the missing Output, got: %v", err)
	}
}

func TestBuildBase64_ProducesValidBase64(t *testing.T) {
	// Happy path: a complete BuildOptions should produce a byte
	// stream that's structurally valid base64. Run a real Build via
	// the wrapper, then attempt base64-decode. The decode is the
	// load-bearing assertion — if BuildBase64 ever skipped the
	// encoder for some reason, the raw zip bytes would surface
	// here and fail to decode.
	ctx := context.Background()
	var buf bytes.Buffer
	if err := buildMinimalUnencryptedBundle(t, ctx, &buf); err != nil {
		t.Fatalf("BuildBase64: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("BuildBase64 produced no output")
	}
	// Every byte must be a valid base64 character (StdEncoding
	// alphabet: A-Z, a-z, 0-9, +, /, =). A regression that piped
	// raw zip bytes past the encoder would fail this check.
	for i, c := range buf.Bytes() {
		if !isBase64Byte(c) {
			t.Fatalf("byte %d = %q is not a base64 character — encoder bypassed?", i, c)
		}
	}
}

func TestBuildBase64_RoundTripsThroughDecode(t *testing.T) {
	// Stronger property: the base64-decoded bytes must be a valid
	// chameleon bundle zip. This is what bundle.OpenBase64 does
	// internally; testing it here without the bundle package import
	// keeps the bundler's test surface self-contained.
	ctx := context.Background()
	var buf bytes.Buffer
	if err := buildMinimalUnencryptedBundle(t, ctx, &buf); err != nil {
		t.Fatalf("BuildBase64: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(buf.String())
	if err != nil {
		t.Fatalf("base64 decode of BuildBase64 output: %v", err)
	}
	// Zip local file header signature: "PK\x03\x04".
	if len(decoded) < 4 || string(decoded[:4]) != "PK\x03\x04" {
		t.Fatalf("decoded bytes are not a zip; first 4 bytes = %q, want \"PK\\x03\\x04\"", decoded[:min(4, len(decoded))])
	}
}

func TestBuildBase64_BuildErrorPropagates(t *testing.T) {
	// When Build itself fails (here: nil Manifest, which validate()
	// rejects), BuildBase64 must surface that error instead of the
	// base64.Encoder.Close error. The "if err == nil { err = cerr }"
	// guard in BuildBase64 is the relevant line — flipping it would
	// silently swallow the Build error.
	var buf bytes.Buffer
	err := BuildBase64(context.Background(), BuildOptions{
		// Manifest deliberately nil.
		Module:    "test",
		Schema:    buildTestDescriptor(t),
		ChainSpec: &chameleonv1.ChainSpec{},
		LayersFS:  fstest.MapFS{},
		Output:    &buf,
	})
	if err == nil {
		t.Fatal("BuildBase64 with nil Manifest should error")
	}
	if !strings.Contains(err.Error(), "manifest") {
		t.Errorf("error should mention the failing field, got: %v", err)
	}
}

func isBase64Byte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	return c == '+' || c == '/' || c == '='
}
