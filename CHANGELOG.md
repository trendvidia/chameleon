# Changelog

All notable changes to `chameleon` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
