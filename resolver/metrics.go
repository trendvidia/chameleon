// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package resolver

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// instruments are the OTel meters chameleon's resolver records on
// each Resolve call. Created once per Resolver, used per-call. With
// no [WithMeterProvider] option the noop provider is used and every
// call is a cheap no-op.
type instruments struct {
	resolveDuration metric.Float64Histogram
	resolveErrors   metric.Int64Counter
	layerCount      metric.Int64Histogram
	secretCount     metric.Int64Histogram
}

const meterName = "github.com/trendvidia/chameleon/resolver"

// newInstruments registers the four metrics on the given provider.
// A nil provider is treated as noop (the default when WithMeterProvider
// isn't passed). Errors from instrument registration are swallowed —
// OTel returns sentinel no-op instruments on failure, and config-load
// is not the place to fail-stop on metric setup.
func newInstruments(mp metric.MeterProvider) *instruments {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	m := mp.Meter(meterName)

	rd, _ := m.Float64Histogram(
		"chameleon.resolve.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time taken by resolver.Resolve, including layer loads + decryption + merge + interpolation + validation + secret extraction."),
	)
	re, _ := m.Int64Counter(
		"chameleon.resolve.errors",
		metric.WithDescription("Number of resolver.Resolve calls that returned an error."),
	)
	lc, _ := m.Int64Histogram(
		"chameleon.resolve.layers",
		metric.WithDescription("Number of layer files materialized per Resolve (after chain expansion + optional skipping)."),
	)
	sc, _ := m.Int64Histogram(
		"chameleon.resolve.secrets",
		metric.WithDescription("Number of pxf.Secret values extracted into memguard Enclaves per Resolve."),
	)

	return &instruments{
		resolveDuration: rd,
		resolveErrors:   re,
		layerCount:      lc,
		secretCount:     sc,
	}
}

// envAttr is the standard attribute key used across the four meters.
// Stable so dashboards can group on it.
const envAttrKey = "chameleon.env"

func envAttr(env string) attribute.KeyValue {
	return attribute.String(envAttrKey, env)
}
