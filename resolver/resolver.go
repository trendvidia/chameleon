// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package resolver is chameleon's public entry point. A Resolver is
// constructed once from a parsed ChainSpec plus a sample of the
// consumer's typed config message, and serves repeated [Resolver.Resolve]
// calls — one per (environment, context) tuple.
//
// Lifecycle:
//
//	import _ "github.com/trendvidia/chameleon/sops"    // only if layers are encrypted
//
//	spec, _ := loadChainSpec("chain.pxf")              // proto.Message
//	r, _ := resolver.New[*AppConfig](
//	    &AppConfig{},                                   // type witness
//	    spec,
//	    resolver.WithSource("default", source.FS(os.DirFS("./layers"))),
//	    resolver.WithSource("secrets", source.FS(os.DirFS("./layers/secrets"))),
//	)
//	res, _ := r.Resolve(ctx, resolver.Request{Environment: "dev"})
//	defer res.Destroy()                                  // wipes Enclaves
//	use(res.Config.BaseUrl)
//	pw := res.Secrets.Get("db_password")
//	buf, _ := pw.Open(); defer buf.Destroy()
//	use(buf.Bytes())
//
// Decryption is opt-in at link time. The resolver depends on the
// [crypt.Decrypter] contract, never on an implementation, so a program
// with plaintext layers links no key-management code at all. A program
// with encrypted layers picks a backend up by blank-importing one
// (chameleon/sops is the in-tree one), or passes its own to
// [WithDecrypter]. Registration follows the module graph, so a program
// that wants an encrypted layer to fail rather than be decrypted says
// so with [WithoutDecrypter]. See the crypt package doc for the model,
// and trendvidia/chameleon#130 for what it costs when the resolver
// gets this wrong.
//
// Per-call pipeline:
//
//  1. chain.Expand → ordered []LayerRef.
//  2. For each ref: source.Open → bytes (decrypt if encrypted) →
//     pxf.UnmarshalFull → (layer message, *pxf.Result). Scalar-shorthand
//     pxf.Secret values are sealed into Enclaves during this decode
//     rather than landing on the layer message at all.
//  3. merge.Merge: overlay each layer onto the running destination.
//  4. defaults.Apply: fill (pxf.default) values for fields no layer
//     set, marking those paths present for step 6.
//  5. interpolate.Apply: expand ${field.path} against the merged
//     state, with cycle detection.
//  6. validate.Validate: (pxf.required) check on the merged result.
//  7. parse.Move: fold the block-form pxf.Secret values into the same
//     secret.Map, keyed by dotted field path.
//  8. Wrap as [Resolution]; destroy closure wipes the Map's enclaves.
//
// Steps 4–6 are ordered, not merely sequential, and each order is
// load-bearing. Defaults run BEFORE interpolation because a default
// may itself be a template (api.${base_url}) that interpolation then
// resolves against the merged state — trendvidia/chameleon#135.
// Validation runs AFTER defaults so a default-filled required field
// counts as satisfied, and BEFORE step 7 so no secret is extracted out
// of a configuration that was never valid. Interpolation also precedes
// step 7 because its walker skips pxf.Secret subtrees: a secret is
// neither templated nor referenceable from another field's template.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/defaults"
	"github.com/trendvidia/chameleon/internal/header"
	"github.com/trendvidia/chameleon/interpolate"
	"github.com/trendvidia/chameleon/merge"
	"github.com/trendvidia/chameleon/parse"
	"github.com/trendvidia/chameleon/secret"
	"github.com/trendvidia/chameleon/source"
	"github.com/trendvidia/chameleon/validate"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/proto"
)

// ErrNoSource is returned when a LayerRef references a source name
// that wasn't registered via WithSource.
var ErrNoSource = errors.New("resolver: layer references unregistered source")

// ErrNoDecrypter is returned when a LayerRef is encrypted but the
// build has no Decrypter to hand it to: none was passed to
// [WithDecrypter] / [WithDecrypterNamed] and no backend registered
// itself with the crypt registry (blank-import chameleon/sops for the
// in-tree one). Also returned when several backends are registered and
// none was named — auto-selection is only defined for the single-
// backend case. The diagnostic says which of the two happened.
var ErrNoDecrypter = errors.New("resolver: encrypted layer without Decrypter")

// NoDecrypterError is the concrete error behind [ErrNoDecrypter]. It
// names the layer that turned out to be encrypted and says which of
// the two cases happened, so a caller can re-word the failure in its
// own vocabulary rather than repeating this package's option names to
// an audience that never typed them — a CLI telling its user to pass a
// flag, not to call [WithDecrypter]. See trendvidia/chameleon#141.
//
// Reach it with [errors.As]. errors.Is against [ErrNoDecrypter], and
// against the crypt sentinel underneath, keeps working unchanged.
type NoDecrypterError struct {
	// LayerID is the chain entry whose layer is encrypted — the same
	// id the diagnostic quotes.
	LayerID string

	// Disabled reports that this Resolver was told not to decrypt
	// ([WithoutDecrypter]), as opposed to having no Decrypter to hand
	// the layer to. Err is nil when Disabled is true: with decryption
	// off, the registry is never consulted, so there is no lookup
	// failure to report.
	Disabled bool

	// Err is the [crypt.SoleDecrypter] lookup failure —
	// [crypt.ErrNoBackend] or [crypt.ErrAmbiguousBackend].
	Err error
}

func (e *NoDecrypterError) Error() string {
	if e.Disabled {
		return fmt.Sprintf("%s: layer=%q: decryption disabled (WithoutDecrypter)",
			ErrNoDecrypter.Error(), e.LayerID)
	}
	return fmt.Sprintf("%s: layer=%q: %s%s",
		ErrNoDecrypter.Error(), e.LayerID, e.Err, decrypterHint(e.Err))
}

// Unwrap reports the sentinel and the registry failure together, so
// errors.Is matches [ErrNoDecrypter], [crypt.ErrNoBackend] and
// [crypt.ErrAmbiguousBackend] exactly as the fmt.Errorf %w chain this
// type replaced did.
func (e *NoDecrypterError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrNoDecrypter}
	}
	return []error{ErrNoDecrypter, e.Err}
}

// ErrRequiredLayerMissing is returned when a LayerRef is marked
// required but the source returned ErrNotFound.
var ErrRequiredLayerMissing = errors.New("resolver: required layer missing")

// ErrLayerTooLarge is returned when a layer's raw bytes (before
// decrypt) exceed the configured cap. See [WithMaxLayerBytes].
// Diagnostic is wrapped with the layer id, path, and the active
// limit so consumers can act on the failure.
var ErrLayerTooLarge = errors.New("resolver: layer exceeds size limit")

// ErrSchemaViolation is returned by [New] when the sample message's
// descriptor closure carries a PXF annotation the format does not
// admit — a (pxf.default) on a repeated field, on a message type no
// literal denotes, a (pxf.required) on a oneof member (draft -01
// §annotation-extensions), plus the PXF-reserved names and (pxf.key)
// placements of §3.13. Those are the sections the violation strings
// cite; §schema-constraints ("Scope of Bind-Time Checks") is what
// makes the scope the transitive import closure, so the file the
// error names may be an imported one rather than the sample's own.
//
// protowire-go enforces these when a schema binds, which for chameleon
// is the first layer parse. That is deterministic across environments
// but not across chains: a chain whose every layer is optional and
// absent parses nothing, so it binds nothing, so it used to resolve an
// invalid schema clean. [New] closes that by asking the same question
// of the descriptor directly. See trendvidia/chameleon#134.
var ErrSchemaViolation = errors.New("resolver: schema violation")

// defaultMaxLayerBytes caps each layer's raw bytes when
// [WithMaxLayerBytes] isn't passed. Defense against pathological
// Sources that return huge files or hang on a slow reader without
// a deadline. 10 MiB covers everything observed in the wild by
// orders of magnitude; raise it explicitly if your authoring
// pipeline genuinely produces larger layers.
const defaultMaxLayerBytes int64 = 10 << 20 // 10 MiB

// Resolver materializes typed config snapshots. T is the consumer's
// config message; pass it as a pointer type — Resolver[*AppConfig], not
// Resolver[AppConfig] — because proto.Message is satisfied by pointer
// receivers.
//
// Resolver is safe for concurrent Resolve calls; each call produces an
// independent Resolution that owns its own secret.Map.
type Resolver[T proto.Message] struct {
	sample            T
	spec              proto.Message
	sources           map[string]source.Source
	dec               crypt.Decrypter
	noDec             bool
	trace             bool
	strictMetadata    bool
	maxLayerBytes     int64
	metrics           *instruments
	secretInstruments *secret.Instruments
}

// Option configures a Resolver at construction.
type Option func(*config)

type config struct {
	sources        map[string]source.Source
	dec            crypt.Decrypter
	decSet         bool // WithDecrypter was passed, even with a nil Decrypter
	decName        string
	decNamed       bool // WithDecrypterNamed was passed, even with an empty name
	noDec          bool
	trace          bool
	strictMetadata bool
	maxLayerBytes  int64
	mp             metric.MeterProvider
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

// WithDecrypter installs a decryption hook explicitly. Use this for a
// Decrypter the caller constructed — programmatic key injection, an
// HSM, a test double — or to pin one implementation in a build that
// links several.
//
// It is not needed for the common case: with exactly one backend
// registered (say, a blank import of chameleon/sops), the resolver
// selects it on its own the first time an encrypted layer appears.
//
// A nil Decrypter fails [New] rather than being ignored: before the
// registry, "no decrypter" and "do not decrypt" were the same thing,
// so WithDecrypter(nil) reading as "not passed" was harmless. It is
// not any more — not passing the option now means auto-select — so a
// caller whose constructor returned nil would silently get ambient
// credentials instead of the refusal they used to get. Use
// [WithoutDecrypter] to say no. (This catches a nil interface; a typed
// nil — WithDecrypter((*myDecrypter)(nil)) — is a non-nil interface
// value and is installed as given, as everywhere else in Go.)
//
// Mutually exclusive with [WithDecrypterNamed] and [WithoutDecrypter].
func WithDecrypter(d crypt.Decrypter) Option {
	return func(c *config) { c.dec, c.decSet = d, true }
}

// WithDecrypterNamed selects a registered backend by name — the
// resolver-side equivalent of naming a driver in sql.Open. Use it when
// a build links more than one backend, where auto-selection is
// deliberately undefined; the in-tree sops backend registers itself as
// "sops" ([github.com/trendvidia/chameleon/sops.BackendName]).
//
// [New] fails if no backend is registered under that name, listing the
// ones that are, so a typo or a missing import surfaces at
// construction rather than at the first encrypted layer. An empty name
// is an error too, rather than a silent fall-through to auto-selection:
// a caller reading the name out of config or an env var wants the
// unset case to say so, not to pick whatever the build happens to
// link. Mutually exclusive with [WithDecrypter] and
// [WithoutDecrypter].
func WithDecrypterNamed(name string) Option {
	return func(c *config) { c.decName, c.decNamed = name, true }
}

// WithoutDecrypter disables decryption for this Resolver: an encrypted
// layer returns [ErrNoDecrypter] even if the build has a backend
// registered.
//
// Needed because "no decrypter configured" and "do not decrypt" stopped
// being the same thing once the registry landed. Registration follows
// the module graph, so a program links a backend as soon as anything in
// its dependency tree imports one — this is how a caller says no
// anyway. The CLI's `--decrypter=none` is exactly that: it must fail on
// an encrypted layer rather than reach for ambient KMS/age credentials
// just because the binary also ships `chameleon bundle`.
//
// Mutually exclusive with [WithDecrypter] and [WithDecrypterNamed].
func WithoutDecrypter() Option {
	return func(c *config) { c.noDec = true }
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

// WithStrictMetadata upgrades WKT-metadata clobber detection from a
// warning (recorded on [Trace.ClobberedMetadata] + the
// chameleon.merge.clobbered_metadata counter) to a hard error: when a
// higher layer's atomic-WKT write would silently drop metadata from
// a lower layer's value, Resolve returns the clobber as a fatal
// error.
//
// Use this when your authoring discipline says "always re-declare
// metadata if you override a Secret" — strict mode makes accidental
// metadata loss fail-loud at resolve time rather than waiting for a
// production surprise.
//
// Without this option, clobbers are observable via Trace + metrics
// but do not abort resolution.
func WithStrictMetadata() Option {
	return func(c *config) { c.strictMetadata = true }
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

// WithMaxLayerBytes caps the size of each layer's raw bytes read
// from a Source. Defense against pathological Sources that return
// huge files or hang on a slow reader without a deadline. The cap
// applies to the bytes BEFORE decrypt; ciphertext size is
// approximately plaintext size, so a single cap covers both
// encrypted and plaintext layers.
//
// The default (with this option unset) is 10 MiB. Pass a larger
// value if your authoring pipeline genuinely produces larger
// layers, or math.MaxInt64 to disable the cap entirely (not
// recommended for untrusted Sources).
//
// A negative value fails [New] with an error; pass 0 to fall back
// to the default.
func WithMaxLayerBytes(n int64) Option {
	return func(c *config) { c.maxLayerBytes = n }
}

// New constructs a Resolver. `sample` is a fresh empty instance of the
// consumer's typed config (e.g. `&AppConfig{}`); it serves as both the
// type witness and the descriptor source. `spec` is a parsed
// chameleon.v1.ChainSpec proto message.
//
// New validates the sample's schema and the wiring it was handed — at
// least one Source, "default" among them, one answer to the decrypter
// question — but does NOT fully validate the spec until the first
// Resolve: Expand surfaces per-call diagnostics that depend on the
// request (env / ctx).
//
// The sample's schema is the exception to that deferral, and
// deliberately so: the bind-time PXF rules are a property of the
// descriptor alone, so they give the same answer at construction that
// they would give at every Resolve. Checking here fails once, in one
// place, instead of on whichever layer parse happens to bind first —
// and catches the chain that never parses a layer at all. Violations
// return [ErrSchemaViolation]; see that sentinel for why the deferral
// rule above does not cover this case.
//
// The schema check runs ahead of the wiring checks, so a malformed
// schema is what a caller who also mis-wired their Sources hears about
// first. That ordering is deliberate: re-wiring would not change the
// schema's answer, so reporting it first saves a round trip.
func New[T proto.Message](sample T, spec proto.Message, opts ...Option) (*Resolver[T], error) {
	m := sample.ProtoReflect()
	if !m.IsValid() && m.Descriptor() == nil {
		return nil, errors.New("resolver: nil sample message")
	}
	if spec == nil {
		return nil, errors.New("resolver: nil spec")
	}
	// Memoized per file closure upstream, so repeated New calls on the
	// same descriptor cost a map lookup rather than a walk — up to
	// upstream's 4096-descriptor cache bound, past which a program that
	// compiles descriptors dynamically pays the walk on every call.
	if vs := pxf.ValidateDescriptor(m.Descriptor()); len(vs) > 0 {
		parts := make([]string, len(vs))
		for i, v := range vs {
			parts[i] = v.String()
		}
		return nil, fmt.Errorf("%w:\n  %s", ErrSchemaViolation, strings.Join(parts, "\n  "))
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
	// The three decrypter options are three different answers to the
	// same question, so any two of them together is a wiring mistake
	// rather than a precedence puzzle.
	if picked := btoi(cfg.decSet) + btoi(cfg.decNamed) + btoi(cfg.noDec); picked > 1 {
		return nil, errors.New("resolver: WithDecrypter, WithDecrypterNamed and WithoutDecrypter are mutually exclusive")
	}
	if cfg.decSet && cfg.dec == nil {
		return nil, errors.New(`resolver: WithDecrypter: nil Decrypter (use WithoutDecrypter to disable decryption)`)
	}
	if cfg.decNamed {
		if cfg.decName == "" {
			return nil, errors.New(`resolver: WithDecrypterNamed: empty backend name (use WithoutDecrypter to disable decryption)`)
		}
		b, err := crypt.Get(cfg.decName)
		if err != nil {
			return nil, fmt.Errorf("resolver: WithDecrypterNamed: %w", err)
		}
		if b.Decrypter == nil {
			return nil, fmt.Errorf("resolver: WithDecrypterNamed: %w: backend %q is encrypt-only",
				crypt.ErrEncryptOnlyBackend, cfg.decName)
		}
		cfg.dec = b.Decrypter
	}
	if cfg.maxLayerBytes < 0 {
		return nil, fmt.Errorf("resolver: WithMaxLayerBytes must be >= 0; got %d", cfg.maxLayerBytes)
	}
	if cfg.maxLayerBytes == 0 {
		cfg.maxLayerBytes = defaultMaxLayerBytes
	}

	return &Resolver[T]{
		sample:            sample,
		spec:              spec,
		sources:           cfg.sources,
		dec:               cfg.dec,
		noDec:             cfg.noDec,
		trace:             cfg.trace,
		strictMetadata:    cfg.strictMetadata,
		maxLayerBytes:     cfg.maxLayerBytes,
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

	// ClobberedMetadata records WKT-metadata losses that happened
	// during the merge — a higher layer's atomic-WKT write whose own
	// metadata was empty silently dropped non-empty metadata from a
	// lower layer. Currently surfaced only for pxf.Secret (hint /
	// fingerprint); see [merge.ClobberInfo]. Empty when no clobber
	// occurred. Surfaced by `chameleon explain` as a separate
	// section.
	ClobberedMetadata []ClobberRecord

	// Decrypter names the crypt-registry backend this Resolve
	// auto-selected, and is set only in that case: empty when no
	// encrypted layer was reached, and empty when the caller named
	// the Decrypter itself via WithDecrypter / WithDecrypterNamed
	// (they already know which one it is).
	//
	// Auto-selection is the case worth reporting because it is the
	// one nobody wrote down — registration follows the module graph,
	// so a dependency can put a backend in a build without the build's
	// author choosing it. This is the per-call answer to "who
	// decrypted my layers"; [crypt.Registered] only says what was
	// available.
	Decrypter string
}

// ClobberRecord describes a single WKT-metadata clobber detected at
// merge time, attributed to the layer that performed the write.
type ClobberRecord struct {
	// Path is the dotted field path that was clobbered.
	Path string
	// Type is the WKT's FullName (e.g. "pxf.Secret").
	Type string
	// LostFields names the metadata sub-fields that were dropped.
	LostFields []string
	// ClobberingLayerID is the LayerEntry.id of the layer whose write
	// dropped the metadata. The lower layer that owned the metadata
	// is not tracked here — `chameleon explain --field=<path>` shows
	// the full layer history.
	ClobberingLayerID string
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
	// layerResults captures each layer's pxf.Result in order. Passed
	// to parse.MoveIntoWithOptions so the walker can distinguish a
	// hook-routed empty-value Secret (most-recent layer marked
	// .value present) from a WKT atomic-merge wholesale replacement
	// (most-recent layer wrote `pw { }` / `pw { hint = "h" }` but
	// no value) — the latter clears the Map entry per the atomic-
	// merge semantic.
	var layerResults []*pxf.Result

	// presenceUnion accumulates every field path any layer mentioned
	// (set or null). It's the input to the post-merge defaults +
	// validate passes — proto3 implicit-presence scalars can't be told
	// apart from "explicitly set to zero", so we can't rely on Has().
	presenceUnion := make(map[string]struct{})

	// Secrets accumulator. Constructed before the layer loop so each
	// layer's OnSecretField hook (wired in loadAndParse) can fold its
	// pxf.Secret values into it directly during PXF decode — both
	// scalar shorthand (protowire-go v0.76+) and block form
	// (protowire-go v0.77+) route through the hook so the value
	// never lands on `*pxfv1.Secret.Value` on the proto message. A
	// transient heap window remains on the lexer string + hook
	// closure value (immutable Go strings, wiped by GC pacing).
	// parse.MoveInto below runs as a defensive sweep that folds
	// block-form hint/fingerprint metadata onto Map entries and
	// drops orphans (paths a higher layer subsequently nulled).
	secrets := secret.NewMap(secret.WithInstruments(r.secretInstruments, req.Environment))
	defer func() {
		// On error: tear down any partially-populated secrets so we
		// don't leak Enclaves. The success path hands them off to the
		// Resolution and clears this defer's effect by re-pointing
		// secrets at nil before returning.
		if err != nil && secrets != nil {
			secrets.Destroy()
		}
	}()

	// Trace state. Nil unless WithTrace() was passed at construction.
	var trace *Trace
	if r.trace {
		trace = &Trace{Entries: make(map[string]TraceEntry)}
	}

	// dec is the Decrypter for this Resolve call: the one installed at
	// construction, or — for a build that just blank-imported a
	// backend — the sole registered one, looked up the first time an
	// encrypted layer actually appears. Lazy on purpose: a chain with
	// no encrypted entries must never require a backend, and the
	// lookup result is reused for the rest of this call, so a chain
	// with twenty encrypted layers takes one read lock, not twenty.
	dec := r.dec

	for _, ref := range refs {
		src, ok := r.sources[ref.Source]
		if !ok {
			return nil, fmt.Errorf("%w: layer=%q source=%q", ErrNoSource, ref.EntryID, ref.Source)
		}
		if ref.Encrypted && r.noDec {
			return nil, &NoDecrypterError{LayerID: ref.EntryID, Disabled: true}
		}
		if ref.Encrypted && dec == nil {
			d, name, lookupErr := crypt.SoleDecrypter()
			if lookupErr != nil {
				return nil, &NoDecrypterError{LayerID: ref.EntryID, Err: lookupErr}
			}
			dec = d
			if trace != nil {
				trace.Decrypter = name
			}
		}

		layer := r.fresh()
		result, err := loadAndParse(ctx, ref, src, dec, layer, secrets, r.maxLayerBytes)
		if err != nil {
			return nil, err
		}
		if result == nil {
			// Optional missing layer; nothing to merge.
			continue
		}
		clobberCb := func(c merge.ClobberInfo) error {
			r.metrics.clobberedMetadata.Add(ctx, 1, envAttrs)
			if trace != nil {
				trace.ClobberedMetadata = append(trace.ClobberedMetadata, ClobberRecord{
					Path:              c.Path,
					Type:              c.Type,
					LostFields:        c.LostFields,
					ClobberingLayerID: ref.EntryID,
				})
			}
			if r.strictMetadata {
				return fmt.Errorf("resolver: layer %q clobbered metadata at %q (lost fields: %v)",
					ref.EntryID, c.Path, c.LostFields)
			}
			return nil
		}
		if err := merge.MergeWithOptions(dst, layer, result, merge.Options{
			OnClobberedMetadata: clobberCb,
		}); err != nil {
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
		layerResults = append(layerResults, result)
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
		// defaults.Apply already prefixes "defaults: "; wrapping with
		// the pass name again would stutter. Same shape as the
		// validate wrap below. (interpolate keeps its prefix here
		// because its errors are bare sentinels.)
		return nil, fmt.Errorf("resolver: %w", err)
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

	// Fold any block-form Secret values (and only block form — the
	// scalar-shorthand ones are already in the Map via the per-layer
	// hook above) into the same secrets accumulator, and drop any
	// hook-routed entries whose path was nulled by a higher layer.
	// LayerResults lets the walker also enforce WKT atomic-merge
	// clearing on empty-value Secrets that were wholesale-replaced
	// by a higher layer's `pw { }` / `pw { hint = "h" }` block.
	if err := parse.MoveIntoWithOptions(dst, secrets, parse.MoveOptions{
		LayerResults: layerResults,
	}); err != nil {
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

// btoi counts a set option, so the mutual-exclusion check in [New]
// reads as "how many of the three were passed".
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// decrypterHint turns a [crypt.SoleDecrypter] failure into the next
// thing the caller should type. The two failures have opposite fixes —
// nothing linked in wants an import, several linked in want a choice —
// and the registry package can't name the resolver options, so the
// wording lands here.
func decrypterHint(err error) string {
	switch {
	case errors.Is(err, crypt.ErrNoBackend):
		return "; link one with `import _ \"github.com/trendvidia/chameleon/sops\"`, or pass resolver.WithDecrypter"
	case errors.Is(err, crypt.ErrAmbiguousBackend):
		return "; pick one with resolver.WithDecrypterNamed, or pass resolver.WithDecrypter"
	default:
		return ""
	}
}

// loadAndParse pulls bytes for ref, decrypts if encrypted, and parses
// PXF into msg. Returns (nil, nil) when the layer is optional and the
// source reports ErrNotFound — caller treats that as "skip".
//
// secrets receives every pxf.Secret value discovered during decode
// via protowire-go's UnmarshalOptions.OnSecretField hook (scalar
// shorthand since v0.76.0; block form since v0.77.0). The hook
// seals each value into a memguard Enclave on the fly so the value
// never lands on `*pxfv1.Secret.Value` on the proto message —
// closing the gap that previously existed between PXF decode and
// the post-decode parse.Move walker. A transient window remains on
// the regular Go heap: the PXF lexer string + the hook closure's
// `value` parameter are immutable Go strings that live until the
// next GC after the lexer advances past the token; closing that
// requires a byte-slice hook variant or a Secret-aware lexer path,
// tracked as future protowire-go work. Block-form hint/fingerprint
// metadata still flows through the proto message and is folded
// onto the Map entry by parse.MoveInto.
func loadAndParse(ctx context.Context, ref chain.LayerRef, src source.Source, dec crypt.Decrypter, msg proto.Message, secrets *secret.Map, maxBytes int64) (*pxf.Result, error) {
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

	// Cap the read at maxBytes+1 so we can detect overflow: if the
	// resulting buffer is larger than maxBytes, the layer was
	// truncated and we reject it. Bounded memory regardless of how
	// large the underlying file is — defense against pathological
	// Sources returning huge files (or hanging on a slow reader
	// without a ctx deadline).
	raw, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("resolver: read layer %q: %w", ref.EntryID, err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("%w: layer=%q path=%q limit=%d",
			ErrLayerTooLarge, ref.EntryID, ref.Path, maxBytes)
	}

	var data []byte
	if ref.Encrypted {
		// Stream ciphertext into the Decrypter, which returns plaintext
		// in a memguard.LockedBuffer. We hold the buffer alive until
		// after parse, then destroy it. Parsing copies bytes into Go
		// strings (immutable, unwipeable until GC) — see parse.Move
		// godoc for the best-effort caveat that closes once codegen
		// emits a custom protoreflect implementation.
		buf, err := dec.Decrypt(ctx, raw, crypt.FormatForPath(ref.Path))
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
	// OnSecretField (protowire-go v0.76.0+) seals every scalar-
	// shorthand pxf.Secret value into a memguard Enclave during decode
	// rather than letting it land on Secret.value as an immutable Go
	// string. Hint and fingerprint metadata are always empty for
	// scalar shorthand by construction, so secret.FromString(v, "",
	// "") is the right shape; block-form Secrets retain their
	// hint/fingerprint on the proto message and parse.MoveInto picks
	// them up afterward.
	//
	// Any leading `@header chameleon.v1.LayerHeader { ... }` preamble
	// is collected by the PXF decoder (since protowire-go v0.72.0) and
	// surfaced on res.Directives(). chameleon's HandleDirectives runs
	// the sanity checks (id / environment / encrypted) against the
	// chain entry the layer was loaded for.
	res, err := pxf.UnmarshalOptions{
		SkipPostDecode: true,
		OnSecretField: func(path, value string) error {
			secrets.Set(path, secret.FromString(value, "", ""))
			return nil
		},
	}.UnmarshalFull(data, msg)
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
