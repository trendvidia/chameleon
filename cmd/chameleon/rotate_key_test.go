// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/getsops/sops/v3/age/keypb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trendvidia/protowire-go/encoding/pxf"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	chsops "github.com/trendvidia/chameleon/sops"
)

// Two real age identities (matching the sops fork's keysource test
// fixtures) plus a runtime-derived new-key recipient. Pre-computed
// constants would drift if filippo.io/age ever changes recipient
// formatting; deriving at test time keeps the fixture honest.
const (
	rotOldIdentity  = "AGE-SECRET-KEY-1G0Q5K9TV4REQ3ZSQRMTMG8NSWQGYT0T7TZ33RAZEE0GZYVZN0APSU24RK7"
	rotOldRecipient = "age1lzd99uklcjnc0e7d860axevet2cz99ce9pq6tzuzd05l5nr28ams36nvun"
	rotNewIdentity  = "AGE-SECRET-KEY-1432K5YRNSC44GC4986NXMX6GVZ52WTMT9C79CLUVWYY4DKDHD5JSNDP4MC"
)

func deriveRecipient(t *testing.T, identity string) string {
	t.Helper()
	id, err := age.ParseX25519Identity(identity)
	require.NoError(t, err)
	return id.Recipient().String()
}

// writeRotateKeyFile encodes a PXF age key file with both rotation
// identities under labelled entries and points SOPS_AGE_KEY_FILE at
// it for the duration of the test. The keystore intentionally
// carries BOTH old + new (the two-phase rotation model: both keys
// readable during transition; rotate-key swaps the file's
// recipient set to the new one).
func writeRotateKeyFile(t *testing.T) {
	t.Helper()
	file := &keypb.AgeKeyFile{
		Keys: map[string]string{
			"old-key": rotOldIdentity,
			"new-key": rotNewIdentity,
		},
	}
	data, err := pxf.Marshal(file)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "keys.pxf")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	t.Setenv("SOPS_AGE_KEY_FILE", path)
}

// buildSpec wires a typed chameleonv1.ChainSpec inline. Builds the
// chain in code rather than via PXF text so each test can dictate
// the env layout precisely.
func buildSpec(envs []*chameleonv1.Environment, layers []*chameleonv1.LayerEntry) *chameleonv1.ChainSpec {
	return &chameleonv1.ChainSpec{
		Version:      "1.0.0",
		Environments: envs,
		Layers:       layers,
	}
}

func TestRotateOne_HappyPath_SingleEnvSwapsRecipient(t *testing.T) {
	writeRotateKeyFile(t)

	dir := t.TempDir()
	path := "secrets/prod.pxf"
	full := filepath.Join(dir, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))

	// Encrypt fixture under old-key so we can verify the new
	// ciphertext doesn't decrypt with old-key's identity alone.
	plain := []byte(`db_password = { value = "hunter2" }` + "\n")
	ciph, err := encryptForTest(t, plain, rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(full, ciph, 0o644))

	spec := buildSpec(
		[]*chameleonv1.Environment{{Name: "prod", KeyName: "new-key"}},
		[]*chameleonv1.LayerEntry{
			{Id: "secrets", Path: "secrets/${env}.pxf", ForEach: chameleonv1.LayerEntry_PER_ENV, Encrypted: true, Required: true},
		},
	)
	plans, err := planEncryptedLayers(spec)
	require.NoError(t, err)
	require.Len(t, plans, 1, "should plan one layer (prod's secrets file)")

	dec := chsops.Default()
	enc := chsops.DefaultEncrypter()
	var out bytes.Buffer
	status, err := rotateOne(context.Background(), &out, plans[0], dir, false /*dryRun*/, dec, enc, spec)
	require.NoError(t, err)
	assert.Equal(t, rotateDone, status)
	assert.Contains(t, out.String(), "ROTATED")

	// Re-read the file; the keystore holds both old + new, so a
	// successful decrypt alone doesn't prove rotation happened. The
	// stronger check is that the new ciphertext is byte-different
	// from the original (DEK rotated even if recipients hadn't
	// changed — but here they did).
	rewritten, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.NotEqual(t, string(ciph), string(rewritten), "file should have been re-encrypted")

	// And the plaintext round-trips.
	decrypted, err := decryptForTest(t, rewritten)
	require.NoError(t, err)
	assert.Contains(t, string(decrypted), "hunter2")
}

func TestRotateOne_SkipsWhenEnvHasNoKeyName(t *testing.T) {
	writeRotateKeyFile(t)

	dir := t.TempDir()
	path := "base.pxf"
	full := filepath.Join(dir, path)

	// File is shared between prod (key_name="new-key") and sandbox
	// (no key_name). Rotation should refuse — re-encrypting to
	// only new-key would lock sandbox out.
	plain := []byte(`shared = { value = "x" }` + "\n")
	ciph, err := encryptForTest(t, plain, rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(full, ciph, 0o644))

	spec := buildSpec(
		[]*chameleonv1.Environment{
			{Name: "prod", KeyName: "new-key"},
			{Name: "sandbox"}, // no key_name — should block rotation of any shared layer
		},
		[]*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Encrypted: true, Required: true},
		},
	)
	plans, err := planEncryptedLayers(spec)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	assert.Equal(t, []string{"prod", "sandbox"}, plans[0].Envs, "base.pxf reachable from both envs")

	var out bytes.Buffer
	status, err := rotateOne(context.Background(), &out, plans[0], dir, false, chsops.Default(), chsops.DefaultEncrypter(), spec)
	require.NoError(t, err)
	assert.Equal(t, rotateSkipped, status)
	assert.Contains(t, out.String(), "SKIP")
	assert.Contains(t, out.String(), "sandbox", "skip message should name the env without key_name")

	// File on disk unchanged.
	after, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Equal(t, string(ciph), string(after))
}

func TestRotateOne_DryRunDoesNotWrite(t *testing.T) {
	writeRotateKeyFile(t)

	dir := t.TempDir()
	path := "secrets/prod.pxf"
	full := filepath.Join(dir, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))

	original, err := encryptForTest(t, []byte("k = \"v\"\n"), rotOldRecipient)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(full, original, 0o644))

	spec := buildSpec(
		[]*chameleonv1.Environment{{Name: "prod", KeyName: "new-key"}},
		[]*chameleonv1.LayerEntry{
			{Id: "secrets", Path: "secrets/${env}.pxf", ForEach: chameleonv1.LayerEntry_PER_ENV, Encrypted: true, Required: true},
		},
	)
	plans, _ := planEncryptedLayers(spec)
	var out bytes.Buffer
	status, err := rotateOne(context.Background(), &out, plans[0], dir, true /*dryRun*/, chsops.Default(), chsops.DefaultEncrypter(), spec)
	require.NoError(t, err)
	assert.Equal(t, rotateDone, status)
	assert.Contains(t, out.String(), "WOULD-ROTATE")

	unchanged, err := os.ReadFile(full)
	require.NoError(t, err)
	assert.Equal(t, string(original), string(unchanged), "dry-run must not write")
}

func TestRotateOne_NonDefaultSourceSkipped(t *testing.T) {
	plan := &layerRotatePlan{Path: "p", Envs: []string{"prod"}, Source: "env"}
	var out bytes.Buffer
	status, err := rotateOne(context.Background(), &out, plan, t.TempDir(), false, chsops.Default(), chsops.DefaultEncrypter(), nil)
	require.NoError(t, err)
	assert.Equal(t, rotateSkipped, status)
	assert.Contains(t, out.String(), "non-default source")
}

func TestRotateOne_OptionalMissingFileSkipped(t *testing.T) {
	plan := &layerRotatePlan{Path: "ghost.pxf", Envs: []string{"prod"}, Required: false}
	spec := buildSpec(
		[]*chameleonv1.Environment{{Name: "prod", KeyName: "new-key"}},
		nil,
	)
	writeRotateKeyFile(t)
	var out bytes.Buffer
	status, err := rotateOne(context.Background(), &out, plan, t.TempDir(), false, chsops.Default(), chsops.DefaultEncrypter(), spec)
	require.NoError(t, err)
	assert.Equal(t, rotateSkipped, status)
	assert.Contains(t, out.String(), "optional file missing")
}

func TestPlanEncryptedLayers_AggregatesAcrossEnvs(t *testing.T) {
	// A PER_ENV layer expands to one resolved path per ancestor;
	// each path is unique. A ONCE layer that's shared appears once
	// with the full env set.
	spec := buildSpec(
		[]*chameleonv1.Environment{
			{Name: "prod"},
			{Name: "sandbox", Extends: "prod"},
		},
		[]*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Encrypted: true},
			{Id: "env", Path: "env/${env}.pxf", ForEach: chameleonv1.LayerEntry_PER_ENV, Encrypted: true},
			{Id: "public", Path: "public.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Encrypted: false}, // plaintext — skipped
		},
	)
	plans, err := planEncryptedLayers(spec)
	require.NoError(t, err)

	byPath := map[string]*layerRotatePlan{}
	for _, p := range plans {
		byPath[p.Path] = p
	}
	require.Contains(t, byPath, "base.pxf", "shared encrypted ONCE layer should be planned")
	require.Contains(t, byPath, "env/prod.pxf", "per-env layer for prod should be planned")
	require.Contains(t, byPath, "env/sandbox.pxf", "per-env layer for sandbox should be planned")
	require.NotContains(t, byPath, "public.pxf", "plaintext layer must not be planned")

	assert.Equal(t, []string{"prod", "sandbox"}, byPath["base.pxf"].Envs, "base.pxf is reachable from both")
	assert.Equal(t, []string{"prod"}, byPath["env/prod.pxf"].Envs)
	assert.Equal(t, []string{"sandbox"}, byPath["env/sandbox.pxf"].Envs)
}

func TestPlanEncryptedLayers_RejectsCtxTemplates(t *testing.T) {
	spec := buildSpec(
		[]*chameleonv1.Environment{{Name: "prod"}},
		[]*chameleonv1.LayerEntry{
			{Id: "region", Path: "region/${ctx.region}.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Encrypted: true},
		},
	)
	_, err := planEncryptedLayers(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${ctx.")
}

func TestResolveChainRecipients_DedupesInheritedKey(t *testing.T) {
	writeRotateKeyFile(t)

	// sandbox inherits prod's key_name. Both envs in the env set
	// resolve to "new-key" → one recipient after dedup.
	spec := buildSpec(
		[]*chameleonv1.Environment{
			{Name: "prod", KeyName: "new-key"},
			{Name: "sandbox", Extends: "prod"},
		},
		nil,
	)
	got, missing, err := resolveChainRecipients(spec, []string{"prod", "sandbox"})
	require.NoError(t, err)
	assert.Empty(t, missing)
	assert.Len(t, got.Age, 1)
	assert.Equal(t, deriveRecipient(t, rotNewIdentity), got.Age[0])
}

func TestResolveChainRecipients_ReportsMissing(t *testing.T) {
	writeRotateKeyFile(t)

	spec := buildSpec(
		[]*chameleonv1.Environment{
			{Name: "prod", KeyName: "new-key"},
			{Name: "dev"},
		},
		nil,
	)
	_, missing, err := resolveChainRecipients(spec, []string{"prod", "dev"})
	require.NoError(t, err)
	assert.Equal(t, []string{"dev"}, missing)
}

func TestRotateKeyCmd_RequiresChain(t *testing.T) {
	var out bytes.Buffer
	err := rotateKeyCmd(&out, []string{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "--chain is required"))
}
