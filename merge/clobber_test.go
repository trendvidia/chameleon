// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package merge_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/merge"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// clobberProtoSrc — bundle pxf.Secret + a user message that has a
// pxf.Secret-typed field. Keeps the test file self-contained (no
// dependency on the chameleon resolver test fixtures).
const clobberProtoSrc = `
syntax = "proto3";
package merge_test.clobber.v1;

message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}

// Same FullName as the real pxf.Secret so wkt.Atomic recognizes it.
// We re-declare it under the right package via the import alias trick:
// here we use a nested message wrapper to mimic pxf.Secret naming.

message AppConfig {
  Secret db_password = 1;
  string base_url = 2;
}
`

// Use the canonical pxf.Secret type instead of the inline one so
// wkt.Atomic recognizes it. compileWithPxfSecret produces an AppConfig
// whose db_password field is typed as pxf.Secret.
const clobberProtoSrcWithPxf = `
syntax = "proto3";
package pxf;
message Secret {
  string value = 1;
  string hint = 2;
  string fingerprint = 3;
}
`

const clobberAppProtoSrc = `
syntax = "proto3";
package merge_test.clobber.v1;

import "pxf/secret.proto";

message AppConfig {
  pxf.Secret db_password = 1;
  string base_url = 2;
}
`

func compileClobberApp(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	sources := map[string]string{
		"pxf/secret.proto": clobberProtoSrcWithPxf,
		"app.proto":        clobberAppProtoSrc,
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{
				Accessor: protocompile.SourceAccessorFromMap(sources),
			},
		),
	}
	result, err := comp.Compile(context.Background(), "app.proto")
	require.NoError(t, err)
	d, err := result.AsResolver().FindDescriptorByName("merge_test.clobber.v1.AppConfig")
	require.NoError(t, err)
	return d.(protoreflect.MessageDescriptor)
}

// parseInto parses pxf text into a dynamicpb message of the given
// descriptor, returning the message and its *pxf.Result.
func parseInto(t *testing.T, desc protoreflect.MessageDescriptor, input string) (proto.Message, *pxf.Result) {
	t.Helper()
	msg := dynamicpb.NewMessage(desc)
	res, err := pxf.UnmarshalFull([]byte(input), msg)
	require.NoError(t, err)
	return msg, res
}

// TestClobber_HintDropped — lower layer's Secret has a hint, higher
// layer's overwrites with just a value. Without a callback the merge
// happens silently; with a callback we receive the ClobberInfo.
func TestClobber_HintDropped(t *testing.T) {
	desc := compileClobberApp(t)
	dst := dynamicpb.NewMessage(desc)

	// Lower layer first.
	base, baseRes := parseInto(t, desc, `db_password { value = "x"  hint = "Postgres primary" }`)
	require.NoError(t, merge.Merge(dst, base, baseRes))

	// Upper layer overwrites the secret with just a value.
	dev, devRes := parseInto(t, desc, `db_password = "y"`)

	var clobbers []merge.ClobberInfo
	err := merge.MergeWithOptions(dst, dev, devRes, merge.Options{
		OnClobberedMetadata: func(c merge.ClobberInfo) error {
			clobbers = append(clobbers, c)
			return nil
		},
	})
	require.NoError(t, err)
	require.Len(t, clobbers, 1, "expected one clobber on db_password")
	assert.Equal(t, "db_password", clobbers[0].Path)
	assert.Equal(t, "pxf.Secret", clobbers[0].Type)
	assert.Equal(t, []string{"hint"}, clobbers[0].LostFields)

	// Sanity: dst's resulting hint really is empty.
	pwFD := desc.Fields().ByName("db_password")
	resultMsg := dst.ProtoReflect().Get(pwFD).Message()
	hintFD := resultMsg.Descriptor().Fields().ByName("hint")
	assert.Equal(t, "", resultMsg.Get(hintFD).String())
}

// TestClobber_NoClobberWhenUpperPreservesMetadata — when the upper
// layer re-states the hint explicitly, no clobber.
func TestClobber_NoClobberWhenUpperPreservesMetadata(t *testing.T) {
	desc := compileClobberApp(t)
	dst := dynamicpb.NewMessage(desc)

	base, baseRes := parseInto(t, desc, `db_password { value = "x"  hint = "Postgres primary" }`)
	require.NoError(t, merge.Merge(dst, base, baseRes))

	dev, devRes := parseInto(t, desc, `db_password { value = "y"  hint = "Postgres primary" }`)

	var clobbers []merge.ClobberInfo
	err := merge.MergeWithOptions(dst, dev, devRes, merge.Options{
		OnClobberedMetadata: func(c merge.ClobberInfo) error {
			clobbers = append(clobbers, c)
			return nil
		},
	})
	require.NoError(t, err)
	assert.Empty(t, clobbers, "upper preserved metadata, no clobber expected")
}

// TestClobber_NoClobberWhenLowerHasNoMetadata — when the lower layer
// had no metadata (just value), there's nothing to clobber.
func TestClobber_NoClobberWhenLowerHasNoMetadata(t *testing.T) {
	desc := compileClobberApp(t)
	dst := dynamicpb.NewMessage(desc)

	base, baseRes := parseInto(t, desc, `db_password = "x"`)
	require.NoError(t, merge.Merge(dst, base, baseRes))

	dev, devRes := parseInto(t, desc, `db_password = "y"`)

	var clobbers []merge.ClobberInfo
	err := merge.MergeWithOptions(dst, dev, devRes, merge.Options{
		OnClobberedMetadata: func(c merge.ClobberInfo) error {
			clobbers = append(clobbers, c)
			return nil
		},
	})
	require.NoError(t, err)
	assert.Empty(t, clobbers, "no metadata to lose")
}

// TestClobber_BothHintAndFingerprintLost — multiple metadata fields.
func TestClobber_BothHintAndFingerprintLost(t *testing.T) {
	desc := compileClobberApp(t)
	dst := dynamicpb.NewMessage(desc)

	base, baseRes := parseInto(t, desc,
		`db_password { value = "x"  hint = "Postgres"  fingerprint = "sha256:abc" }`)
	require.NoError(t, merge.Merge(dst, base, baseRes))

	dev, devRes := parseInto(t, desc, `db_password = "y"`)

	var clobbers []merge.ClobberInfo
	require.NoError(t, merge.MergeWithOptions(dst, dev, devRes, merge.Options{
		OnClobberedMetadata: func(c merge.ClobberInfo) error {
			clobbers = append(clobbers, c)
			return nil
		},
	}))
	require.Len(t, clobbers, 1)
	assert.ElementsMatch(t, []string{"hint", "fingerprint"}, clobbers[0].LostFields)
}

// TestClobber_StrictModeAbortsMerge — when OnClobberedMetadata returns
// an error, the merge aborts with that error.
func TestClobber_StrictModeAbortsMerge(t *testing.T) {
	desc := compileClobberApp(t)
	dst := dynamicpb.NewMessage(desc)

	base, baseRes := parseInto(t, desc, `db_password { value = "x"  hint = "Postgres" }`)
	require.NoError(t, merge.Merge(dst, base, baseRes))

	dev, devRes := parseInto(t, desc, `db_password = "y"`)

	sentinel := assert.AnError
	err := merge.MergeWithOptions(dst, dev, devRes, merge.Options{
		OnClobberedMetadata: func(c merge.ClobberInfo) error {
			return sentinel
		},
	})
	require.ErrorIs(t, err, sentinel, "callback error must abort the merge")
}

// TestClobber_NoCallbackNoOverhead — without a callback, the existing
// merge.Merge path runs unchanged: no clobber detection, no allocations.
// We exercise it for behavioral equivalence to the pre-clobber code.
func TestClobber_NoCallbackNoOverhead(t *testing.T) {
	desc := compileClobberApp(t)
	dst := dynamicpb.NewMessage(desc)

	base, baseRes := parseInto(t, desc, `db_password { value = "x"  hint = "Postgres" }`)
	require.NoError(t, merge.Merge(dst, base, baseRes))

	dev, devRes := parseInto(t, desc, `db_password = "y"`)
	require.NoError(t, merge.Merge(dst, dev, devRes)) // no options → no clobber detection
}

// Compile-time sanity that we don't break protocompile when the inline
// schema declares two separately-loaded namespaces. Just exercises
// the helper without asserting anything beyond setup success.
func TestCompileClobberApp(t *testing.T) {
	desc := compileClobberApp(t)
	require.NotNil(t, desc)
}

// _ = clobberProtoSrc — keep the first protoSrc constant from being
// reported as unused while still being part of this file for
// documentation. Tests above use clobberProtoSrcWithPxf / Compile.
var _ = clobberProtoSrc
