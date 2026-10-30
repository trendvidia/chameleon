// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package proto_test

import (
	"io/fs"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protos"
)

// TestVendoredPxfSourcesMatchProtos keeps the two pxf sources buf compiles
// equal to the ones github.com/trendvidia/protos generated its Go from.
// buf needs the source to compile the files that import it, but generates
// no Go for it: the bindings import protos/pxf instead, which protocompile
// also links, and a second generated copy registers the same file twice
// and panics at startup (#210). A copy that drifts from protos would
// compile bindings against descriptors that the linked Go does not have.
// After bumping protos, copy its files over these and run `make generate`.
func TestVendoredPxfSourcesMatchProtos(t *testing.T) {
	for path, local := range map[string]string{
		"pxf/annotations.proto": "pxf/annotations.proto",
		"pxf/secret.proto":      "../examples/buf/proto/pxf/secret.proto",
	} {
		want, err := fs.ReadFile(protos.Files, path)
		require.NoError(t, err)
		got, err := os.ReadFile(local)
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), "%s differs from protos' %s; copy it over and run `make generate`", local, path)
	}
}
