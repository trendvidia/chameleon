// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestBodyHash_NoHeader(t *testing.T) {
	body := []byte(`base_url = "x.com"` + "\n")
	got, err := BodyHash(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sha256hex(body) {
		t.Errorf("got %q, want sha256 of full input", got)
	}
}

func TestBodyHash_WithHeader_StableUnderHeaderEdits(t *testing.T) {
	body := "\n" + `base_url = "x.com"` + "\n"
	a := []byte(`@header chameleon.v1.LayerHeader { id = "base" }` + body)
	b := []byte(`@header chameleon.v1.LayerHeader { id = "base"  notes = "different" }` + body)
	c := []byte(`@header chameleon.v1.LayerHeader
{
  id = "base"
  environment = "dev"
}` + body)

	for _, in := range [][]byte{a, b, c} {
		got, err := BodyHash(in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != sha256hex([]byte(body)) {
			t.Errorf("BodyHash should ignore @header bytes, got %q", got)
		}
	}
}

func TestBodyHash_DiffersWhenBodyDiffers(t *testing.T) {
	a, _ := BodyHash([]byte(`@header X { id = "x" } base_url = "a"`))
	b, _ := BodyHash([]byte(`@header X { id = "x" } base_url = "b"`))
	if a == b {
		t.Fatal("body change must change the hash")
	}
}

func TestParsedContentSHA256_Absent(t *testing.T) {
	p, _ := Parse([]byte(`id = "x"`))
	if _, ok := p.ContentSHA256(); ok {
		t.Fatal("absent field should report ok=false")
	}
}

func TestParsedContentSHA256_Present(t *testing.T) {
	p, _ := Parse([]byte(`content_sha256 = "abc123"`))
	got, ok := p.ContentSHA256()
	if !ok || got != "abc123" {
		t.Fatalf("got (%q, %v); want (\"abc123\", true)", got, ok)
	}
}
