// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package resolver is chameleon's public entry point. A Resolver is
// constructed once from a parsed ChainSpec plus a sample of the
// consumer's typed config message, and serves repeated [Resolver.Resolve]
// calls — one per (environment, context) tuple.
//
// Lifecycle:
//
//	spec, _ := loadChainSpec("chain.pxf")              // proto.Message
//	r, _ := resolver.New[*AppConfig](
//	    &AppConfig{},                                   // type witness
//	    spec,
//	    resolver.WithSource("default", source.FS(os.DirFS("./layers"))),
//	    resolver.WithSource("secrets", source.FS(os.DirFS("./layers/secrets"))),
//	    resolver.WithDecrypter(sops.Default()),
//	)
//	res, _ := r.Resolve(ctx, resolver.Request{Environment: "dev"})
//	defer res.Destroy()                                  // wipes Enclaves
//	use(res.Config.BaseUrl)
//	pw := res.Secrets.Get("db_password")
//	buf, _ := pw.Open(); defer buf.Destroy()
//	use(buf.Bytes())
//
// Per-call pipeline:
//
//  1. chain.Expand → ordered []LayerRef.
//  2. For each ref: source.Open → bytes (decrypt if encrypted) →
//     pxf.UnmarshalFull → (layer message, *pxf.Result).
//  3. merge.Merge: overlay each layer onto the running destination.
//  4. parse.Move: extract pxf.Secret values into per-field memguard
//     Enclaves, returning a secret.Map keyed by dotted field path.
//  5. Wrap as [Resolution]; destroy closure wipes the Map's enclaves.
//
// The defaults / interpolation / validation passes are not yet wired —
// they layer in here once their helpers are implemented.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/defaults"
	"github.com/trendvidia/chameleon/internal/header"
	"github.com/trendvidia/chameleon/interpolate"
	"github.com/trendvidia/chameleon/merge"
	"github.com/trendvidia/chameleon/parse"
	"github.com/trendvidia/chameleon/validate"
	"github.com/trendvidia/chameleon/secret"
	"github.com/trendvidia/chameleon/sops"
	"github.com/trendvidia/chameleon/source"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/proto"
)

// ErrNoSource is returned when a LayerRef references a source name
// that wasn't registered via WithSource.
var ErrNoSource = errors.New("resolver: layer references unregistered source")

// ErrNoDecrypter is returned when a LayerRef is encrypted but the
// resolver has no Decrypter configured.
var ErrNoDecrypter = errors.New("resolver: encrypted layer without Decrypter")

// ErrRequiredLayerMissing is returned when a LayerRef is marked
// required but the source returned ErrNotFound.
var ErrRequiredLayerMissing = errors.New("resolver: required layer missing")

// Resolver materializes typed config snapshots. T is the consumer's
// config message; pass it as a pointer type — Resolver[*AppConfig], not
// Resolver[AppConfig] — because proto.Message is satisfied by pointer
// receivers.
//
// Resolver is safe for concurrent Resolve calls; each call produces an
// independent Resolution that owns its own secret.Map.
type Resolver[T proto.Message] struct {
	sample             T
	spec               proto.Message
	sources            map[string]source.Source
	dec                sops.Decrypter
	trace              bool
	metrics            *instruments
	secretInstruments  *secret.Instruments
}

// Option configures a Resolver at construction.
type Option func(*config)

type config struct {
	sources map[string]source.Source
	dec     sops.Decrypter
	trace   bool
	mp      metric.MeterProvider
}

// WithSource registers a Source under a logical name. The name must
// match what LayerEntry.source declares in the spec (or "default" for
// entries that omit it).
func WithSource(name string, src source.Source) Option {
	return func(c *config) {
		if c.sources == nil {
			c.sources = make(map[string]source.Source)
		}
		c.sources[name] = src
	}
}

// WithDecrypter installs the sops decryption hook. Required only if at
// least one LayerEntry has encrypted = true.
func WithDecrypter(d sops.Decrypter) Option {
	return func(c *config) { c.dec = d }
}

// WithTrace turns on per-field provenance tracking. When enabled,
// each [Resolution] carries a populated [Trace] with one entry per
// dotted field path indicating which layer set the value, whether a
// default was applied, whether interpolation rewrote it, and whether
// the field is sensitive. Costs a small map per Resolve call; off by
// default.
func WithTrace() Option {
	return func(c *config) { c.trace = true }
}

// WithMeterProvider installs an OpenTelemetry [metric.MeterProvider]
// for resolver metrics. The resolver records on every Resolve call:
//
//   - chameleon.resolve.duration (histogram, seconds)
//   - chameleon.resolve.errors (counter)
//   - chameleon.resolve.layers (histogram)
//   - chameleon.resolve.secrets (histogram)
//
// Each instrument carries a `chameleon.env` attribute. With no
// MeterProvider passed, instruments come from the noop provider and
// every record is a cheap no-op.
//
// The same metrics could be obtained globally by setting OTel's
// global meter provider and calling [WithMeterProvider] with
// [otel.GetMeterProvider]; this option is here so callers don't have
// to reach for global state.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(c *config) { c.mp = mp }
}

// New constructs a Resolver. `sample` is a fresh empty instance of the
// consumer's typed config (e.g. `&AppConfig{}`); it serves as both the
// type witness and the descriptor source. `spec` is a parsed
// chameleon.v1.ChainSpec proto message.
//
// New validates that at least one Source was registered, but does NOT
// fully validate the spec until the first Resolve — Expand surfaces
// per-call diagnostics that depend on the request (env / ctx).
func New[T proto.Message](sample T, spec proto.Message, opts ...Option) (*Resolver[T], error) {
	if !sample.ProtoReflect().IsValid() && sample.ProtoReflect().Descriptor() == nil {
		return nil, errors.New("resolver: nil sample message")
	}
	if spec == nil {
		return nil, errors.New("resolver: nil spec")
	}

	cfg := config{}
	for _, o := range opts {
		o(&cfg)
	}
	if len(cfg.sources) == 0 {
		return nil, errors.New("resolver: no sources registered (use WithSource)")
	}
	if _, ok := cfg.sources["default"]; !ok {
		// "default" is the implicit source for layer entries that omit
		// `source`. Require it so a typo in the spec doesn't silently
		// fall through to "unregistered source" later.
		return nil, errors.New(`resolver: "default" source required (use WithSource("default", ...))`)
	}

	return &Resolver[T]{
		sample:            sample,
		spec:              spec,
		sources:           cfg.sources,
		dec:               cfg.dec,
		trace:             cfg.trace,
		metrics:           newInstruments(cfg.mp),
		secretInstruments: secret.NewInstruments(cfg.mp),
	}, nil
}

// Request is the input to [Resolver.Resolve].
type Request struct {
	// Environment is the leaf env name. Empty falls back to
	// ChainSpec.default_environment.
	Environment string

	// Context supplies values for ${ctx.*} substitutions in layer
	// templates. Keys must match ChainSpec.context_keys unless
	// AllowUnknownContext is true.
	Context map[string]string

	// AllowUnknownContext relaxes the strict-key check. Default false
	// catches typos like `regions` vs `region`.
	AllowUnknownContext bool
}

// Resolution is the output of a successful Resolve. Destroy must be
// called when the consumer is done; it wipes the Enclaves backing
// every Secret in [Resolution.Secrets].
type Resolution[T proto.Message] struct {
	// Config is the merged typed message. Sensitive scalars
	// (pxf.Secret-typed fields) have their `value` cleared on the
	// proto message — the live plaintext lives only in Secrets.
	Config T

	// Secrets holds the memguard-backed sensitive values, keyed by
	// dotted field path (see [parse.Move]).
	Secrets *secret.Map

	// Trace carries per-field provenance when WithTrace was passed at
	// construction; nil otherwise.
	Trace *Trace

	destroyOnce sync.Once
	destroy     func()
}

// Destroy wipes every Enclave in [Resolution.Secrets]. Idempotent and
// safe to call from any goroutine.
//
// Note: by the time Destroy runs, the proto-generated `*pxfv1.Secret`
// fields on Config already have their `Value` cleared — that happens
// during [parse.Move] inside Resolve, before Destroy is reachable.
// What Destroy wipes is the memguard Enclaves the Map holds, which
// are the live plaintext storage post-merge.
func (r *Resolution[T]) Destroy() {
	r.destroyOnce.Do(func() {
		if r.destroy != nil {
			r.destroy()
		}
	})
}

// Trace is the per-resolution provenance map. Keyed by dotted field
// path (matching parse.Move's format), values describe how each field
// arrived at its final value. Populated only when the resolver was
// constructed with [WithTrace]; nil otherwise.
type Trace struct {
	Entries map[string]TraceEntry
}

// Get returns the entry at path, or the zero TraceEntry if absent.
// Distinguish "no entry" from "zero entry" via [Trace.Has].
func (t *Trace) Get(path string) TraceEntry {
	if t == nil {
		return TraceEntry{}
	}
	return t.Entries[path]
}

// Has reports whether the Trace recorded any provenance for path.
// False both for paths the resolver never saw AND when t is nil.
func (t *Trace) Has(path string) bool {
	if t == nil {
		return false
	}
	_, ok := t.Entries[path]
	return ok
}

// Paths returns the dotted paths the Trace knows about, sorted
// alphabetically. Useful for `chameleon explain`-style listings.
func (t *Trace) Paths() []string {
	if t == nil {
		return nil
	}
	paths := make([]string, 0, len(t.Entries))
	for p := range t.Entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// TraceEntry records how a single field arrived at its final value.
type TraceEntry struct {
	// LayerID is the LayerEntry.id of the layer that set the winning
	// value. Empty if the field was filled by (pxf.default) instead
	// of any layer.
	LayerID string

	// FromDefault is true if the value was supplied by (pxf.default),
	// not by any layer. LayerID is empty when this is true.
	FromDefault bool

	// Interpolated is true if the field's final value was rewritten by
	// the post-merge interpolation pass (i.e. it contained `${...}`).
	Interpolated bool

	// Sensitive is true for fields whose type is pxf.Secret. The Trace
	// never records the value itself — just the fact a secret existed
	// at this path.
	Sensitive bool
}

// Resolve materializes a typed config for the given request. Safe for
// concurrent use; each call produces an independent Resolution.
//
// Records OTel metrics if a [WithMeterProvider] was passed:
//   - chameleon.resolve.duration (always)
//   - chameleon.resolve.errors (when err != nil)
//   - chameleon.resolve.layers (after chain expansion)
//   - chameleon.resolve.secrets (after Move)
func (r *Resolver[T]) Resolve(ctx context.Context, req Request) (res *Resolution[T], err error) {
	start := time.Now()
	envAttrs := metric.WithAttributes(envAttr(req.Environment))
	defer func() {
		r.metrics.resolveDuration.Record(ctx, time.Since(start).Seconds(), envAttrs)
		if err != nil {
			r.metrics.resolveErrors.Add(ctx, 1, envAttrs)
		}
	}()

	refs, err := chain.Options{AllowUnknownContextKeys: req.AllowUnknownContext}.
		Expand(r.spec, req.Environment, req.Context)
	if err != nil {
		return nil, err
	}
	r.metrics.layerCount.Record(ctx, int64(len(refs)), envAttrs)

	dst := r.fresh()
	// presenceUnion accumulates every field path any layer mentioned
	// (set or null). It's the input to the post-merge defaults +
	// validate passes — proto3 implicit-presence scalars can't be told
	// apart from "explicitly set to zero", so we can't rely on Has().
	presenceUnion := make(map[string]struct{})

	// Trace state. Nil unless WithTrace() was passed at construction.
	var trace *Trace
	if r.trace {
		trace = &Trace{Entries: make(map[string]TraceEntry)}
	}

	for _, ref := range refs {
		src, ok := r.sources[ref.Source]
		if !ok {
			return nil, fmt.Errorf("%w: layer=%q source=%q", ErrNoSource, ref.EntryID, ref.Source)
		}
		if ref.Encrypted && r.dec == nil {
			return nil, fmt.Errorf("%w: layer=%q", ErrNoDecrypter, ref.EntryID)
		}

		layer := r.fresh()
		result, err := loadAndParse(ctx, ref, src, r.dec, layer)
		if err != nil {
			return nil, err
		}
		if result == nil {
			// Optional missing layer; nothing to merge.
			continue
		}
		if err := merge.Merge(dst, layer, result); err != nil {
			return nil, fmt.Errorf("resolver: merge layer %q: %w", ref.EntryID, err)
		}
		for _, p := range result.PresentFields() {
			presenceUnion[p] = struct{}{}
			if trace != nil {
				// Last layer to mark this path wins for trace purposes,
				// matching merge's "highest layer overrides" semantics.
				e := trace.Entries[p]
				e.LayerID = ref.EntryID
				e.FromDefault = false
				trace.Entries[p] = e
			}
		}
	}

	// Defaults BEFORE interpolation: a default value may itself be a
	// template (`api.${base_url}`), which interpolation then resolves
	// against the merged state. Defaults pass mutates presenceUnion to
	// mark default-filled paths as present so validate accepts them.
	var defaultsOpts []defaults.Option
	if trace != nil {
		defaultsOpts = append(defaultsOpts, defaults.OnApplied(func(path string) {
			e := trace.Entries[path]
			e.LayerID = ""
			e.FromDefault = true
			trace.Entries[path] = e
		}))
	}
	if err := defaults.Apply(dst, presenceUnion, defaultsOpts...); err != nil {
		return nil, fmt.Errorf("resolver: defaults: %w", err)
	}

	// Interpolation runs BEFORE secrets are moved into Enclaves: the
	// walker skips pxf.Secret subtrees, so secrets are neither
	// templated nor referenceable from other fields' templates.
	var interpOpts []interpolate.Option
	if trace != nil {
		interpOpts = append(interpOpts, interpolate.OnApplied(func(path string) {
			e := trace.Entries[path]
			e.Interpolated = true
			trace.Entries[path] = e
		}))
	}
	if err := interpolate.Apply(dst, interpOpts...); err != nil {
		return nil, fmt.Errorf("resolver: interpolate: %w", err)
	}

	// Validate AFTER defaults so a default-filled required field
	// counts as satisfied. Validate BEFORE Move so we don't extract
	// secrets out of an invalid configuration.
	if err := validate.Validate(dst, presenceUnion); err != nil {
		return nil, fmt.Errorf("resolver: %w", err)
	}

	secrets, err := parse.MoveWithOptions(dst, secret.WithInstruments(r.secretInstruments, req.Environment))
	if err != nil {
		return nil, fmt.Errorf("resolver: extract secrets: %w", err)
	}
	r.metrics.secretCount.Record(ctx, int64(secrets.Len()), envAttrs)

	if trace != nil {
		// Mark sensitive paths after Move so the Trace reflects exactly
		// the keys that ended up in secret.Map.
		secrets.Range(func(path string, _ *secret.Secret) bool {
			e := trace.Entries[path]
			e.Sensitive = true
			trace.Entries[path] = e
			return true
		})
	}

	return &Resolution[T]{
		Config:  dst,
		Secrets: secrets,
		Trace:   trace,
		destroy: secrets.Destroy,
	}, nil
}

// fresh returns a freshly-constructed empty T using the sample as a
// descriptor witness. The returned value has the same dynamic type as
// r.sample.
func (r *Resolver[T]) fresh() T {
	return r.sample.ProtoReflect().New().Interface().(T)
}

// loadAndParse pulls bytes for ref, decrypts if encrypted, and parses
// PXF into msg. Returns (nil, nil) when the layer is optional and the
// source reports ErrNotFound — caller treats that as "skip".
func loadAndParse(ctx context.Context, ref chain.LayerRef, src source.Source, dec sops.Decrypter, msg proto.Message) (*pxf.Result, error) {
	rc, err := src.Open(ctx, ref.Path)
	if err != nil {
		if errors.Is(err, source.ErrNotFound) {
			if ref.Required {
				return nil, fmt.Errorf("%w: layer=%q path=%q", ErrRequiredLayerMissing, ref.EntryID, ref.Path)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("resolver: open layer %q: %w", ref.EntryID, err)
	}
	defer rc.Close()

	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("resolver: read layer %q: %w", ref.EntryID, err)
	}

	var data []byte
	if ref.Encrypted {
		// Stream ciphertext into the Decrypter, which returns plaintext
		// in a memguard.LockedBuffer. We hold the buffer alive until
		// after parse, then destroy it. Parsing copies bytes into Go
		// strings (immutable, unwipeable until GC) — see parse.Move
		// godoc for the best-effort caveat that closes once codegen
		// emits a custom protoreflect implementation.
		buf, err := dec.Decrypt(ctx, raw, sops.FormatForPath(ref.Path))
		if err != nil {
			return nil, fmt.Errorf("resolver: decrypt layer %q: %w", ref.EntryID, err)
		}
		defer buf.Destroy()
		data = buf.Bytes()
	} else {
		data = raw
	}

	// SkipPostDecode disables protowire-go's per-parse defaults +
	// required-validation pass. chameleon runs both at the merged-
	// result level via the defaults / validate packages — applying
	// them per-layer would silently drop defaults (presence is
	// "absent" so merge falls through and clobbers the default-filled
	// value) and reject base layers that legitimately omit a required
	// field a higher layer provides.
	//
	// Any leading `@header chameleon.v1.LayerHeader { ... }` preamble
	// is collected by the PXF decoder (since protowire-go v0.72.0) and
	// surfaced on res.Directives(). chameleon's HandleDirectives runs
	// the sanity checks (id / environment / encrypted) against the
	// chain entry the layer was loaded for.
	res, err := pxf.UnmarshalOptions{SkipPostDecode: true}.UnmarshalFull(data, msg)
	if err != nil {
		return nil, fmt.Errorf("resolver: parse layer %q: %w", ref.EntryID, err)
	}
	if err := header.HandleDirectives(res.Directives(), header.Ref{
		EntryID:   ref.EntryID,
		Env:       ref.Env,
		Encrypted: ref.Encrypted,
	}); err != nil {
		return nil, fmt.Errorf("resolver: layer %q: %w", ref.EntryID, err)
	}
	return res, nil
}
