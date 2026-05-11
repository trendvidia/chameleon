// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secret

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/awnumar/memguard"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OpenedBuffer is the return value of [Secret.Open]. It wraps the
// underlying [memguard.LockedBuffer] with observability — Destroy
// records the open-duration histogram and decrements the active gauge
// before destroying the underlying buffer.
//
// Behavior on top of the wrapped buffer:
//   - [OpenedBuffer.Bytes] delegates to the underlying LockedBuffer.
//   - [OpenedBuffer.Locked] returns the underlying *memguard.LockedBuffer
//     for callers that need to pass it to APIs that take the concrete
//     memguard type. Most callers should not need this.
//   - [OpenedBuffer.Destroy] is idempotent and records metrics on the
//     first call only.
//
// Like *memguard.LockedBuffer, OpenedBuffer is not safe for concurrent
// Destroy by multiple goroutines (the first Destroy wins atomically;
// subsequent calls are no-ops).
type OpenedBuffer struct {
	buf    *memguard.LockedBuffer
	closed int32 // atomic guard against double-record / double-destroy

	// Recording context — captured at Open time so Destroy can compute
	// duration + decrement the gauge.
	instruments *Instruments
	attrs       metric.MeasurementOption
	openedAt    time.Time
}

// Bytes returns the plaintext bytes of the underlying mlocked buffer.
// Same lifetime / safety rules as [memguard.LockedBuffer.Bytes]: the
// slice is invalidated by [OpenedBuffer.Destroy], so callers must not
// retain it past Destroy.
func (o *OpenedBuffer) Bytes() []byte {
	return o.buf.Bytes()
}

// Locked returns the underlying [memguard.LockedBuffer] for callers
// that need to pass it to APIs typed in terms of memguard. The
// returned buffer's lifetime is tied to this OpenedBuffer — destroying
// the OpenedBuffer destroys the underlying buffer. Most callers should
// use [OpenedBuffer.Bytes] instead.
func (o *OpenedBuffer) Locked() *memguard.LockedBuffer {
	return o.buf
}

// Destroy wipes the underlying mlocked buffer and records observability
// metrics. Idempotent — subsequent calls are no-ops. Safe to call from
// `defer`.
func (o *OpenedBuffer) Destroy() {
	if !atomic.CompareAndSwapInt32(&o.closed, 0, 1) {
		return
	}
	o.buf.Destroy()
	if o.instruments == nil {
		return
	}
	ctx := context.Background()
	o.instruments.active.Add(ctx, -1, o.attrs)
	o.instruments.openDuration.Record(ctx, time.Since(o.openedAt).Seconds(), o.attrs)
}

// pathAttr builds the standard attribute set used across the secret
// package's instruments. Stable so dashboards can group on it.
func pathAttr(path, env string) metric.MeasurementOption {
	attrs := []attribute.KeyValue{attribute.String("chameleon.path", path)}
	if env != "" {
		attrs = append(attrs, attribute.String("chameleon.env", env))
	}
	return metric.WithAttributes(attrs...)
}
