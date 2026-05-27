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
	_, err := Compile(&chameleonv1.BundleManifest{}, "", Catalog{})
	if err == nil {
		t.Fatal("Compile with empty module name should error")
	}
}

func TestCompile_NilManifestUsesDefaults(t *testing.T) {
	f, err := Compile(nil, "billing-worker", Catalog{Secrets: fakeSites("db.password")})
	if err != nil {
		t.Fatalf("nil manifest should compile fine: %v", err)
	}
	// Nil manifest → no module entry → default ALLOW_ALL → everything passes.
	if !f.AllowsSecret("db.password") {
		t.Errorf("nil manifest should allow everything (ALLOW_ALL default)")
	}
	if !f.AllowsSecret("anything.else") {
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
	f, err := Compile(manifest, "billing-worker", Catalog{Secrets: sites})
	if err != nil {
		t.Fatalf("unknown module should compile via fallback: %v", err)
	}
	// Unknown module → manifest default DENY_ALL → nothing passes.
	if f.AllowsSecret("db.password") {
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
	_, err := Compile(manifest, "auth", Catalog{})
	if err == nil || !strings.Contains(err.Error(), "duplicate module") {
		t.Fatalf("duplicate module names should error, got: %v", err)
	}
}

func TestCompile_EmptyModuleNameInManifestRejected(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{Name: ""}},
	}
	_, err := Compile(manifest, "anything", Catalog{})
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
	_, err := Compile(manifest, "auth", Catalog{Secrets: sites})
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
	_, err := Compile(manifest, "auth", Catalog{Secrets: sites})
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
	f, err := Compile(manifest, "auth", Catalog{Secrets: sites})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !f.AllowsSecret("db.password") {
		t.Errorf("db.password should be allowed (matches db.**)")
	}
	if f.AllowsSecret("db.admin_password") {
		t.Errorf("db.admin_password should be denied (deny wins)")
	}
	if !f.AllowsSecret("db.user.token") {
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
	f, err := Compile(manifest, "auth", Catalog{Secrets: sites})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !f.AllowsSecret("auth.token") {
		t.Errorf("auth.token should be allowed (matches allow)")
	}
	if f.AllowsSecret("db.password") {
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
	f, err := Compile(manifest, "auth", Catalog{Secrets: sites})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !f.AllowsSecret("auth.token") {
		t.Errorf("auth.token should be allowed (matches allow)")
	}
	if !f.AllowsSecret("db.password") {
		t.Errorf("db.password should be allowed (explicit ALLOW_ALL default)")
	}
}

func TestAllows_NilFilterAllowsEverything(t *testing.T) {
	var f *Filter
	if !f.AllowsSecret("anything") {
		t.Errorf("nil filter should allow everything (no narrowing)")
	}
}

func TestModule(t *testing.T) {
	f, err := Compile(nil, "billing-worker", Catalog{})
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

func TestCompile_AttributeSelectorDefaultsToAllowAll(t *testing.T) {
	// No attribute_* fields set → attribute selector defaults to
	// ALLOW_ALL, preserving the historical non-secrets pass-through.
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{Name: "billing", Allow: []string{"db.password"}},
		},
	}
	f, err := Compile(manifest, "billing", Catalog{Secrets: fakeSites("db.password")})
	if err != nil {
		t.Fatal(err)
	}
	if !f.AllowsAttribute("any.non.secret.path") {
		t.Errorf("attribute selector with no patterns should ALLOW_ALL")
	}
}

func TestCompile_AttributeAllowImpliesDenyAll(t *testing.T) {
	// attribute_allow set with no attribute_default → implicit
	// DENY_ALL (whitelist intent), mirroring the secret-side rule.
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{
				Name:           "billing",
				AttributeAllow: []string{"public.*"},
			},
		},
	}
	f, err := Compile(manifest, "billing", Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	if !f.AllowsAttribute("public.banner_url") {
		t.Errorf("public.banner_url should be allowed by attribute_allow=public.*")
	}
	if f.AllowsAttribute("private.debug_endpoint") {
		t.Errorf("private.debug_endpoint should be denied (implicit DENY_ALL from attribute_allow)")
	}
}

func TestCompile_AttributeDenyOverridesAllow(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{
				Name:             "billing",
				AttributeAllow:   []string{"**"},
				AttributeDeny:    []string{"debug.*"},
				AttributeDefault: chameleonv1.BundleManifest_ALLOW_ALL,
			},
		},
	}
	f, err := Compile(manifest, "billing", Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	if !f.AllowsAttribute("public.banner_url") {
		t.Errorf("public.banner_url should pass (** allows)")
	}
	if f.AllowsAttribute("debug.tracing_endpoint") {
		t.Errorf("debug.tracing_endpoint should be denied (deny wins)")
	}
}

func TestCompile_InvalidAttributeGlobErrors(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{
				Name:           "billing",
				AttributeAllow: []string{"***"}, // invalid glob
			},
		},
	}
	_, err := Compile(manifest, "billing", Catalog{})
	if err == nil {
		t.Errorf("invalid attribute_allow glob should error")
	}
}

func TestCompile_AttributePatternMatchesNoNonSecretSiteErrors(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{
				Name:           "billing",
				AttributeAllow: []string{"typo.field"}, // not in catalog
			},
		},
	}
	catalog := Catalog{
		NonSecrets: fakeSites("primary.url", "service_name"),
	}
	_, err := Compile(manifest, "billing", catalog)
	if err == nil {
		t.Errorf("attribute_allow pattern matching no non-secret site should error (typo guard)")
	}
}

func TestCompile_AttributePatternMatchingNonSecretSiteCompiles(t *testing.T) {
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{
				Name:           "billing",
				AttributeAllow: []string{"primary.url"},
			},
		},
	}
	catalog := Catalog{
		NonSecrets: fakeSites("primary.url", "service_name"),
	}
	if _, err := Compile(manifest, "billing", catalog); err != nil {
		t.Errorf("attribute_allow pattern matching a real non-secret site should compile; got %v", err)
	}
}

func TestCompile_NilNonSecretsSkipsAttributeTypoGuard(t *testing.T) {
	// Compile callers that don't have a non-secret catalog handy
	// (e.g. integration tests building Filter directly) can pass a
	// zero Catalog to skip the membership check. Syntax is still
	// validated.
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{
			{
				Name:           "billing",
				AttributeAllow: []string{"typo.field"},
			},
		},
	}
	if _, err := Compile(manifest, "billing", Catalog{}); err != nil {
		t.Errorf("nil NonSecrets catalog should skip typo-guard; got %v", err)
	}
}
