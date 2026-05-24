// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"strings"
	"testing"

	"github.com/trendvidia/chameleon/internal/secretcatalog"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// fakeSites returns Sites with the given schema paths. Container
// kind doesn't matter for filter tests — only SchemaPath is read.
func fakeSites(paths ...string) []secretcatalog.Site {
	sites := make([]secretcatalog.Site, 0, len(paths))
	for _, p := range paths {
		segs := strings.Split(p, ".")
		steps := make([]secretcatalog.PathStep, len(segs))
		for i, s := range segs {
			steps[i] = secretcatalog.PathStep{Name: s, Container: secretcatalog.ContainerScalar}
		}
		sites = append(sites, secretcatalog.Site{Path: steps})
	}
	return sites
}

func TestCompile_ModuleNameRequired(t *testing.T) {
	_, err := Compile(&chameleonv1.BundleManifest{}, "", nil)
	if err == nil {
		t.Fatal("Compile with empty module name should error")
	}
}

func TestCompile_NilManifestUsesDefaults(t *testing.T) {
	f, err := Compile(nil, "billing-worker", fakeSites("db.password"))
	if err != nil {
		t.Fatalf("nil manifest should compile fine: %v", err)
	}
	// Nil manifest → no module entry → default ALLOW_ALL → everything passes.
	if !f.Allows("db.password") {
		t.Errorf("nil manifest should allow everything (ALLOW_ALL default)")
	}
	if !f.Allows("anything.else") {
		t.Errorf("nil manifest should allow everything (ALLOW_ALL default)")
	}
}

func TestCompile_UnknownModuleUsesManifestDefault(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Default: chameleonv1.BundleManifest_DENY_ALL,
		Modules: []*chameleonv1.BundleManifest_Module{
			{Name: "auth", Allow: []string{"auth.*"}},
		},
	}
	sites := fakeSites("auth.token", "db.password")
	f, err := Compile(manifest, "billing-worker", sites)
	if err != nil {
		t.Fatalf("unknown module should compile via fallback: %v", err)
	}
	// Unknown module → manifest default DENY_ALL → nothing passes.
	if f.Allows("db.password") {
		t.Errorf("unknown module under DENY_ALL default should deny")
	}
}

func TestCompile_DuplicateModulesRejected(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{Name: "auth"},
			{Name: "auth"},
		},
	}
	_, err := Compile(manifest, "auth", nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate module") {
		t.Fatalf("duplicate module names should error, got: %v", err)
	}
}

func TestCompile_EmptyModuleNameInManifestRejected(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{Name: ""}},
	}
	_, err := Compile(manifest, "anything", nil)
	if err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("empty module name should error, got: %v", err)
	}
}

func TestCompile_PatternMustMatchCatalog(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{Name: "auth", Allow: []string{"db.passwod"}}, // typo
		},
	}
	sites := fakeSites("db.password", "auth.token")
	_, err := Compile(manifest, "auth", sites)
	if err == nil || !strings.Contains(err.Error(), "matches no schema path") {
		t.Fatalf("typo'd pattern should error, got: %v", err)
	}
}

func TestCompile_SyntaxErrorReported(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{Name: "auth", Allow: []string{"pre**fix"}},
		},
	}
	sites := fakeSites("db.password")
	_, err := Compile(manifest, "auth", sites)
	if err == nil || !strings.Contains(err.Error(), "must be a whole segment") {
		t.Fatalf("malformed pattern should error, got: %v", err)
	}
}

func TestAllows_DenyWinsOverAllow(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:  "auth",
			Allow: []string{"db.**"},
			Deny:  []string{"db.admin_password"},
		}},
	}
	sites := fakeSites("db.password", "db.admin_password", "db.user.token")
	f, err := Compile(manifest, "auth", sites)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !f.Allows("db.password") {
		t.Errorf("db.password should be allowed (matches db.**)")
	}
	if f.Allows("db.admin_password") {
		t.Errorf("db.admin_password should be denied (deny wins)")
	}
	if !f.Allows("db.user.token") {
		t.Errorf("db.user.token should be allowed (matches db.**, not denied)")
	}
}

func TestAllows_ImplicitDenyAllWhenAllowSet(t *testing.T) {
	// No explicit default — non-empty allow should imply DENY_ALL.
	manifest := &chameleonv1.BundleManifest{
		Default: chameleonv1.BundleManifest_ALLOW_ALL,
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:  "auth",
			Allow: []string{"auth.*"},
		}},
	}
	sites := fakeSites("auth.token", "db.password")
	f, err := Compile(manifest, "auth", sites)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !f.Allows("auth.token") {
		t.Errorf("auth.token should be allowed (matches allow)")
	}
	if f.Allows("db.password") {
		t.Errorf("db.password should be denied — non-empty allow implies DENY_ALL despite manifest default")
	}
}

func TestAllows_ExplicitDefaultOverridesImplicit(t *testing.T) {
	// Module sets explicit ALLOW_ALL default *and* has allow entries
	// — explicit beats implicit. Used for the "allow lists as
	// documentation, everything is allowed but these are the
	// security surface" posture documented in the proto comment.
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:    "auth",
			Default: chameleonv1.BundleManifest_ALLOW_ALL,
			Allow:   []string{"auth.*"},
		}},
	}
	sites := fakeSites("auth.token", "db.password")
	f, err := Compile(manifest, "auth", sites)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !f.Allows("auth.token") {
		t.Errorf("auth.token should be allowed (matches allow)")
	}
	if !f.Allows("db.password") {
		t.Errorf("db.password should be allowed (explicit ALLOW_ALL default)")
	}
}

func TestAllows_NilFilterAllowsEverything(t *testing.T) {
	var f *Filter
	if !f.Allows("anything") {
		t.Errorf("nil filter should allow everything (no narrowing)")
	}
}

func TestModule(t *testing.T) {
	f, err := Compile(nil, "billing-worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Module() != "billing-worker" {
		t.Errorf("Module() = %q, want %q", f.Module(), "billing-worker")
	}
	var nilFilter *Filter
	if nilFilter.Module() != "" {
		t.Errorf("nil Module() should return empty string")
	}
}
