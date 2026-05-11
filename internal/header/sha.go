// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package header

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// BodyHash returns the hex-encoded SHA-256 of a layer file's
// schema-typed body — the bytes after any leading @type / @<directive>
// blocks. The body offset is what `pxf.Document.BodyOffset` reports
// after parsing.
//
// Hashing body-only avoids the chicken-and-egg problem of "hash the
// file that contains its own hash": authors can set, clear, or edit
// `@header.content_sha256` without invalidating the hash. The
// trade-off is that BodyHash does not detect tampering with the
// header's id / environment / encrypted assertions — but those are
// already cross-checked against the chain spec at resolve time, so
// tampering would surface there.
func BodyHash(data []byte) (string, error) {
	doc, err := pxf.Parse(data)
	if err != nil {
		return "", fmt.Errorf("header: parse for hash: %w", err)
	}
	if doc.BodyOffset < 0 || doc.BodyOffset > len(data) {
		return "", fmt.Errorf("header: invalid body offset %d (len=%d)", doc.BodyOffset, len(data))
	}
	sum := sha256.Sum256(data[doc.BodyOffset:])
	return hex.EncodeToString(sum[:]), nil
}

// ContentSHA256 returns the parsed header's content_sha256 field. The
// second return is false if the field was absent — three-state
// presence, distinct from `content_sha256 = ""`.
func (p *Parsed) ContentSHA256() (string, bool) {
	if !p.result.IsSet("content_sha256") {
		return "", false
	}
	fd := p.msg.Descriptor().Fields().ByName("content_sha256")
	return p.msg.Get(fd).String(), true
}
