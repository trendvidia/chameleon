# Changelog

All notable changes to `chameleon` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.8.0] — 2026-05-12

Compile-time safety release. Closes [chameleon#6](https://github.com/trendvidia/chameleon/issues/6) end-to-end: with the new `with_resolved=true` plugin mode, the codegen now emits a `<Root>Resolved` wrapper per Secret-bearing message that exposes every field through typed accessor methods. The wrapper has no `Value` field, no public `*pxfv1.Secret`, and chained access for nested submessages / repeated / map containers. The remaining escape hatch (`Underlying()`) carries Go's standard `Deprecated:` marker so `staticcheck` / `golangci-lint` flag any consumer that still bypasses it. Combined with chameleon-lint (v0.7.0), `cfg.X.Value` is now caught at the typed-API tier (compile error) and at the CI tier (lint).

Phase 2b of chameleon#6 shipped across four incremental PRs ([#20](https://github.com/trendvidia/chameleon/pull/20), [#21](https://github.com/trendvidia/chameleon/pull/21), [#22](https://github.com/trendvidia/chameleon/pull/22), [#23](https://github.com/trendvidia/chameleon/pull/23)) on top of Phase 2a's scaffold ([#19](https://github.com/trendvidia/chameleon/pull/19)).

### Added

- **`protoc-gen-chameleon-go` wrapper mode** ([#6](https://github.com/trendvidia/chameleon/issues/6) Phase 2). Opt-in via `--chameleon-go_opt=with_resolved=true`. For every top-level message containing a `pxf.Secret` transitively, the plugin emits a `<Root>Resolved` struct with:

  - **Typed Secret accessors** for every Secret-bearing path. Returns `*secret.Secret` directly; the `.Value` field of `*pxfv1.Secret` is never exposed:

    ```go
    cfg := pkg.NewAppConfigResolved(res.Config, res.Secrets)
    buf, _ := cfg.DbPassword().Open()
    ```

  - **Chained nested access** through scalar message subfields ([#20](https://github.com/trendvidia/chameleon/pull/20)) and through repeated / map containers ([#21](https://github.com/trendvidia/chameleon/pull/21)). Each wrapper-bearing nested type gets its own `<Nested>Resolved` plus, for containers, a `<Parent><Field>Resolved` accessor with `Len()` and `At(i)` (repeated) or `Get(key)` (map). Out-of-range / absent key returns nil:

    ```go
    cfg.Primary().AuthToken().Open()                    // scalar nested
    cfg.Replicas().At(0).AuthToken().Open()             // repeated
    cfg.Sites().Get("acme").AuthToken().Open()          // map
    cfg.PrimaryCluster().Nodes().At(0).AuthToken().Open()  // mixed
    ```

  - **Field-by-field getters** for every non-Secret, non-chain-bearing field ([#22](https://github.com/trendvidia/chameleon/pull/22)): scalar primitives, bytes, enums, repeated, map, non-wrapper-bearing messages. Bodies delegate to protoc-gen-go's nil-safe `GetX()` accessors so the wrapper never panics on absent fields:

    ```go
    cfg.BaseUrl()         // string
    cfg.Enabled()         // bool
    cfg.MaxReplicas()     // int32
    cfg.FeatureFlags()    // []string
    cfg.Labels()          // map[string]string
    cfg.Mode()            // AppConfig_Mode enum
    cfg.Metadata()        // *Metadata (no Secrets inside)
    ```

  - **Function-form accessors stay emitted** alongside the wrapper — callers that prefer the standalone `AppConfigSecret_DbPassword(s *secret.Map)` form aren't disrupted.

- **`secret.Map.Delete(path)`** (already shipped in v0.7.0's parse.MoveInto; documenting here for completeness).

### Changed

- **`Underlying()` on every generated wrapper is now Deprecated** ([#23](https://github.com/trendvidia/chameleon/pull/23)). The method is still emitted — callers that pass the wrapper's payload to `proto.Marshal` / `proto.Equal` / other reflection-driven APIs still need it. But `staticcheck` (`SA1019`) and `golangci-lint`'s deprecation linters now flag any consumer that reads `cfg.Underlying().<Field>` in place of the typed accessor. Hard removal is gated on a future PR that delegates `proto.Message` onto the wrapper itself.

### Notes for upgraders

- **No breaking changes.** Wrapper mode is opt-in (`with_resolved=true`); existing generated files don't change shape until consumers add the flag. The existing function-form accessors (`AppConfigSecret_DbPassword(s)`) stay emitted regardless.
- **Plugin runtime unchanged.** Same protoc invocation pattern; the new flag is the only delta.
- **Consumers adopting wrapper mode** should treat `Underlying()` as the discouraged path even though it compiles. Use the typed accessors for routine reads; reach for `Underlying()` only when a third-party API genuinely needs `proto.Message`.

### Coordination

- No protowire-go bump required (v0.76.0 still the minimum).
- chameleon-lint (v0.7.0) continues to provide the CI-tier check for consumers who don't yet adopt wrapper mode.

## [0.7.0] — 2026-05-12

Plaintext-in-heap closure + Milestone 2 tooling. The headline change
is the resolver's adoption of protowire-go v0.76.0's
`UnmarshalOptions.OnSecretField` hook ([#7](https://github.com/trendvidia/chameleon/issues/7)),
which closes the heap-residency window for scalar-shorthand secrets
end-to-end: plaintext flows from the PXF lexer into a memguard
`Enclave` without an intermediate `*pxfv1.Secret.Value` Go string.
Alongside, this release ships the `chameleon migrate` CLI for bulk
schema-evolution edits across plaintext and encrypted layers
([#5](https://github.com/trendvidia/chameleon/issues/5)) and the
`chameleon-lint` static analyzer for catching `cfg.X.Value` reads
that v0.6.0 made silently observable
([#6](https://github.com/trendvidia/chameleon/issues/6) phase 1).

This release also rolls in v0.6.0's content — that section was
CHANGELOG'd in the v0.6.0 prep commit but never tagged as a separate
release, mirroring protowire-go's v0.73 / v0.74 / v0.75 rollup
pattern. The [0.6.0] section below is retained verbatim as the
historical record.

### Added

- **Memguard-direct decode for scalar-shorthand secrets** ([#7](https://github.com/trendvidia/chameleon/issues/7)). The resolver now wires
  protowire-go v0.76.0's `UnmarshalOptions.OnSecretField` hook on
  every per-layer decode. Each scalar-shorthand `pxf.Secret` value
  (`pw = "x"`, list elements, map values) is sealed into a memguard
  `Enclave` mid-decode and never assigned to `*pxfv1.Secret.Value`
  on the proto message. Closes the plaintext-in-heap window for the
  common secret form — see the rewritten "Plaintext-in-heap window"
  entry in README's Limitations section for the precise scope (block
  form still rides the post-decode walker, with the window now
  scoped to `[decode, MoveInto]` and limited to one form rather than
  every Secret-bearing assignment).

  New `parse.MoveInto(msg, *secret.Map) error` — walker that writes
  into a caller-owned `secret.Map`, tracks every Secret-typed path
  it reaches, and `Delete()`s any hook-routed entries whose path is
  no longer in the merged message. The orphan-cleanup pass is the
  load-bearing security check for the null-override case (lower
  layer scalar-shorthand secret + higher layer `null` = secret
  dropped). `parse.Move` / `MoveWithOptions` retain their existing
  signatures for consumers outside the resolver.

  New `(*secret.Map).Delete(path)` — destroys + removes one entry.
  Used by `MoveInto`'s orphan sweep.

- **`chameleon migrate` CLI** ([#5](https://github.com/trendvidia/chameleon/issues/5)). Walks a chain spec,
  decrypts each encrypted layer (via the v3.13.2 sops fork's
  `encrypt.UsingSameKeysAsWithContext`), applies a transform at the
  PXF AST level, re-encrypts preserving the original key set, and
  writes back:

  ```sh
  chameleon migrate \
      --chain=chain.pxf --layers=./layers --env=dev \
      --transform=add-field --path=feature_flag --bool=true

  chameleon migrate ... --transform=delete-field --path=feature_flag
  chameleon migrate ... --transform=rename-field --from=db.user --to=db.username
  ```

  Dotted paths navigate nested `Block`s for all three transforms.
  Auto-creation of intermediate blocks is deliberately out of scope
  for v0.7.0; missing parent on `add-field` is an error, and missing
  parent on `delete-field` / `rename-field` is treated as
  idempotent skip (lets fleet-wide runs proceed without per-layer
  guards). `--dry-run`, `--only=<id>` for single-layer targeting,
  and the full encrypted-layer round-trip are all in.

- **`chameleon-lint` static analyzer** ([#6](https://github.com/trendvidia/chameleon/issues/6) phase 1). `go/analysis`
  vet-like tool that flags `cfg.X.Value` reads and writes on
  `*pxfv1.Secret`-typed fields in consumer code. The v0.6.0
  per-Open observability made `cfg.X.Value == ""` after `Resolve`
  silently observable; the linter makes it a compile-time signal
  instead of a runtime surprise:

  ```sh
  go run github.com/trendvidia/chameleon/cmd/chameleon-lint ./...
  ```

  Phase 2 (codegen field-replacement so the field doesn't exist on
  the generated type at all) is deferred to a later milestone.

### Changed

- **`protowire-go` minimum bumped to `>= v0.76.0`** (was `>= v0.71.0`).
  Required for `UnmarshalOptions.OnSecretField`. Transitive consumers
  inherit the bump.

- **README Limitations entry rewritten** for the plaintext-in-heap
  window — scoped to block-form secrets, with a documented
  workaround (normalize layer secrets to scalar shorthand).
  Pipeline diagram updated to mention `MoveInto` + the hook.

### Notes for upgraders

- No breaking changes for resolver consumers — `Resolver.Resolve`'s
  contract is unchanged, `res.Secrets` has the same shape; only the
  residency of the underlying bytes changed.
- If you call `parse.Move` directly outside the resolver, nothing
  changes — `Move` / `MoveWithOptions` still work exactly as before.
  The new `MoveInto` is the resolver-internal variant that
  participates in the hook handshake.
- If you wrote tests that asserted on `*pxfv1.Secret.Value`'s
  contents AFTER `Resolve` (which already returned `""` since v0.6.0
  due to `parse.Move`), nothing changes either — the value is still
  `""` after Resolve; it just got there a step earlier in the
  pipeline.

## [0.6.0] — 2026-05-11

Observability + memory-residency hardening. Closes Milestone 1 of
[#9](https://github.com/trendvidia/chameleon/issues/9) (the
plaintext-in-heap meta-issue): chameleon now adopts the trendvidia/sops
fork v3.13.2 so plaintext flows from sops directly into mlocked
memory, and adds per-Open runtime metrics so operators can detect
secret-buffer leaks at runtime. WKT-metadata clobber detection lands
to catch a separate footgun where higher layers silently drop
lower-layer Secret metadata.

### Added

- **Per-`Secret.Open()` OTel observability** ([#3](https://github.com/trendvidia/chameleon/issues/3)). Three new instruments:
  - `chameleon.secret.opens` — counter, keyed by `chameleon.path` + `chameleon.env`.
  - `chameleon.secret.active` — up-down counter / gauge keyed by `chameleon.path`. Answers "is any goroutine holding this secret open right now?".
  - `chameleon.secret.open.duration` — histogram (seconds) keyed by `chameleon.path`.

  Wired via the existing `resolver.WithMeterProvider`. Without it, the noop provider makes every record cheap. See the new README "Debugging: pairing resolve-time provenance with runtime state" section for production query examples.

- **`Secret.Open()` returns `*OpenedBuffer`**. New wrapper type with `Bytes()` / `Locked()` / `Destroy()` methods. `Destroy()` records the duration histogram + decrements the active gauge before destroying the underlying memguard buffer. **Breaking-ish**: callers using type inference (`buf, _ := s.Open()`) work unchanged; callers that explicitly typed the variable as `*memguard.LockedBuffer` need to switch to `*secret.OpenedBuffer` (or call `buf.Locked()` for the underlying type).

- **WKT-metadata clobber warning** ([#2](https://github.com/trendvidia/chameleon/issues/2)). When a higher layer's atomic-WKT write whose own metadata is empty would replace a lower layer's value with non-empty metadata (e.g. `env/dev.pxf`'s `db_password = "y"` dropping `base.pxf`'s `hint`), the resolver records:
  - `Resolution.Trace.ClobberedMetadata []ClobberRecord` (when `WithTrace()`).
  - `chameleon.merge.clobbered_metadata` OTel counter increment.
  - With new `resolver.WithStrictMetadata()` option, the clobber is upgraded to a hard `Resolve` error.

  Currently detects only `pxf.Secret` metadata (`hint`, `fingerprint`); other atomic WKTs have no metadata concept.

- **`secret.NewMap` accepts options**. `secret.WithInstruments(i, env)` is the load-bearing one; wired by the resolver to thread the per-Open instruments through `parse.MoveWithOptions`. Backward compat preserved: `secret.NewMap()` with no args still works.

- **`parse.MoveWithOptions(msg, opts ...secret.MapOption)`** — sibling of `parse.Move` that takes Map options. Existing `parse.Move` still works.

### Changed

- **Adopt trendvidia/sops v3.13.2 fork** ([#11](https://github.com/trendvidia/chameleon/pull/11)). `go.mod` adds:

  ```
  replace github.com/getsops/sops/v3 => github.com/trendvidia/sops/v3 v3.13.2
  ```

  `chameleon/sops/decrypt.go` switches from `decrypt.Data` + `memguard.NewBufferFromBytes` to the fork's `decrypt.DataIntoWriterContext` + a new internal `memguardWriter`. **Closes the largest plaintext-in-heap window** (Window A of [#9](https://github.com/trendvidia/chameleon/issues/9) phase 1): plaintext now lands directly in mlocked memory without a transient `[]byte` on the regular Go heap. `context.Context` threads end-to-end through the sops keyservice + KMS layers; a deadlined ctx aborts in-flight KMS round-trips for AWS/GCP/Azure/Vault natively, HuaweiCloud KMS via a goroutine shim.

- **ROADMAP**: "Sops `context.Context` mid-call cancellation" moves from "tracked upstream" to **CLOSED via fork**. `getsops/sops#2179` remains open as an upstream PR candidate; until merged, chameleon consumes via the `replace` directive.

### Not yet closed (chameleon#9 follow-up phases)

- **Window B** (per-field secret strings during PXF parse) — [chameleon#7](https://github.com/trendvidia/chameleon/issues/7), protowire-go work.
- **Window C** (`Secret.Value` field on cfg struct until `parse.Move` clears it) — partially closed by phase 2; fully closed by phase 3.
- **Footgun D** (`cfg.X.Value` exists at the type level) — [chameleon#6](https://github.com/trendvidia/chameleon/issues/6) phase 2 codegen field-replacement.

## [0.5.0] — 2026-05-11

Upstream-handoff release. `@header` preamble parsing moves from
chameleon's own byte scanner (`internal/header/scan.go`, ~250 lines)
to native PXF grammar support introduced in protowire-go v0.72.0
(`Result.Directives()`). End-to-end behavior is identical — same
sanity checks, same CLI output, same file format — but the chameleon
side is ~500 lines lighter and the directive vocabulary becomes
extensible (chameleon can add `@trace`, `@provenance`, etc. later
without re-tokenizing).

### Changed

- **Resolver consumes `pxf.Result.Directives()`.** `loadAndParse` is
  back to a single `pxf.UnmarshalFull` call; chameleon iterates the
  resulting directive list and calls `header.HandleDirectives(...)`
  to run the existing id / environment / encrypted checks.

- **`@header` type tag is now optional.** Authors may write
  `@header chameleon.v1.LayerHeader { ... }` (preferred — file is
  self-documenting) or `@header { ... }` (terse). chameleon owns
  the directive-name → LayerHeader mapping; if the type tag is
  present, it must match. The block contents are validated against
  the same descriptor either way.

- **`chameleon verify` and `chameleon hash`** now go through
  `pxf.Parse` + `Document.BodyOffset` instead of the local scanner.
  Output identical.

### Removed

- **`internal/header/scan.go` and `scan_test.go`** (498 lines).
  Replaced by `pxf.Result.Directives()`. Functions previously
  exported (`header.Split`) are gone; consumers should switch to
  iterating `Result.Directives()`.

### Updated

- **`require github.com/trendvidia/protowire-go`** bumped from
  v0.71.0 to v0.72.0. Spec change tracked in
  [`protowire` v0.72.0](https://github.com/trendvidia/protowire/releases/tag/v0.72.0).

## [0.4.0] — 2026-05-11

`@header` preamble release. Layer files can now carry sanity-check
metadata (`id` / `environment` / `encrypted`) that the resolver
enforces against the chain entry, plus opt-in `content_sha256`
verification via two new CLI subcommands.

### Fixed

- **`examples/simple/` now parses cleanly.** Three issues that broke the
  canonical example end-to-end:
  - Layer files (`layers/base.pxf`, `layers/env/dev.pxf`) started with
    `@header chameleon.v1.LayerHeader { ... }`, a directive the v0.3.0
    PXF parser does not recognize (`@type` is the only `@`-form the
    lexer accepts; everything else returns `ILLEGAL`). The resolver
    now peels the header off itself before handing the body to
    `pxf.UnmarshalFull` — see the new `@header` feature below.
  - `chain.pxf` separated fields inside `{ ... }` blocks with commas
    (`{ name = "sandbox", extends = "prod" }`), which the README's
    "PXF authoring gotchas" section already explicitly calls out as
    invalid. Same bug repeated in the `sources` map entries.
  - The README still documents commas-inside-blocks as the failure
    mode; the example was the thing that was wrong.
  - `env/dev.pxf` did `base_url = "dev.${base_url}"`, the exact
    self-referencing pattern the README's "tweak rather than
    override" section warns against (interpolation runs post-merge
    against the final state, so a self-reference is a cycle).
    Rewrote `base.pxf` and `env/dev.pxf` to use the documented
    intermediate-root pattern: `domain` is the root variable;
    `base_url` and `api_url` interpolate against it; dev.pxf
    overrides only `domain`.

### Added

- **`@header chameleon.v1.LayerHeader { ... }` preamble** at the top of
  layer files. The resolver scans for a leading `@header` block before
  parsing the schema-typed body and runs sanity checks against the
  chain entry the layer was loaded for:
  - `id` — must match `LayerEntry.id` (catches file-at-wrong-slot).
  - `environment` — must match the env the layer materialization is
    bound to (catches "prod settings ended up in the dev file" mistakes
    in PER_ENV layers).
  - `encrypted` — must match `LayerEntry.encrypted` (catches plaintext-
    in-encrypted-slot and vice versa).
  - `notes` — informational, parsed and ignored at resolve time.
  - `content_sha256` — opt-in verification via the new `chameleon
    verify` CLI (below). The resolver itself parses the field but
    does not run the check; adding a logger-driven warning at
    resolve time is still deferred.

  All fields are optional. Author-omitted fields are not checked (PXF
  three-state presence). The whole preamble is optional; layer files
  without `@header` parse exactly as before.

- **`chameleon verify` subcommand** — walks a chain spec, opens each
  resolved layer file, and checks `@header.content_sha256` against
  the file's actual body hash. Non-zero exit on mismatch. Encrypted
  layers are skipped (sops already authenticates them). Pass
  `--strict` to fail on layers without a hash.

- **`chameleon hash <file>` subcommand** — prints the canonical body
  SHA-256 an author should paste into `@header.content_sha256`. The
  canonical bytes are everything after the closing `}` of the
  `@header` preamble (or the whole file if no header), so adding /
  editing / removing fields inside `@header` does not change the
  hash.

  Implementation: protowire-go's PXF lexer still only knows about
  `@type`, so chameleon does the preamble extraction itself in
  `internal/header` (a byte scanner that mirrors PXF tokenization
  rules for strings, comments, and escapes). A future protowire-go
  release with generic `@<directive>` support would let this
  collapse to a `pxf.UnmarshalDocument`-style call — tracked in
  `ROADMAP.md`.

- **README install section for `protoc-gen-chameleon-go`** spelling out
  the `GOPRIVATE` / `insteadOf` / `GH_TOKEN` prerequisites needed to
  `go install` from the private module set. Same setup applies to
  consumers that import chameleon directly.

## [0.3.0] — 2026-05-10

Observability + tooling release. OTel metrics, the `chameleon explain`
CLI, runtime ChainSpec loaders, fuzz harnesses, leak guard.

### Added

- **OpenTelemetry metrics** via `resolver.WithMeterProvider(mp)`:
  `chameleon.resolve.duration` (histogram, seconds),
  `chameleon.resolve.errors` (counter), `chameleon.resolve.layers`
  (histogram), `chameleon.resolve.secrets` (histogram). All carry a
  `chameleon.env` attribute. Without the option, instruments use the
  noop provider.
- **`chameleon explain` CLI** under `cmd/chameleon`. Resolves a
  config against a chain spec + layer dir and prints per-field
  provenance (which layer set the value, whether it came from a
  default, whether interpolation rewrote it, whether it's
  sensitive). Schema compiled at runtime via `bufbuild/protocompile`
  — no codegen step required.
- **`chain.LoadSpec(path)` / `chain.ParseSpec(data)`** — runtime
  helpers that compile the canonical `chameleon.v1.ChainSpec`
  descriptor inline and parse a `chain.pxf` file or bytes. Means
  consumers don't need to figure out how to materialize a ChainSpec
  themselves.
- **Fuzz harnesses** for the two parsers chameleon owns:
  `interpolate.FuzzApply` (template expansion) and
  `chain.FuzzExpand` (path templating + context). Property: never
  panic. Seed corpus exercises malformed templates, invalid UTF-8,
  deep paths.
- **Goroutine-leak guard test** — runs Load+Destroy in a 500-iter
  loop and verifies the goroutine count doesn't grow.
- **`ROADMAP.md`** — explicit deferred items (typed ChainSpec
  bindings, plugin field-replacement variant) and out-of-scope
  non-goals.

### Removed

- Proto2 `GroupKind` branches throughout the pipeline. PXF is
  proto3-only by spec; the codec never produces a `GroupKind`
  field. The defensive `case MessageKind, GroupKind:` clauses just
  made the supported surface ambiguous to readers.

## [0.2.0] — 2026-05-10

Documentation + ergonomics release. Top-level README, simple-case API
shortcut, and a few small cleanups from the post-v0.1.0 review.

### Added

- **Top-level `chameleon.Load[T]` shortcut** for the common simple
  case (one filesystem-backed source, no encryption, no extra
  options). Collapses the 5-line `resolver.New` + `Resolve` setup to
  one call. For complex setups (sops, multiple sources, request
  context, trace) callers go straight to the `resolver` package.
- **`README.md`** — usage guide, pipeline diagram, concepts (chain,
  layers, secrets), codegen walkthrough, PXF authoring gotchas,
  known limitations.
- **`CHANGELOG.md`** — Keep-a-Changelog format, seeded from v0.1.0.
- **`internal/pathfmt`** — single source of truth for the dotted
  path format `parse.Move`, `interpolate.Apply`, and the codegen
  plugin must agree on byte-for-byte. Replaces three duplicated
  copies of `formatMapKey`.

### Fixed

- **`source.FS.Open` and `Stat` now honor `ctx.Context`** —
  short-circuit on a pre-cancelled context. The interface always
  took ctx; the FS impl was ignoring it.
- **`Resolution.Destroy` doc** clarifies what's actually wiped: by
  the time Destroy runs, `parse.Move` has already cleared
  `*pxfv1.Secret.Value` on the proto message; what Destroy wipes is
  the memguard Enclaves the Map holds.

## [0.1.0] — 2026-05-10

Initial public release. Schema-driven layered configuration with sops + memguard.

### Added

- **Resolver pipeline**: `chain.Expand` → source + sops → `pxf.UnmarshalFull` → `merge.Merge` → `defaults.Apply` → `interpolate.Apply` → `validate.Validate` → `parse.Move`.
- **Three-state presence model** (set / null / absent) drives layer fall-through.
- **WKT-atomic merge** — `pxf.Secret`, `Timestamp`, `BigInt`, etc. replace whole rather than deep-merging.
- **Sops integration** decrypts encrypted layer files in-process and streams plaintext directly into a memguard `LockedBuffer`.
- **`protoc-gen-chameleon-go`** protoc plugin emitting a sidecar `*.chameleon.go` with typo-safe accessors against `secret.Map`. Walks `repeated UserMessage` and `map<K, UserMessage>` containers.
- **Trace plumbing** — opt-in per-field provenance (`WithTrace()`).
- **`secret.Map`** — thread-safe Enclave-keyed map with idempotent `Destroy` and snapshot `Range`.
- **`Resolution.Destroy`** — `sync.Once`-protected, concurrency-safe.
- **`examples/buf/`** — working `buf.gen.yaml` invoking both `protoc-gen-go` and `protoc-gen-chameleon-go`.
- **`examples/simple/`** — chain.pxf + base/dev layers matching the canonical "production base url is mybigproject.com but for dev it is dev.mybigproject.com" use case.

### Compatibility

- Go 1.25+
- `github.com/trendvidia/protowire-go` v0.71.0+ (pxf.Secret WKT recognition + layered-config exports)
- `github.com/getsops/sops/v3` v3.13.0+
- `github.com/awnumar/memguard` v0.22.5+

### Known limitations

Documented in `README.md`:
- Sidecar plugin doesn't hide `*pxfv1.Secret.Value` on the proto-generated struct (discipline-based protection; field-replacement plugin variant pending).
- Plaintext-in-heap window between PXF parse and `parse.Move` (Go string immutability).
- Sops API has no `ctx.Context` hook for in-flight cancellation.
