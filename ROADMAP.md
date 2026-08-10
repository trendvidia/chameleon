# Roadmap

Each milestone groups issues that ship together. Detailed designs live in the linked issues; this file is the index.

## Status: feature-complete

All three planned milestones shipped. Pre-1.0 development is in a soak period; v1.0 cuts after one release cycle without API churn. See [v1.0 readiness criteria](#v10-readiness-criteria) below.

## Milestone 1 — v0.6.0 (chameleon-internal, cheap)

All three reuse the existing OTel meter plumbing. No cross-repo coordination.

- **[#2 — merge: warn when a higher layer clobbers WKT metadata](https://github.com/trendvidia/chameleon/issues/2)** — detect when `base.pxf`'s `db_password { value="x" hint="..." }` gets clobbered by `env/dev.pxf`'s `db_password = "y"`. Surface via `Resolution.Trace` + new OTel counter `chameleon.merge.clobbered_metadata`. Opt-in strict mode upgrades to error.
- **[#3 — secret: per-Open() observability](https://github.com/trendvidia/chameleon/issues/3)** — three new instruments (`chameleon.secret.opens` counter, `.active` gauge, `.open.duration` histogram). Wraps memguard's `LockedBuffer` so chameleon can track active holders, not just resolve-time counts.
- **[#4 — docs: pair resolve-time provenance with runtime observability](https://github.com/trendvidia/chameleon/issues/4)** — README "Debugging" section that distinguishes `chameleon explain` (compile-time: which layer set X) from per-Open metrics (runtime: who's holding X right now). Ships with #3.

Plus closed via fork: **sops context.Context** — first shipped in [trendvidia/sops v3.13.2](https://github.com/trendvidia/sops/releases/tag/v3.13.2) and adopted by chameleon (via ``decrypt.DataIntoWriterContext`` in `chameleon/sops/decrypt.go`). Now consumed under the fork's canonical module path `github.com/trendvidia/sops/v4` — no `replace` directive needed. Upstream [getsops/sops#2179](https://github.com/getsops/sops/issues/2179) remains open as a pull-request candidate. Cancellation now honored end-to-end through AWS / GCP / Azure / Vault natively, HuaweiCloud KMS via goroutine shim (see fork [#10](https://github.com/trendvidia/sops/issues/10)).

## Milestone 2 — v0.7.0 (new tooling)

- **[#5 — cmd/chameleon migrate: bulk-edit encrypted layers](https://github.com/trendvidia/chameleon/issues/5)** — `chameleon migrate --transform=add-field --path=db.password --kind=secret` decrypts every layer in a chain spec, applies the transform at the PXF AST level, re-encrypts. Adds an encrypt wrapper to chameleon's `sops` package alongside the existing decrypt wrapper.
- **[#6 phase 1 — chameleon-lint: flag `cfg.X.Value` reads at CI time](https://github.com/trendvidia/chameleon/issues/6)** — Go AST analyzer that catches the `pxfv1.Secret.Value` footgun in consumer code without waiting for the full field-replacement codegen. 95% of the protection for 5% of the cost.

## Milestone 3 — v0.8.0 / v0.9.0 (cross-repo, shipped)

Originally scoped as "v1.0 prep, weeks of work." Shipped in two consecutive minor releases:

- **[#6 — protoc-gen-chameleon-go `with_resolved` wrapper mode](https://github.com/trendvidia/chameleon/issues/6)** — opt-in plugin flag emits a `<Root>Resolved` wrapper per Secret-bearing message. Typed Secret accessors return `*secret.Secret` directly; nested / repeated / map containers route through chained accessor helpers; field-by-field getters cover every non-Secret shape. v0.9.0 follow-up implements `proto.Message` on the wrapper via delegation and hard-removes the deprecated `Underlying()` escape hatch. Net result: `cfg.DbPassword.Value` is a compile error end-to-end. Phase 1 (chameleon-lint) stays in place as defense-in-depth for consumers who don't opt into wrapper mode.
- **[#7 — protowire-go `OnSecretField` decoder hook](https://github.com/trendvidia/chameleon/issues/7)** — protowire-go [v0.76.0](https://github.com/trendvidia/protowire-go/releases/tag/v0.76.0) added the hook for scalar shorthand; [v0.77.0](https://github.com/trendvidia/protowire-go/releases/tag/v0.77.0) extended it to block form. Chameleon's resolver wires it on every per-layer decode so plaintext bytes flow from the PXF lexer straight into a memguard Enclave without an intermediate `*pxfv1.Secret.Value` Go string. `parse.MoveInto` folds block-form hint/fingerprint metadata back onto the Map entry via `(*secret.Secret).SetMetadata` for diagnostic visibility. Window closed for both authoring forms.

## Post-v0.9.0 additions

- **[Proposal 0001 — `kind = "env"` source](docs/proposals/0001-env-source.md)** — `source.Env(desc, ...)` reads process environment variables at resolve time and emits a synthetic PXF layer the existing pipeline merges normally. Eliminates the parallel "sync env from chameleon" sidecar pattern downstreams were starting to need. Scalar coercion + well-known leaves (`pxf.Secret`, `google.protobuf.Duration`, `google.protobuf.Timestamp`) covered in v1; opt-out annotation + `explain`/`reveal` provenance UX deferred to follow-ups documented in the proposal.
- **Distribution bundles** (PRs #80 → #84) — `chameleon bundle build` produces a per-module zip artifact (chain spec + narrowed, re-encrypted layers) distributable as a single secret-manager value. A `BundleManifest` declares per-module allow/deny globs over secret-bearing field paths so a compromised bundle only leaks that module's surface area; `recipients_by_env` keeps env isolation in the ciphertext of a single env-agnostic bundle. Consumer side is the public `bundle` package: `bundle.Open` / `bundle.OpenBase64` return `Bundle{FS, Chain, Audit}` that plugs straight into `chameleon.Load`. The `--base64` flag on `bundle build` matches `bundle.OpenBase64` for paste-into-secret-manager-field workflows.

## v1.0 readiness criteria

A v1.0 cut promises API stability — breaking changes thereafter require a major version bump. The criteria to clear before tagging v1.0:

- **One release cycle of soak without API churn.** v0.9.0 just landed; let it bake for a release before locking in. Hidden surface that real consumers exercise tends to surface in this window.
- **No open enhancement issues blocking core flows.** All listed milestones are shipped; the two remaining "Other deferred items" below are explicitly non-blocking (typed ChainSpec access is an internal refactor; resolver-time content_sha256 verification has out-of-band CLI coverage).
- **README + godoc cross-checked against current behavior.** Sweep for stale notes referencing deprecated or removed APIs (e.g. anything that still mentions `Underlying()` post-v0.9.0).
- **Cross-repo pins documented.** protowire-go minimum, sops fork dependency, memguard version — all listed in the compatibility table.

Out of scope for v1.0:
- Anything in "Out of scope (intentional non-goals)" stays out — file-watch, logging hooks, proto2 groups.
- Anything in "Other deferred items" stays deferred. Not required for v1.0.

When v1.0 cuts, the [public API surface freeze](#public-api-surface-at-v10) section below documents what's covered by the stability promise.

## Public API surface (at v1.0)

These packages and types are stable at v1.0; consumers may rely on their signatures across the 1.x line.

The repo publishes three modules since v0.23.0 — `github.com/trendvidia/chameleon` (library), `.../chameleon/sops` (backend), `.../chameleon/cmd/chameleon` (CLI). Import paths are unchanged; the split keeps key management out of a library consumer's module graph ([#130](https://github.com/trendvidia/chameleon/issues/130)). Tagging order is in [docs/RELEASING.md](docs/RELEASING.md).

Enumeration is exhaustive — `go doc <pkg>` at the v1.0 tag should produce the same set. `internal/*` packages are excluded by Go's compiler-enforced rule and aren't listed.

### `github.com/trendvidia/chameleon` (top-level convenience)

- **Functions**: `Load[T proto.Message](sample T, spec proto.Message, layersFS fs.FS, env string) (*resolver.Resolution[T], error)` — shorthand wrapper around `resolver.New` + `Resolve` for the common "load a single config" use case.

### `github.com/trendvidia/chameleon/resolver`

- **Functions**: `New[T proto.Message](sample T, spec proto.Message, opts ...Option) (*Resolver[T], error)`, `WithDecrypter`, `WithDecrypterNamed`, `WithoutDecrypter`, `WithMaxLayerBytes`, `WithMeterProvider`, `WithSource`, `WithStrictMetadata`, `WithTrace`.
- **Types**: `Resolver[T proto.Message]`, `Resolution[T proto.Message]`, `Request`, `Trace`, `TraceEntry`, `ClobberRecord`, `Option`.
- **Errors**: `ErrNoSource`, `ErrNoDecrypter`, `NoDecrypterError` (the concrete form behind `ErrNoDecrypter`; fields `LayerID string`, `Disabled bool`, `Err error`), `ErrRequiredLayerMissing`, `ErrLayerTooLarge`, `ErrSchemaViolation`.
- **Methods**: `(*Resolver[T]).Resolve(ctx, Request) (*Resolution[T], error)`, `(*Resolution[T]).Destroy()`.
- **Methods on `*Trace`**: `Get(path) TraceEntry`, `Has(path) bool`, `Paths() []string`.

### `github.com/trendvidia/chameleon/chain`

- **Functions**: `LoadSpec(path)`, `ParseSpec(data)`, `Expand(spec, env, ctx)`.
- **Types**: `LayerRef`, `Options`.
- **Methods**: `(Options).Expand(spec, env, ctx) ([]LayerRef, error)`.
- **Errors**: `ErrUnknownEnvironment`, `ErrCyclicAncestry`, `ErrUnknownContextKey`, `ErrMissingRequiredContext`, `ErrContextValueNotAllowed`, `ErrUnresolvedRequiredLayer`.

### `github.com/trendvidia/chameleon/secret`

- **Functions**: `NewMap(opts ...MapOption)`, `NewInstruments(mp metric.MeterProvider)`, `WithInstruments(i, env)`, `FromString(s, hint, fingerprint)`, `FromLockedBytes(buf, hint, fingerprint)`.
- **Types**: `Secret`, `Map`, `MapOption`, `Instruments`, `OpenedBuffer`.
- **Methods on `*Secret`**: `Open() (*OpenedBuffer, error)`, `Destroy()`, `IsSet() bool`, `Hint() string`, `Fingerprint() string`, `SetMetadata(hint, fingerprint string)`.
- **Methods on `*Map`**: `Get(path)`, `Has(path)`, `Len()`, `Set(path, s)`, `Delete(path)`, `Range(fn)`, `Destroy()`.
- **Methods on `*OpenedBuffer`**: `Bytes() []byte`, `Locked() *memguard.LockedBuffer`, `Destroy()`.
- **Errors**: `ErrDestroyed`, `ErrUnset`.

### `github.com/trendvidia/chameleon/parse`

- **Functions**: `Move(msg) (*secret.Map, error)`, `MoveWithOptions(msg, opts ...secret.MapOption) (*secret.Map, error)`, `MoveInto(msg, secrets *secret.Map) error`.
- **Errors**: `ErrInvalidSecret`.

### `github.com/trendvidia/chameleon/merge`

- **Functions**: `Merge(dst, src, srcPresence) error`, `MergeWithOptions(dst, src, srcPresence, opts Options) error`.
- **Types**: `Options`, `ClobberInfo`.

### `github.com/trendvidia/chameleon/interpolate`

- **Functions**: `Apply(msg, opts ...Option) error`, `OnApplied(fn) Option`.
- **Types**: `Option`.
- **Errors**: `ErrCycle`, `ErrInvalidReference`, `ErrMalformedTemplate`, `ErrUnknownReference`.

### `github.com/trendvidia/chameleon/defaults`

- **Functions**: `Apply(msg, present, opts ...Option) error`, `OnApplied(fn) Option`.
- **Types**: `Option`.

### `github.com/trendvidia/chameleon/validate`

- **Functions**: `Validate(msg, present) error`.
- **Errors**: `ErrMissingRequired`.

### `github.com/trendvidia/chameleon/crypt`

The decryption contract, kept free of any implementation so consumers link one only by choice (see CHANGELOG [#130](https://github.com/trendvidia/chameleon/issues/130)).

- **Functions**: `FormatForPath(path) Format`, `Register(name, Backend)`, `Unregister(name) bool`, `Registered() []string`, `Get(name) (Backend, error)`, `SoleDecrypter() (Decrypter, string, error)`.
- **Types**: `Decrypter`, `Encrypter`, `Recipients` (with `IsEmpty()` method), `Backend`, `Format` (with `String()` method).
- **Constants**: `FormatAuto`, `FormatProtowire`, `FormatBinary`, `FormatDotenv`, `FormatINI`, `FormatJSON`, `FormatYAML`.
- **Errors**: `ErrNoBackend`, `ErrAmbiguousBackend`, `ErrUnknownBackend`, `ErrEncryptOnlyBackend`.

### `github.com/trendvidia/chameleon/sops`

One implementation of the above, registered as `"sops"` from `init`.

- **Functions**: `Default() crypt.Decrypter`, `DefaultEncrypter() crypt.Encrypter`, `RecipientsFromKeyName(name) (crypt.Recipients, error)`.
- **Constants**: `BackendName`.
- **Errors**: `ErrEmptyCiphertext`, `ErrPlaintextTooLarge`, `ErrEmptyPlaintext`, `ErrNoRecipients`.
- **Deprecated aliases** for the `crypt` types of the same name, kept so existing consumer code compiles unchanged: `Decrypter`, `Encrypter`, `Recipients`, `Format`, the `Format*` constants, `FormatForPath`.

### `github.com/trendvidia/chameleon/source`

- **Functions**: `FS(fsys fs.FS) Source`.
- **Types**: `Source`, `Info`.
- **Errors**: `ErrNotFound`.

### `github.com/trendvidia/chameleon/bundle`

- **Functions**: `Open(r io.Reader) (*Bundle, error)`, `OpenBase64(r io.Reader) (*Bundle, error)`.
- **Types**: `Bundle` (fields `FS fs.FS`, `Chain proto.Message`, `Audit []byte`).
- **Errors**: `ErrMissingChain`.

### `github.com/trendvidia/chameleon/wkt`

- **Functions**: `Atomic(d protoreflect.MessageDescriptor) bool`.

### CLI binaries (stable)

- `chameleon` with subcommands: `explain`, `get`, `reveal`, `verify`, `migrate`, `hash`, `lint`, `bundle build`. The `lint` subcommand is a `go/analysis` singlechecker; usable directly (`chameleon lint ./...`) or via `go vet -vettool`. `bundle build` produces per-module distribution bundles (`--base64` for paste-friendly output).

### Codegen output (stable)

- `protoc-gen-chameleon-go` function-form accessors (`<Root>Secret_<Path>(s *secret.Map, ...)`).
- `protoc-gen-chameleon-go` wrapper mode (`<Root>Resolved` + chained access + field-by-field getters + `proto.Message` delegation). Opt-in via `--chameleon-go_opt=with_resolved=true`.

---

## Other deferred items

### P2: Typed `ChainSpec` access via generated bindings

`chain/expand.go` accesses `ChainSpec` fields by name via `protoreflect.FieldDescriptors.ByName(...)`. Typos in `expand.go` itself return `nil` silently rather than failing at compile time. Once `protoc-gen-go` is wired against `proto/chameleon/v1/chain.proto`, swap the descriptor-driven access for typed field access. **Why deferred:** requires setting up codegen for chameleon's own `.proto` files. **Impact when done:** type safety in `chain/expand.go`, no user-visible API change.

### Resolver-time `content_sha256` verification

The `chameleon verify` CLI checks `@header.content_sha256` on demand. The resolver itself parses the field but doesn't run the check at every `Resolve` — that would need a warning channel (the spec says "warn on mismatch, don't reject") and chameleon has intentionally avoided a logger ABI (see "Logging hooks" under non-goals below). A `resolver.WithContentSHA256Check` option could surface as either a hard-error mode or via the OTel meter; neither is yet pressing. **Why deferred:** out-of-band CLI verification covers the typical "did this file get tampered with?" workflow without inflating the resolver's hot path.

---

## Skipped — low value vs. cost

### Real-sops integration test with programmatic encryption

The chameleon `sops.Decrypter` wrapper is plumbing around `trendvidia/sops/v4/decrypt.Data`. Its non-trivial logic (memguard copy, ciphertext wipe, error prefix propagation) is tested at the unit level. Adding a "real sops file with a known age key" round-trip test would only verify `decrypt.Data` works against real bytes — sops's job, not chameleon's. Programmatic encryption at test setup requires unstable sops internals (`EncryptTree`, `Tree`); couples the test suite to a non-stable API for marginal gain.

Production consumers wanting end-to-end confidence can drop their own sops-encrypted fixture into the test directory and exercise it under `-tags integration` — two lines.

Note: the `chameleon migrate` work in [#5](https://github.com/trendvidia/chameleon/issues/5) DOES need a programmatic-encrypt wrapper, but that's a CLI-side capability for production migrations, not a test setup tool.

---

## Out of scope (intentional non-goals)

### File-watch / hot-reload

Resolved configs are immutable; if you need reload-on-change, wrap the `Resolver` with a watcher in your own code. Pulling fsnotify or similar into chameleon's surface would force a transitive dep on every consumer.

### Logging hooks

Distinct from metrics (which we DO support — see OTel). Chameleon doesn't emit log lines; log around the call site if you want one per `Resolve`. Adding a `slog.Logger` option inflates the public surface for marginal value.

### Proto2 group support

PXF is proto3-only by spec; the codec never produces a `GroupKind` field. Chameleon's pipeline matches that scope. If a future PXF revision admits proto2, this gets revisited.

### Sops `context.Context` mid-call cancellation — CLOSED via fork

Resolved in [trendvidia/sops v3.13.2](https://github.com/trendvidia/sops/releases/tag/v3.13.2) and adopted by chameleon (see v0.6.0). Chameleon's `Decrypter.Decrypt` now threads `ctx` through `decrypt.DataIntoWriterContext`; in-flight KMS round-trips abort on ctx deadline / cancellation across AWS / GCP / Azure / Vault. HuaweiCloud KMS is approximated via a goroutine shim — tracked at [sops fork #10](https://github.com/trendvidia/sops/issues/10). Upstream [getsops/sops#2179](https://github.com/getsops/sops/issues/2179) remains open as an upstream PR candidate; chameleon consumes the fork directly under its canonical `github.com/trendvidia/sops/v4` module path — no `replace` directive needed.
