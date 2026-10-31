// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package upstream_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/trendvidia/chameleon/internal/upstream"
	"github.com/trendvidia/chameleon/internal/upstream/upstreamtest"
)

// Not the whole sentence: upstream's own report quotes the spelling the
// author used, so this holds on either side of the protowire-go bump.
const exclusive = "are exclusive on one field"

func msgs(m ...*descriptorpb.DescriptorProto) []*descriptorpb.DescriptorProto { return m }

// Every spelling of the pair is refused, draft -01 §annotation-extensions
// "Required and Default". The mixed rows matter because one field can be
// half-migrated from the bracket form to the annotation form.
func TestValidateFile_RefusesThePairInEverySpelling(t *testing.T) {
	for name, opts := range map[string][][]byte{
		"bracket options":               {upstreamtest.Required(true), upstreamtest.Default("x")},
		"annotations":                   {upstreamtest.AtRequired(), upstreamtest.AtDefault("x")},
		"(pxf.required) + @default":     {upstreamtest.Required(true), upstreamtest.AtDefault("x")},
		"@required + (pxf.default)":     {upstreamtest.AtRequired(), upstreamtest.Default("x")},
		"an empty default is a default": {upstreamtest.Required(true), upstreamtest.Default("")},
	} {
		t.Run(name, func(t *testing.T) {
			fd := upstreamtest.File(t, "a.proto", msgs(upstreamtest.Message("M", opts...)))
			vs := upstream.ValidateFile(fd)
			require.Len(t, vs, 1, "%v", vs)
			assert.Equal(t, pxf.ViolationRequiredOption, vs[0].Kind)
			assert.Equal(t, "a.proto", vs[0].File)
			assert.Equal(t, "pair.v1.M.f", vs[0].Element)
			assert.Contains(t, vs[0].String(), exclusive)
		})
	}
}

// (pxf.required) = false demands nothing, so the default beside it
// stands, and each option alone is the ordinary case.
func TestValidateFile_AcceptsWhatIsNotThePair(t *testing.T) {
	for name, opts := range map[string][][]byte{
		"required false beside default": {upstreamtest.Required(false), upstreamtest.Default("x")},
		"default alone":                 {upstreamtest.Default("x")},
		"required alone":                {upstreamtest.Required(true)},
		"@default alone":                {upstreamtest.AtDefault("x")},
		"@required alone":               {upstreamtest.AtRequired()},
	} {
		t.Run(name, func(t *testing.T) {
			fd := upstreamtest.File(t, "a.proto", msgs(upstreamtest.Message("M", opts...)))
			assert.Empty(t, upstream.ValidateFile(fd))
		})
	}
}

func TestValidateFile_ReachesNestedMessages(t *testing.T) {
	outer := upstreamtest.Message("Outer")
	outer.NestedType = msgs(upstreamtest.Message("Inner", upstreamtest.Required(true), upstreamtest.Default("x")))
	vs := upstream.ValidateFile(upstreamtest.File(t, "a.proto", msgs(outer)))
	require.Len(t, vs, 1, "%v", vs)
	assert.Equal(t, "pair.v1.Outer.Inner.f", vs[0].Element)
}

// Same scope as every other bind-time check: the transitive imports,
// whether or not the bound file refers to the type, each file once.
func TestValidateFile_CoversTheImportClosureOnce(t *testing.T) {
	shared := upstreamtest.File(t, "shared.proto", msgs(upstreamtest.Message("Bad", upstreamtest.Required(true), upstreamtest.Default("x"))))
	left := upstreamtest.File(t, "left.proto", msgs(upstreamtest.Message("L")), shared)
	right := upstreamtest.File(t, "right.proto", msgs(upstreamtest.Message("R")), shared)
	root := upstreamtest.File(t, "root.proto", msgs(upstreamtest.Message("Root")), left, right)

	vs := upstream.ValidateFile(root)
	require.Len(t, vs, 1, "the diamond reports the shared file once: %v", vs)
	assert.Equal(t, "shared.proto", vs[0].File, "attributed to the declaring file")
}

// The pair lands among upstream's own violations, in upstream's order.
func TestValidateFile_KeepsFileThenElementOrder(t *testing.T) {
	pair := func(name string) *descriptorpb.DescriptorProto {
		return upstreamtest.Message(name, upstreamtest.Required(true), upstreamtest.Default("x"))
	}
	imported := upstreamtest.File(t, "b.proto", msgs(pair("Z")))
	root := upstreamtest.File(t, "a.proto", msgs(pair("Y"), pair("X")), imported)

	var got []string
	for _, v := range upstream.ValidateFile(root) {
		got = append(got, v.File+":"+v.Element)
	}
	assert.Equal(t, []string{"a.proto:pair.v1.X.f", "a.proto:pair.v1.Y.f", "b.proto:pair.v1.Z.f"}, got)
}

func TestValidate_NilIsConformant(t *testing.T) {
	assert.Empty(t, upstream.ValidateFile(nil))
	assert.Empty(t, upstream.ValidateDescriptor(nil))
}
