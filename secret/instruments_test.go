// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secret_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/trendvidia/chameleon/secret"
)

func newReader(t *testing.T) (*metric.MeterProvider, *metric.ManualReader) {
	t.Helper()
	r := metric.NewManualReader()
	return metric.NewMeterProvider(metric.WithReader(r)), r
}

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

// withInstruments builds a Map wired to a fresh ManualReader, returns
// the map + a snapshot helper for assertions.
func withInstruments(t *testing.T, env string) (*secret.Map, *metric.ManualReader) {
	t.Helper()
	mp, r := newReader(t)
	m := secret.NewMap(secret.WithInstruments(secret.NewInstruments(mp), env))
	return m, r
}

func TestOpen_IncrementsOpensCounter(t *testing.T) {
	m, r := withInstruments(t, "prod")
	m.Set("db_password", secret.FromString("hunter2", "", ""))

	buf, err := m.Get("db_password").Open()
	require.NoError(t, err)
	defer buf.Destroy()

	got := snapshot(t, r)
	counter, ok := got["chameleon.secret.opens"].Data.(metricdata.Sum[int64])
	require.True(t, ok, "chameleon.secret.opens should be an Int64 Sum")
	require.Len(t, counter.DataPoints, 1)
	assert.Equal(t, int64(1), counter.DataPoints[0].Value)

	attrs := counter.DataPoints[0].Attributes
	pathV, _ := attrs.Value("chameleon.path")
	envV, _ := attrs.Value("chameleon.env")
	assert.Equal(t, "db_password", pathV.AsString())
	assert.Equal(t, "prod", envV.AsString())
}

func TestOpenDestroy_BalancesActiveGauge(t *testing.T) {
	m, r := withInstruments(t, "")
	m.Set("api_key", secret.FromString("k", "", ""))
	s := m.Get("api_key")

	// Open three times concurrently (without destroying yet); the active
	// gauge should report 3.
	bufs := make([]*secret.OpenedBuffer, 3)
	for i := range bufs {
		var err error
		bufs[i], err = s.Open()
		require.NoError(t, err)
	}
	got := snapshot(t, r)
	active, ok := got["chameleon.secret.active"].Data.(metricdata.Sum[int64])
	require.True(t, ok, "chameleon.secret.active should be Int64 Sum (up-down counter)")
	require.Len(t, active.DataPoints, 1)
	assert.Equal(t, int64(3), active.DataPoints[0].Value, "three active opens")

	// Destroy two — gauge should drop to 1.
	bufs[0].Destroy()
	bufs[1].Destroy()
	got = snapshot(t, r)
	active = got["chameleon.secret.active"].Data.(metricdata.Sum[int64])
	assert.Equal(t, int64(1), active.DataPoints[0].Value, "one active after two destroys")

	// Destroy the third — gauge back to 0.
	bufs[2].Destroy()
	got = snapshot(t, r)
	active = got["chameleon.secret.active"].Data.(metricdata.Sum[int64])
	assert.Equal(t, int64(0), active.DataPoints[0].Value, "back to zero")
}

func TestDestroy_Idempotent_DoesNotDoubleDecrement(t *testing.T) {
	m, r := withInstruments(t, "")
	m.Set("api_key", secret.FromString("k", "", ""))
	buf, err := m.Get("api_key").Open()
	require.NoError(t, err)

	buf.Destroy()
	buf.Destroy() // second Destroy must be a no-op for the gauge
	buf.Destroy() // third too

	got := snapshot(t, r)
	active := got["chameleon.secret.active"].Data.(metricdata.Sum[int64])
	assert.Equal(t, int64(0), active.DataPoints[0].Value, "active gauge must not go negative")
}

func TestOpenDestroy_RecordsDuration(t *testing.T) {
	m, r := withInstruments(t, "")
	m.Set("api_key", secret.FromString("k", "", ""))
	buf, err := m.Get("api_key").Open()
	require.NoError(t, err)
	// 50ms (not 5ms) leaves comfortable slack on overloaded CI runners
	// where scheduler jitter or histogram bucket rounding can otherwise
	// flake the dp.Sum >= 0.005 assertion below. The 45ms cost is paid
	// once per test run.
	time.Sleep(50 * time.Millisecond)
	buf.Destroy()

	got := snapshot(t, r)
	hist, ok := got["chameleon.secret.open.duration"].Data.(metricdata.Histogram[float64])
	require.True(t, ok, "chameleon.secret.open.duration should be Float64 histogram")
	require.Len(t, hist.DataPoints, 1)
	dp := hist.DataPoints[0]
	assert.Equal(t, uint64(1), dp.Count, "one observation recorded")
	assert.GreaterOrEqual(t, dp.Sum, 0.005, "observed duration ≥ 5ms")
}

func TestOpen_WithoutInstruments_NoOpAndNoPanic(t *testing.T) {
	// Map constructed WITHOUT WithInstruments — Open should work and
	// not panic, just no metrics recorded.
	m := secret.NewMap()
	m.Set("api_key", secret.FromString("k", "", ""))
	buf, err := m.Get("api_key").Open()
	require.NoError(t, err)
	buf.Destroy()
}
