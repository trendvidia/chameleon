# Changelog

All notable changes to `chameleon` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.14.2] — 2026-05-14

Additive patch — `chameleon explain` gains a `--include` flag for
proto-import search paths, mirroring `protoc -I`. Closes a usability
gap on schemas with non-standard transitive imports.

### Added

- **`chameleon explain --include <dir>` (repeatable)**
  ([#66](https://github.com/trendvidia/chameleon/pull/66)).
  Before this release, `compileSchema` built a `SourceResolver`
  whose only entry was an in-memory map of `{schemaPath: data}`;
  standard WKTs resolved via `WithStandardImports`, but custom
  imports — `pxf/secret.proto` is the motivating case —
  produced:

  ```
  chameleon explain: compile <path>: <path>:N:C: could not resolve
  path "pxf/secret.proto": file does not exist
  ```

  The fix swaps the resolver from in-memory `Accessor` to
  `ImportPaths`. The schema's own directory is prepended
  implicitly (so a schema with no non-WKT imports still
  compiles without a flag), and `--include` appends further
  search roots. Each path is `filepath.Abs`-ed so protocompile's
  import-relative-to-import-path joining is stable across
  callers running from arbitrary CWDs. The schema file's
  existence is checked up-front via `os.Stat` so a missing
  schema produces a clean `read schema <path>: ...` error
  instead of the deeper `could not resolve` message
  protocompile would otherwise emit from inside its
  import-search loop.

  Reported by a consumer running `chameleon explain` against
  `steward.config.v1.AppConfig`. Post-release invocation:

  ```sh
  chameleon explain \
      --schema    ../proto/steward/config/v1/config.proto \
      --include   ../proto \
      --message   steward.config.v1.AppConfig \
      --chain     chain.pxf --layers layers --env dev
  ```

  Callers without custom imports continue to work without
  `--include` — the implicit schema-dir prepend preserves the
  pre-existing no-flags case.

## [0.14.1] — 2026-05-14

Bug-fix patch — `chameleon lint` now skips files marked as generated
per the standard Go convention, eliminating false positives on the
proto-go-emitted `Secret.GetValue() string` accessor.

### Fixed

- **`chameleon lint` skips generated files**
  ([#64](https://github.com/trendvidia/chameleon/pull/64)). The
  analyzer's file loop now short-circuits on any file whose header
  comments (before the package clause) match the standard
  `^Code generated .* DO NOT EDIT\.$` line documented in
  `go/build`. Before the fix, the standard accessor that
  `protoc-gen-go` emits for every `Secret`-typed message field:

  ```go
  func (x *Secret) GetValue() string {
      if x != nil {
          return x.Value
      }
      return ""
  }
  ```

  was flagged as a `pxfv1.Secret.Value` read — a false positive,
  since the read is legitimate inside the generator's output (the
  plaintext lands there before `chameleon.parse.Move` sweeps it
  into memguard) and consumer code is the only place the analyzer
  should catch direct `.Value` access. Reported by a consumer
  running `chameleon lint ./...` against the `steward-api` Go
  tree; the path-based exclude workaround
  (`chameleon lint $(go list ./... | grep -v /internal/gen/)`)
  becomes obsolete after this release.

  Both `//` line comments and `/* */` block comments are honored.
  New `cmd/chameleon/testdata/src/genpkg/` fixture mimics
  proto-go output; new `TestAnalyzer_GeneratedFilesSkipped`
  verifies the skip and would fail if the regression returned.
  Existing `TestAnalyzer_FlagsBadPatterns` and
  `TestAnalyzer_GoodPatternsClean` pass unchanged — the skip
  only affects files carrying the generated-file marker, which
  neither fixture has.

## [0.14.0] — 2026-05-14

Tooling release: the `chameleon-lint` standalone binary folds into the
main CLI as `chameleon lint`, so the project ships one CLI plus the
protoc plugin. Adds a top-level `Makefile` covering the common
developer workflow.

The merge is technically breaking — anyone running
`go install github.com/trendvidia/chameleon/cmd/chameleon-lint@latest`
or the installed `chameleon-lint` binary needs to switch to
`chameleon lint`. Behavior, exit codes, and diagnostic text are
otherwise unchanged.

### Changed

- **`chameleon-lint` → `chameleon lint` subcommand**
  ([#62](https://github.com/trendvidia/chameleon/pull/62)). The
  `pxfv1.Secret.Value` analyzer (and its testdata) move from
  `cmd/chameleon-lint/` to `cmd/chameleon/lint.go`; the old binary
  directory is removed. The subcommand uses a small custom driver
  (`packages.Load` + `analysis/checker.Analyze` +
  `Graph.PrintText`) instead of `singlechecker.Main` so the help
  banner reads `chameleon lint — ...` rather than the analyzer's
  flat `chameleonlint:` prefix. Top-level usage and bash/zsh/fish
  completions carry `lint`. Exit codes: 0 clean, 1 diagnostics or
  load error, 2 bad flags.

  Trade-off worth flagging: the custom driver omits singlechecker's
  `-fix`, `-json`, and `-c N` flags. None of these were known to be
  in use; happy to add them back on request.

### Added

- **Top-level `Makefile`**
  ([#62](https://github.com/trendvidia/chameleon/pull/62)). Targets:
  `build`, `install`, `test`, `test-short`, `test-integration`,
  `fuzz`, `bench`, `cover`, `cover-html`, `vet`, `lint`
  (`golangci-lint`, pinned to CI's `v2.12.2`), `self-lint` (eats own
  dogfood: `chameleon lint ./...`), `fmt`, `fmt-check`, `tidy`,
  `verify`, `generate`, `tools`, `ci`, `check`, `clean`. Knobs like
  `FUZZ_TIME`, `BENCHTIME`, `GOLANGCI_LINT_VERSION` are
  override-friendly. `make help` (default) lists every target.

## [0.13.0] — 2026-05-13

Full-codebase audit cycle on top of v0.12.0 — four parallel agents
swept security, correctness, API/design, and tests/CI; findings
were verified by hand and shipped in three severity-batched PRs
([#58](https://github.com/trendvidia/chameleon/pull/58),
[#59](https://github.com/trendvidia/chameleon/pull/59),
[#60](https://github.com/trendvidia/chameleon/pull/60)) plus one
pre-existing fix
([#57](https://github.com/trendvidia/chameleon/pull/57)).

Two findings turned out to be API-shaped — `sops.ErrPlaintextTooLarge`
is a new exported error, and `secret.Secret`'s `Open`/`Destroy`
contract tightened from "documented as racy" to "actually
goroutine-safe." Both warrant another soak cycle before v1.0 locks
the public surface, hence v0.13 rather than v1.0.

### Added

- **`sops.ErrPlaintextTooLarge`**
  ([#58](https://github.com/trendvidia/chameleon/pull/58)). New
  sentinel returned by `Decrypter.Decrypt` (default implementation)
  when a single plaintext would exceed `1 << 30` bytes.
  Defense-in-depth — the resolver's `ErrLayerTooLarge` ceiling fires
  earlier under normal use; this protects direct `sops.Decrypt`
  callers and guards the memguard growth loop against `int` wrap.

- **`examples/simple/README.md`**
  ([#60](https://github.com/trendvidia/chameleon/pull/60)). Short
  in-directory note labelling the contents as a chain-spec fixture
  (no Go code) with a pointer to `examples/buf/` for the runnable
  Go setup. The top-level README already labels it the same way;
  this catches users who land in the directory via a file browser
  or GitHub directly.

### Changed

- **`secret.Secret`: `Open` / `Destroy` are now goroutine-safe**
  ([#59](https://github.com/trendvidia/chameleon/pull/59)). An
  `sync.RWMutex` serializes access to the enclave/destroyed state.
  Previously documented as racy: observable in the wild as either a
  misleading `ErrUnset` return or, worse, a nil-pointer panic inside
  memguard if a `Destroy` interleaved between `Open`'s nil-check
  and the enclave dereference. Concurrent `Open` calls still
  parallelize (RLock); the memguard work runs outside the lock.
  `SetMetadata`'s constructor-only contract is unchanged. Consumers
  following the old "destroy from the same goroutine that opened"
  pattern see no behavior change — the new contract is strictly
  broader.

- **`protowire-go` v0.77.0 → v1.0.0**
  ([#57](https://github.com/trendvidia/chameleon/pull/57)). The
  dependency reached 1.0; no chameleon-side API surface change.

### Fixed

- **`protoc-gen-chameleon-go`: advertise proto3 optional support**
  ([#57](https://github.com/trendvidia/chameleon/pull/57)). The
  plugin now sets
  `CodeGeneratorResponse.supported_features = FEATURE_PROTO3_OPTIONAL`,
  silencing protoc's `plugin "protoc-gen-chameleon-go" does not
  support required features. Feature "proto3 optional" is required
  by N file(s)` warning. Bookkeeping only — the plugin walks
  `pxf.Secret`-typed fields exclusively and never inspected the
  proto3-optional bit.

- **`sops`: cap `memguardWriter` growth at 1 GiB**
  ([#58](https://github.com/trendvidia/chameleon/pull/58)). The
  growth loop in `memguardWriter.Write` doubled `int` without an
  overflow guard. On a 64-bit host you'd OOM long before integer
  wrap in practice, but defense-in-depth now caps any single
  plaintext at `1 << 30` bytes and returns
  `sops.ErrPlaintextTooLarge` past that.

- **`examples/buf`: broken `go_package` in vendored `pxf/secret.proto`**
  ([#58](https://github.com/trendvidia/chameleon/pull/58)). The
  directive pointed at `github.com/trendvidia/protowire/proto/pxf`
  — a path the protowire spec repo intentionally publishes no Go
  code at. Now points into the example's own `gen/go/pxf` tree,
  mirroring `myapp/v1/config.proto`. Affected anyone copying the
  example and running `buf generate`; CI did not catch it because
  `examples/buf/gen/` is gitignored.

- **Tighter `require.Error` test assertions**
  ([#59](https://github.com/trendvidia/chameleon/pull/59)). Six
  call sites in `merge`, `validate`, and `resolver` tests that
  asserted "an error happened" without checking which now use
  `ErrorContains` or `errors.Is(...)` against the relevant
  sentinel / message. Catches the "test passes for wrong reason"
  drift where an unrelated error silently satisfies the test.

- **`secret`: de-flake `TestOpenDestroy_RecordsDuration`**
  ([#60](https://github.com/trendvidia/chameleon/pull/60)). Bumped
  the observation sleep from 5ms to 50ms so scheduler jitter and
  histogram bucket rounding don't push the `dp.Sum >= 0.005`
  assertion under threshold on overloaded runners. 45ms is paid
  once per test run.

### Notes for upgraders

- **No breaking changes.** `secret.Secret`'s tightened concurrency
  contract is strictly broader than the prior documented contract;
  every old caller pattern still works.
- **`sops.ErrPlaintextTooLarge`** is a new symbol in the public
  `sops` package. The v1.0 frozen API surface (per ROADMAP) will be
  re-checked once this soak completes.

### v1.0 readiness — soak status

| Criterion | v0.13.0 status |
|---|---|
| One release cycle of soak | v0.12.0 ran clean; v0.13.0 starts another (audit-driven API additions warrant it) |
| No open enhancement issues blocking core flows | Zero |
| README + godoc cross-checked | Yes (secret godoc updated for the new contract) |
| Cross-repo pins documented | Yes (`protowire-go` v1.0.0 noted above) |

v1.0.0 follows after this soak if no API-shaped feedback surfaces.

## [0.12.0] — 2026-05-12

Low-priority polish + tooling sweep on top of v0.11.0. Six items from
the post-v0.11.0 review, plus one ROADMAP-deferred (P2). One internal
type swap that's invisible through the public API surface but worth
a soak cycle before locking in v1.0.

### Added

- **First CI workflow on the repo**
  ([#51](https://github.com/trendvidia/chameleon/pull/51)).
  `.github/workflows/ci.yml` — runs build + vet + race-test +
  golangci-lint on push to main and every PR. Pinned Go 1.26.x.

- **`.golangci.yml` — golangci-lint v2 config**
  ([#51](https://github.com/trendvidia/chameleon/pull/51)). 18
  linters beyond the stdlib defaults: gosec, bodyclose,
  contextcheck, copyloopvar, durationcheck, errorlint, gocritic,
  intrange, misspell, nilerr, nilnesserr, noctx, nolintlint,
  prealloc, revive, unconvert, usestdlibvars, wastedassign,
  whitespace. Exclusions documented per file pattern (no
  blanket-disable). `golangci-lint run ./...` reports 0 issues.

- **Benchmark regression gate**
  ([#52](https://github.com/trendvidia/chameleon/pull/52)). Six
  benchmarks across `resolver` (TwoLayers / FiveLayers /
  WithSecret) and `interpolate` (NoTemplates / ShallowSubstitution
  / ThreeFieldChain). New `bench` CI job runs them on every PR and
  prints a `benchstat` diff vs main — non-blocking until a baseline
  range stabilizes.

- **`chameleon version` and `chameleon completion` subcommands**
  ([#53](https://github.com/trendvidia/chameleon/pull/53)).
  `version` prints the module version, Go toolchain, and VCS
  commit/timestamp via `runtime/debug.ReadBuildInfo()`.
  `completion <bash|zsh|fish>` emits hand-rolled completion scripts
  with subcommand + per-flag completion (no cobra dep).

- **Real-sops round-trip integration test**
  ([#55](https://github.com/trendvidia/chameleon/pull/55)). New
  `sops/integration_test.go` gated behind `//go:build integration`.
  Generates an age X25519 keypair in-process via `filippo.io/age`,
  encrypts a canonical PXF plaintext via sops's `encrypt.Data`,
  decrypts through chameleon's `Decrypter`, asserts byte-for-byte
  round-trip. Wired into CI; failure blocks merge. Closes the
  "Skipped — low value vs. cost" item in ROADMAP per explicit user
  opt-in.

- **Keep-a-Changelog version comparison footer**
  ([#50](https://github.com/trendvidia/chameleon/pull/50)). Each
  release header (`[0.12.0]`, `[0.11.0]`, …) is now a clickable diff
  link against its predecessor. Two stale bare `chameleon#N`
  references promoted to issue links.

### Changed

- **`chain` package uses typed `*chameleonv1.ChainSpec` access**
  ([#54](https://github.com/trendvidia/chameleon/pull/54), ROADMAP
  P2). Descriptor-driven `Fields().ByName("name")` reflection
  replaced with typed getters everywhere in `chain/expand.go`.
  Typos in expand.go now fail at compile time instead of returning
  nil silently. `chain/load.go` drops the `bufbuild/protocompile`
  runtime descriptor compile + `sync.OnceValues` cache; `ParseSpec`
  is 3 lines. Net **-113 lines** across the two files.

  The public API (`LoadSpec` / `ParseSpec` / `Expand`) is unchanged
  — same `proto.Message` interface contract — but the underlying
  concrete type returned from `LoadSpec` / `ParseSpec` switches from
  `*dynamicpb.Message` to `*chameleonv1.ChainSpec`. Callers passing
  dynamicpb-backed messages into `Expand` keep working via a
  marshal/unmarshal bridge at entry.

- **`--help` on every CLI subcommand exits 0**
  ([#53](https://github.com/trendvidia/chameleon/pull/53)). Was
  exit 1 with the awkward `chameleon explain: flag: help requested`
  line — `flag.ErrHelp` is now intercepted and returned as `nil`.

### Added — codegen

- **`proto/buf.yaml` + `proto/buf.gen.yaml`** — `cd proto && buf
  generate` regenerates the bindings.
  `proto/pxf/annotations.proto` is vendored with a `go_package`
  option (the runtime extension impl still lives in protowire-go).
  `proto/generate.go` carries a `//go:generate buf generate`
  directive.
- **`proto/chameleon/v1/{chain,layer}.pb.go` + `proto/pxf/annotations.pb.go`**
  checked in; no consumer-side codegen step required.

### Notes for upgraders

- **No breaking changes to library APIs.** The `chain` type swap is
  internal — `proto.Message` callers see no difference; type-assert
  consumers (rare) switch from `*dynamicpb.Message` to
  `*chameleonv1.ChainSpec`.
- **`bufbuild/protocompile`** is no longer a runtime dep of the
  `chain` package (still used by `cmd/chameleon/explain` for the
  user's app schema and by test fixtures).
- The new CI / lint / bench / integration jobs run automatically on
  PRs against `main`; no consumer action required.

### v1.0 readiness — soak status

| Criterion | v0.12.0 status |
|---|---|
| One release cycle of soak | v0.11.0 ran clean; v0.12.0 starts another |
| No open enhancement issues blocking core flows | Zero |
| README + godoc cross-checked | Yes (CLI section updated for new subcommands) |
| Cross-repo pins documented | Yes |

v1.0.0 follows after this soak if no API-shaped feedback surfaces.

## [0.11.0] — 2026-05-12

Medium-priority polish from the v1.0 readiness review. v0.10.0
shipped the criticals + highs; this release closes the remaining
mediums and tightens a documented-but-undelivered WKT atomic-merge
semantic. One small behavior change for ``resolver.Resolve``
consumers — called out below; everything else is doc / test /
opt-in API addition.

### Added

- **`interpolate.MaxResolveDepth` + `ErrDepthExceeded`**
  ([#39](https://github.com/trendvidia/chameleon/pull/39), shipped in
  v0.10.0 — listed here for completeness). Hard cap on `${...}`
  reference chain depth (64). Defense against pathological
  non-cyclic chains.

- **`parse.MoveOptions` + `parse.MoveIntoWithOptions`**
  ([#48](https://github.com/trendvidia/chameleon/pull/48)). New
  opt-in API for callers that want WKT atomic-merge clearing
  semantics on empty-value Secrets. Carries per-layer
  `[]*pxf.Result`; the walker uses it to detect "most-recent
  layer wrote the Secret field but not `.value`" and clear the
  corresponding Map entry per the documented atomic-merge
  contract. `parse.MoveInto` becomes a zero-options shim with
  the pre-v0.11 behavior — direct callers see no change.

- **5 testable examples** ([#46](https://github.com/trendvidia/chameleon/pull/46)).
  ``chameleon.ExampleLoad``, ``secret.ExampleSecret_Open``,
  ``secret.ExampleMap_Range``, ``chain.ExampleParseSpec``,
  ``wkt.ExampleAtomic``. Each has a `// Output:` line so
  ``go test`` verifies the documented behavior against the
  rendered API. pkg.go.dev surfaces these prominently.

- **`internal/header.FuzzParse`**
  ([#45](https://github.com/trendvidia/chameleon/pull/45)). 20-seed
  fuzz harness covering legitimate / weird-but-valid /
  adversarial header bodies. Properties: ``Parse`` never panics;
  successful Parse returns non-nil; subsequent ``Check`` doesn't
  panic. A 5-second 16-worker fuzz pass found 447 newly-
  interesting inputs with zero failures.

- **9 new CLI subcommand smoke tests + `io.Writer` plumbing**
  ([#47](https://github.com/trendvidia/chameleon/pull/47)). Each
  `*Cmd` (`explainCmd`, `verifyCmd`, `migrateCmd`, `hashCmd`)
  gained an `out io.Writer` parameter so tests can drive it with
  a `bytes.Buffer` instead of an `os.Pipe` swap. End-to-end
  smokes assert against output substrings for the common code
  paths.

### Changed

- **WKT atomic-merge clear for block-form-without-value Secrets**
  ([#48](https://github.com/trendvidia/chameleon/pull/48)).
  Previously, a higher layer's `pw { hint = "h" }` or `pw { }`
  (block form without `value`) ATOMICALLY replaced the proto
  message's `pw` field, but the lower layer's hook-routed value
  survived in `res.Secrets` — inconsistent with the documented
  "higher layer replaces the Secret as a unit" semantic. Now
  the value is cleared per atomic-merge. To preserve the value
  while updating hint, re-supply value
  (`pw { value = "..." hint = "h" }`); to clear explicitly,
  `pw = null` remains the unambiguous primitive.

  This is the **one behavior change in this release**. Affects
  only consumers using `resolver.Resolve` (not direct
  `parse.MoveInto` callers — which get the legacy behavior
  unless they opt in via `MoveIntoWithOptions`).

- **`protoc-gen-chameleon-go` renders `map<*, enum>` value type
  with the enum's `GoIdent`** ([#43](https://github.com/trendvidia/chameleon/pull/43))
  rather than punning to `int32`. Wrapper-getter signature for a
  `map<string, MyEnum>` field changes from `map[string]int32`
  to `map[string]<EnumGoIdent>`. Values are type-compatible at
  the proto-wire level so existing call sites continue to
  compile, but new code can pattern-match against the enum's
  constants directly.

- **`secret.SetMetadata` concurrency invariant tightened in docs**
  ([#44](https://github.com/trendvidia/chameleon/pull/44)). The
  godoc previously said "safe to call on a Secret already
  inserted into a Map" — wider than the actual safety window.
  Now names it explicitly: goroutine-safe ONLY while the Secret
  is being constructed by `parse.MoveInto`, before the
  containing Map escapes to consumer code. Empty-overwrite
  semantics also reconciled with `parse.MoveInto.takeSecret`:
  SetMetadata overwrites unconditionally; the skip-when-empty
  check happens at the call site.

### Tests

- **`cmd/chameleon` coverage 45.6% → 74.8%**
  ([#47](https://github.com/trendvidia/chameleon/pull/47)).
  `explainCmd` 0% → 79.4%; `verifyCmd` 0% → 73.3%;
  `migrateCmd` 0% → 63.4%.

- **`protoc-gen-chameleon-go` map-key formatter coverage**
  ([#42](https://github.com/trendvidia/chameleon/pull/42)).
  `joinFragmentRefs` 56.8% → 100%; `mapKeyRuntimeExpr`
  25.0% → 75%; `goTypeForMapKey` 37.5% → 87.5%. Eight new
  map-keyed fixture fields cover all six valid proto3 map-key
  kinds.

- **`secret.Secret.SetMetadata` coverage 0% → 100%**
  ([#44](https://github.com/trendvidia/chameleon/pull/44)).
  Three new tests lock the overwrite-unconditional contract +
  nil-receiver-is-noop + value-untouched invariants.

### Notes for upgraders

- **No breaking changes to library APIs.** The behavior change in
  `resolver.Resolve` (block-form-without-value now clears) is a
  documented-but-undelivered semantic finally enforced; the rare
  schemas that depend on the OLD behavior (lower-layer value
  surviving a hint-only block) should re-supply value
  explicitly. The README's "WKT atomic merge" Limitations entry
  was updated to describe the new behavior + workarounds.

- ``parse.MoveOptions`` is opt-in. Callers of ``parse.MoveInto``
  see no behavior change.

- Wrapper-mode codegen consumers with `map<*, enum>` fields will
  see their generated getter signature change (`int32` → enum
  GoIdent). Values are type-compatible; regenerate and recompile.

## [0.10.0] — 2026-05-12

v1.0 readiness sweep. v0.9.0 was the planned v1.0 candidate; before
tagging the stability promise, a four-axis audit (security,
pipeline correctness, codegen, API + docs + tests) surfaced
thirteen items ranging from one stale README line to one actual
data race. This release rolls them all up. The ROADMAP's v1.0
readiness criteria are now satisfied; v1.0.0 follows after a short
soak.

### Added

- **`resolver.WithMaxLayerBytes(n int64)` + `ErrLayerTooLarge`
  sentinel** ([#38](https://github.com/trendvidia/chameleon/pull/38)).
  Caps the size of each layer's raw bytes read from a Source.
  Default 10 MiB; pass `math.MaxInt64` to disable. Defense against
  pathological Sources that return huge files or hang on slow
  readers without a ctx deadline. Cap applies before decrypt —
  ciphertext size is approximately plaintext size, so a single cap
  covers both shapes. `ErrLayerTooLarge` wraps the layer id, path,
  and active limit for actionable logs.

- **`interpolate.MaxResolveDepth` constant + `ErrDepthExceeded`
  sentinel** ([#39](https://github.com/trendvidia/chameleon/pull/39)).
  Hard cap on `${...}` reference chain depth (64 frames).
  Defense against pathological non-cyclic chains
  (`a -> b -> c -> ... -> z[1000]`) that previously recursed
  without bound. Cycle detection (`ErrCycle`) still runs and is
  the distinct error class for actual cycles.

- **`chain.LoadSpec` / `chain.ParseSpec` are now safe for concurrent
  first-callers** ([#29](https://github.com/trendvidia/chameleon/pull/29)).
  The previous unsynchronized cache in `chain/load.go`
  (package-level `cachedDesc` / `cachedErr` / `cachedSet`) had two
  real races: a wasted-compile race, and a `(nil, nil)` race where
  a reader could observe `cachedSet=true` before the writer's
  assignment to `cachedDesc` was visible. Replaced with
  `sync.OnceValues`. `Resolve` was already safe (per-Resolve
  allocation of every mutable structure); this fix closes the
  construction-time concurrency hole — the broader "safe for
  concurrent use" promise now holds.

- **`protoc-gen-chameleon-go` reserved-name collision check**
  ([#30](https://github.com/trendvidia/chameleon/pull/30)).
  Wrapper mode emits fixed methods (`Reset`, `String`,
  `ProtoReflect`, `Secrets`). A user proto whose field PascalCases
  into any of those would generate a duplicate method on the
  wrapper → opaque "method redeclared" at the consumer's `go
  build`. The plugin now fails fast at codegen time with a clear
  diagnostic naming the field, the message, the colliding method,
  and two remediation paths (rename the field, or opt out of
  with_resolved). Two reserved sets applied — proto-side paths
  (no protogen mangling) vs Go-side field names (protogen
  pre-mangles `Reset` / `String` only).

### Changed

- **README + godoc cross-checked against current behavior.** Five
  stale claims tightened or rewritten:

  - The "plaintext never lands on the Go heap" claim
    ([#31](https://github.com/trendvidia/chameleon/pull/31)) was
    overstated: true for `*pxfv1.Secret.Value` (the proto-field
    path), but the PXF lexer string + the hook closure's `value`
    parameter are immutable Go strings that live on the heap
    until the next GC after the lexer advances past the token.
    Window is bounded by GC pacing, not resolve duration — a
    real improvement over the pre-v0.7 walker, but worth precise
    documentation before locking in the v1.0 promise.

  - The "Sops API has no ctx.Context hook" Limitations entry
    ([#33](https://github.com/trendvidia/chameleon/pull/33))
    hadn't been true since v0.6.0 (ctx threading shipped via the
    trendvidia/sops/v3 fork). Rewritten to describe what's
    actually closed and the residual HuaweiCloud goroutine shim.

  - The compatibility table now documents the `replace` directives
    consumers must add to their own `go.mod`
    ([#32](https://github.com/trendvidia/chameleon/pull/32)).
    Go's `replace` is non-transitive — the chameleon repo's
    directives do NOT propagate to consumers — so any consumer
    that doesn't copy the sops + protobuf-go replaces will fail
    `go build` because chameleon calls into functions absent from
    the upstream surfaces. Previous wording implied the replaces
    were inherited; this corrects the record.

  - The README's Go minimum version is bumped to 1.26+
    ([#28](https://github.com/trendvidia/chameleon/pull/28)) to
    match `go.mod`. The previous 1.25+ line silently failed `go
    build` for consumers on the older toolchain.

  - Stale `Underlying()`-era doc comments in the plugin's
    `gen_resolved.go` ([#35](https://github.com/trendvidia/chameleon/pull/35))
    referenced `Underlying()` as a live API; hard-removed in
    v0.9.0. Scrubbed. The historical removal note stays in
    `main.go`'s package godoc as the canonical reference.

- **`ROADMAP.md`'s "Public API surface (at v1.0)" enumeration**
  ([#34](https://github.com/trendvidia/chameleon/pull/34))
  regenerated from `go doc` walks. The previous stylized summary
  missed 10+ de-facto-public types/funcs the v1.0 stability
  promise will cover — including the top-level `chameleon.Load`,
  the `interpolate` / `defaults` / `validate` / `wkt` packages,
  several `resolver` types (`ClobberRecord`, `TraceEntry`,
  `WithStrictMetadata`, `WithMeterProvider`), and detailed
  method sets on `secret.Map` / `Secret` / `OpenedBuffer`. New
  format groups Functions / Types / Methods / Errors / Constants
  per package so future audits diff a stable shape.

- **README CLI documentation** ([#40](https://github.com/trendvidia/chameleon/pull/40)).
  The `chameleon` CLI section now lists all four subcommands
  (`explain`, `verify`, `migrate`, `hash`) instead of just
  `explain`; the duplicate H3 header is removed; the repo-layout
  block calls out `cmd/chameleon-lint` alongside the other
  binaries; and all five previously-undocumented `sops.Format*`
  constants now carry godoc.

### Tests

- **Concurrent `Resolve` regression guard**
  ([#36](https://github.com/trendvidia/chameleon/pull/36)). 64
  goroutines through one `*Resolver`, alternating environments.
  Verifies independent Secrets maps + env-consistent values +
  race detector clean. Backs the long-standing godoc
  thread-safety claim that had been asserted but unverified.

- **`source` (0% → 100%) and `internal/pathfmt` (0% → 87.5%)
  test coverage** ([#37](https://github.com/trendvidia/chameleon/pull/37)).
  `source.FS`'s `ErrNotFound` wrapping is the only thing standing
  between a typo'd layer path and the resolver's "skip optional
  layer" semantics. `internal/pathfmt.MapKey` is the documented
  "single source of truth" for dotted-path format across three
  callers — drift between any two would silently break
  `secret.Map.Get` lookups. Both now lock the behavior in.

### Notes for upgraders

- **No breaking changes.** All additions are opt-in or
  defense-in-depth. The new `WithMaxLayerBytes` default (10 MiB)
  is well above realistic layer sizes; the new
  `interpolate.MaxResolveDepth` (64) is well above realistic
  template chain depths.

- The `chain` concurrency fix unblocks consumers who construct
  multiple Resolvers in `init()` across goroutines, but
  single-Resolver users see no behavior change.

- Consumers of `protoc-gen-chameleon-go` in `with_resolved=true`
  mode whose schemas happen to use a field named `secrets`,
  `proto_reflect`, or whose `pxf.Secret` field is named `reset`
  / `string` / `secrets` will see the new collision diagnostic at
  codegen time. Rename the proto field per the error's
  remediation hint.

### v1.0 readiness criteria — satisfied

| Criterion | PRs |
|---|---|
| One release cycle of soak | v0.9.0 ran clean; v0.10.0 starts another |
| No open enhancement issues blocking core flows | Zero open issues |
| README + godoc cross-checked against current behavior | [#28](https://github.com/trendvidia/chameleon/pull/28), [#31](https://github.com/trendvidia/chameleon/pull/31), [#33](https://github.com/trendvidia/chameleon/pull/33), [#35](https://github.com/trendvidia/chameleon/pull/35), [#40](https://github.com/trendvidia/chameleon/pull/40) |
| Cross-repo pins documented | [#32](https://github.com/trendvidia/chameleon/pull/32) |

v1.0.0 cuts after a short soak if no API-shaped feedback surfaces.

## [0.9.0] — 2026-05-12

API-stability release. Two closures that finish the v0.8.0 wrapper-
mode design and the long-standing plaintext-in-heap window
([chameleon#6](https://github.com/trendvidia/chameleon/issues/6) and
[chameleon#7](https://github.com/trendvidia/chameleon/issues/7)
threads):

- **proto.Message delegation on every `<Root>Resolved` wrapper**
  ([#25](https://github.com/trendvidia/chameleon/pull/25)). The
  wrapper now implements `proto.Message` (Reset / String /
  ProtoReflect, each delegating to the inner message). Callers
  hand the wrapper directly to `proto.Marshal` / `proto.Equal` /
  `proto.Clone` / any reflection-driven API — no escape hatch
  needed. The deprecated `Underlying()` method introduced in v0.8.0
  is hard-removed; the wrapper IS the proto representation.
- **Block-form `pxf.Secret` heap closure**
  ([#26](https://github.com/trendvidia/chameleon/pull/26),
  protowire-go [v0.77.0](https://github.com/trendvidia/protowire-go/releases/tag/v0.77.0)).
  `OnSecretField` now fires for block-form assignments
  (`pw { value = "x" hint = "h" }`) in addition to scalar shorthand.
  The resolver routes value bytes from the decoder lexer straight
  into a memguard `Enclave`; `parse.MoveInto` folds the block-form
  hint/fingerprint metadata back onto the Map entry via the new
  `(*secret.Secret).SetMetadata` method. End state: regardless of
  authoring form, plaintext never lands on `*pxfv1.Secret.Value`
  on the Go heap.

### Changed

- **`<Root>Resolved.Underlying()` removed.** Was Deprecated in
  v0.8.0; the v0.9.0 cycle hard-removes it. Migration path:
  - **Reflection APIs** (`proto.Marshal`, `proto.Equal`,
    `proto.Clone`): pass the wrapper directly — it satisfies
    `proto.Message`.
  - **Typed `*AppConfig` signatures**: change the signature to
    accept `proto.Message`, OR keep the raw `*AppConfig` in a
    separate variable (`resolver.Resolve` already returns it on
    `res.Config`).

- **`protowire-go` minimum bumped to `>= v0.77.0`** (was
  `>= v0.76.0`). Required for the block-form `OnSecretField` route.

### Added

- **Compile-time `var _ proto.Message = (*XResolved)(nil)`
  assertion** in the generated `.chameleon.go` file. Self-validating
  codegen: if a future emit path forgets one of Reset / String /
  ProtoReflect, the consumer's next `go build` fails at the
  assertion line.

- **`(*secret.Secret).SetMetadata(hint, fingerprint string)`** —
  mutator for the two diagnostic strings on a Secret. Used by
  `parse.MoveInto` to enrich a hook-routed Map entry with block-
  form metadata after decode. Hint and fingerprint are plain Go
  strings on the Secret struct (not memguard-backed); mutating
  them post-construction is safe.

### Notes for upgraders

- **Breaking change is wrapper-mode-only.** Consumers who didn't
  opt into `--chameleon-go_opt=with_resolved=true` aren't affected
  by the `Underlying()` removal. The function-form accessors stay
  emitted regardless.
- **No other API changes.** Resolver, secret.Map, parse.MoveInto's
  external signature, sops wrapper, chain expansion — all
  unchanged from v0.8.0.

### What v0.9.0 closes

Combined with v0.8.0's wrapper API + v0.7.0's chameleon-lint:

| Path | Outcome |
|---|---|
| `cfg.DbPassword.Value` (wrapper, scalar shorthand or block form) | Compile error — `DbPassword()` returns `*secret.Secret`. |
| `cfg.Underlying().DbPassword.Value` | Compile error — method removed. |
| Plaintext on `*pxfv1.Secret.Value` for any authoring form | Never — hook routes mid-decode. |
| `proto.Marshal(cfg)` etc. | Works — wrapper IS `proto.Message`. |

## [0.8.0] — 2026-05-12

Compile-time safety release. Closes [chameleon#6](https://github.com/trendvidia/chameleon/issues/6) end-to-end: with the new `with_resolved=true` plugin mode, the codegen now emits a `<Root>Resolved` wrapper per Secret-bearing message that exposes every field through typed accessor methods. The wrapper has no `Value` field, no public `*pxfv1.Secret`, and chained access for nested submessages / repeated / map containers. The remaining escape hatch (`Underlying()`) carries Go's standard `Deprecated:` marker so `staticcheck` / `golangci-lint` flag any consumer that still bypasses it. Combined with chameleon-lint (v0.7.0), `cfg.X.Value` is now caught at the typed-API tier (compile error) and at the CI tier (lint).

Phase 2b of [chameleon#6](https://github.com/trendvidia/chameleon/issues/6) shipped across four incremental PRs ([#20](https://github.com/trendvidia/chameleon/pull/20), [#21](https://github.com/trendvidia/chameleon/pull/21), [#22](https://github.com/trendvidia/chameleon/pull/22), [#23](https://github.com/trendvidia/chameleon/pull/23)) on top of Phase 2a's scaffold ([#19](https://github.com/trendvidia/chameleon/pull/19)).

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

### Not yet closed ([chameleon#9](https://github.com/trendvidia/chameleon/issues/9) follow-up phases)

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

[Unreleased]: https://github.com/trendvidia/chameleon/compare/v0.13.0...HEAD
[0.13.0]: https://github.com/trendvidia/chameleon/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/trendvidia/chameleon/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/trendvidia/chameleon/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/trendvidia/chameleon/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/trendvidia/chameleon/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/trendvidia/chameleon/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/trendvidia/chameleon/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/trendvidia/chameleon/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/trendvidia/chameleon/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/trendvidia/chameleon/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/trendvidia/chameleon/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/trendvidia/chameleon/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/trendvidia/chameleon/releases/tag/v0.1.0
