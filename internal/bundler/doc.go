// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package bundler narrows a resolved chameleon configuration to a
// per-module whitelist of schema paths and packages the result for
// distribution as a single secret.
//
// Pipeline:
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
//     BuildOptions.Decrypter), parse to a typed message, Narrow it
//     against the Filter to clear denied secret fields, then
//     re-encrypt to the module's per-env recipients (via
//     BuildOptions.Encrypter) and write to the output zip.
//  4. Copy the chain spec to the bundle unchanged. Add an audit
//     entry recording manifest name + hash + module name + build
//     time.
//
// Filter and Narrow are I/O-free and live behind small, pure APIs so
// they can be exercised in isolation; Build (in pipeline.go)
// orchestrates the I/O.
//
// # No backend, by construction
//
// Every cryptographic capability arrives through BuildOptions: the
// crypt.Decrypter and crypt.Encrypter to use, and RecipientsForKeyName
// to turn a chain spec's `key_name` into recipients. None of it is
// reached for directly, because this package ships in the library
// module and importing an implementation there puts the whole cloud
// key-management tree into every consumer's binary and module graph —
// see trendvidia/chameleon#130. The CLI, which links the sops backend
// anyway, supplies all three.
//
// A consequence worth knowing when reading the tests: the end-to-end
// coverage here runs against a fake backend (fakecrypto_test.go), and
// the real age round-trip through Build lives in the sops module
// (sops/bundle_test.go).
//
// Narrowing applies to pxf.Secret fields via the secret selector and
// to every other field via the attribute selector. When a module
// manifest sets no attribute_* patterns, the attribute selector
// defaults to ALLOW_ALL and non-secrets pass through unchanged — the
// historical "secrets-only narrowing" behavior.
package bundler
