// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package crypt

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// Backend is a named implementation pair: what a build links in when
// it imports an implementation package. At least one half must be
// non-nil.
//
// A decrypt-only backend (Encrypter nil) is the expected shape for a
// runtime that reads encrypted layers and never writes them; the
// resolver only ever asks for the Decrypter. An encrypt-only backend
// is legal too, and is skipped by [SoleDecrypter].
type Backend struct {
	// Decrypter is the read half, used by the resolver. Nil for an
	// encrypt-only backend.
	Decrypter Decrypter

	// Encrypter is the write half. Nil for a decrypt-only backend.
	//
	// Nothing in the registry selects on it yet: the bundler and the
	// CLI's re-encrypting subcommands construct their Encrypter
	// directly (sops.DefaultEncrypter), because the write side is a
	// build-time authoring tool that already links sops and has no
	// binary-size problem to solve. It is carried here so a backend
	// registers as one thing rather than two, and so [Registered] can
	// report a build's full crypto surface. Wiring the write side
	// through the registry — a SoleEncrypter to match [SoleDecrypter]
	// — is a separate change; see trendvidia/chameleon#130.
	Encrypter Encrypter
}

// ErrNoBackend is returned by [SoleDecrypter] when the build links no
// decryption backend at all. The resolver wraps it into its own
// ErrNoDecrypter, which is the error consumers match on.
var ErrNoBackend = errors.New("crypt: no decryption backend registered")

// ErrAmbiguousBackend is returned by [SoleDecrypter] when more than
// one decryption backend is registered and the caller did not name
// one. Auto-selection is deliberately only defined for the single-
// backend case: picking one of several by sort order would make the
// choice depend on a name, not on a decision.
var ErrAmbiguousBackend = errors.New("crypt: multiple decryption backends registered")

// ErrUnknownBackend is returned by [Get] when no backend is
// registered under the requested name.
var ErrUnknownBackend = errors.New("crypt: unknown backend")

// ErrEncryptOnlyBackend reports a backend that exists under the
// requested name but carries no Decrypter. Declared here, alongside
// the other selection failures, so a caller naming a backend from
// configuration can tell "typo or missing import"
// ([ErrUnknownBackend]) from "that one only encrypts" with errors.Is
// rather than by matching on message text. Returned by the resolver's
// WithDecrypterNamed handling.
var ErrEncryptOnlyBackend = errors.New("crypt: backend cannot decrypt")

var (
	mu       sync.RWMutex
	backends = map[string]Backend{}
)

// Register makes a backend available under name. Implementation
// packages call this from init; consumers select a backend by
// importing one (see the package doc).
//
// Register panics on an empty name, an empty Backend, or a duplicate
// registration — all three are build-time mistakes in the linked-in
// set, not conditions a running program can act on. This mirrors
// sql.Register.
func Register(name string, b Backend) {
	if name == "" {
		panic("crypt: Register with empty name")
	}
	if b.Decrypter == nil && b.Encrypter == nil {
		panic("crypt: Register " + name + ": Backend has neither a Decrypter nor an Encrypter")
	}

	mu.Lock()
	defer mu.Unlock()
	if _, dup := backends[name]; dup {
		panic("crypt: Register called twice for backend " + name)
	}
	backends[name] = b
}

// Unregister removes the named backend, reporting whether it was
// registered. Registration is normally an init-time, program-lifetime
// fact; this exists for tests that need to exercise the
// nothing-linked-in path or swap a double, and pairs with
// t.Cleanup.
//
// Unregistering is safe for concurrent use but does not reach a
// Decrypter a caller already holds: the resolver reads the registry at
// most once per Resolve — at the first encrypted layer, then reuses
// that Decrypter for the rest of the call — so an in-flight Resolve may
// still finish against the removed backend.
func Unregister(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := backends[name]
	delete(backends, name)
	return ok
}

// Registered returns the names of the registered backends, sorted.
// Useful in diagnostics ("what did this build actually link?") and
// mirrors sql.Drivers.
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	return names(backends)
}

// Get returns the backend registered under name, or a wrapped
// [ErrUnknownBackend] naming what the build does have.
func Get(name string) (Backend, error) {
	mu.RLock()
	defer mu.RUnlock()
	b, ok := backends[name]
	if !ok {
		return Backend{}, fmt.Errorf("%w %q (registered: %s)", ErrUnknownBackend, name, listOrNone(names(backends)))
	}
	return b, nil
}

// SoleDecrypter returns the Decrypter of the one registered backend
// that has one, along with its name. This is what the resolver falls
// back to when the caller passed neither WithDecrypter nor
// WithDecrypterNamed.
//
// Returns [ErrNoBackend] when nothing decrypt-capable is linked in and
// [ErrAmbiguousBackend] when several are. Both name the backends the
// build does have, so the caller can tell "you linked nothing" from
// "you linked the wrong half" — an encrypt-only build would otherwise
// be told to link a backend it already has.
func SoleDecrypter() (Decrypter, string, error) {
	mu.RLock()
	defer mu.RUnlock()

	var capable []string
	for n, b := range backends {
		if b.Decrypter != nil {
			capable = append(capable, n)
		}
	}

	switch len(capable) {
	case 0:
		// Bare sentinel for the empty registry: "(registered: none)"
		// would only repeat what the message already says. With
		// encrypt-only backends linked in, naming them is the whole
		// point — the fix is a decrypt half, not a first import.
		if all := names(backends); len(all) > 0 {
			return nil, "", fmt.Errorf("%w (registered, none decrypt-capable: %s)",
				ErrNoBackend, strings.Join(all, ", "))
		}
		return nil, "", ErrNoBackend
	case 1:
		return backends[capable[0]].Decrypter, capable[0], nil
	default:
		slices.Sort(capable)
		return nil, "", fmt.Errorf("%w (%s)", ErrAmbiguousBackend, strings.Join(capable, ", "))
	}
}

// names returns the keys of m, sorted. Callers hold mu.
func names(m map[string]Backend) []string {
	return slices.Sorted(maps.Keys(m))
}

// listOrNone renders a backend-name list for an error message,
// spelling the empty case out rather than leaving a dangling "()".
func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}
