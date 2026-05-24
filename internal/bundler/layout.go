// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

// Bundle zip layout constants. Exported so the public bundle
// consume package (github.com/trendvidia/chameleon/bundle) reads
// the same paths the producer writes; if either side hard-codes
// the string the two could drift silently.
//
// Layout:
//
//	<root>/
//	  chain.pxf            ← the chain spec, copied verbatim
//	  <resolved layer paths>...  ← per-layer ciphertext / plaintext
//	  META/
//	    audit.txt          ← produce-time provenance (manifest hash etc.)
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
