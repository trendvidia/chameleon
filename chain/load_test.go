// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/chameleon/chain"
)

// minimalSpec is the smallest valid input that exercises the
// descriptor cache without other parsing complexity.
const minimalSpec = `version = "1.0.0"
environments = [{ name = "prod" }]
layers = []
`

// TestParseSpec_ConcurrentFirstCallers fans out N goroutines into
// ParseSpec from a single test entry. Under -race, the prior
// unsynchronized cache (cachedDesc / cachedErr / cachedSet at
// chain/load.go pre-v0.10) would flag a write/read race on
// cachedSet or report (nil, nil) when a goroutine observed
// cachedSet=true before the writer's assignment to cachedDesc had
// been published. sync.OnceValues handles both: exactly one body
// runs, concurrent callers block on it, and the (desc, err) tuple
// publishes atomically.
//
// The test runs in a single process; the cache may already be warm
// from an earlier subtest. Even with a warm cache, -race verifies
// that OnceValues' published-atomic semantics hold under
// concurrent reads. The "cold start" half of the regression is
// implicit — if the new code reverts to unsynchronized package
// vars, this test goes red within a few `go test -race -count=10`
// runs because the first iteration of each `go test` invocation is
// cold.
func TestParseSpec_ConcurrentFirstCallers(t *testing.T) {
	const n = 64
	var (
		wg       sync.WaitGroup
		errCount atomic.Int32
		descs    = make([]protoreflect.MessageDescriptor, n)
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			spec, err := chain.ParseSpec([]byte(minimalSpec))
			if err != nil {
				errCount.Add(1)
				return
			}
			if spec == nil {
				errCount.Add(1)
				return
			}
			descs[i] = spec.ProtoReflect().Descriptor()
		}(i)
	}
	wg.Wait()

	require.Equal(t, int32(0), errCount.Load(), "no goroutine should observe an error or nil spec")

	// Every goroutine should observe the same descriptor instance
	// (the OnceValues cache means one compile, one descriptor).
	first := descs[0]
	require.NotNil(t, first, "first goroutine should have produced a descriptor")
	for i := 1; i < n; i++ {
		assert.Equal(t, first, descs[i],
			"goroutine %d produced a different descriptor instance — cache may have re-compiled", i)
	}
}

// TestParseSpec_DescriptorIdentityStable — two sequential ParseSpec
// calls share the cached descriptor. Catches accidental re-
// compilation on every call (a regression that would slip past
// TestParseSpec_ConcurrentFirstCallers because all goroutines
// would see the same WRONG behavior of "always recompile").
func TestParseSpec_DescriptorIdentityStable(t *testing.T) {
	s1, err := chain.ParseSpec([]byte(minimalSpec))
	require.NoError(t, err)
	s2, err := chain.ParseSpec([]byte(minimalSpec))
	require.NoError(t, err)
	assert.Equal(t, s1.ProtoReflect().Descriptor(), s2.ProtoReflect().Descriptor(),
		"chainSpecDescriptor should reuse the cached MessageDescriptor across ParseSpec calls")
}
