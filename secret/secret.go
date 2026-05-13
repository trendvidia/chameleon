// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package secret is chameleon's runtime home for [Secret], the in-
// memory representation of a `pxf.Secret` well-known type field.
// Generated config types reference *Secret (via protoc-gen-chameleon-
// go) instead of a raw proto-generated message, so the plaintext never
// appears as a Go string on the public API.
//
// The canonical descriptor for `pxf.Secret` lives in the protowire
// spec (`proto/pxf/secret.proto`). The PXF codec in protowire-go
// recognizes that FullName and routes the inner `value` string; this
// package owns everything *after* parse: moving the bytes into a
// memguard Enclave, exposing Open()/Destroy(), and wiping on
// resolution shutdown.
//
// Memory model:
//   - On parse, the chameleon PXF extension routes value bytes from the
//     layer-level memguard.LockedBuffer directly into a per-field
//     memguard.Enclave via NewBufferFromBytes (which copies + wipes the
//     source slice). No transient *string is ever created for the
//     plaintext.
//   - On read, [Secret.Open] returns a *memguard.LockedBuffer that the
//     caller MUST Destroy when done. The buffer is mlocked and
//     scrambled-on-destroy.
//   - On resolution destroy, [Secret.Destroy] zeroes the underlying
//     enclave; further Open calls return ErrDestroyed.
//
// Threat model: Secret protects the in-process memory representation
// against swap, core dumps, and process-memory inspection. Encryption at
// rest is handled by sops at the layer-file level.
package secret

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/awnumar/memguard"
)

// ErrDestroyed is returned by [Secret.Open] after the secret has been
// destroyed (typically by Resolution.Destroy).
var ErrDestroyed = errors.New("secret: destroyed")

// ErrUnset is returned by [Secret.Open] when the secret was never
// populated (e.g. an absent optional field). Callers should branch on
// [Secret.IsSet] before opening to distinguish "unset" from "destroyed".
var ErrUnset = errors.New("secret: unset")

// Secret is a memguard-backed sensitive value. The zero value is an
// unset Secret; populate it via [FromLockedBytes] (used by the chameleon
// PXF decoder) or [FromString] (used by tests and one-off construction).
//
// Open and Destroy are goroutine-safe: an [sync.RWMutex] serializes
// access to the enclave/destroyed state so concurrent Opens parallelize
// and a concurrent Destroy never races the enclave dereference.
// Mutating metadata via [Secret.SetMetadata] remains constructor-only
// — see that method's doc for the rationale.
type Secret struct {
	// mu guards enclave and destroyed. Hint/fingerprint stay outside
	// the lock under the constructor-only SetMetadata contract.
	mu sync.RWMutex

	// enclave holds the encrypted plaintext. nil when unset or destroyed.
	enclave *memguard.Enclave

	// destroyed is set after Destroy; distinguishes "never set" from
	// "wiped" so Open can return a meaningful error.
	destroyed bool

	hint        string
	fingerprint string

	// Observability fields, populated by Map.Set when the Map was
	// constructed with [WithInstruments]. nil instruments → recordings
	// become no-op. path + env are used as attribute values.
	instruments *Instruments
	path        string
	env         string
}

// FromLockedBytes constructs a Secret by sealing the supplied
// LockedBuffer into an Enclave. The input buffer is consumed (destroyed)
// by this call — the caller must not access it afterwards.
//
// This is the canonical entry point used by the chameleon PXF decoder:
// it slices the value out of the layer-level locked region into a fresh
// LockedBuffer, then hands ownership here.
func FromLockedBytes(buf *memguard.LockedBuffer, hint, fingerprint string) *Secret {
	return &Secret{
		enclave:     buf.Seal(), // Seal destroys buf and returns a new Enclave.
		hint:        hint,
		fingerprint: fingerprint,
	}
}

// FromString constructs a Secret from a Go string. NewBufferFromBytes
// copies the bytes into mlocked memory and wipes the transient
// []byte, but the input string itself is immutable and lives on the
// regular Go heap until GC reclaims it. The chameleon resolver uses
// FromString from its OnSecretField hook closure, accepting this
// transient heap window because the alternative — passing a
// byte-slice through the protowire-go hook — requires deeper codec
// changes that haven't shipped. Tests and programmatic construction
// share the same trade-off.
//
// Callers building Secrets from already-mlocked bytes should reach
// for [FromLockedBytes] instead — that path is the only one that
// never touches the regular Go heap.
func FromString(s, hint, fingerprint string) *Secret {
	buf := memguard.NewBufferFromBytes([]byte(s))
	return FromLockedBytes(buf, hint, fingerprint)
}

// Open returns an [OpenedBuffer] holding the plaintext. The caller MUST
// Destroy the returned buffer when finished:
//
//	buf, err := s.Open()
//	if err != nil { ... }
//	defer buf.Destroy()
//	use(buf.Bytes())
//
// Callers should keep the open window narrow — the buffer is mlocked
// but represents a moment of plaintext exposure.
//
// When this Secret was inserted into a [Map] constructed with
// [WithInstruments], Open records three OTel metrics: chameleon.secret.opens
// (counter), chameleon.secret.active (up-down counter, incremented here
// and decremented by [OpenedBuffer.Destroy]), and chameleon.secret.open.duration
// (histogram, recorded by Destroy). When the Secret has no Instruments
// attached, the recordings are no-op.
func (s *Secret) Open() (*OpenedBuffer, error) {
	s.mu.RLock()
	destroyed := s.destroyed
	enclave := s.enclave
	s.mu.RUnlock()
	if destroyed {
		return nil, ErrDestroyed
	}
	if enclave == nil {
		return nil, ErrUnset
	}
	// memguard.Enclave.Open runs outside the lock — it allocates a
	// fresh LockedBuffer and is CPU-bound; serializing it would defeat
	// the point of using an RWMutex here.
	buf, err := enclave.Open()
	if err != nil {
		return nil, err
	}
	o := &OpenedBuffer{buf: buf, openedAt: time.Now()}
	if s.instruments != nil {
		o.instruments = s.instruments
		o.attrs = pathAttr(s.path, s.env)
		ctx := context.Background()
		s.instruments.opens.Add(ctx, 1, o.attrs)
		s.instruments.active.Add(ctx, 1, o.attrs)
	}
	return o, nil
}

// Destroy wipes the underlying enclave. Subsequent Open calls return
// [ErrDestroyed]. Idempotent. Safe to call concurrently with Open.
func (s *Secret) Destroy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return
	}
	s.destroyed = true
	s.enclave = nil // GC reclaims the encrypted ciphertext; key material is wiped by memguard finalizer.
}

// IsSet reports whether the secret holds a value (i.e. populated and
// not yet destroyed).
func (s *Secret) IsSet() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.destroyed && s.enclave != nil
}

// Hint returns the operator-facing description of what this secret is
// (e.g. "Postgres primary"). Never the value itself.
func (s *Secret) Hint() string { return s.hint }

// Fingerprint returns the optional non-reversible identifier set at
// authoring time (SHA-256 prefix, JWK thumbprint, etc.). Empty if the
// authoring file did not provide one.
func (s *Secret) Fingerprint() string { return s.fingerprint }

// SetMetadata updates hint and fingerprint on an existing Secret.
// Used by [parse.MoveInto] to enrich a hook-routed Map entry with
// the diagnostic metadata that block-form `pw { value hint
// fingerprint }` carries — the protowire-go OnSecretField hook
// (since v0.77.0) routes value through the hook but assigns hint
// and fingerprint to the proto message normally, so the resolver
// folds them back onto the Secret after decode.
//
// Semantics:
//
//   - Both arguments overwrite unconditionally. A caller that wants
//     "skip when both empty" semantics must implement that itself
//     (see parse.MoveInto.takeSecret for the canonical pattern).
//
//   - SetMetadata is goroutine-safe ONLY while the Secret is being
//     constructed by parse.MoveInto, before the containing [Map]
//     escapes to consumer code. Once the Map is returned from
//     [parse.MoveInto] or [parse.Move] (i.e. once parse.Move's
//     walker has finished), no further SetMetadata calls are safe
//     because consumer goroutines may now hold the *Secret and
//     race on the hint/fingerprint field reads. This is the same
//     window as the rest of [Secret]'s "NOT goroutine-safe for
//     mutation" invariant; calling SetMetadata after Map escape
//     would race with consumer Hint() / Fingerprint() reads with
//     no synchronization.
//
//   - Hint and fingerprint are plain Go strings (not memguard-
//     backed); the bytes live on the regular GC heap. Calling
//     SetMetadata does not allocate mlocked memory.
func (s *Secret) SetMetadata(hint, fingerprint string) {
	if s == nil {
		return
	}
	s.hint = hint
	s.fingerprint = fingerprint
}
