// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secret

import "sync"

// Map is a path-keyed collection of resolved secrets, produced by the
// chameleon parse pass (chameleon/parse.Move). Path format mirrors the
// resolver's Trace:
//
//	"db_password"                          // top-level
//	"primary.auth_token"                   // nested message
//	"replicas[0].auth_token"               // repeated message
//	"tenant_keys[\"acme\"]"                // map<string, pxf.Secret>
//	"port_keys[8080]"                      // map<int32, pxf.Secret>
//
// Map is safe for concurrent reads, writes, and Destroy. The typical
// lifetime is one resolution: build via [parse.Move], hand to the
// consumer alongside the typed config, Destroy on shutdown. Always
// pass *Map by reference — copying the value loses the mutex
// invariant.
type Map struct {
	mu        sync.Mutex
	entries   map[string]*Secret
	destroyed bool

	// Observability — when non-nil, each Set() wires the Instruments
	// onto the inserted Secret so its Open()/Destroy() record per-path
	// OTel metrics. env is attached as a stable attribute.
	instruments *Instruments
	env         string
}

// MapOption configures a Map at construction time.
type MapOption func(*Map)

// WithInstruments enables per-path observability on every Secret
// inserted into the Map. The env string is attached as the
// `chameleon.env` attribute on each recording; pass "" to suppress it.
// Pass nil instruments (or no [WithInstruments] option) for the
// noop-meter default.
func WithInstruments(i *Instruments, env string) MapOption {
	return func(m *Map) {
		m.instruments = i
		m.env = env
	}
}

// NewMap returns an empty Map ready for population. Used by
// [parse.Move]; consumers normally don't construct one directly.
//
// Options:
//   - [WithInstruments] — wires per-path OTel metrics on every
//     Open/Destroy of secrets inserted into this Map.
func NewMap(opts ...MapOption) *Map {
	m := &Map{entries: make(map[string]*Secret)}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Get returns the Secret stored at path, or nil if no secret was put
// there. Returns nil after Destroy. Nil-safe receiver.
func (m *Map) Get(path string) *Secret {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[path]
}

// Has reports whether the Map has a Secret at path. Distinct from
// `Get(path) != nil` for the unlikely case where a nil *Secret was
// inserted explicitly. Nil-safe receiver.
func (m *Map) Has(path string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[path]
	return ok
}

// Len returns the number of secrets currently held. Nil-safe receiver.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// Set inserts a Secret at path. If a Secret already exists at that
// path, the previous one is Destroy'd before being overwritten —
// otherwise overwriting would leak the prior memguard Enclave.
//
// Used internally by [parse.Move]. Consumers normally don't call Set
// directly; the Map is built once and then read.
func (m *Map) Set(path string, s *Secret) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.destroyed {
		// Map already wiped — just destroy the incoming Secret too so
		// the caller can't accidentally retain plaintext past Destroy.
		if s != nil {
			s.Destroy()
		}
		return
	}
	if old, ok := m.entries[path]; ok && old != nil {
		old.Destroy()
	}
	if s != nil && m.instruments != nil {
		s.instruments = m.instruments
		s.path = path
		s.env = m.env
	}
	m.entries[path] = s
}

// Range iterates entries in arbitrary order. The callback returns
// false to stop early. Range snapshots the entries inside the lock and
// calls the callback unlocked, so the callback may safely call back
// into Map methods (no deadlock risk). However: a Set or Destroy
// happening concurrently with Range may not be observed by the
// snapshot — Range reflects the state at its start.
func (m *Map) Range(fn func(path string, s *Secret) bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	type entry struct {
		k string
		v *Secret
	}
	snap := make([]entry, 0, len(m.entries))
	for k, v := range m.entries {
		snap = append(snap, entry{k, v})
	}
	m.mu.Unlock()
	for _, e := range snap {
		if !fn(e.k, e.v) {
			return
		}
	}
}

// Destroy wipes every Secret in the Map and clears the entries.
// Idempotent and safe to call from any goroutine. After Destroy:
//   - Get returns nil for every path.
//   - Has returns false.
//   - Len returns 0.
//   - Set destroys its argument and discards (the Map is closed).
func (m *Map) Destroy() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.destroyed {
		return
	}
	m.destroyed = true
	for _, s := range m.entries {
		if s != nil {
			s.Destroy()
		}
	}
	m.entries = nil
}
