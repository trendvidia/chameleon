// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain

import (
	"fmt"
	"os"

	"google.golang.org/protobuf/proto"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// LoadSpec reads a chain.pxf file from disk and parses it as a
// chameleon.v1.ChainSpec. Returns the parsed message ready to pass
// to [Expand] or [resolver.New].
//
// The returned [proto.Message] is the typed *chameleonv1.ChainSpec.
// Callers may assert on the concrete type, but the [proto.Message]
// interface is the stable contract.
func LoadSpec(path string) (proto.Message, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a documented API parameter
	if err != nil {
		return nil, fmt.Errorf("chain: read %s: %w", path, err)
	}
	return ParseSpec(data)
}

// ParseSpec parses chain.pxf bytes as a ChainSpec. Useful when the
// spec lives in an embed.FS, a network response, or otherwise isn't
// a filesystem path. [LoadSpec] is the file convenience.
func ParseSpec(data []byte) (proto.Message, error) {
	msg := &chameleonv1.ChainSpec{}
	if err := pxf.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("chain: parse: %w", err)
	}
	return msg, nil
}
