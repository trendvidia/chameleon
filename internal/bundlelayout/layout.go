// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package bundlelayout holds the bundle zip layout constants shared by
// the producer (internal/bundler) and the public consumer package
// (github.com/trendvidia/chameleon/bundle); if either side hard-coded
// the string the two could drift silently.
//
// It is its own package, rather than a file in internal/bundler, for
// the reason in trendvidia/chameleon#130: bundler links a sops
// Encrypter because it re-encrypts, so a consumer package reaching
// into it for two string constants would pull every cloud KMS SDK into
// a read-only binary. This package's dependency set is empty, and
// linkage_test.go pins that the bundle read path stays clean.
//
// Layout:
//
//	<root>/
//	  chain.pxf            ← the chain spec, copied verbatim
//	  <resolved layer paths>...  ← per-layer ciphertext / plaintext
//	  META/
//	    audit.txt          ← produce-time provenance (manifest hash etc.)
package bundlelayout

const (
	// ChainEntryPath is the in-bundle path of the chain spec.
	ChainEntryPath = "chain.pxf"

	// AuditEntryPath is the in-bundle path of the audit record.
	AuditEntryPath = "META/audit.txt"

	// MetaDirPrefix is the directory under which audit / metadata
	// files live. Consumers may grep entry names against this
	// prefix to enumerate metadata without coupling to specific
	// filenames.
	MetaDirPrefix = "META/"
)
