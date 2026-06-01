// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"os"
	"path/filepath"
	"testing"

	sopsage "github.com/trendvidia/sops/v4/age"
	"github.com/trendvidia/sops/v4/age/keypb"
	"github.com/trendvidia/protowire-go/encoding/pxf"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// writePXFKeyFile encodes a PXF age key file and points
// SOPS_AGE_KEY_FILE at it for the duration of the test.
func writePXFKeyFile(t *testing.T, file *keypb.AgeKeyFile) {
	t.Helper()
	data, err := pxf.Marshal(file)
	if err != nil {
		t.Fatalf("marshal age key file: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.pxf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write age key file: %v", err)
	}
	t.Setenv(sopsage.SopsAgeKeyFileEnv, path)
}

// Test identity matches the one used in sops's keysource_test.go so
// the derived recipient is stable.
const (
	testIdentity  = "AGE-SECRET-KEY-1G0Q5K9TV4REQ3ZSQRMTMG8NSWQGYT0T7TZ33RAZEE0GZYVZN0APSU24RK7"
	testRecipient = "age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun"
)

func TestUnionRecipients_ChainSpecKeyNameResolves(t *testing.T) {
	writePXFKeyFile(t, &keypb.AgeKeyFile{
		Keys: map[string]string{"prod-key": testIdentity},
	})

	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "prod-key"},
		},
	}

	got, err := unionRecipients(spec, nil, []string{"prod"})
	if err != nil {
		t.Fatalf("unionRecipients: %v", err)
	}
	if len(got.Age) != 1 || got.Age[0] != testRecipient {
		t.Errorf("Age = %v, want [%s]", got.Age, testRecipient)
	}
}

func TestUnionRecipients_ChildEnvInheritsParentKeyName(t *testing.T) {
	writePXFKeyFile(t, &keypb.AgeKeyFile{
		Keys: map[string]string{"shared-key": testIdentity},
	})

	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "shared-key"},
			{Name: "sandbox", Extends: "prod"},
		},
	}

	got, err := unionRecipients(spec, nil, []string{"sandbox"})
	if err != nil {
		t.Fatalf("unionRecipients: %v", err)
	}
	if len(got.Age) != 1 || got.Age[0] != testRecipient {
		t.Errorf("sandbox should inherit prod's key_name; Age = %v", got.Age)
	}
}

func TestUnionRecipients_NoKeyNameFallsBackToManifest(t *testing.T) {
	// Chain spec has no key_name → only the BundleManifest path
	// contributes recipients. Verifies the historical behavior is
	// untouched for unmigrated chains.
	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
	}
	module := &chameleonv1.BundleManifest_Module{
		RecipientsByEnv: map[string]*chameleonv1.BundleManifest_RecipientSet{
			"prod": {Kms: []string{"arn:aws:kms:us-east-1:000:key/abc"}},
		},
	}

	got, err := unionRecipients(spec, module, []string{"prod"})
	if err != nil {
		t.Fatalf("unionRecipients: %v", err)
	}
	if len(got.KMS) != 1 || got.KMS[0] != "arn:aws:kms:us-east-1:000:key/abc" {
		t.Errorf("KMS = %v, want one ARN from manifest", got.KMS)
	}
	if len(got.Age) != 0 {
		t.Errorf("Age = %v, want empty (no key_name set)", got.Age)
	}
}

func TestUnionRecipients_UnionsBothSources(t *testing.T) {
	writePXFKeyFile(t, &keypb.AgeKeyFile{
		Keys: map[string]string{"prod-key": testIdentity},
	})

	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "prod-key"},
		},
	}
	module := &chameleonv1.BundleManifest_Module{
		RecipientsByEnv: map[string]*chameleonv1.BundleManifest_RecipientSet{
			"prod": {Kms: []string{"arn:aws:kms:us-east-1:000:key/abc"}},
		},
	}

	got, err := unionRecipients(spec, module, []string{"prod"})
	if err != nil {
		t.Fatalf("unionRecipients: %v", err)
	}
	if len(got.KMS) != 1 {
		t.Errorf("expected KMS from manifest, got %v", got.KMS)
	}
	if len(got.Age) != 1 || got.Age[0] != testRecipient {
		t.Errorf("expected Age from chain spec, got %v", got.Age)
	}
}

func TestUnionRecipients_DuplicateInheritedKeyDeduped(t *testing.T) {
	// Two envs (prod, sandbox extending prod) both resolve to the
	// same key_name via inheritance. The age recipient should appear
	// once, not twice.
	writePXFKeyFile(t, &keypb.AgeKeyFile{
		Keys: map[string]string{"shared-key": testIdentity},
	})

	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "shared-key"},
			{Name: "sandbox", Extends: "prod"},
		},
	}

	got, err := unionRecipients(spec, nil, []string{"prod", "sandbox"})
	if err != nil {
		t.Fatalf("unionRecipients: %v", err)
	}
	if len(got.Age) != 1 {
		t.Errorf("Age = %v, want exactly one (deduped)", got.Age)
	}
}

func TestUnionRecipients_UnknownKeyNameErrors(t *testing.T) {
	writePXFKeyFile(t, &keypb.AgeKeyFile{
		Keys: map[string]string{"present-key": testIdentity},
	})

	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "missing-key"},
		},
	}

	_, err := unionRecipients(spec, nil, []string{"prod"})
	if err == nil {
		t.Errorf("expected error for unknown key_name, got nil")
	}
}

func TestUnionRecipients_NilChainSpecPreservesOldBehavior(t *testing.T) {
	module := &chameleonv1.BundleManifest_Module{
		RecipientsByEnv: map[string]*chameleonv1.BundleManifest_RecipientSet{
			"prod": {Age: []string{"age1abc"}},
		},
	}
	got, err := unionRecipients(nil, module, []string{"prod"})
	if err != nil {
		t.Fatalf("nil chain spec should not error: %v", err)
	}
	if len(got.Age) != 1 || got.Age[0] != "age1abc" {
		t.Errorf("Age = %v, want [age1abc] from manifest", got.Age)
	}
}
