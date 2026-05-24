// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"context"
	"errors"
	"io"
	"io/fs"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/sops"
)

// sops.Encrypter + sops.Recipients carry the per-layer re-encrypt
// contract. The bundler computes a sops.Recipients per layer from
// the module's recipients_by_env entry (or recipients fallback) so
// each layer file inside an env-agnostic bundle is encrypted only
// to the env that consumes it.

// BuildOptions is the input bundle for [Build]. Required fields
// cause Build to error if unset; optional fields fall back to
// documented defaults.
type BuildOptions struct {
	// Manifest is the BundleManifest authored by ops. Required.
	// Use a typed *chameleonv1.BundleManifest at the call site;
	// the proto.Message indirection keeps this package independent
	// of the generated Go binding's import path.
	Manifest proto.Message

	// Module names the target module — matches an entry in
	// Manifest.modules, or falls back to Manifest.default policy
	// with a warning. Required.
	Module string

	// Schema is the descriptor for the consumer's top-level config
	// message (e.g. AppConfig). Used for two things: catalog
	// enumeration (manifest pattern validation) and tree narrowing.
	// Required.
	Schema protoreflect.MessageDescriptor

	// ChainSpec is the resolved chain configuration. Required;
	// expanded via chain.Expand to enumerate layer files. Copied
	// verbatim into the bundle so the runtime can re-resolve.
	ChainSpec proto.Message

	// LayersFS is the source filesystem holding the chain spec's
	// referenced layer files. Required.
	LayersFS fs.FS

	// Decrypter reads sops-encrypted layers. Required when any
	// LayerEntry in ChainSpec is marked encrypted; ignored otherwise.
	Decrypter sops.Decrypter

	// Encrypter writes sops-encrypted layers in the output bundle.
	// Required when any layer's resolved sops.Recipients are non-empty.
	Encrypter sops.Encrypter

	// Output is the io.Writer the zip bundle is streamed to.
	// Required. The caller is responsible for any wrapping
	// concerns (file, multipart upload, in-memory buffer).
	Output io.Writer

	// ManifestSHA256 is recorded in the bundle's audit entry so
	// incident response can answer "which manifest version
	// produced this bundle". Optional; computed by the bundler
	// from Manifest's serialized form if empty.
	ManifestSHA256 []byte

	// ManifestName is recorded in the audit entry alongside the
	// hash. Optional; defaults to "" if unset (the hash alone is
	// sufficient to identify the manifest, but the name aids
	// human readability).
	ManifestName string
}

// errBuildNotImplemented is the temporary error returned by Build
// until the sops.Encrypter capability lands and the pipeline body
// is wired. Surfaced via errors.Is so callers can branch on it
// during the staged rollout.
var errBuildNotImplemented = errors.New("bundler: Build is not implemented yet — pending sops.Encrypter and pipeline wiring (tracked in ROADMAP)")

// Build runs the decrypt → narrow → re-encrypt → package pipeline.
// Currently a stub returning errBuildNotImplemented; the next two
// PRs fill in the body without changing this signature. Flow:
//
//  1. secretcatalog.FindInMessage(opts.Schema) → site list.
//  2. Compile(opts.Manifest, opts.Module, sites) → *Filter.
//     Validates every pattern at this point; manifest typos fail
//     the bundler before any layer I/O.
//  3. chain.Expand(opts.ChainSpec, "", nil)-style expansion to
//     enumerate every (env, LayerRef) pair the bundle must carry.
//     Bundle is env-agnostic — we iterate all envs declared in
//     the chain spec, not just one.
//  4. For each LayerRef:
//     a. fs.ReadFile(opts.LayersFS, ref.Path) → ciphertext bytes.
//     b. If ref.Encrypted: opts.Decrypter.Decrypt(...) → plaintext.
//     c. internal/header.Parse → split @header preamble from body.
//     d. pxf.UnmarshalFull(body, msg) where msg is a fresh
//        opts.Schema instance.
//     e. Narrow(msg, filter) — clears denied Secret fields.
//     f. pxf.Marshal(msg) → trimmed body bytes.
//     g. Reattach the original @header preamble.
//     h. If ref.Encrypted: opts.Encrypter.Encrypt(...) using
//        resolved per-env Recipients → re-encrypted bytes.
//     i. Write to zip entry `layers/<env>/<basename>`.
//  5. Write the chain spec to `chain.pxf`.
//  6. Write the audit entry to `META/audit.pxf` (manifest
//     name + sha + module + built_at + chameleon version).
//  7. Finalize the zip.
func Build(_ context.Context, _ BuildOptions) error {
	return errBuildNotImplemented
}

// ErrNotImplemented is the sentinel returned by Build until the
// pipeline body is wired. Exported so the CLI layer can present a
// "feature in progress" message instead of leaking the internal
// error string.
var ErrNotImplemented = errBuildNotImplemented
