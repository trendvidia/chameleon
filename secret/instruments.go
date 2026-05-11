// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secret

import (
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// Instruments holds the OTel meters chameleon's secret package records
// on each Open()/Destroy(). Created once per resolver, attached to each
// secret via [Map] / [Map.Set]. A zero / nil Instruments is a no-op —
// every recording becomes a cheap noop call, no allocation overhead.
//
// Three instruments cover the three production-debugging questions:
//
//   - "How often is this secret being opened?"
//     → chameleon.secret.opens (counter; attributes: chameleon.path,
//     chameleon.env)
//
//   - "Is any goroutine holding this secret open right now?"
//     → chameleon.secret.active (up-down counter; attribute:
//     chameleon.path). Increment on Open, decrement on the returned
//     OpenedBuffer's Destroy.
//
//   - "How long is the typical Open → Destroy window?"
//     → chameleon.secret.open.duration (histogram, seconds; attribute:
//     chameleon.path). Recorded on Destroy.
//
// Path attribute uses the same dotted-path format as [Map] keys
// ("db_password", "primary.auth_token", `tenant_keys["acme"]`, etc.) —
// so a Prometheus query like
// `chameleon_secret_active{chameleon_path="db_password"} > 0` answers
// "is anything holding the db_password enclave open right now?".
type Instruments struct {
	opens        metric.Int64Counter
	active       metric.Int64UpDownCounter
	openDuration metric.Float64Histogram
}

// NewInstruments registers the three per-open instruments on mp. A nil
// provider produces a no-op Instruments (every record becomes a cheap
// noop call). Errors from instrument registration are swallowed — OTel
// returns sentinel no-op instruments on failure, and resolver setup is
// not the place to fail-stop on metric setup. Matches the convention in
// resolver/metrics.go.
func NewInstruments(mp metric.MeterProvider) *Instruments {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	m := mp.Meter("github.com/trendvidia/chameleon/secret")
	opens, _ := m.Int64Counter(
		"chameleon.secret.opens",
		metric.WithDescription("Number of Secret.Open() calls per dotted path. Useful for spotting hot read paths and unexpectedly frequent reopens."),
	)
	active, _ := m.Int64UpDownCounter(
		"chameleon.secret.active",
		metric.WithDescription("Number of OpenedBuffers currently outstanding per dotted path (incremented on Open, decremented on Destroy). Useful for spotting leaks — a goroutine that holds a secret open without destroying it."),
	)
	dur, _ := m.Float64Histogram(
		"chameleon.secret.open.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time between Secret.Open() and OpenedBuffer.Destroy(). Useful for spotting unusually long-held secrets."),
	)
	return &Instruments{
		opens:        opens,
		active:       active,
		openDuration: dur,
	}
}
