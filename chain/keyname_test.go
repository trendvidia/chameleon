// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/chain"
)

func TestEffectiveKeyName_LeafSetsKey(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [
  { name = "prod" key_name = "prod-key" }
]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	got, err := chain.EffectiveKeyName(spec, "prod")
	require.NoError(t, err)
	assert.Equal(t, "prod-key", got)
}

func TestEffectiveKeyName_InheritsFromParent(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [
  { name = "prod"    key_name = "prod-key" }
  { name = "sandbox" extends  = "prod" }
  { name = "uat"     extends  = "sandbox" }
]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	got, err := chain.EffectiveKeyName(spec, "uat")
	require.NoError(t, err)
	assert.Equal(t, "prod-key", got, "uat with no key_name should inherit nearest ancestor's")
}

func TestEffectiveKeyName_LeafOverridesAncestor(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [
  { name = "prod" key_name = "prod-key" }
  { name = "uat"  extends  = "prod" key_name = "uat-key" }
]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	got, err := chain.EffectiveKeyName(spec, "uat")
	require.NoError(t, err)
	assert.Equal(t, "uat-key", got)
}

func TestEffectiveKeyName_NoneSetReturnsEmpty(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [
  { name = "prod" }
  { name = "uat" extends = "prod" }
]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	got, err := chain.EffectiveKeyName(spec, "uat")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestEffectiveKeyName_UnknownEnvironmentErrors(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	_, err := chain.EffectiveKeyName(spec, "nope")
	require.Error(t, err)
}

func TestEffectiveKeyName_EmptyEnvUsesDefault(t *testing.T) {
	desc := compileChainSpec(t)
	spec := parseSpec(t, desc, `
version = "1.0.0"
default_environment = "dev"
environments = [{ name = "dev" key_name = "dev-key" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	got, err := chain.EffectiveKeyName(spec, "")
	require.NoError(t, err)
	assert.Equal(t, "dev-key", got)
}
