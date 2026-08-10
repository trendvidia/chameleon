// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"errors"
	"fmt"

	"github.com/trendvidia/chameleon/internal/secretcatalog"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// Filter is the compiled per-module narrowing predicate the narrow
// pass consults. It carries two independent selectors:
//
//   - secret: applied to fields whose value type is pxf.Secret
//     (scalar / repeated / map<K, Secret>). Backed by
//     BundleManifest.Module.allow / deny / default.
//   - attribute: applied to every other (non-secret) field. Backed by
//     BundleManifest.Module.attribute_allow / attribute_deny /
//     attribute_default. The default ALLOW_ALL posture (when no
//     attribute_* patterns are set) preserves the historical
//     "non-secrets pass through unchanged" behavior.
//
// Build with Compile; query with AllowsSecret / AllowsAttribute.
// Filters are immutable after Compile and safe for concurrent reads.
type Filter struct {
	moduleName string
	secret     selector
	attribute  selector
}

// selector is one of the two filter halves. allow/deny are
// schema-form globs; defaultPolicy decides the verdict when neither
// list matches.
type selector struct {
	allow         []string
	deny          []string
	defaultPolicy chameleonv1.BundleManifest_Policy
}

func (s selector) allows(schemaPath string) bool {
	for _, d := range s.deny {
		if matchGlob(d, schemaPath) {
			return false
		}
	}
	for _, a := range s.allow {
		if matchGlob(a, schemaPath) {
			return true
		}
	}
	return s.defaultPolicy == chameleonv1.BundleManifest_ALLOW_ALL
}

// Module returns the name this filter was compiled for. Useful for
// diagnostics ("filter for module 'billing-worker' denies...").
func (f *Filter) Module() string {
	if f == nil {
		return ""
	}
	return f.moduleName
}

// AllowsSecret reports whether the given schema path passes the
// secret selector (the historical Filter.Allows behavior).
//
// The path must be in schema form (no `[i]` / `[key]` brackets) —
// the form Site.SchemaPath returns. Decision order:
//
//  1. If any deny pattern matches → false (deny wins).
//  2. If any allow pattern matches → true.
//  3. Otherwise → secret default policy == ALLOW_ALL.
func (f *Filter) AllowsSecret(schemaPath string) bool {
	if f == nil {
		return true
	}
	return f.secret.allows(schemaPath)
}

// AllowsAttribute reports whether the given schema path passes the
// non-secret (attribute) selector. Same evaluation order as
// AllowsSecret, just against the attribute_* patterns.
func (f *Filter) AllowsAttribute(schemaPath string) bool {
	if f == nil {
		return true
	}
	return f.attribute.allows(schemaPath)
}

// Catalog bundles the two schema-site enumerations Compile uses to
// typo-guard manifest patterns. Secrets are matched against
// secretcatalog.FindInMessage; non-secrets against
// secretcatalog.FindNonSecretFieldsInMessage. Nil slices skip the
// corresponding catalog check (still surfaces glob-syntax errors).
type Catalog struct {
	Secrets    []secretcatalog.Site
	NonSecrets []secretcatalog.Site
}

// Compile resolves a BundleManifest + target module name into a
// Filter, validating every pattern against the schema catalog.
//
// Resolution:
//
//   - If moduleName is in manifest.modules → use that Module entry.
//     Otherwise → empty allow/deny with manifest.default policy on
//     the secret side; attribute side gets an empty selector with
//     ALLOW_ALL default (non-secrets pass through unchanged).
//
//   - Per-module default policy (secret side):
//     module.default if explicitly set (not UNSPECIFIED)
//     else DENY_ALL if module.allow is non-empty (whitelist intent)
//     else manifest.default if set
//     else ALLOW_ALL (the proto's documented top-level default)
//
//   - Per-module default policy (attribute side):
//     module.attribute_default if explicitly set
//     else DENY_ALL if module.attribute_allow is non-empty
//     else ALLOW_ALL (non-secrets pass through by default)
//
//   - Catalog validation: every allow/deny pattern must match at
//     least one site's SchemaPath in the corresponding catalog
//     (catalog.Secrets for allow/deny; catalog.NonSecrets for
//     attribute_allow/attribute_deny). A pattern that matches
//     nothing is almost always a typo. Passing a nil slice for
//     either catalog skips the membership check on that side
//     (glob-syntax is still verified).
//
// Returns the filter on success, or a multi-error describing every
// invalid pattern (so authors can fix all typos in one round-trip).
func Compile(manifest *chameleonv1.BundleManifest, moduleName string, catalog Catalog) (*Filter, error) {
	if moduleName == "" {
		return nil, errors.New("bundler: module name is required")
	}
	if manifest == nil {
		manifest = &chameleonv1.BundleManifest{}
	}

	if err := validateNoDuplicateModules(manifest); err != nil {
		return nil, err
	}
	module, found := lookupModule(manifest, moduleName)

	secretSel := selector{
		allow:         append([]string{}, module.GetAllow()...),
		deny:          append([]string{}, module.GetDeny()...),
		defaultPolicy: resolveSecretDefault(manifest, module, found),
	}
	attrSel := selector{
		allow:         append([]string{}, module.GetAttributeAllow()...),
		deny:          append([]string{}, module.GetAttributeDeny()...),
		defaultPolicy: resolveAttributeDefault(module),
	}

	if err := validatePatternsAgainst(secretSel.allow, secretSel.deny, catalog.Secrets, "allow", "deny"); err != nil {
		return nil, fmt.Errorf("bundler: module %q manifest patterns: %w", moduleName, err)
	}
	if err := validatePatternsAgainst(attrSel.allow, attrSel.deny, catalog.NonSecrets, "attribute_allow", "attribute_deny"); err != nil {
		return nil, fmt.Errorf("bundler: module %q attribute patterns: %w", moduleName, err)
	}

	return &Filter{
		moduleName: moduleName,
		secret:     secretSel,
		attribute:  attrSel,
	}, nil
}

// lookupModule returns the Module entry matching name, plus a
// `found` bool distinguishing "no entry → use manifest default" from
// "entry present with all-empty fields → respect those empties".
// Returns a zero Module when not found so accessor calls on the
// result still work nil-safely.
func lookupModule(manifest *chameleonv1.BundleManifest, name string) (*chameleonv1.BundleManifest_Module, bool) {
	for _, m := range manifest.GetModules() {
		if m.GetName() == name {
			return m, true
		}
	}
	return &chameleonv1.BundleManifest_Module{}, false
}

// validateNoDuplicateModules surfaces ambiguous manifests early.
// Two entries with the same name would silently shadow each other
// (lookupModule picks the first) — that's a manifest bug, not a
// runtime behavior to document.
func validateNoDuplicateModules(manifest *chameleonv1.BundleManifest) error {
	seen := make(map[string]bool, len(manifest.GetModules()))
	for _, m := range manifest.GetModules() {
		name := m.GetName()
		if name == "" {
			return errors.New("bundler: manifest contains module with empty name")
		}
		if seen[name] {
			return fmt.Errorf("bundler: manifest contains duplicate module name %q", name)
		}
		seen[name] = true
	}
	return nil
}

// resolveSecretDefault implements the precedence documented on
// Compile for the secret selector: explicit module default >
// implicit DENY_ALL from non-empty allow > manifest default >
// ALLOW_ALL. The `found` arg distinguishes "no module entry" (use
// manifest default) from "module entry exists, allow happens to be
// empty" (no implicit DENY_ALL — that rule only fires when the
// author actively listed patterns).
func resolveSecretDefault(manifest *chameleonv1.BundleManifest, module *chameleonv1.BundleManifest_Module, found bool) chameleonv1.BundleManifest_Policy {
	if p := module.GetDefault(); p != chameleonv1.BundleManifest_POLICY_UNSPECIFIED {
		return p
	}
	if found && len(module.GetAllow()) > 0 {
		return chameleonv1.BundleManifest_DENY_ALL
	}
	if p := manifest.GetDefault(); p != chameleonv1.BundleManifest_POLICY_UNSPECIFIED {
		return p
	}
	return chameleonv1.BundleManifest_ALLOW_ALL
}

// resolveAttributeDefault picks the attribute-selector default. No
// manifest-level attribute default exists — the policy is local to
// each module. Implicit DENY_ALL fires only when attribute_allow is
// non-empty (whitelist intent); empty attribute_allow + unset
// attribute_default keeps the historical "non-secrets pass through"
// behavior.
func resolveAttributeDefault(module *chameleonv1.BundleManifest_Module) chameleonv1.BundleManifest_Policy {
	if p := module.GetAttributeDefault(); p != chameleonv1.BundleManifest_POLICY_UNSPECIFIED {
		return p
	}
	if len(module.GetAttributeAllow()) > 0 {
		return chameleonv1.BundleManifest_DENY_ALL
	}
	return chameleonv1.BundleManifest_ALLOW_ALL
}

// validatePatternsAgainst runs each pattern through validGlob
// (syntax) and, when `sites` is non-nil, checks it matches at least
// one site's SchemaPath (typo guard). A nil `sites` slice means
// "skip the membership check on this side" — callers pass nil when
// they don't have a catalog handy (some tests, or codepaths that
// only want syntax validation). Errors are joined so authors see
// every problem in one bundler invocation.
//
// allowLabel / denyLabel parameterize the error message wording
// so the same routine serves both selectors ("allow" vs
// "attribute_allow", etc.).
func validatePatternsAgainst(allow, deny []string, sites []secretcatalog.Site, allowLabel, denyLabel string) error {
	var paths []string
	if sites != nil {
		paths = make([]string, 0, len(sites))
		for _, s := range sites {
			paths = append(paths, s.SchemaPath())
		}
	}
	var errs []error
	check := func(label string, patterns []string) {
		for _, p := range patterns {
			if err := validGlob(p); err != nil {
				errs = append(errs, fmt.Errorf("%s %q: %w", label, p, err))
				continue
			}
			if paths != nil && !anyMatch(p, paths) {
				errs = append(errs, fmt.Errorf("%s %q matches no schema path (typo? schema mismatch?)", label, p))
			}
		}
	}
	check(allowLabel, allow)
	check(denyLabel, deny)
	return errors.Join(errs...)
}

func anyMatch(pattern string, paths []string) bool {
	for _, p := range paths {
		if matchGlob(pattern, p) {
			return true
		}
	}
	return false
}
