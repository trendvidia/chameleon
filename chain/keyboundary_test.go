// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/chain"
)

// keyBoundaryLayers is the README's recommended layout: a plaintext
// PER_ENV layer, an encrypted PER_ENV secrets layer, and an encrypted
// ONCE layer that must not be affected by the key boundary.
const keyBoundaryLayers = `
layers = [
  { id = "env"     path = "env/${env}.pxf"     for_each = PER_ENV }
  { id = "secrets" path = "secrets/${env}.pxf" for_each = PER_ENV encrypted = true }
  { id = "shared"  path = "shared.pxf"         for_each = ONCE    encrypted = true }
]
`

// pathsByEntry groups the expanded paths by LayerEntry id, in merge
// order, so a case can state what each entry contributes.
func pathsByEntry(refs []chain.LayerRef) map[string][]string {
	out := map[string][]string{}
	for _, r := range refs {
		out[r.EntryID] = append(out[r.EntryID], r.Path)
	}
	return out
}

// TestExpand_EncryptedPerEnvStopsAtKeyBoundary pins #236: an encrypted
// PER_ENV layer takes only the ancestors whose effective key_name is
// the leaf's, so a dev runtime holding only its own key never reaches
// a secrets file encrypted to prod's. Plaintext PER_ENV layers still
// walk the whole ancestry, and a chain with no key_name expands as it
// did before.
func TestExpand_EncryptedPerEnvStopsAtKeyBoundary(t *testing.T) {
	desc := compileChainSpec(t)

	cases := []struct {
		name         string
		environments string
		env          string
		wantEnv      []string
		wantSecrets  []string
	}{
		{
			name: "README keys: dev inherits sandbox-key, stops below prod",
			environments: `
  { name = "prod"    key_name = "prod-key" }
  { name = "sandbox" extends = "prod"    key_name = "sandbox-key" }
  { name = "dev"     extends = "sandbox" }`,
			env:         "dev",
			wantEnv:     []string{"env/prod.pxf", "env/sandbox.pxf", "env/dev.pxf"},
			wantSecrets: []string{"secrets/sandbox.pxf", "secrets/dev.pxf"},
		},
		{
			name: "README keys: sandbox loads only its own secrets",
			environments: `
  { name = "prod"    key_name = "prod-key" }
  { name = "sandbox" extends = "prod"    key_name = "sandbox-key" }
  { name = "dev"     extends = "sandbox" }`,
			env:         "sandbox",
			wantEnv:     []string{"env/prod.pxf", "env/sandbox.pxf"},
			wantSecrets: []string{"secrets/sandbox.pxf"},
		},
		{
			name: "README keys: prod is unaffected",
			environments: `
  { name = "prod"    key_name = "prod-key" }
  { name = "sandbox" extends = "prod"    key_name = "sandbox-key" }
  { name = "dev"     extends = "sandbox" }`,
			env:         "prod",
			wantEnv:     []string{"env/prod.pxf"},
			wantSecrets: []string{"secrets/prod.pxf"},
		},
		{
			name: "no key_name anywhere: no boundary, whole ancestry",
			environments: `
  { name = "prod" }
  { name = "sandbox" extends = "prod" }
  { name = "dev"     extends = "sandbox" }`,
			env:         "dev",
			wantEnv:     []string{"env/prod.pxf", "env/sandbox.pxf", "env/dev.pxf"},
			wantSecrets: []string{"secrets/prod.pxf", "secrets/sandbox.pxf", "secrets/dev.pxf"},
		},
		{
			name: "one inherited key: secrets inherit across the whole chain",
			environments: `
  { name = "prod"    key_name = "shared-key" }
  { name = "sandbox" extends = "prod" }
  { name = "dev"     extends = "sandbox" }`,
			env:         "dev",
			wantEnv:     []string{"env/prod.pxf", "env/sandbox.pxf", "env/dev.pxf"},
			wantSecrets: []string{"secrets/prod.pxf", "secrets/sandbox.pxf", "secrets/dev.pxf"},
		},
		{
			name: "root without key_name below a keyed env is a boundary too",
			environments: `
  { name = "prod" }
  { name = "sandbox" extends = "prod"    key_name = "sandbox-key" }
  { name = "dev"     extends = "sandbox" }`,
			env:         "dev",
			wantEnv:     []string{"env/prod.pxf", "env/sandbox.pxf", "env/dev.pxf"},
			wantSecrets: []string{"secrets/sandbox.pxf", "secrets/dev.pxf"},
		},
		{
			name: "leaf overrides an inherited key: only its own secrets",
			environments: `
  { name = "prod"    key_name = "k1" }
  { name = "sandbox" extends = "prod" }
  { name = "dev"     extends = "sandbox" key_name = "k2" }`,
			env:         "dev",
			wantEnv:     []string{"env/prod.pxf", "env/sandbox.pxf", "env/dev.pxf"},
			wantSecrets: []string{"secrets/dev.pxf"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := parseSpec(t, desc, fmt.Sprintf("version = \"1.0.0\"\nenvironments = [%s\n]\n%s", tc.environments, keyBoundaryLayers))
			refs, err := chain.Expand(spec, tc.env, nil)
			require.NoError(t, err)
			got := pathsByEntry(refs)
			assert.Equal(t, tc.wantEnv, got["env"], "plaintext PER_ENV walks the whole ancestry")
			assert.Equal(t, tc.wantSecrets, got["secrets"], "encrypted PER_ENV stops at the key boundary")
			assert.Equal(t, []string{"shared.pxf"}, got["shared"], "ONCE is untouched by the boundary")
			for _, r := range refs {
				if r.EntryID == "secrets" {
					assert.True(t, r.Encrypted, "%s keeps its encrypted flag", r.Path)
				}
			}
		})
	}
}
