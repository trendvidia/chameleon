// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package bundler narrows a resolved chameleon configuration to a
// per-module whitelist of schema paths and packages the result for
// distribution as a single secret.
//
// Pipeline (target shape, only Filter + Narrow shipped in the first
// PR — Build is gated on sops.Encrypter landing):
//
//  1. Catalog the schema via internal/secretcatalog — enumerate
//     every Secret-bearing field's SchemaPath (FindInMessage) and
//     every non-Secret field's SchemaPath
//     (FindNonSecretFieldsInMessage). Together they form the
//     universe of paths the filter operates on, and they back the
//     Compile-time typo guard: a secret pattern that matches no
//     real Secret site, or an attribute pattern that matches no
//     real non-secret site, is a bundler error.
//  2. Compile a Filter from the BundleManifest + the target
//     `--module=NAME`. Filter.AllowsSecret and Filter.AllowsAttribute
//     are the two predicates the narrow pass consults — one per
//     module-manifest selector (the historical `allow`/`deny` for
//     secrets, and `attribute_allow`/`attribute_deny` for everything
//     else).
//  3. For each layer file declared by the chain spec: decrypt (via
//     sops.Decrypter — existing), parse to a typed message, Narrow
//     it against the Filter to clear denied secret fields, then
//     re-encrypt with the module's per-env KMS recipients (via
//     sops.Encrypter — NOT YET IMPLEMENTED) and write to the
//     output zip.
//  4. Copy the chain spec to the bundle unchanged. Add an audit
//     entry recording manifest name + hash + module name + build
//     time.
//
// Filter and Narrow are I/O-free and live behind small, pure APIs
// so they can be exercised in isolation. Build (in pipeline.go)
// orchestrates the I/O — its body is currently a stub returning
// errors.ErrUnsupported until the sops encryption capability lands;
// the signature is fixed so the next two PRs (sops.Encrypter and
// then the pipeline wiring) compose cleanly.
//
// Narrowing applies to pxf.Secret fields via the secret selector and
// to every other field via the attribute selector. When a module
// manifest sets no attribute_* patterns, the attribute selector
// defaults to ALLOW_ALL and non-secrets pass through unchanged — the
// historical "secrets-only narrowing" behavior.
package bundler
