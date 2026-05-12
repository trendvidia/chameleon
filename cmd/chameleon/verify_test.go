// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trendvidia/chameleon/chain"
)

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func writeLayer(t *testing.T, dir, name, contents string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyOne_OK(t *testing.T) {
	dir := t.TempDir()
	body := "\nbase_url = \"x\"\n"
	hash := sha256hex([]byte(body))
	writeLayer(t, dir, "base.pxf",
		`@header chameleon.v1.LayerHeader { id = "base"  content_sha256 = "`+hash+`" }`+body)

	var out bytes.Buffer
	status := verifyOne(&out, chain.LayerRef{
		EntryID: "base", Path: "base.pxf", Required: true,
	}, dir, false)

	if status != statusOK {
		t.Fatalf("status=%v, want OK; output: %s", status, out.String())
	}
	if !strings.HasPrefix(out.String(), "OK\t") {
		t.Errorf("expected OK line, got %q", out.String())
	}
}

func TestVerifyOne_Mismatch(t *testing.T) {
	dir := t.TempDir()
	body := "\nbase_url = \"x\"\n"
	wrongHash := strings.Repeat("0", 64)
	writeLayer(t, dir, "base.pxf",
		`@header chameleon.v1.LayerHeader { content_sha256 = "`+wrongHash+`" }`+body)

	var out bytes.Buffer
	status := verifyOne(&out, chain.LayerRef{
		EntryID: "base", Path: "base.pxf", Required: true,
	}, dir, false)

	if status != statusMismatch {
		t.Fatalf("status=%v, want Mismatch; output: %s", status, out.String())
	}
	if !strings.HasPrefix(out.String(), "FAIL\t") {
		t.Errorf("expected FAIL line, got %q", out.String())
	}
}

func TestVerifyOne_NoHeaderSkipped(t *testing.T) {
	dir := t.TempDir()
	writeLayer(t, dir, "base.pxf", `base_url = "x"`+"\n")

	var out bytes.Buffer
	status := verifyOne(&out, chain.LayerRef{
		EntryID: "base", Path: "base.pxf", Required: true,
	}, dir, false)

	if status != statusMissingHash {
		t.Fatalf("status=%v, want MissingHash", status)
	}
	if !strings.HasPrefix(out.String(), "SKIP\t") {
		t.Errorf("expected SKIP line, got %q", out.String())
	}
}

func TestVerifyOne_NoHashSkipped(t *testing.T) {
	dir := t.TempDir()
	writeLayer(t, dir, "base.pxf",
		`@header chameleon.v1.LayerHeader { id = "base" }`+"\nbase_url = \"x\"\n")

	var out bytes.Buffer
	status := verifyOne(&out, chain.LayerRef{
		EntryID: "base", Path: "base.pxf", Required: true,
	}, dir, false)

	if status != statusMissingHash {
		t.Fatalf("status=%v, want MissingHash; output: %s", status, out.String())
	}
}

func TestVerifyOne_NoHeaderStrict(t *testing.T) {
	dir := t.TempDir()
	writeLayer(t, dir, "base.pxf", `base_url = "x"`+"\n")

	var out bytes.Buffer
	status := verifyOne(&out, chain.LayerRef{
		EntryID: "base", Path: "base.pxf", Required: true,
	}, dir, true /* strict */)

	if status != statusMissingHash {
		t.Fatalf("status=%v, want MissingHash", status)
	}
	if !strings.HasPrefix(out.String(), "MISS\t") {
		t.Errorf("strict mode should label with MISS, got %q", out.String())
	}
}

func TestVerifyOne_OptionalMissing(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	status := verifyOne(&out, chain.LayerRef{
		EntryID: "env", Path: "env/prod.pxf", Required: false,
	}, dir, false)

	if status != statusSkippedMissing {
		t.Fatalf("status=%v, want SkippedMissing", status)
	}
}

func TestVerifyOne_EncryptedSkipped(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	status := verifyOne(&out, chain.LayerRef{
		EntryID: "secrets", Path: "dev.sops.pxf", Encrypted: true,
	}, dir, false)

	if status != statusSkippedEncrypted {
		t.Fatalf("status=%v, want SkippedEncrypted", status)
	}
	if !strings.Contains(out.String(), "encrypted") {
		t.Errorf("expected note about encrypted, got %q", out.String())
	}
}

func TestHashCmd(t *testing.T) {
	dir := t.TempDir()
	body := "\nbase_url = \"x\"\n"
	path := filepath.Join(dir, "base.pxf")
	if err := os.WriteFile(path,
		[]byte(`@header chameleon.v1.LayerHeader { id = "base" }`+body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := hashCmd(&out, []string{path}); err != nil {
		t.Fatalf("hashCmd: %v", err)
	}
	got := strings.TrimSpace(out.String())
	want := sha256hex([]byte(body))
	if got != want {
		t.Errorf("hash = %q, want %q", got, want)
	}
}
