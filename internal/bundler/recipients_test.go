// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/trendvidia/chameleon/crypt"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// What unionRecipients owns is the plumbing: which envs contribute,
// how manifest and chain-spec sources combine, and that the result is
// deduped. Resolving a key_name against a real key store is the
// backend's job and is tested there (chameleon/sops keyname_test.go) —
// these cases used to reach for a real age keystore, which is what put
// the sops dependency in the library module (trendvidia/chameleon#130).
// The hook makes both halves testable in the module that owns them.

// keyStore is a fake BuildOptions.RecipientsForKeyName: a fixed
// name → recipient table that errors on anything else, exactly like
// a keystore lookup miss.
func keyStore(table map[string]string) func(string) (crypt.Recipients, error) {
	return func(name string) (crypt.Recipients, error) {
		recipient, ok := table[name]
		if !ok {
			return crypt.Recipients{}, fmt.Errorf("no key named %q", name)
		}
		return crypt.Recipients{Age: []string{recipient}}, nil
	}
}

const testRecipient = "age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun"

func TestUnionRecipients_ChainSpecKeyNameResolves(t *testing.T) {
	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "prod-key"},
		},
	}

	got, err := unionRecipients(spec, nil, []string{"prod"}, keyStore(map[string]string{"prod-key": testRecipient}))
	if err != nil {
		t.Fatalf("unionRecipients: %v", err)
	}
	if len(got.Age) != 1 || got.Age[0] != testRecipient {
		t.Errorf("Age = %v, want [%s]", got.Age, testRecipient)
	}
}

func TestUnionRecipients_ChildEnvInheritsParentKeyName(t *testing.T) {
	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "shared-key"},
			{Name: "sandbox", Extends: "prod"},
		},
	}

	got, err := unionRecipients(spec, nil, []string{"sandbox"}, keyStore(map[string]string{"shared-key": testRecipient}))
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

	got, err := unionRecipients(spec, module, []string{"prod"}, keyStore(nil))
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

func TestUnionRecipients_NoKeyNameNeedsNoResolver(t *testing.T) {
	// A chain that carries recipients only in the manifest must build
	// without a resolver at all — that is the case a caller with no
	// key store is entitled to.
	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
	}
	module := &chameleonv1.BundleManifest_Module{
		RecipientsByEnv: map[string]*chameleonv1.BundleManifest_RecipientSet{
			"prod": {Age: []string{"age1abc"}},
		},
	}

	got, err := unionRecipients(spec, module, []string{"prod"}, nil /* no resolver */)
	if err != nil {
		t.Fatalf("unionRecipients with no key_name must not need a resolver: %v", err)
	}
	if len(got.Age) != 1 || got.Age[0] != "age1abc" {
		t.Errorf("Age = %v, want [age1abc]", got.Age)
	}
}

func TestUnionRecipients_KeyNameWithoutResolverErrors(t *testing.T) {
	// The dangerous case: the chain names a key, the caller wired no
	// resolver. Dropping it silently would encrypt the layer to a
	// smaller recipient set than the chain asked for and surface as an
	// undecryptable layer in production.
	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "prod-key"},
		},
	}

	_, err := unionRecipients(spec, nil, []string{"prod"}, nil)
	if err == nil {
		t.Fatal("key_name with a nil RecipientsForKeyName must error")
	}
	for _, want := range []string{"prod-key", "RecipientsForKeyName"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestUnionRecipients_UnionsBothSources(t *testing.T) {
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

	got, err := unionRecipients(spec, module, []string{"prod"}, keyStore(map[string]string{"prod-key": testRecipient}))
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
	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "shared-key"},
			{Name: "sandbox", Extends: "prod"},
		},
	}

	got, err := unionRecipients(spec, nil, []string{"prod", "sandbox"}, keyStore(map[string]string{"shared-key": testRecipient}))
	if err != nil {
		t.Fatalf("unionRecipients: %v", err)
	}
	if len(got.Age) != 1 {
		t.Errorf("Age = %v, want exactly one (deduped)", got.Age)
	}
}

func TestUnionRecipients_UnknownKeyNameErrors(t *testing.T) {
	spec := &chameleonv1.ChainSpec{
		Environments: []*chameleonv1.Environment{
			{Name: "prod", KeyName: "missing-key"},
		},
	}

	_, err := unionRecipients(spec, nil, []string{"prod"}, keyStore(map[string]string{"present-key": testRecipient}))
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
	got, err := unionRecipients(nil, module, []string{"prod"}, keyStore(nil))
	if err != nil {
		t.Fatalf("nil chain spec should not error: %v", err)
	}
	if len(got.Age) != 1 || got.Age[0] != "age1abc" {
		t.Errorf("Age = %v, want [age1abc] from manifest", got.Age)
	}
}
