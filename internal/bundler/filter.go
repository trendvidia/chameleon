// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"errors"
	"fmt"

	"github.com/trendvidia/chameleon/internal/secretcatalog"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// Filter is the compiled per-module allow/deny predicate that the
// narrow pass consults. Build one with Compile; consult with Allows.
// Filters are immutable after Compile and safe for concurrent reads.
type Filter struct {
	moduleName    string
	allow         []string
	deny          []string
	defaultPolicy chameleonv1.BundleManifest_Policy
}

// Module returns the name this filter was compiled for. Useful for
// diagnostics ("filter for module 'billing-worker' denies...").
func (f *Filter) Module() string {
	if f == nil {
		return ""
	}
	return f.moduleName
}

// Allows reports whether the given schema path passes the filter.
// The path must be in schema form (no `[i]` / `[key]` brackets) —
// the form Site.SchemaPath returns. Decision order:
//
//  1. If any deny pattern matches → false (deny wins).
//  2. If any allow pattern matches → true.
//  3. Otherwise → modulePolicy == ALLOW_ALL.
func (f *Filter) Allows(schemaPath string) bool {
	if f == nil {
		return true // nil filter = no narrowing
	}
	for _, d := range f.deny {
		if matchGlob(d, schemaPath) {
			return false
		}
	}
	for _, a := range f.allow {
		if matchGlob(a, schemaPath) {
			return true
		}
	}
	return f.defaultPolicy == chameleonv1.BundleManifest_ALLOW_ALL
}

// Compile resolves a BundleManifest + target module name into a
// Filter, validating every pattern against the schema catalog.
//
// Resolution:
//
//   - If moduleName is in manifest.modules → use that Module entry.
//     Otherwise → empty allow/deny with manifest.default policy
//     (per the manifest comment: "policy applied to modules not
//     listed"). The caller should typically warn on the unknown-
//     module fallthrough so a typo'd --module flag doesn't silently
//     produce a fat bundle.
//
//   - Per-module default policy:
//       module.default if explicitly set (not UNSPECIFIED)
//       else DENY_ALL if module.allow is non-empty (typical whitelist intent)
//       else manifest.default if set
//       else ALLOW_ALL (the proto's documented top-level default)
//
//   - Catalog validation: every allow/deny pattern must match at
//     least one site.SchemaPath. A pattern that matches nothing is
//     almost always a typo (silent over-restriction → broken
//     module at runtime is a worse failure mode than fail-loud at
//     bundle-build).
//
// Returns the filter on success, or a multi-error describing every
// invalid pattern (so authors can fix all typos in one round-trip).
func Compile(manifest *chameleonv1.BundleManifest, moduleName string, sites []secretcatalog.Site) (*Filter, error) {
	if moduleName == "" {
		return nil, errors.New("bundler: module name is required")
	}
	if manifest == nil {
		manifest = &chameleonv1.BundleManifest{}
	}

	module, found := lookupModule(manifest, moduleName)
	if err := validateNoDuplicateModules(manifest); err != nil {
		return nil, err
	}

	allow := module.GetAllow()
	deny := module.GetDeny()
	policy := resolveDefaultPolicy(manifest, module, found)

	if err := validatePatterns(allow, deny, sites); err != nil {
		return nil, fmt.Errorf("bundler: module %q manifest patterns: %w", moduleName, err)
	}

	return &Filter{
		moduleName:    moduleName,
		allow:         append([]string{}, allow...),
		deny:          append([]string{}, deny...),
		defaultPolicy: policy,
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

// resolveDefaultPolicy implements the precedence documented on
// Compile: explicit module default > implicit DENY_ALL from non-
// empty allow > manifest default > ALLOW_ALL. The `found` arg
// distinguishes "no module entry" (use manifest default) from
// "module entry exists, allow happens to be empty" (the empty allow
// list doesn't trigger the implicit DENY_ALL — that rule only fires
// when the author actively listed patterns).
func resolveDefaultPolicy(manifest *chameleonv1.BundleManifest, module *chameleonv1.BundleManifest_Module, found bool) chameleonv1.BundleManifest_Policy {
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

// validatePatterns runs each pattern through validGlob (syntax) and
// then checks it matches at least one site's SchemaPath (typo
// guard). Errors are accumulated and joined so authors see every
// problem in one bundler invocation.
func validatePatterns(allow, deny []string, sites []secretcatalog.Site) error {
	paths := make([]string, 0, len(sites))
	for _, s := range sites {
		paths = append(paths, s.SchemaPath())
	}
	var errs []error
	check := func(kind string, patterns []string) {
		for _, p := range patterns {
			if err := validGlob(p); err != nil {
				errs = append(errs, fmt.Errorf("%s %q: %w", kind, p, err))
				continue
			}
			if !anyMatch(p, paths) {
				errs = append(errs, fmt.Errorf("%s %q matches no schema path (typo? schema mismatch?)", kind, p))
			}
		}
	}
	check("allow", allow)
	check("deny", deny)
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
