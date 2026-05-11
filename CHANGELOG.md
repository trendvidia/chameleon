# Changelog

All notable changes to `chameleon` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
