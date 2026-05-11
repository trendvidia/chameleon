// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"strings"
	"testing"
)

func TestParse_Empty(t *testing.T) {
	p, err := Parse([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{EntryID: "base", Env: "dev", Encrypted: false}); err != nil {
		t.Fatalf("empty header should pass any Ref check, got %v", err)
	}
}

func TestParse_AllFields(t *testing.T) {
	in := `
id = "base"
environment = "dev"
encrypted = false
notes = "the base layer"
content_sha256 = "abc123"
`
	p, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{EntryID: "base", Env: "dev", Encrypted: false}); err != nil {
		t.Fatalf("Check failed: %v", err)
	}
}

func TestCheck_IDMismatch(t *testing.T) {
	p, err := Parse([]byte(`id = "env"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = p.Check(Ref{EntryID: "base"})
	if err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("expected id mismatch error, got %v", err)
	}
}

func TestCheck_EnvironmentMismatch(t *testing.T) {
	p, err := Parse([]byte(`environment = "prod"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = p.Check(Ref{Env: "dev"})
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("expected environment mismatch error, got %v", err)
	}
}

func TestCheck_EncryptedMismatch(t *testing.T) {
	p, err := Parse([]byte(`encrypted = true`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = p.Check(Ref{Encrypted: false})
	if err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("expected encrypted mismatch error, got %v", err)
	}
}

func TestCheck_EncryptedFalseExplicit(t *testing.T) {
	// `encrypted = false` is *present* (three-state). If the chain
	// entry is encrypted=true, this must fail — proving we use Result
	// presence, not the zero value.
	p, err := Parse([]byte(`encrypted = false`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{Encrypted: true}); err == nil {
		t.Fatal("expected mismatch error for encrypted=false vs Ref.Encrypted=true")
	}
}

func TestCheck_AbsentFieldNotChecked(t *testing.T) {
	// If author omitted `environment`, the check ignores it regardless
	// of Ref.Env.
	p, err := Parse([]byte(`id = "base"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := p.Check(Ref{EntryID: "base", Env: "anything"}); err != nil {
		t.Fatalf("Check should ignore absent environment, got %v", err)
	}
}
