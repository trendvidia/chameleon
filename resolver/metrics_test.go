// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package resolver_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
)

// newMeterReader returns a MeterProvider backed by an in-memory
// reader so the test can inspect what metrics chameleon recorded.
func newMeterReader(t *testing.T) (*metric.MeterProvider, *metric.ManualReader) {
	t.Helper()
	r := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(r))
	return mp, r
}

// snapshot collects + reads all metrics. Returns flattened (name → metric).
func snapshot(t *testing.T, r *metric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, r.Collect(context.Background(), &rm))
	out := make(map[string]metricdata.Metrics)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func TestMetrics_RecordedOnSuccessfulResolve(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [
  { id = "base" path = "base.pxf" for_each = ONCE required = true }
]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`
base_url = "x"
db_password = "shh"
`)},
	}

	mp, reader := newMeterReader(t)
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithMeterProvider(mp),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	got := snapshot(t, reader)
	assert.Contains(t, got, "chameleon.resolve.duration")
	assert.Contains(t, got, "chameleon.resolve.layers")
	assert.Contains(t, got, "chameleon.resolve.secrets")
	// errors not recorded on success path.
	assert.NotContains(t, got, "chameleon.resolve.errors")
}

func TestMetrics_ErrorCounterFiresOnFailure(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	// Empty fs — required layer missing → Resolve errors.
	fs := fstest.MapFS{}

	mp, reader := newMeterReader(t)
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithMeterProvider(mp),
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, resolver.ErrRequiredLayerMissing),
		"empty fs + required layer should surface ErrRequiredLayerMissing; got %v", err)

	got := snapshot(t, reader)
	require.Contains(t, got, "chameleon.resolve.errors")
	errCounter, ok := got["chameleon.resolve.errors"].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, errCounter.DataPoints, 1)
	assert.EqualValues(t, 1, errCounter.DataPoints[0].Value)
	// Duration also recorded even on failure.
	assert.Contains(t, got, "chameleon.resolve.duration")
}

func TestMetrics_NoProviderIsNoOp(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	// No WithMeterProvider; resolver must build noop instruments and
	// the Resolve call must succeed without panicking.
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()
}
