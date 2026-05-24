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
//     every pxf.Secret-bearing field's SchemaPath. This is the
//     universe of paths the filter operates on; the catalog is also
//     used to lint manifest patterns at Compile time (a pattern that
//     matches no real path is a bundler error).
//  2. Compile a Filter from the BundleManifest + the target
//     `--module=NAME`. Filter.Allows(schemaPath) is the predicate
//     the narrow pass consults.
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
// Narrowing only applies to pxf.Secret fields — the user-visible
// goal is "smaller blast radius for secrets". Non-secret structural
// fields are preserved verbatim. Generalizing the filter to non-
// secret subtrees is plausibly a v2 ask and is deliberately not
// designed for here.
package bundler
