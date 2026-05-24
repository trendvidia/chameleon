// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package bundle is the consumer-side API for chameleon distribution
// bundles — the zip artifacts produced by `chameleon bundle build`.
//
// A bundle ships a chain spec + its referenced layer files as a
// single addressable blob suitable for stuffing into one secret in
// AWS Secrets Manager / GCP Secret Manager / your KV store of
// choice. The producer (CLI today, programmatic Build in the
// internal package) optionally encodes the blob as base64 for
// paste-friendly transport; this package decodes either form into
// an [fs.FS] + the parsed in-bundle chain spec, ready to feed
// straight into [chameleon.Load].
//
// Typical use:
//
//	// 1. Pull the bundle out of wherever you stored it.
//	raw, err := secretsClient.GetSecretValue(ctx, "billing-worker-bundle")
//	if err != nil { return err }
//
//	// 2. Open it. Use OpenBase64 if the secret holds the base64 form;
//	//    Open for raw zip bytes.
//	b, err := bundle.OpenBase64(bytes.NewReader(raw))
//	if err != nil { return err }
//
//	// 3. Resolve normally — b.Chain + b.FS plug straight in.
//	res, err := chameleon.Load[*myappv1.AppConfig](
//	    &myappv1.AppConfig{}, b.Chain, b.FS, "prod",
//	)
//	if err != nil { return err }
//	defer res.Destroy()
//
// The bundle's layer files keep the same on-disk shape the runtime
// already knows how to load (sops-encrypted PXF or plaintext PXF) —
// chameleon.Load runs unchanged, with the bundle's [fs.FS] standing
// in for what used to be `os.DirFS("./layers")`.
package bundle

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"google.golang.org/protobuf/proto"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/internal/bundler"
)

// Bundle is the in-memory view of an opened chameleon bundle. The
// caller hands FS + Chain to [chameleon.Load] and is done; Audit is
// the optional provenance record for diagnostics or display.
type Bundle struct {
	// FS exposes the bundle's layer files. Backed by an in-memory
	// zip reader so the bundle bytes can come from anywhere
	// (network, secret manager, embedded asset). Pass this to
	// chameleon.Load as the layers FS.
	FS fs.FS

	// Chain is the chain spec the bundle was built against, parsed
	// as a *chameleonv1.ChainSpec. Pass this as the second
	// argument to chameleon.Load. The runtime re-resolves the
	// chain against FS — no special "bundle mode" required.
	Chain proto.Message

	// Audit is the raw bytes of META/audit.txt, or nil if the
	// bundle predates the audit entry (older bundles may not
	// carry it). Format is freeform `key: value` lines today;
	// callers wanting structured access should defer until the
	// audit entry is promoted to a typed proto.
	Audit []byte
}

// ErrMissingChain is returned when the bundle has no chain.pxf
// entry — almost always a sign that the bytes aren't actually a
// chameleon bundle (e.g. a raw zip of layers without the spec).
var ErrMissingChain = errors.New("bundle: missing chain.pxf entry")

// Open reads a raw (uncompressed) chameleon bundle zip from r. The
// entire input is buffered in memory — bundles are KB-to-low-MB and
// random access to the zip central directory needs a ReaderAt — so
// callers handling very large inputs should consider whether the
// memory cost matches their environment.
//
// Use [OpenBase64] for bundles delivered as base64 text (the
// `chameleon bundle build --base64` output shape).
func Open(r io.Reader) (*Bundle, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("bundle: read input: %w", err)
	}
	return openZipBytes(data)
}

// OpenBase64 reads a base64-encoded chameleon bundle from r,
// decodes it, and unpacks the underlying zip. The wrapping is
// standard padded base64 (matching base64.StdEncoding) — what
// `chameleon bundle build --base64` produces.
//
// Equivalent to:
//
//	bundle.Open(base64.NewDecoder(base64.StdEncoding, r))
//
// but with a more obvious name at the call site and a slightly
// clearer error message on malformed base64 input.
func OpenBase64(r io.Reader) (*Bundle, error) {
	dec := base64.NewDecoder(base64.StdEncoding, r)
	data, err := io.ReadAll(dec)
	if err != nil {
		return nil, fmt.Errorf("bundle: decode base64: %w", err)
	}
	return openZipBytes(data)
}

// openZipBytes is the shared core of Open / OpenBase64. Takes the
// already-buffered zip bytes, opens the zip, pulls chain.pxf out
// for early parsing (so the caller sees a clear "missing chain" /
// "bad chain spec" error here rather than at chameleon.Load time),
// and returns the assembled Bundle.
func openZipBytes(data []byte) (*Bundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("bundle: open zip: %w", err)
	}

	chainBytes, err := readZipEntry(zr, bundler.ChainEntryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrMissingChain
		}
		return nil, err
	}
	chainMsg, err := chain.ParseSpec(chainBytes)
	if err != nil {
		return nil, fmt.Errorf("bundle: parse %s: %w", bundler.ChainEntryPath, err)
	}

	// Audit entry is optional — bundles produced before the entry
	// existed (or by other tooling) won't have it. Surface as nil
	// rather than error.
	var audit []byte
	if data, err := readZipEntry(zr, bundler.AuditEntryPath); err == nil {
		audit = data
	}

	return &Bundle{
		FS:    zr,
		Chain: chainMsg,
		Audit: audit,
	}, nil
}

// readZipEntry pulls one entry out of zr by exact name match. zip's
// fs.FS view supports fs.ReadFile, but going through it for the
// known-name lookups here would walk the central directory twice.
// Direct File-slice iteration is fine for the two metadata reads
// (chain spec + audit); per-layer reads go through the fs.FS view
// the caller hands to chameleon.Load.
func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("bundle: open %s: %w", name, err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, fmt.Errorf("bundle: read %s: %w", name, err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("bundle: %s: %w", name, fs.ErrNotExist)
}
