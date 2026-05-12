// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The codegen plugin emits a runtime key-formatting expression for
// every `map<K, pxf.Secret>` field — `strconv.Itoa(i)`,
// `strconv.Quote(k)`, etc. The pre-v0.10 test fixture exercised only
// `string` and `int32` key kinds; the remaining four kinds (int64,
// uint32, uint64, bool) had no coverage despite being permitted by
// the proto3 grammar. Drift in any of those formatters would
// silently produce wrong dotted paths at runtime, breaking
// `secret.Map.Get` lookups for the affected map.
//
// These tests assert the per-kind runtime expression appears in the
// generated function-form accessor for each map field added to the
// fixture (see gen_test.go's `shard_keys`, `rate_buckets`,
// `large_ids`, `flag_keys` declarations). The wrapper-mode emit
// uses the same `mapKeyRuntimeExpr` helper, so these assertions
// double-cover both surfaces.

// TestMapKeyKind_Int64 — `map<int64, pxf.Secret>` should emit
// `strconv.FormatInt(v, 10)` for the key parameter, with no int64
// → int conversion (the param is already int64).
func TestMapKeyKind_Int64(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_ShardKeys(s *",
		"function-form accessor missing for map<int64, Secret>")
	assert.Contains(t, out, ", key int64)",
		"key parameter should be typed int64")
	assert.Contains(t, out, "strconv.FormatInt(key, 10)",
		"int64 key formatter should be FormatInt(v, 10) without conversion")
}

// TestMapKeyKind_Uint32 — `map<uint32, pxf.Secret>` should widen the
// key to uint64 for FormatUint.
func TestMapKeyKind_Uint32(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_RateBuckets(s *")
	assert.Contains(t, out, ", key uint32)")
	assert.Contains(t, out, "strconv.FormatUint(uint64(key), 10)",
		"uint32 key formatter should be FormatUint(uint64(v), 10)")
}

// TestMapKeyKind_Uint64 — `map<uint64, pxf.Secret>` should emit
// FormatUint directly with no conversion.
func TestMapKeyKind_Uint64(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_LargeIds(s *")
	assert.Contains(t, out, ", key uint64)")
	assert.Contains(t, out, "strconv.FormatUint(key, 10)",
		"uint64 key formatter should be FormatUint(v, 10) without conversion")
}

// TestMapKeyKind_Bool — `map<bool, pxf.Secret>` should emit
// FormatBool. Keys true / false render as `"true"` / `"false"` in
// the dotted path — matching internal/pathfmt.MapKey.
func TestMapKeyKind_Bool(t *testing.T) {
	out := runPlugin(t)
	assert.Contains(t, out, "func AppConfigSecret_FlagKeys(s *")
	assert.Contains(t, out, ", key bool)")
	assert.Contains(t, out, "strconv.FormatBool(key)",
		"bool key formatter should be FormatBool(v)")
}

// TestMapKeyKind_WrapperEmit — wrapper mode reuses the function-form
// path's `joinFragmentRefs` helper for `map<*, pxf.Secret>` flat
// methods. Verify each new map<*, Secret> field produces a
// corresponding wrapper method with the matching runtime formatter
// so the dotted path agrees byte-for-byte between function-form
// and wrapper-form surfaces.
func TestMapKeyKind_WrapperEmit(t *testing.T) {
	out := runPluginWithResolved(t)

	// int64
	assert.Contains(t, out, "func (r *AppConfigResolved) ShardKeys(key int64) *")
	assert.Contains(t, out, `"shard_keys["`)
	assert.Contains(t, out, "strconv.FormatInt(key, 10)")

	// uint32
	assert.Contains(t, out, "func (r *AppConfigResolved) RateBuckets(key uint32) *")
	assert.Contains(t, out, `"rate_buckets["`)
	assert.Contains(t, out, "strconv.FormatUint(uint64(key), 10)")

	// uint64
	assert.Contains(t, out, "func (r *AppConfigResolved) LargeIds(key uint64) *")
	assert.Contains(t, out, `"large_ids["`)
	assert.Contains(t, out, "strconv.FormatUint(key, 10)")

	// bool
	assert.Contains(t, out, "func (r *AppConfigResolved) FlagKeys(key bool) *")
	assert.Contains(t, out, `"flag_keys["`)
	assert.Contains(t, out, "strconv.FormatBool(key)")
}

// TestMapKeyKind_ChainHelperEmit — the SEPARATE wrapper chain-helper
// emit (`emitMapChainMethod` in gen_resolved.go) uses its own
// `mapKeyRuntimeExpr` helper for `map<K, UserMessage>` where the
// value type is wrapper-bearing. Same kind-dispatch logic as
// joinFragmentRefs, separate code path. The four `sites_by_*`
// fields in the fixture exist solely to cover this surface.
func TestMapKeyKind_ChainHelperEmit(t *testing.T) {
	out := runPluginWithResolved(t)

	// int64 — Get(key int64) on the chain helper.
	assert.Contains(t, out, "type AppConfigSitesBySeqResolved struct {")
	assert.Contains(t, out, "func (r *AppConfigSitesBySeqResolved) Get(key int64) *EndpointResolved")
	assert.Contains(t, out, `r.prefix + "sites_by_seq["`)
	assert.Contains(t, out, "strconv.FormatInt(key, 10)")

	// uint32 — Get(key uint32).
	assert.Contains(t, out, "type AppConfigSitesByPortResolved struct {")
	assert.Contains(t, out, "func (r *AppConfigSitesByPortResolved) Get(key uint32) *EndpointResolved")
	assert.Contains(t, out, "strconv.FormatUint(uint64(key), 10)")

	// uint64 — Get(key uint64).
	assert.Contains(t, out, "type AppConfigSitesByUidResolved struct {")
	assert.Contains(t, out, "func (r *AppConfigSitesByUidResolved) Get(key uint64) *EndpointResolved")
	assert.Contains(t, out, "strconv.FormatUint(key, 10)")

	// bool — Get(key bool).
	assert.Contains(t, out, "type AppConfigSitesByFlagResolved struct {")
	assert.Contains(t, out, "func (r *AppConfigSitesByFlagResolved) Get(key bool) *EndpointResolved")
	assert.Contains(t, out, "strconv.FormatBool(key)")
}
