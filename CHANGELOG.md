# Changelog

All notable changes to `chameleon` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

_Nothing yet — changes live in [changelog.d/](changelog.d/) until release._

## [0.25.1] — 2026-09-10

A maintenance release with nothing to tell a consumer. **The library,
`chameleon/sops` and the CLI behave exactly as in v0.25.0** — no Go code
under any published package changed; the three modules carry the tag
because they are released together. Everything below is about how this
repository releases and records itself: the CLI module keeps no `replace`
directives, so a release cut no longer drops and restores them; the
changelog-fragment CI gate no longer goes red on a PR merged before its
checks finish; and `CHANGELOG.md`'s link-reference block is derived from
the release headings and was repaired for every release back to 0.1.0.

### Internal

- **`CHANGELOG.md`'s link-reference block is derived from its release
  headings and kept that way**
  ([#184](https://github.com/trendvidia/chameleon/issues/184)). The block
  had not been touched since 0.13.0: `[Unreleased]` compared against
  `v0.13.0`, and 0.14.0 through 0.25.0 had no compare link at all. The
  assembler now rewrites the block on every `make changelog`,
  `make changelog-check` (run on every PR) refuses a block that does not
  match the headings, and `make changelog-relink` regenerates it — which
  is what repaired the file here, for all 31 releases. Two of the
  regenerated links point at a tag that was never pushed: `v0.6.0` has a
  changelog section but no tag, so `[0.6.0]` and the `v0.6.0...v0.7.0`
  compare are dead links until someone tags the 0.6.0 commit.

- **`cmd/chameleon/go.mod` carries no `replace` directives any more, and the
  release procedure stops dropping and restoring them**
  ([#187](https://github.com/trendvidia/chameleon/issues/187)). Every CLI tag
  used to need a commit removing the two `replace`s (because `go install`
  refuses a module that has them) and a follow-up putting them back; the
  library and sops are stable enough that the CLI can name their released
  tags at all times, and inside the workspace nothing changes. Outside the
  workspace the CLI now builds against the last released library and sops
  rather than the worktree. RELEASING.md step 5 is shorter, and steps 4 and
  5 may be one PR, which removes the commit on `main` where
  `TestNoUpstreamProtoCompilerAnywhere` failed during the v0.25.0 cut.

- **The changelog-fragment gate diffs against the PR's recorded base
  commit** ([#190](https://github.com/trendvidia/chameleon/issues/190)).
  It resolved `origin/main` at run time, so a PR merged before the job
  started had its own change on `main` already, the merge-base became the
  PR head, and the gate reported "adds no fragment" for a PR that added one
  (#189). It now uses `github.event.pull_request.base.sha`, the base tip at
  event time, and a fast merge no longer produces a false red.

## [0.25.0] — 2026-09-10

A dependency release with one descriptor-contract break. **The PXF
annotation numbers move to protowire's registered block** — a `.proto`
compiled against the old `pxf/annotations.proto` must be recompiled, or
`(pxf.required)`, `(pxf.default)` and `(pxf.key)` stop applying, and the
reader now says so rather than staying quiet. Read the first **Changed**
entry before upgrading anything that owns a schema.

The rest is what the toolchain underneath moved to. Chameleon depends on
one protocompile now, the trendvidia fork, in every module; upstream
`bufbuild/protocompile` is out of every consumer's graph. protowire-go
goes v1.5.0 → v1.8.1, and two of its changes reach documents: a bool
default spelled other than `true` / `false` is an error instead of a silent
`false`, and the canonical spelling of map keys settled, so `chameleon get`
output and bundle bodies can differ byte-for-byte from v0.24.0's for a
document with a dotted or quoted key. Nothing in this release changes what
a document binds to.

The **Internal** entries are for contributors: the bindings #177 left on
the retired numbers are regenerated and guarded, the repository builds and
lints on Go 1.27, and the pins that tracked the fork's gaps are gone
because the gaps are.

### Changed

- **PXF annotation extension numbers move to protowire's registered block**
  ([#177](https://github.com/trendvidia/chameleon/pull/177),
  [protowire#244](https://github.com/trendvidia/protowire/issues/244)).
  `(pxf.required)`, `(pxf.default)` and `(pxf.key)` move from the
  unregistered `50000`-series to `1314`, `1315` and `1316` — the block the
  Protocol Buffers global extension registry granted protowire in
  [protocolbuffers/protobuf#28919](https://github.com/protocolbuffers/protobuf/pull/28919).
  The old range was a squat: nothing prevented another project from
  claiming the same integers.

  **This is a descriptor-contract break.** A `.proto` compiled against the
  old `pxf/annotations.proto` carries `50000`-series numbers, and chameleon
  no longer reads them: `(pxf.required)` stops being enforced,
  `(pxf.default)` stops substituting, and a keyed repeated field stops
  being keyed. There is no dual-read and no fallback — the two surfaces are
  disjoint by design.

  **What to do.** Recompile schemas against the matching
  `pxf/annotations.proto`, and upgrade any tool that reads these
  annotations in the same step. Chameleon's own vendored copy, its
  generated bindings and its `protowire-go` dependency (v1.4.0 → v1.5.0,
  across all three modules) move together here.

  PXF document syntax, `pb` and SBE wire output are unchanged — these
  numbers live in descriptors, not in encoded payloads.

- **Chameleon depends on one protocompile, the trendvidia fork; upstream
  `github.com/bufbuild/protocompile` is out of every module**
  ([#178](https://github.com/trendvidia/chameleon/issues/178)). All three
  modules move `github.com/trendvidia/protowire-go` v1.5.0 → v1.8.1, and the
  root and `cmd/chameleon` modules swap `bufbuild/protocompile` v0.14.1 for
  `github.com/trendvidia/protocompile` v0.34.0 — the compiler protowire-go
  itself compiles with since its v1.6.0. A consumer's `go list -m all`,
  dependency review and SBOM now name one protocompile; the library's
  plaintext path still links none (`TestPlaintextPathLinksNoProtoCompiler`),
  and `TestNoUpstreamProtoCompilerAnywhere` keeps upstream out of each
  module's build list.

  `chameleon explain` and `chameleon check-schema` compile user schemas with
  the fork now. Their behaviour is unchanged: the message lookup through a
  schema's imports and their public imports, the missing-import error that
  names the file, and the refusal of a tree whose vendored duplicate
  declares the same symbols twice (the compiler's wording is now
  `` `required` declared multiple times `` rather than upstream's "already
  defined"; `--exclude` remains the answer for that tree). Three gaps the
  fork had at v0.33.1 were filed from this work and closed in v0.34.0
  ([trendvidia/protocompile#222](https://github.com/trendvidia/protocompile/issues/222),
  [#223](https://github.com/trendvidia/protocompile/issues/223),
  [#224](https://github.com/trendvidia/protocompile/issues/224)).

  **One default that used to bind now errors.** protowire-go v1.6.0 made a
  bool default that is not exactly `true` or `false` a bind-time error
  ([protowire-go#90](https://github.com/trendvidia/protowire-go/issues/90)):
  `[(pxf.default) = "True"]`, `"1"` or `"yes"` used to bind silently to
  `false` — the opposite of what the schema author wrote — and now fail
  `defaults.Apply` with an error naming the field and the spelling. The
  `defaults` package documented the old behaviour as a rough edge and pinned
  it so the upstream fix would be noticed; the pin now asserts the error.

  **What to do.** Nothing, unless a schema carries a bool default spelled
  other than `true`/`false`: fix the spelling. Consumers that vendor or
  replace `bufbuild/protocompile` for chameleon's sake can drop that.

- **What the protowire-go v1.5.0 → v1.8.1 move changes for PXF documents
  chameleon reads and writes.** Chameleon's reader and writer are
  protowire-go's `encoding/pxf`, so its releases
  ([v1.6.0](https://github.com/trendvidia/protowire-go/releases/tag/v1.6.0),
  [v1.7.0](https://github.com/trendvidia/protowire-go/releases/tag/v1.7.0),
  [v1.8.0](https://github.com/trendvidia/protowire-go/releases/tag/v1.8.0))
  reach every `chameleon.Load`, `chameleon get`, `chameleon bundle` and
  keystore file. None of it changes what a document binds to; the moves are
  in what is accepted and how canonical text is spelled.

  **Reading.** A bare `true` / `false` map key is a bool key, matching a
  `map<bool, V>` field (protowire-go#109); write `"true"` for the string.
  Input over 64 MiB is refused before a byte is read, and the other two
  mandatory limits from the spec's HARDENING.md are enforced
  (protowire-go#112). The nesting counter starts at the root, so a
  document exactly at the depth limit decodes (protowire-go#111). A
  descriptor compiled against a pre-registered-block `pxf/annotations.proto`
  is now diagnosed as stale — `option number 50000 … was (pxf.required)
  before protowire v1.12.0 and is 1314 now` — rather than silently losing
  its annotations (protowire-go#98); see the registered-block entry for
  what to recompile.

  **Writing.** The canonical spelling of map keys settled: a dotted string
  key is written bare (`"a.b":` → `a.b:`), a bare integer key stays bare, and
  a quoted `"true"` / `"123"` on a string-keyed map keeps its quotes
  (protowire-go#125, #123). `chameleon get` output, bundle bodies written by
  `chameleon bundle`, and keystores may therefore differ byte-for-byte from
  v0.24.0's for a document with such a key; existing bundles and keystores
  read unchanged, and re-reading the new text yields the same message.

  **What to do.** Nothing for most trees. If a tool of yours diffs
  `chameleon get` output or bundle bodies against stored copies, expect the
  key-spelling changes above once; if a document keys a `map<string, V>`
  with a bare `true` or `false`, quote it.

### Internal

- **The generated bindings are regenerated against the renumbered
  `pxf/annotations.proto`, and a test keeps them there**
  ([#179](https://github.com/trendvidia/chameleon/issues/179)). The
  renumbering in [#177](https://github.com/trendvidia/chameleon/pull/177)
  regenerated `annotations.pb.go` but not the bindings that use the
  annotations, so on `main` — never in a tagged release — `chain.pb.go`,
  `bundle_manifest.pb.go` and `user_config.pb.go` kept the retired
  `50000`-series option numbers, and with protowire-go v1.5.0 every
  `(pxf.required)` on `ChainSpec`, `Environment`, `LayerEntry`, `SourceRef`
  and `ContextKey` was silently unenforced. protowire-go v1.8.0 diagnoses a
  retired number rather than ignoring it, which is how this surfaced on the
  bump (`option number 50000 on FieldOptions was (pxf.required) before
  protowire v1.12.0 and is 1314 now`). `make generate` fixes it;
  `TestBindingsCarryRegisteredAnnotationNumbers` fails if the bindings fall
  behind the vendored proto again. #177 and this land in the same release,
  so no released chameleon ever had the lapse and there is nothing for an
  operator to do.

- **The repository builds and lints on Go 1.27: `golang.org/x/net` v0.55.0
  in the `sops` and `cmd/chameleon` modules, and golangci-lint v2.13.2 in
  CI and `make tools`.** The self-hosted runners moved to Go 1.27.1 on
  2026-09-10. Two things broke on `main` with no diff involved: x/net
  v0.54.0's `http2` no longer provides `TrailerPrefix` under 1.27, which
  grpc v1.81.0 (reached through the sops fork) needs, so the `sops` module
  failed to build; and the golangci-lint v2.12.2 binary, built with Go 1.26,
  cannot typecheck the 1.27 standard library (`math/rand/v2/rand.go: method
  must have no type parameters`), so lint failed on every PR. v0.55.0 and
  v2.13.0 are the first releases of each with Go 1.27 support. `make
  fmt-check` and `make lint` are clean on all three modules at v2.13.2, so
  gofumpt output did not move.

- **protowire-go v1.8.1 and protocompile v0.34.0; the pins written against
  the fork's v0.33.1 gaps flipped and are gone.** The three CLI pins for
  trendvidia/protocompile#222/#223/#224 and the `defaults` pin for
  protowire-go#135 each failed on the bump exactly as their comments said
  they would; the upstream-style assertions are restored, and the local
  `findVisibleDescriptor` walk is deleted in favour of the restored
  `linker.ResolverFromFile`.

## [0.24.0] — 2026-08-10

A CLI and tooling release. **The library, `chameleon/crypt`, `chameleon/sops` and the generator are untouched** — every change here is in `chameleon check-schema` or in how this repo builds itself, so upgrading the library is a version bump and nothing else. The three modules carry the same tag because they are released together, not because all three changed.

What moved: `check-schema --schema=<dir>` now follows symlinks, and its `OK` line reports what `--exclude` pruned and no longer changes shape when a pattern narrows the search. Both were cases of a green run quietly answering a narrower question than the one asked — read the **Changed** entries if you match on its output or run it over a tree with symlinks in it.

The **Internal** entries are for contributors rather than consumers, and one of them has teeth: this Makefile needs GNU Make ≥ 3.82 now, which macOS does not ship. `brew install make` and use `gmake`.

### Changed

- **`chameleon check-schema` reports what `--exclude` skipped, and stops letting a pattern change the `OK` line's shape** ([#166](https://github.com/trendvidia/chameleon/issues/166)). Two ways a well-formed pattern could quietly narrow a green run:

  ```sh
  # before
  chameleon check-schema --schema=./proto --exclude=vendor
  # OK    2 root(s)    2 file(s) checked
  chameleon check-schema --schema=./proto --exclude='v*'      # also takes v1/
  # OK    ./proto    1 file(s) checked

  # now
  # OK    2 root(s)    2 file(s) checked    1 skipped
  # OK    1 root(s)    1 file(s) checked    2 skipped
  ```

  **The skipped count** is new. It is of *roots the patterns took away*, not of files the patterns matched, and not of files left unread: one pruned from the walk and then named directly by another `--schema` is a root anyway and is not counted, while one a surviving root imports is still compiled and checked and *is* counted — it appears in both numbers, because the file count says it was checked and this one says it lost its own line in the report. The field is absent when nothing was skipped, so no run without `--exclude` grows one.

  **The `OK` line's shape now follows what `--schema` named**, not how many roots came out of it. A directory search that exclusion narrowed to a single root used to flip to the single-file form — so a CI job matching `N root(s)` stopped matching at exactly the moment a pattern over-pruned, which is the failure it was watching for. One `--schema` naming one file keeps the single-file form; a directory, or more than one `--schema`, keeps the `N root(s)` form however many roots survive.

  **If you match on stdout.** The `N root(s)` form is now reachable with `N` = 1 — by over-pruning, by a directory that holds a single `.proto`, or by naming the same file twice — so a run that passes no `--exclude` at all can change shape, even though it grows no field. A `\t<n> skipped` field can follow the file count. Anchoring on the whole line is what breaks; matching the fields you care about is not. Pruning *every* `.proto` under a directory remains an error rather than a count, unchanged.

- **`chameleon check-schema --schema=<dir>` follows symlinks** ([#167](https://github.com/trendvidia/chameleon/issues/167)). A subtree symlinked into place — a normal monorepo layout, and this command exists for the shared-schema repo most likely to have one — was silently not searched, so the run reported `OK` over a narrower set than you named:

  ```sh
  $ find -L proto -name '*.proto' | wc -l
  4
  $ chameleon check-schema --schema=./proto
  # before
  OK    3 root(s)    3 file(s) checked
  # now
  OK    4 root(s)    4 file(s) checked    1 linked
  ```

  Linked **files** are resolved too, not only directories: the old walk collected a linked `.proto` by name without ever asking what it pointed at, so a *dangling* one went to the compiler and failed there, and one whose target the walk also reached directly was compiled twice. It is linked **directories** whose contents are new.

  The new `linked` field counts the files no path through the real tree reaches. It does not move with whether a link's name happens to sort before its target's, or with how many arguments cross a link to the same file, so it can never exceed the file count beside it.

  Three things following symlinks obliges, all handled rather than left to protocompile:

  - **A file reachable both directly and through a link is checked once**, under the path that crosses no link. Two paths to one file would otherwise compile it twice under two names, which is the duplicate-symbol failure the `--schema` naming rules exist to prevent — and preferring the real path matters as much as the deduplication does, because a root named through a link leaves a sibling's import of the real name compiling a second copy.
  - **A link pointing at an ancestor terminates.** Each directory is entered once, keyed by its resolved path. Terminating is all that guarantees: the ancestor's other subtrees still enter the search, because leaving the directory you named is what following a link means.
  - **A dangling link is skipped**, not fatal. It names nothing, and the rest of the tree is still worth checking.

  `--exclude` matches a link by its **own** name — `--exclude=linked` prunes it — because that is the name you see in the tree rather than the one it points at.

  **If you match on stdout:** a `\t<n> linked` field can now follow the file count (and the `skipped` field from [#166](https://github.com/trendvidia/chameleon/issues/166)), and a tree with symlinks will report higher counts than it did. That second part is the fix, not a side effect: the old numbers were answering a narrower question than the one asked.

### Internal

- **`make ci` runs what CI runs again, and the tidy check is a target rather than workflow YAML** ([#165](https://github.com/trendvidia/chameleon/issues/165)). The target's help said "Reproduce the CI matrix locally" while skipping three gates the workflow applies: `make compile`, `make fmt-check`, and `go mod tidy` + `git diff --exit-code`. A green `make ci` could and did push a red PR — the tidy leg caught [#164](https://github.com/trendvidia/chameleon/pull/164) after a clean local run.

  `fmt-check` was a regression rather than an oversight. [#149](https://github.com/trendvidia/chameleon/issues/149) added it to `ci` and its changelog entry says so; `202ae0e`, the merge of `main` into the [#150](https://github.com/trendvidia/chameleon/issues/150) branch, resolved a same-line conflict on that target by keeping its own side, and `fmt-check` went with it. Both sides had edited the one line, so nothing was silently lost in the diff sense — it was lost in the resolution.

  New `make tidy-check` runs `make tidy` and fails if any `go.mod` / `go.sum` / `go.work` / `go.work.sum` changed. `.github/workflows/ci.yml`'s "modules are tidy" step now calls it instead of carrying its own copy, which is the argument that workflow already makes for the module walk: one definition, or the two drift. **It rewrites those files in place** — `go mod tidy` is the only way to learn whether they were already right — so a failure leaves the fix in your working tree, unstaged, for you to `git add`.

  It reports three states rather than one, because the fix each wants is different: `make tidy` rewrote something (commit it), `make tidy` rewrote nothing but a module file still differs from `HEAD` (your own in-progress edit, or a `go.work.sum` an earlier target rewrote), and git itself failed (the check did not run, and says so instead of blaming the module graph). The old one-message form told a developer with an uncommitted `go.mod` edit that their tree was untidy and that `make tidy` had just fixed it — neither true, and the diff it pointed at was their own text.

  New `make ci-check` closes the mechanism rather than the instance: it fails if `ci.yml` runs a `make` target that `make ci` does not, and CI runs it next to `modules-check`. #165 was two hand-maintained lists drifting, and a comment asking for them to be kept in step is exactly what did not stop it the first time. `test-integration` is the one entry on the exemption list, with its reason beside it: it needs live KMS credentials, and a target that fails on a clean checkout is one people learn to ignore.

  `ci` is now `modules-check ci-check changelog-check fmt-check compile vet test tidy-check lint`, and the Makefile is `.NOTPARALLEL` — `tidy-check` writes the module files that `compile`, `vet`, `test` and `lint` read, so `make -j ci` could otherwise observe the graph mid-rewrite. Four gates still sit outside `make ci` because they do not run through make at all, and the `ci` comment names them: `test-integration`, the workflow's "did this PR add a fragment?" step, the `bench` job, and the go-plus toolchain assertion.

- **The Makefile now requires GNU Make ≥ 3.82, and says so instead of misbehaving** ([#171](https://github.com/trendvidia/chameleon/issues/171)). `.SHELLFLAGS := -eu -o pipefail -c` has been in this Makefile for a while and did nothing on macOS: the variable arrived in GNU Make **3.82**, and macOS still ships **3.81** as `/usr/bin/make`. Recipes ran with flags `hBc` — no `-e` — so every `for m in $(MODULES)` loop returned only its **last** module's status:

  ```
  ==> vet .
  <failure in the library module>
  ==> vet sops        ok
  ==> vet cmd/chameleon   ok
  exit=0
  ```

  `MODULES := . sops cmd/chameleon` puts the library first, so the largest module was the most likely to be silently skipped, and nine targets had that shape (`compile`, `test`, `test-short`, `vet`, `lint`, `fmt`, `tidy`, `verify`, and `fmt-check`'s inner loop — `fmt-check` survived only because it accumulates failures by hand). A green `make test` on a Mac could be hiding a broken library module, which is where most of this repo is written.

  A `$(error)` guard on `$(MAKE_VERSION)` now refuses to run rather than run wrong, naming the fix. With the requirement enforced, `.SHELLFLAGS` finally takes effect and a failure in *any* module fails the target: injecting a `go vet` error into the library module takes `vet` from `exit=0` after all three modules to `exit=2` after one.

  **On macOS: `brew install make` and use `gmake`** — Homebrew deliberately does not displace `/usr/bin/make`. Linux already ships ≥ 4.x.

  The macOS CI runner turned out to be affected too, which the guard is how we found out: nothing in a log had ever said which `make` a runner had. So **every macOS CI run to date was discarding non-final module failures** — `compile`, `vet` and `test` are all per-module loops, and the library module is first. The workflow now prepends Homebrew's `gnubin` directory — which holds GNU Make under its normal name — to `PATH`, and asserts the version it ends up with. The calls themselves are unchanged: `gmake` does not exist on most Linux, so renaming them would need different commands per matrix leg, and `make ci-check` reads this workflow for `make <target>` lines.

## [0.23.0] — 2026-08-10

The module split release. `github.com/trendvidia/chameleon` is now three modules — the library, `chameleon/sops`, and `chameleon/cmd/chameleon` — so a consumer of the library no longer carries the cloud key-management tree in its module graph ([#130](https://github.com/trendvidia/chameleon/issues/130)). Decryption became a link-time opt-in behind the new `chameleon/crypt` contract, which is the one change here that can alter a working program's behaviour: read the **Changed** entries before upgrading.

Alongside it: PXF bind-time violations are now caught at `resolver.New` rather than at the first layer parse ([#134](https://github.com/trendvidia/chameleon/issues/134)), and `chameleon check-schema` answers the same question off a `.proto` alone, for the repo that authors a shared schema and never resolves anything ([#144](https://github.com/trendvidia/chameleon/issues/144), [#153](https://github.com/trendvidia/chameleon/issues/153), [#159](https://github.com/trendvidia/chameleon/issues/159)).

Upgrading the library alone needs no `go.mod` change. Programs with encrypted layers need one import added and possibly one `go get` — the link-time opt-in entry under **Changed** has the specifics, including what happens if you skip it.

### Added

- **`chameleon/crypt` — the decryption contract, split from the implementation** ([#130](https://github.com/trendvidia/chameleon/issues/130)). New leaf package holding `Decrypter`, `Encrypter`, `Format` (+ `FormatForPath`) and `Recipients`, plus a registry of named backends: `Register` / `Get` / `Registered` / `SoleDecrypter` / `Unregister`. Its entire dependency set is the standard library and memguard. The resolver now depends on this and nothing else for decryption, which is what makes the link-time opt-in ([#130](https://github.com/trendvidia/chameleon/issues/130), under **Changed**) possible; `chameleon/sops` became one implementation of it rather than the definition of it.

  The registry is `database/sql`'s driver model: `chameleon/sops` calls `crypt.Register("sops", …)` from `init`, and a consumer selects it with `import _ "github.com/trendvidia/chameleon/sops"`. `crypt.Registered()` reports what a build actually linked — worth calling in a startup log line if you have several.

- **`crypt.ErrEncryptOnlyBackend`** ([#130](https://github.com/trendvidia/chameleon/issues/130)). Wrapped by `resolver.New` when `WithDecrypterNamed` names a backend that registered only an `Encrypter`. Joins `ErrUnknownBackend` / `ErrNoBackend` / `ErrAmbiguousBackend` as an `errors.Is`-matchable outcome, so a caller reading the backend name out of configuration can tell "typo or missing import" from "that one only encrypts" without matching on message text.

- **`resolver.Trace.Decrypter`** ([#130](https://github.com/trendvidia/chameleon/issues/130)). Under `WithTrace`, names the crypt-registry backend that Resolve auto-selected for the call — empty when no encrypted layer was reached, and empty when the caller named the Decrypter itself. Auto-selection is the case worth reporting precisely because nobody wrote it down: registration follows the module graph, so a dependency can put a backend in a build without the build's author choosing it. `crypt.Registered()` says what was available; this says what was used.

- **`resolver.WithDecrypterNamed(name)`** ([#130](https://github.com/trendvidia/chameleon/issues/130)). Selects a registered backend by name, the resolver-side equivalent of naming a driver in `sql.Open`. Needed only in a build that links more than one backend, where auto-selection is deliberately undefined. Fails at `resolver.New` — not at the first encrypted layer — if no backend is registered under that name, and lists the ones that are, so a missing import surfaces in a plaintext dev environment instead of in prod. Mutually exclusive with `WithDecrypter` and `WithoutDecrypter`; passing two is an error rather than a silent last-one-wins. An empty name is an error too — a caller reading the backend name out of config wants the unset case to say so, not to fall through to auto-selection.

- **`resolver.WithoutDecrypter()`** ([#130](https://github.com/trendvidia/chameleon/issues/130)). Disables decryption for a Resolver: an encrypted layer returns `ErrNoDecrypter` even when the build has a backend registered. Needed because "no decrypter configured" and "do not decrypt" stopped being the same thing once the registry landed — registration follows the module graph, so a program links a backend as soon as anything in its dependency tree imports one. This is how a caller says no anyway. `chameleon get/explain/reveal --decrypter=none` is wired to it, so those keep failing on an encrypted layer instead of reaching for ambient KMS/age credentials just because the same binary also ships `chameleon bundle`.

- **`resolver.ErrSchemaViolation`** ([#134](https://github.com/trendvidia/chameleon/issues/134)). Returned by `resolver.New` when the sample message's descriptor closure carries a PXF annotation the format does not admit. `errors.Is`-matchable, so a consumer that loads schemas dynamically can tell "this schema is malformed" from "this wiring is wrong" without matching on message text. See the `### Changed` entry for what moved.

- **`resolver.NoDecrypterError`** ([#141](https://github.com/trendvidia/chameleon/issues/141)). The concrete error behind `ErrNoDecrypter`, carrying the layer that turned out to be encrypted (`LayerID`), which of the two cases happened (`Disabled` — `WithoutDecrypter` was passed, as opposed to nothing being registered), and the crypt registry failure underneath (`Err`). Reach it with `errors.As`:

  ```go
  var nde *resolver.NoDecrypterError
  if errors.As(err, &nde) && nde.Disabled {
      return fmt.Errorf("layer %q is encrypted, and --decrypter=none was passed", nde.LayerID)
  }
  ```

  Added because a program that re-words this failure for its own users could not do so without repeating chameleon's Go option names to an audience that never typed them — scraping the layer id back out of the message string is not an interface. The chameleon CLI is the first consumer.

  **Nothing changes for callers that do not use it.** `errors.Is` against `ErrNoDecrypter`, `crypt.ErrNoBackend` and `crypt.ErrAmbiguousBackend` matches exactly as before, and both diagnostics are byte-for-byte what they were — pinned verbatim by `TestNoDecrypterError_CarriesTheLayerAndTheCase`.

- **`chameleon check-schema` — ask whether a `.proto` is PXF-conformant, without resolving anything** ([#144](https://github.com/trendvidia/chameleon/issues/144)). Every other subcommand that takes `--schema` also needs a chain, layers and an environment, because it is on its way to resolving something. The bind-time rules — PXF-reserved names, `(pxf.key)` / `(pxf.default)` / `(pxf.required)` placement — are a property of a file closure, so they can be answered without any of that:

  ```sh
  chameleon check-schema --schema=config.proto
  # OK    config.proto    4 file(s) checked
  ```

  Non-zero exit and one line per violation otherwise. **The violation names the file that declares it, which need not be the one you passed**: since [protowire-go#71](https://github.com/trendvidia/protowire-go/issues/71) the checks cover the transitive import closure, so a misplaced annotation in a shared `common.proto` fails every schema importing it, whether or not the importing message refers to the offending type. Run this in the repo that *owns* a shared schema — a consumer's resolve is the wrong place to find out, and a schema-only repo has nothing else to run.

  `--message` is optional and does **not** narrow the check; it only additionally asserts the named message exists, in the schema or in anything it imports. The file count on the `OK` line is the closure size rather than the number of files named, so it says how much of the closure a green run actually spoke for.

- **`chameleon check-schema --schema` is repeatable and takes a directory** ([#153](https://github.com/trendvidia/chameleon/issues/153)). A repo that owns a shared schema owns many `.proto` files, and its question is "is every file I own conformant?" — not "is this one file's closure conformant?", asked N times:

  ```sh
  # before: one compile per file, and one report per importing file
  for f in proto/**/*.proto; do chameleon check-schema --schema="$f" || exit 1; done

  # now
  chameleon check-schema --schema=./proto --include=./proto
  # OK    5 root(s)    6 file(s) checked
  ```

  All roots compile **once, together**, so a shared import is parsed and linked once no matter how many roots reach it, and a violation in it is reported **once** — with the roots whose closure reaches it, so deduplication does not cost you the blast radius:

  ```
  common/v1/common.proto: field "common.v1.Shared.labels": invalid (pxf.default) = "nope": …    (via svc/a.proto, svc/b.proto, svc/c.proto)
  ```

  A directory is searched recursively for `*.proto`, symlinks not followed, so a subtree symlinked into place is not searched; `--exclude` prunes the rest of what you do not want walked. The directory is what replaces a glob rather than a shorthand for one: no shell expands `--schema=proto/*.proto`, because the pattern has to match the whole word, flag name included (bash passes it through literally; zsh and fish abort). Repeated flags are for a script that builds them. Glob expansion is not implemented here either, since `**` means different things in bash and fish and nothing in sh, so a glob this command expanded itself would disagree with the same string typed at a prompt.

  With several roots the `OK` line carries **both** counts — roots asked for, and distinct files covered. The file count stayed the cheapest signal that `--include` is wrong ([#144](https://github.com/trendvidia/chameleon/issues/144) made it load-bearing), and on its own it can no longer say how many roots produced it. `--message` is asked once of the union closure rather than once per root: with several roots the message need only exist somewhere in what was checked.

  Files found under a directory are named the way their siblings import them (protoc's `-I` contract), which is what lets one file's violations be recognised as one violation. An `--include` containing the file outranks the directory it was found under, because `--include` is where the operator states that contract: under `--schema=./proto/svc --include=./proto` a file is `svc/a.proto`, so a sibling's `import "svc/base.proto"` resolves to the root itself instead of compiling a second copy of it. Two *different* files that would compile under one name are rejected, naming both and suggesting `--include`, rather than being silently resolved to whichever import path came first.

  **The single-file form is unchanged** — same naming, same violations, same `OK <path> <n> file(s) checked` line. What used to be a hard error is now the feature: a directory passed to `--schema` was rejected with "not a regular file", and is now searched. An *empty* directory is still an error, since "OK, 0 files" is not an answer anyone wanted.

- **`chameleon check-schema --exclude`** ([#159](https://github.com/trendvidia/chameleon/issues/159)). Prunes the `--schema` directory search by name. The case it exists for is a repo that vendors a second copy of a file its own files import: two copies of `pxf/annotations.proto` are two declarations of `pxf.required`, so compiling both as roots fails on duplicate symbols and the directory form added in [#153](https://github.com/trendvidia/chameleon/issues/153) could not be used on that tree at all.

  ```sh
  chameleon check-schema --schema=./proto --include=./proto
  # chameleon check-schema: compile 3 schema root(s): pxf/annotations.proto:5:8: symbol "pxf.required" already defined at third_party/pxf/annotations.proto:5:8

  chameleon check-schema --schema=./proto --include=./proto --exclude=third_party
  # OK    2 root(s)    3 file(s) checked
  ```

  (Which of the two copies that first line names as the offender, and which as the "already defined at", swaps between runs of the same command.)

  A pattern matches **one path element** at a time — a directory's or a file's own name — with `filepath.Match` syntax. That is what lets `--exclude=third_party` prune the directory at whatever depth it appears without needing a `**`: the walk stops there, rather than the pattern spanning the rest of the path. Within an element the usual syntax works (`--exclude='*_test.proto'`). A pattern containing a path separator cannot match an element, so it is rejected rather than silently matching nothing — as is an empty one. A malformed pattern fails the run as well, naming both the flag and the pattern; it is caught while searching rather than while parsing flags, because `filepath.Match` only reports bad syntax once its scan reaches it, so `--exclude='vendor*[0-9'` looks well-formed against every name that does not start with `vendor`. A pattern that prunes *every* `.proto` under the directory says so, instead of reporting the "no .proto files found" that would send you off to check the path.

  **What it does not do**, all three deliberate:

  - It does not touch `--include`. An excluded file stays importable, which is the point for a vendored tree — the second copy stops being a *root* while the files that legitimately import it still resolve.
  - It does not apply to a `.proto` named directly by `--schema`, nor to the `--schema` directory itself. Naming something explicitly is the more specific instruction, and reading it otherwise would turn `--schema=./third_party --exclude=third_party` into "no .proto files found".
  - Nothing is excluded by default. `third_party/`, `vendor/` and dot-directories are walked like any other until you ask for them to be skipped.

  Additive: a run with no `--exclude` behaves exactly as before. Shell completion for the flag ships for bash, zsh and fish.

- **`docs/RELEASING.md`** — the tag order the three modules have to be released in, and what a stale `require` looks like when it goes wrong. Worth reading before cutting v0.23.0.

### Changed

- **A `(pxf.default)` inside a `oneof` no longer overrides the layer's choice.** Setting any oneof member clears its siblings, so a default on member `B` used to silently discard an operator's explicit `A` — `file = "/tmp/x"` came back empty with an unasked-for `timeout` in its place. The defaults pass now skips a member whenever some layer already selected a different member of the same oneof. Pre-existing for scalar members; this release widened the exposure to WKT members and fixes both. Synthetic oneofs (proto3 `optional`) hold a single field, express presence rather than choice, and default as before.

- **A `(pxf.default)` the defaults pass cannot honor is now a resolve-time error instead of a silent no-op** ([#129](https://github.com/trendvidia/chameleon/issues/129)). Read this if you have a `(pxf.default)` on a message-typed field. A default on a message type PXF cannot parse a literal for — `google.protobuf.Any`, `pxf.Secret`, `google.protobuf.Struct`, any user-defined message, and, until the v1.4.0 bump ([#139](https://github.com/trendvidia/chameleon/issues/139)) fixed it, `google.protobuf.BytesValue` — now fails the resolve with `resolver: defaults: <path>: default values not supported for message type <T>` where it was previously ignored. That is the deliberate other half of the WKT fix under **Fixed**: a silently ignored annotation is the worst of the available states, and plain `pxf.Unmarshal` already errors on these. The gate is unchanged otherwise — the annotation is consulted only for a path no layer set, so a field some layer did set is untouched. That made the error data-dependent in the branch that introduced it; the protowire-go v1.4.0 bump ([#139](https://github.com/trendvidia/chameleon/issues/139)) moves the rejection to bind time and removes the data-dependence before this release ships, so there is nothing to re-resolve per environment.

- **Decryption is opt-in at link time: plaintext consumers no longer link the cloud KMS SDKs** ([#130](https://github.com/trendvidia/chameleon/issues/130)). `resolver` imported `chameleon/sops` at package level for the `Decrypter` interface and `FormatForPath`, so every consumer of `chameleon.Load` or `resolver.New` linked the whole sops key-management tree — AWS, Azure, GCP, Vault, Huawei KMS and their transitive weight — whether or not a single layer was encrypted. Measured on a program that resolves two plaintext layers and nothing else:

  | | v0.22.1 | this release |
  |---|---|---|
  | binary (darwin/arm64) | 67.5 MB | **11.7 MB** |
  | `go list -deps` on `./resolver` | 1057 packages | **207** |
  | …of those, key-management packages | 264 | **0** |

  7.0 MB of what remains is the protobuf runtime any protobuf program pays; chameleon's own share is ~4.7 MB.

  **Nothing you have written stops compiling.** `resolver.WithDecrypter(sops.Default())` still works and still wins over the registry — `sops.Default()` returns a `crypt.Decrypter`, and the old `sops` type names are aliases (see *Deprecated*). What changes is that the wiring is no longer required: with exactly one backend registered, the resolver looks it up the first time an encrypted layer appears, so a consumer can drop the option and keep a blank import instead.

  **It compiles the same; it does not always *behave* the same.** Auto-selection means a resolve that used to fail can now succeed. Passing no decrypter used to be a guarantee that an encrypted layer would be refused; it is now a request for whatever backend the build happens to link, and registration follows the module graph, so a transitive dependency importing `chameleon/sops` for its own reasons turns decryption on in your program with no source change on your side. Two places to look:

  - **`chameleon.Load`** never passes a decrypter, so this applies to every caller of it. It takes no options, which means it has no `WithoutDecrypter` escape hatch — a program that wants encrypted layers to stay refused has to drop down to `resolver.New` + `resolver.WithoutDecrypter()`.
  - **Any `resolver.New` call site with no decrypter option**, for the same reason.

  If refusing encrypted layers was load-bearing for you (a dev/CI runtime that must not hold decrypt capability, say), say so explicitly with `WithoutDecrypter()` rather than by omission. `crypt.Registered()` tells you what your build actually linked.

  **What to do.** Nothing, if your layers are plaintext — you get the binary back for free. If you have encrypted layers, either keep `WithDecrypter(sops.Default())` as is, or replace it with `import _ "github.com/trendvidia/chameleon/sops"`. If you wrote your own `Decrypter`, switch its import from `chameleon/sops` to `chameleon/crypt`; that is the change that actually sheds the weight for you, since naming the type was enough to link the tree.

  **`resolver.WithDecrypter(nil)` is now an error at `New`** instead of being ignored. It used to be harmless — "nil decrypter" and "no decrypter" both meant an encrypted layer failed — but "not passed" now means auto-select, so a caller whose constructor returned nil would silently get ambient credentials from a line that reads as opt-in. The error names `WithoutDecrypter` as the way to mean no. (A *typed* nil is a non-nil interface and is installed as given, as everywhere else in Go.)

  **The failure mode moved, and says more.** An encrypted layer with no decrypter still returns `resolver.ErrNoDecrypter`, now wrapping the registry's cause and naming the fix:

  ```
  resolver: encrypted layer without Decrypter: layer="secrets": crypt: no decryption
  backend registered; link one with `import _ "github.com/trendvidia/chameleon/sops"`,
  or pass resolver.WithDecrypter
  ```

  A build that links **several** decrypt-capable backends without naming one is a new failure rather than an arbitrary pick — `… crypt: multiple decryption backends registered (alpha, beta); pick one with resolver.WithDecrypterNamed, or pass resolver.WithDecrypter`. It wraps `ErrNoDecrypter` too, so existing `errors.Is` handling still catches it. Naming one that isn't linked fails at construction: `resolver: WithDecrypterNamed: crypt: unknown backend "gamma" (registered: alpha, beta)`.

  **The scope of "linked in" is the module graph, not configuration** — the same trade `database/sql` makes. A dependency that imports `chameleon/sops` for its own reasons registers the backend in your build too. `crypt.Registered()` tells you what you ended up with.

  This is half of #130 — it fixes what a consumer *links*. What a consumer *requires* is fixed by the module split entry in this same section.

  **`chameleon/bundle` is on the plaintext path too.** Opening a bundle is a read, and its output feeds straight into `chameleon.Load`, but the package imported `internal/bundler` — the *write* side, which links a sops Encrypter because it re-encrypts — for two zip-layout string constants. That was enough to put the whole KMS tree back into a read-only consumer (1031 packages; 114 now). The constants moved to a dependency-free `internal/bundlelayout`, and `bundle` is in the linkage test's pinned set.

  The split is pinned by a test rather than by discipline: `linkage_test.go` shells out to `go list -deps` over the public plaintext path (`chameleon`, `resolver`, `bundle`, `chain`, `crypt`, `merge`, `parse`, `secret`, `source`, `defaults`, `validate`, `interpolate`, `wkt`) and fails if a sops or cloud-SDK package reappears, with a negative control asserting the same check does trip on `chameleon/sops`. A single stray import compiles fine and costs every consumer 56 MB, so the graph itself is the assertion.

- **The sops backend and the CLI are now separate modules; the library's dependency graph no longer carries key management** ([#130](https://github.com/trendvidia/chameleon/issues/130)). The link-time opt-in entry in this same section stopped consumers *linking* the KMS SDKs. This stops them *requiring* them: Go's module graph is per-module, so as long as any package or test in the library module imported `chameleon/sops`, `go mod tidy` kept the whole tree in the library's `go.mod` — and a consumer's `go list -m all`, dependency review and SBOM inherit our requirements whether or not their build reaches the code.

  Measured on the same plaintext two-layer consumer:

  | | v0.22.1 | this release |
  |---|---|---|
  | `go list -m all` | 347 modules | **35** |
  | …key-management modules of those | 164 | **0** |
  | library `go.mod` require lines | 134 | **22** |

  **Three modules, tagged separately** — `github.com/trendvidia/chameleon` (`vX.Y.Z`), `github.com/trendvidia/chameleon/sops` (`sops/vX.Y.Z`), and `github.com/trendvidia/chameleon/cmd/chameleon` (`cmd/chameleon/vX.Y.Z`). The CLI had to move too: it links the backend by nature, and a binary in the library module put the whole tree back in every consumer's graph — splitting only `sops/` was measured to change nothing at all. `protoc-gen-chameleon-go` has no backend dependency and stays in the library module. Import paths are unchanged for every package; a `go.work` at the repo root wires the three together for local development.

  **What consumers do.** Plaintext consumers: nothing. Encrypted-layer consumers: one extra `require github.com/trendvidia/chameleon/sops`, which `go get` / `go mod tidy` adds the first time the blank import appears. CLI users: `go install github.com/trendvidia/chameleon/cmd/chameleon@latest` is unchanged, and a version-pinned install keeps its spelling — `@v0.23.0`, which Go maps to the `cmd/chameleon/v0.23.0` repo tag by itself, exactly as `gopls@v0.14.2` maps to `gopls/v0.14.2`. From v0.23.0 that pin follows the CLI module's own tags; an existing `@v0.22.1` still resolves through the root module, because that revision predates the split. `protoc-gen-chameleon-go` pins are unaffected.

  **One API change fell out of it.** `bundler.BuildOptions` (internal, reached through the CLI) gained `RecipientsForKeyName func(string) (crypt.Recipients, error)`: the bundler used to call `sops.RecipientsFromKeyName` directly, which is precisely the import that would have kept the KMS tree in the library module. The CLI passes `sops.RecipientsFromKeyName`; a chain spec that declares no `key_name` never needs it. A spec that *does* declare one with the hook unset is now an error rather than a silent drop — dropping it would encrypt the layer to a smaller recipient set than the chain asked for, and surface in production as a layer nobody can open.

  **Test coverage moved with the code, deliberately.** The bundler's end-to-end test now runs against a fake backend in the library module (it enforces the same wipe-on-success contract as sops, and can assert *which recipients* each layer was encrypted to — something the real-age version could not see through sops's envelope), and the real age round-trip through `bundler.Build` lives in the sops module (`sops/bundle_test.go`), along with the first coverage of `RecipientsForKeyName` wired to the real key store. `TestLibraryModuleRequiresNoKeyManagement` pins the go.mod half the same way the linkage test pins the link half, and CI fails if any module's `go.mod` is untidy.

- **`resolver.New` now rejects a schema whose PXF annotations cannot bind, instead of waiting for a layer parse to do it** ([#134](https://github.com/trendvidia/chameleon/issues/134)). The protowire-go v1.4.0 bump ([#139](https://github.com/trendvidia/chameleon/issues/139)) moved the bind-time rules off layer data, so the same schema fails identically for every environment. This moves the *report* the rest of the way: `New` asks `pxf.ValidateDescriptor` about the sample's descriptor closure and fails with `ErrSchemaViolation` before opening a single layer.

  What that catches which the parse-time check did not: **a chain whose every layer is optional and absent binds nothing at all**, so an invalid schema used to resolve completely clean. A `(pxf.default)` on a `repeated` field is the sharp case, because chameleon's own defaults pass skips list and map fields outright — upstream's bind-time check was the only thing standing between that schema and a silent resolve, and a chain that never parsed a layer never reached it.

  This is a deliberate exception to `New`'s documented rule that it does not fully validate until the first `Resolve`. That rule exists because `Expand` surfaces diagnostics that depend on the request (env / ctx); the bind-time rules depend on the descriptor alone, so deferring them buys nothing and costs determinism. Validation is memoized per file closure upstream, so repeated `New` calls on one descriptor cost a map lookup — up to upstream's 4096-descriptor cache bound, past which a program that compiles descriptors dynamically pays the closure walk on every call.

  **What to do.** If `New` starts failing after this upgrade, the schema was already invalid — the error names the file, the fully-qualified field, and the rule. Note the file it names may be an *imported* one: bind-time checks cover the transitive import closure (see the v1.4.0 bump entry in this same section), so a misplaced annotation in a shared `common.proto` fails every schema that imports it, whether or not your config type refers to the offending message.

- **Resolving a plaintext layer no longer links a protobuf compiler: ~3 MB off every consumer's binary** ([#137](https://github.com/trendvidia/chameleon/issues/137)). `internal/header` built the `chameleon.v1.LayerHeader` descriptor by compiling an inline copy of the message with `bufbuild/protocompile` at runtime. That package is on the resolve path (`resolver.loadAndParse` → `header.HandleDirectives`), so every consumer carried a full protobuf compiler in order to parse a five-field message the generated bindings in `proto/chameleon/v1` already describe — and the resolve path links those bindings either way. The descriptor now comes from the generated type. Measured on darwin/arm64, a program that constructs a resolver and nothing else:

  | | before | this release |
  |---|---|---|
  | binary | 11,642,370 B | **8,570,002 B** |
  | `go list -deps` on `./resolver` | 209 packages | **176** |
  | …of those, `bufbuild/protocompile` packages | 13 | **0** |

  **Nothing changes for you but the size.** No exported API moves, `@header` parsing and its sanity checks behave exactly as before, and the existing `internal/header` tests pass unchanged. The two descriptors were compared field-by-field — name, number, kind, cardinality and presence — before the switch, and that comparison is now a test (`TestDescriptorMatchesGeneratedShape`) that keeps the generated shape pinned to the one the parse path was written against. Alongside it, `TestDescriptorFieldContract` writes the five fields out as a literal: the parse path reaches every value through a string (`IsSet("id")`, `ByName("content_sha256")`), so a rename in `layer.proto` would not fail the build — it would quietly make the check stop firing — and a mirror-versus-mirror comparison can always be repaired by editing the mirror.

  The one real difference between the two sources is that the generated descriptor's file imports `pxf/annotations.proto` and the inline copy did not. That shows up in two places, and both are pinned. It is inert for this message — no `LayerHeader` field carries a pxf extension, so `pxf.UnmarshalFull` sees the same thing either way, and `TestDescriptorCarriesNoFieldExtensions` fails if someone later annotates one. It also widens the file closure `pxf.UnmarshalFull` validates on every call, from one self-contained file to three (`chameleon/v1/layer.proto` → `pxf/annotations.proto` → `google/protobuf/descriptor.proto`); since a bind-time violation anywhere in that closure would fail every header parse, `TestDescriptorClosureHasNoBindTimeViolations` asserts the closure is clean.

  **Why it was inline in the first place, since the code did not say.** The mirror landed on 2026-05-11 with the `@header` preamble, and its comment — "so chameleon doesn't need a codegen step for its own protos; matches the pattern in chain/load.go" — was accurate at the time: `chain/load.go` really did compile `chainProtoSrc` the same way. The next day, the typed-`ChainSpec` change migrated `chain` to protoc-gen-go, which gave the repo a codegen step and generated `proto/chameleon/v1` regardless. `internal/header` was simply not migrated with it. The stated reason had already expired; nothing else was holding the mirror in place.

  **This does not touch `chameleon explain`.** The CLI compiles user-supplied schemas, which is what protocompile is for, and it keeps its dependency. What moved is that the compiler is now only on the path that actually compiles something. Like the link-time decryption opt-in from [#130](https://github.com/trendvidia/chameleon/issues/130), the boundary is pinned by the package graph rather than by discipline: `linkage_test.go` fails if `bufbuild/protocompile` reappears anywhere under the plaintext path, with a negative control asserting the same check does trip on the cmd/chameleon module.

- **Bumped protowire-go to v1.4.0 — every `(pxf.default)` placement one literal cannot denote is now rejected when your schema binds** ([#139](https://github.com/trendvidia/chameleon/issues/139)). The annotation family, settled end to end upstream. Three chameleon gaps are answered by it rather than by code here — two of them tracked as issues, the third only by a test:

  - **A default on a `repeated` or `map` field no longer binds** ([#131](https://github.com/trendvidia/chameleon/issues/131), [protowire-go#68](https://github.com/trendvidia/protowire-go/issues/68)). It used to be dropped in silence — the one silent-ignore case #129 deliberately left alone. One literal cannot denote a list, so the schema is rejected outright and no implementation may invent a semantics for it.
  - **A default on a message type PXF has no literal for is caught at bind time, identically for every environment** ([#134](https://github.com/trendvidia/chameleon/issues/134), [protowire-go#69](https://github.com/trendvidia/protowire-go/issues/69)). The rejection chameleon added for `google.protobuf.Any` / `pxf.Secret` / `Struct` / user-defined messages was consulted only for a path no layer set, so the same schema resolved cleanly for `dev` and failed for `prod` — CI green, deploy broken. Binding is a property of the descriptor, not of what the layers set, so that data-dependence is gone for every resolve that parses a layer. `resolver.Resolve` surfaces it as `resolver: parse layer "<id>": PXF schema violations: …`, naming the field by fully-qualified name. **Upstream runs the check when a layer is parsed, and only then** — a resolve whose layers are all optional-and-absent binds nothing, so the bump on its own would leave that chain on the old resolve-time path (`resolver: defaults: <path>: …`, and silence for a list placement). The `resolver.New` entry for #134 in this same section closes that hole chameleon-side, and `TestNew_UnboundSchemaIsRejectedRegardlessOfLayers` pins the boundary it removes.
  - **`google.protobuf.BytesValue` accepts a base64 default** ([protowire-go#67](https://github.com/trendvidia/protowire-go/issues/67)), closing the ninth-wrapper exception the #129 entries describe.

  **Bind-time checks now cover your schema's transitive imports** ([protowire-go#71](https://github.com/trendvidia/protowire-go/issues/71)), not just the file declaring the message. A `config.proto` importing a shared `common.proto` fails to bind if `common.proto` carries a misplaced annotation anywhere, whether or not your config type refers to the offending message. This is the widest blast radius in the bump — budget a repo-wide pass, not a per-file one. Chameleon's own fixtures are single-file, so nothing here moved. The cost is negative: results are memoized per descriptor and upstream measured decode 6–7% faster than the single-file walk it replaced.

  **Three more placements are newly rejected at bind time**, beyond the `(pxf.default)` family above. v1.0.0 checked only PXF-reserved names; v1.4.0 adds `(pxf.key)`, `(pxf.default)` and `(pxf.required)` placement, so a schema that bound before can fail on a rule this entry's migration note does not describe:

  - **`(pxf.required)` on a member of a `oneof`** is rejected outright — read per field it demands that one specific arm always be chosen, which makes every other arm undecodable. Move the requirement to the oneof's container, or drop it.
  - **More than one `(pxf.default)` across the members of one `oneof`** is rejected: at most one member may carry a default, since applying a second would clear the first.
  - **A misplaced `(pxf.key)`** — on a field that is not repeated-message-typed, or naming something that is not a singular string field of the element message — is rejected where it was previously ignored.

  **One upstream change alters decoded output**, and chameleon already agreed with it: a oneof member's `(pxf.default)` applies only when no member of that oneof is present ([protowire-go#72](https://github.com/trendvidia/protowire-go/issues/72)). That is the same rule the defaults pass adopted for [#129](https://github.com/trendvidia/chameleon/issues/129) — with one gap this release also closes: a member the operator explicitly cleared (`file = null`) counts as present upstream but did not in chameleon's pass, so a `null` could be replaced by a sibling's default. Fixed in `defaults.Apply`, which now reads the presence union rather than the merged message's oneof case. With that, a chameleon resolve decodes identically either side of the bump.

  **What to do.** Run `chameleon check-schema --schema=<file.proto>` over each schema before upgrading in production — a misplaced annotation that was previously ignored now fails, and the command names the file and the fully-qualified field without needing a chain, layers or an environment. `chameleon verify` does **not** answer this — it compares each layer's `@header.content_sha256` against the file body and never binds your config message, so it reports `OK` for a schema that `resolver.New` will reject. Migration is deleting the annotation or moving it to a field one literal can denote. Nothing else in the resolve path changes: a schema that bound before and still binds decodes to identical bytes.

- **`chameleon get/explain/reveal` say what to do when a layer is encrypted and `--decrypter` is `"none"`** ([#141](https://github.com/trendvidia/chameleon/issues/141)). The refusal is unchanged and still correct — `"none"` is the fail-closed default, and it is what stops the CLI reaching for ambient KMS/age credentials just because the same binary also ships `chameleon bundle`. What you got told about it was the problem:

  ```
  chameleon get: resolver: encrypted layer without Decrypter: layer="base": decryption disabled (WithoutDecrypter)
  ```

  `WithoutDecrypter` is a Go option from `chameleon/resolver`. Someone running the binary never called it, disabled nothing knowingly, and finds it in no `--help` output. Now:

  ```
  chameleon get: layer "base" is encrypted: --decrypter is "none" (the fail-closed default); pass --decrypter=sops to decrypt it, or set decrypter = "sops" on this app in /home/you/.chameleon
  ```

  and, when the flag was actually typed, the statement without the instruction — being refused is the flag doing its job:

  ```
  chameleon get: layer "base" is encrypted: --decrypter=none was passed on the command line
  ```

  That second message **echoes the value you passed** rather than naming `"none"`. `--decrypter=` — what `--decrypter=$DECRYPTER` expands to with the variable unset — disables decryption exactly as `"none"` does, so it reports `--decrypter= was passed on the command line`. Seeing the empty value is also the answer to "why was the `decrypter` in my `~/.chameleon` ignored": a typed flag outranks the config file, empty value included.

  The `~/.chameleon` path in the first message is the one the loader would really read, `CHAMELEON_USER_CONFIG` included.

  **If you match on stderr.** Scripts keying on `decryption disabled` or `WithoutDecrypter` will stop matching; both CLI phrasings share the substring `is encrypted: --decrypter`. The Go-facing wording from `resolver` is untouched for programs using the library directly.

### Deprecated

- **`sops.Decrypter`, `sops.Encrypter`, `sops.Format`, `sops.Recipients`, the `sops.Format*` constants, and `sops.FormatForPath`** ([#130](https://github.com/trendvidia/chameleon/issues/130)). These are now aliases for their `chameleon/crypt` counterparts — identical types, so existing code compiles and interoperates unchanged — but importing `chameleon/sops` to *name* a type is what put 56 MB of cloud SDKs into binaries that never decrypted anything. Change the import to `chameleon/crypt`; the identifiers keep their spelling. `sops.Default()`, `sops.DefaultEncrypter()`, `sops.RecipientsFromKeyName()` and the `sops.Err*` values are not deprecated — they are the implementation, and a build that wants sops should have them.

### Fixed

- **`resolver.Resolve` no longer stutters the pass name in defaults errors.** The failure surfaced as `resolver: defaults: defaults: <path>: …` because both `defaults.Apply` and the resolver prefixed it. The resolver now wraps with a bare `resolver: `, matching how it wraps `validate`, so the error reads `resolver: defaults: <path>: …` as the docs describe.

- **`(pxf.default)` on a well-known-type field is applied instead of silently dropped** ([#129](https://github.com/trendvidia/chameleon/issues/129)). `defaults.Apply` walked message-kind fields and `continue`d on the atomic-WKT check before ever reaching the annotation lookup, so `(pxf.default) = "250ms"` on a `google.protobuf.Duration` resolved to a nil Duration — no error, no warning, just the proto zero. The capability existed one layer down the whole time: protowire-go's `pxf.ApplyDefault` binds WKT message fields directly and pins the behavior in its own tests. The lookup now runs **before** the message dispatch, matching how `validate` already orders its `(pxf.required)` check and how protowire-go's own per-parse pass is ordered.

  Covered types are the ones PXF has a literal form for: `Duration`, `Timestamp`, `pxf.BigInt`, `pxf.Decimal`, `pxf.BigFloat`, and eight of the nine `google.protobuf` wrapper types. The default sets the **whole** WKT value — the atomic boundary governs *merge* semantics (whole-value replacement), and never implied "cannot be defaulted". Default-filled WKT paths join the presence union like any other default, so `(pxf.required)` counts them as satisfied and `Resolution.Trace` marks them `FromDefault`.

  **All nine `google.protobuf` wrappers are covered, `BytesValue` included.** It was the one exception while this work was in flight — protowire-go's wrapper-default path had no bytes branch, so a default on a `BytesValue` field was rejected where the same literal on a plain `bytes` scalar decoded fine. The pin chameleon added to notice a fix did its job: [protowire-go#67](https://github.com/trendvidia/protowire-go/issues/67) added the branch and the v1.4.0 bump ([#139](https://github.com/trendvidia/chameleon/issues/139)) picks it up, so the exception never reaches a release.

  **Two more edges, documented and pinned rather than fixed here.** A default on a WKT is never template-expanded: defaults deliberately run before interpolation so a default may itself be a template (`"api.${base_url}"`), but interpolation stops at the same atomic boundary, so a `${...}` in a default on a `google.protobuf.StringValue` reaches the resolved config verbatim — use a plain `string` field when the default needs interpolation. And protowire-go parses bool literals as `literal == "true"`, so `"True"` / `"1"` / `"yes"` silently yield `false` (and still satisfy `(pxf.required)`) where every other literal kind reports a parse failure.

  `(pxf.default)` on a `repeated` or `map` field was still ignored at the time of writing, tracked in [#131](https://github.com/trendvidia/chameleon/issues/131) — and the panic found on the way (protowire-go's own per-parse pass had no list guard) was filed as [protowire-go#66](https://github.com/trendvidia/protowire-go/issues/66). Both are answered by the v1.4.0 bump ([#139](https://github.com/trendvidia/chameleon/issues/139)): the placement no longer binds, so the silence is gone before this release ships.

- **An explicit `chameleon … --decrypter=none` is no longer overridden by `$HOME/.chameleon`** ([#130](https://github.com/trendvidia/chameleon/issues/130)). The flag's Go default is `"none"`, so `applyAppDefaults` could not tell "the operator typed `--decrypter=none`" from "the flag was omitted" and treated both as unset — adopting the selected app's `decrypter = "sops"` in either case. On any machine whose user config named a decrypter, the one explicit way to decline decryption did nothing, and for `reveal` the result was the secret on stdout under a flag that asked for the opposite. Which flags the user actually typed is now read off the `flag.FlagSet`, so an explicit `--decrypter` always wins; with the flag omitted the config's value is still adopted, unchanged. Pinned end-to-end in `cli_smoke_test.go` against a user config that does name a decrypter.

  Only `--decrypter` is treated this way. `--layers` has the same "non-empty Go default" shape (`"."`) and the same blind spot, but its failure mode is a wrong path rather than an unasked-for decrypt; it is left as is here rather than folded into a security fix.

- **A `(pxf.default)` on a `google.protobuf.StringValue` is now template-expanded, like one on a plain `string`** ([#135](https://github.com/trendvidia/chameleon/issues/135)). The defaults pass runs before interpolation specifically so a default may itself be a template — `"api.${base_url}"` — and since [#129](https://github.com/trendvidia/chameleon/issues/129) a default can land on an atomic WKT. Interpolation stopped at the atomic boundary, so the two features did not compose: the raw `${...}` reached the resolved config verbatim, with no error and no warning.

  ```proto
  string                      base  = 1 [(pxf.default) = "example.com"];
  string                      api_s = 2 [(pxf.default) = "api.${base}"];
  google.protobuf.StringValue api_w = 3 [(pxf.default) = "api.${base}"];
  ```

  `api_s` resolved to `api.example.com` and `api_w.value` to the literal `api.${base}`. Both now resolve to `api.example.com`.

  **The boundary this crosses is narrower than it looks.** `wkt.Atomic` is unchanged, and merge, defaults and validate still treat every WKT as one whole value. Only interpolate's *write* walk descends, and only into `StringValue` — the new `wkt.TemplatableText` predicate, which is exactly one type wide and pinned that way per type. Interpolate's *lookup* walk is untouched: `${some_wrapper.value}` remains an `ErrInvalidReference`, as it does for a `pxf.Secret`. Writing into a WKT leaks nothing; reading out of one is the vector that boundary exists for, and it stays shut.

  The rewrite is reported to `OnApplied` — and so to `resolver.Trace` and `chameleon explain` — at the WKT field's own path rather than an inner `.value`, matching how every other pass keys a WKT.

- **A `oneof` member cleared with `= null` is no longer replaced by a sibling's `(pxf.default)`.** `defaults.Apply` asked the merged message which oneof arm was set, but `null` records the path as present *and* leaves the field unset — so an operator writing `file = null` to mean "no backend" got the sibling `timeout` default applied over it, the same data-loss shape as the oneof default fixed under **Changed**, reached through an explicit clear instead of an explicit choice. The guard now reads the presence union, which is what `pxf.ApplyDefault`'s contract asks of a `SkipPostDecode` caller and what protowire-go's own post-decode pass reads ([protowire-go#72](https://github.com/trendvidia/protowire-go/issues/72)); the message check is kept for callers who hand `defaults.Apply` a populated message with no presence map. Pinned by `TestApply_NulledOneofMemberSuppressesSiblingDefault`, plus a test that runs the same document through both passes and requires them to agree.

### Internal

- **Formatting is gofumpt now, and CI enforces it.** The repo had drifted: `make fmt-check` existed but was not in `make ci` or the CI matrix, so 24 of 162 tracked `.go` files were not even `gofmt`-clean, and "don't run `make fmt`" had become working folklore because a repo-wide run would have buried any real diff. Normalized in one pass — 25 files, no executable code touched — and the check is now a step in the `lint` job and a prerequisite of `make ci`, so it cannot drift again.

  gofumpt is applied **through the pinned golangci-lint**, not a standalone binary, and that is the load-bearing detail: a separately installed `gofumpt` drifts ahead of the version golangci-lint bundles (v0.10.0 splits `f(a, b,` onto its own line where the bundled build leaves it), and a formatter whose check and fix disagree is worse than no formatter at all. `make tools` already installs the pinned version and CI already runs it, so this adds no tool anywhere.

  Scope follows golangci-lint's own file discovery, which excludes generated code and the analyzer fixtures under `cmd/chameleon/testdata` for free — reformatting the first would be reverted by `make generate`, and the second is skipped because golangci-lint ignores `testdata` directories the way the go tool does. (`lint_test.go` would itself survive a reformat: it pins expectations with same-line `` // want `` markers, not absolute line numbers.)

  One sharp edge worth knowing before the next repo-wide run: gofmt's doc-comment printer rewrites a backtick pair and a single-quote pair into Unicode curly quotes. The normalization pass hit this twice — a backtick pair denoting the empty glob pattern in `internal/bundler/glob.go`, and an empty-string `--passphrase` argument in a `sops` test comment — turning both into punctuation that no longer says anything. Neither is recoverable by retyping, since the printer re-applies the rewrite on every `make fmt`; both comments were reworded to avoid the sequences. Formatting-only diffs are worth grepping for non-ASCII before merge.

- **Changelog entries are now one file per change, in [`changelog.d/`](changelog.d/).** `CHANGELOG.md` is written only at release time, by `make changelog VERSION=x.y.z`, which folds the fragments into a new section and deletes them. Nothing about the published format changes: a fragment's entire content *is* the entry, reproduced verbatim — markdown, tables and code blocks included — under the same Keep a Changelog headings. What did change in the migration is entry *order*, now derived from the fragment IDs, and the wording of the six entries whose cross-references were positional.

  **Why:** two PRs that both touch `CHANGELOG.md` conflict, every time, on a file where the conflict carries no information — you keep both sides and move on. That happened three times in one afternoon across #143, #146 and #148. A fragment per change cannot conflict, because no two PRs write the same file.

  CI requires a PR to add a fragment; label a PR `skip-changelog` when a change genuinely has nothing to tell a consumer. `make changelog-check` validates the filenames and is part of `make ci`.

  **One rule the old format did not need:** a fragment must not say "the entry below" or "described above". Order is derived from the fragment IDs at release time and shifts as other PRs land, so positional references break silently — six of the sixteen migrated entries pointed at the wrong thing after assembly, and were rewritten to name their target instead. `changelog.d/README.md` has the details.

## [0.22.1] — 2026-06-03

Patch release — dependency-only bump to [trendvidia/sops v4.0.1](https://github.com/trendvidia/sops/releases/tag/v4.0.1), plus a substantial round of test hardening across the chameleon code base (~25 PRs landed in `tests/` branches between #107 and #125). No chameleon API surface change, no operator action required. Drop-in upgrade for every consumer.

The user-visible diagnostic gain comes from sops: `time.Parse` errors inside `sops { }` metadata blocks now identify which field failed instead of surfacing the bare Go `time` package message — affects operators hand-editing sops files, not the chameleon resolver pipeline.

### Changed

- **Bumped trendvidia/sops to v4.0.1** ([#126](https://github.com/trendvidia/chameleon/pull/126)). Patch release on top of v4.0.0 — no API surface change, no operator action required. The diagnostic improvement: `time.Parse` errors in `sops { }` metadata blocks now name the offending field (`lastmodified`, `kms.created_at`, `gcp_kms.created_at`, `azure_kv.created_at`, `hc_vault.created_at`, `pgp.created_at`, `hc_kms.created_at`) instead of surfacing the bare `time` package message. Affects operators hand-editing sops files; chameleon's resolver pipeline is unchanged.

### Internal

- **Substantial test hardening pass** (PRs [#107](https://github.com/trendvidia/chameleon/pull/107)–[#125](https://github.com/trendvidia/chameleon/pull/125)). No behavior change; mentioned here as a confidence signal to consumers. Coverage uplifts at the package level: `internal/secretcatalog` 49% → 91%, `sops` 74% → 94%, `internal/header` 67% → 85%, `cmd/chameleon` 71% → 79%, `resolver` 87% → 93%. New fuzz harness on `bundle.Open` / `OpenBase64`. New integration suite (`-tags=integration`) covering CLI binary smoke + cross-subcommand data flow, multi-env age round-trip, real-gpg PGP round-trip (single, multi-recipient, mixed age+PGP), real-`protoc` plugin invocation, bash/zsh/fish completion-script syntax validation, and the destructive `rotate-key` operation with a cross-decrypt canary (new-key must decrypt; old-key must NOT). One latent test bug was found and fixed during the work — the rotate-key canary's "old-key MUST NOT decrypt" assertion was passing for the wrong reason because the Decrypter's wipe-on-success contract had consumed the input buffer; PR #125 corrected it by passing each Decrypt call its own ciphertext copy.

## [0.22.0] — 2026-06-01

Minor release — adopts [trendvidia/sops v4.0.0](https://github.com/trendvidia/sops/releases/tag/v4.0.0), which unifies the age key-source resolution between encrypt and decrypt around a single four-source contract (`SOPS_AGE_KEY_FILE > SOPS_AGE_KEY > SOPS_AGE_KEY_CMD > $HOME/.config/sops/age/keys.pxf`) plus a decrypt-only SSH side channel. Adds CLI flags (`--age-key-file`, `--age-key`, `--age-key-cmd`) translated to env vars at subcommand entry. **Action required for two consumer classes**: (1) operators relying on the implicit `~/.config/sops/age/keys.txt` line-based fallback must migrate to PXF `keys.pxf` at the new standardized default path or set `SOPS_AGE_KEY_FILE` explicitly; (2) Go consumers depending directly on chameleon's `sops` package surface must rewrite their own `trendvidia/sops/v3/...` imports to `/v4/...` and bump `require` to `github.com/trendvidia/sops/v4 v4.0.0`. Pure chameleon consumers (no direct `chameleon/sops/...` import) need no changes beyond `go get`.

### Changed

- **Bumped trendvidia/sops to v4.0.0 and migrated to the v4 module path** ([#105](https://github.com/trendvidia/chameleon/pull/105)). sops v4 unifies the age key-source resolution chain between encrypt and decrypt: both `pxfSources()` and `MasterKey.loadIdentities()` consult the same ordered list — `SOPS_AGE_KEY_FILE > SOPS_AGE_KEY > SOPS_AGE_KEY_CMD > $HOME/.config/sops/age/keys.pxf` (standardized default; PXF only, exists-gated) — plus a decrypt-only SSH side channel. sops also adds three CLI flags (`--age-key-file`, `--age-key`, `--age-key-cmd`) that translate to the corresponding env vars at subcommand entry so CLI > env > default precedence is symmetric on both paths. Module path bumps from `github.com/trendvidia/sops/v3` to `/v4` per Go semantic-import-versioning; every chameleon source file's `github.com/trendvidia/sops/v3/...` import was rewritten to `github.com/trendvidia/sops/v4/...`.

  **Breaking for sops consumers**: the implicit `~/.config/sops/age/keys.txt` line-based fallback (driven by `XDG_CONFIG_HOME` / `os.UserConfigDir()`) is gone. Operators relying on that file must either migrate to a PXF-formatted `keys.pxf` at `$HOME/.config/sops/age/keys.pxf`, or set `SOPS_AGE_KEY_FILE` / `--age-key-file` explicitly. SSH identities, `SOPS_AGE_KEY{,_FILE,_CMD}` env vars, and `--age=age1...` inline recipients continue to work unchanged. See the [trendvidia/sops v4.0.0 CHANGELOG](https://github.com/trendvidia/sops/blob/trendvidia/CHANGELOG.md#400) for the full migration note.

  **Breaking for chameleon Go consumers**: the import path bump v3 → v4. Downstream consumers need to rewrite their own `github.com/trendvidia/sops/v3/...` imports (if any direct dependency on the sops package surface) and bump their `require` line to `github.com/trendvidia/sops/v4 v4.0.0`. Code that only imports chameleon (not sops directly) needs no consumer-side change beyond `go get github.com/trendvidia/chameleon` to pick up the new version.

  Chameleon's `sops/decrypt.go` package doc rewrites the Credentials enumeration for the new four-source contract; the README's runtime-decryption credential-source table cell and the key-name lookup section both updated to match.

## [0.21.0] — 2026-06-01

Minor release — dependency-only release that promotes the `trendvidia/sops` soft-fork to direct consumption under its own canonical module path. **Action required for consumers** whose own `go.mod` carried `replace github.com/getsops/sops/v3 => github.com/trendvidia/sops/v3 ...`: that line must be dropped after upgrading to v0.21.0 (otherwise the consumer-side replace silently shadows chameleon's `require github.com/trendvidia/sops/v3 v3.16.0` and pins sops to whichever older fork tag the replace named). No chameleon Go API surface changed.

### Changed

- **Bumped trendvidia/sops to v3.16.0 and migrated to the fork's own module path** ([#102](https://github.com/trendvidia/chameleon/pull/102), [#103](https://github.com/trendvidia/chameleon/pull/103)). Two related dependency changes:
  - **v3.15.0 → v3.16.0 bump.** v3.15.0 broadened where multi-key PXF age files can live: the fork now content-sniffs `SOPS_AGE_KEY` and `SOPS_AGE_KEY_CMD` for the PXF top-level marker (previously PXF dispatch was driven only by `SOPS_AGE_KEY_FILE`'s `.pxf` suffix), and the encrypt-side default/name lookup walks the full `SOPS_AGE_KEY_FILE > SOPS_AGE_KEY > SOPS_AGE_KEY_CMD` chain so `chameleon migrate` / `chameleon bundle build` / `chameleon rotate-key` can resolve a chain-spec `key_name` from any of the three SOPS env vars. v3.16.0 promotes the fork to consumption under its own module path. No public API surface changed in chameleon; line-based callers see no behavior change because the probe only takes the PXF path when the bytes positively look like a PXF top-level entry.
  - **Dropped the `replace github.com/getsops/sops/v3 => github.com/trendvidia/sops/v3` directive.** v3.16.0 is published under the fork's canonical module path (`github.com/trendvidia/sops/v3`), so chameleon now imports it directly and the `go.mod` `replace` is gone. Every chameleon source file that imported `github.com/getsops/sops/v3/...` was rewritten to `github.com/trendvidia/sops/v3/...`. **Consumers must drop the corresponding `replace github.com/getsops/sops/v3 => github.com/trendvidia/sops/v3 ...` line from their own `go.mod`** — keeping it would pin to an older fork tag and silently shadow chameleon's v3.16.0 require. See README → Compatibility → Replace directives for the updated single-fork (`trendvidia/protobuf-go` only) consumer-side guidance.

## [0.20.0] — 2026-05-26

Minor release — per-env named encryption keys land end-to-end. `chain.ChainSpec.Environment` gains a `key_name` field that names the age key its layers are encrypted with; `chameleon migrate` and `chameleon bundle build` both honor it automatically, and a new `chameleon rotate-key` subcommand closes the rotation loop by re-encrypting every layer to the chain spec's current key set. `chameleon keystore add` rounds out the workflow with a guarded helper for populating `SOPS_AGE_KEY_FILE` without hand-editing. `BundleManifest.Module` gains independent narrowing of non-secret fields (`attribute_allow` / `attribute_deny` / `attribute_default`) alongside the existing secret selector — both selectors are Compile-time typo-guarded against the schema catalog.

### Added

- **Per-env named encryption keys in the chain spec** ([#93](https://github.com/trendvidia/chameleon/pull/93), [#96](https://github.com/trendvidia/chameleon/pull/96)). `Environment` gains a `key_name` field that names the age key its layers are encrypted with. The chain spec — not an out-of-band operator convention — is now the source of truth for "which key encrypts `prod`'s layers vs. `dev`'s". Resolution walks the ancestry leaf → root: a child env with empty `key_name` inherits the nearest non-empty ancestor's value, mirroring how `extends` already composes layer state.

  Two consumers honor `key_name`:
  - **`chameleon migrate`** picks the env's `key_name` when re-encrypting a layer. Envs without a `key_name` fall back to the existing `UsingSameKeysAsWithContext` path so unmigrated chains round-trip unchanged.
  - **`chameleon bundle build`** unions the `key_name`-derived recipient into the layer's recipient set alongside any `BundleManifest.recipients_by_env` entry. A typo'd or missing `key_name` fails at bundle-build, not at runtime decrypt.

  Names are resolved against `SOPS_AGE_KEY_FILE` (PXF multi-key format, `.pxf` extension) via the sops library's `RecipientFromKeyFileByName` — chameleon never reads the keystore directly. The trendvidia/sops fork was bumped from v3.13.2 → v3.14.0 to pick up the API.

- **Non-secret attribute narrowing in bundles** ([#94](https://github.com/trendvidia/chameleon/pull/94), [#95](https://github.com/trendvidia/chameleon/pull/95)). `BundleManifest.Module` gains three new fields — `attribute_allow`, `attribute_deny`, `attribute_default` — that gate non-secret schema-form field paths the same way the existing `allow` / `deny` / `default` gate `pxf.Secret`-bearing fields. The two selectors are evaluated independently: a secret field's verdict doesn't depend on the attribute filter and vice versa.

  Use cases:
  - Strip debug-only config from a production module's bundle (`attribute_deny: ["debug.*"]`).
  - Whitelist a narrow non-secret surface for a third-party consumer (`attribute_allow: ["public.*"]` with implicit `DENY_ALL`).
  - Mixed posture: `attribute_default: ALLOW_ALL` with selective `attribute_deny` carve-outs.

  Behavior is unchanged for manifests that don't set any `attribute_*` field: the attribute selector defaults to `ALLOW_ALL` and non-secrets pass through verbatim, matching the pre-#94 "secrets-only narrowing" behavior.

  `attribute_*` patterns get the same Compile-time typo guard as secret patterns: a new `secretcatalog.FindNonSecretFieldsInMessage` enumerates every non-secret field path in the consumer schema, and a pattern matching nothing is a bundler error. `bundler.Compile`'s third argument changed from `[]secretcatalog.Site` to a `Catalog{Secrets, NonSecrets}` struct to carry both enumerations; a zero `Catalog{}` (or a `Catalog` with one nil side) skips the membership check on that side while keeping glob syntax validation.

- **`chameleon rotate-key`** ([#98](https://github.com/trendvidia/chameleon/pull/98)). New subcommand that closes the per-env named-key rotation loop opened by #93 / #96. Walks the chain spec, identifies each unique encrypted layer plus the envs that reach it, resolves the union of those envs' `Environment.key_name` through the sops library, and re-encrypts each layer in place. Supports `--dry-run` for preflighting which files would change.

  Typical workflow (replacing the hand-rolled `sops rotate --in-place --add-age ... --rm-age ...` per file):

  ```sh
  chameleon keystore add --name=prod-key-2026     # 1. add new key
  # edit chain.pxf: Environment.key_name → "prod-key-2026"
  chameleon rotate-key --chain=chain.pxf          # 2. re-encrypt every layer
  # manually remove the old entry from SOPS_AGE_KEY_FILE
  ```

  Refusal rule: a shared layer reachable from an env without `key_name` is skipped (re-encrypting would lock that env out). The skip message names the unresolved envs so the operator can either set `key_name` on every reaching env or accept that the layer can't be rotated yet.

- **`chameleon keystore add`** ([#99](https://github.com/trendvidia/chameleon/pull/99)). New subcommand that appends a named entry to the PXF-formatted SOPS age key file. Either generates a fresh X25519 identity or adopts a supplied one (`--identity=AGE-SECRET-KEY-...`). Writes atomically via temp-file + rename at `0o600`; refuses to overwrite an existing entry without `--force`. The new entry's recipient is printed to stdout (pipeable); the identity stays inside the keystore file.

  Defaults `--file` to `$SOPS_AGE_KEY_FILE` and refuses paths not ending in `.pxf` (the sops fork dispatches its PXF parser on the suffix). X25519 only for first cut; plugin / hybrid identities (yubikey, post-quantum) need IPC-aware recipient derivation and are deferred.

### Changed

- **`bundler.Filter.Allows` renamed to `AllowsSecret`** ([#94](https://github.com/trendvidia/chameleon/pull/94)). The historical method was always secret-only in semantics; the rename makes that explicit and pairs with the new `AllowsAttribute`. The package is internal (`internal/bundler`), so no public API surface broke.

## [0.19.1] — 2026-05-25

Patch release — fixes the bundle-input source-registration gap shipped in v0.19.0. `reveal` / `get` / `explain` with `--bundle*` were only registering the bundle's FS as `"default"`, so chains declaring additional sources (most commonly `kind=env` for runtime overrides at the top of the chain) errored mid-resolve with `unregistered source`. The new `buildBundleSources` helper walks the chain spec the same way the on-disk path does, restoring on-disk-vs-bundle parity for every reveal/get/explain flag combination.

### Fixed

- **`chameleon reveal` / `get` / `explain` with `--bundle*` now register every source declared in the bundle's chain spec, not just the bundle's FS as `"default"`** ([#91](https://github.com/trendvidia/chameleon/pull/91)). Previously the three subcommands hardcoded a one-entry source map when bundle input was active, so chains that declared additional sources — most commonly `kind=env` for runtime overrides at the top of the chain — errored mid-resolve with `resolver: layer references unregistered source: layer="<id>" source="<name>"`. A new `buildBundleSources` helper (sibling to `buildSources`) walks the chain spec's declared sources, registers `kind=env` from the schema descriptor, and routes every `kind=fs` source — declared or implicit — to the bundle's FS root. Same encrypted-on-env-source rejection rule as `buildSources`. Tests in `cmd/chameleon/bundleflags_test.go` cover the env-source happy path for reveal/get/explain plus the encrypted-on-env-source rejection.

## [0.19.0] — 2026-05-24

Minor release — closes the "ship a bundle → debug it" loop. `chameleon reveal` / `get` / `explain` now accept bundle input via `--bundle=PATH` (file), `--bundle=-` (stdin), or `--bundle-env=VAR` (env var read by name, so the bundle bytes never leak onto the process command line). `chameleon bundle build` gains a `--force` flag for the everyday "edit manifest → rebuild" loop. Plus a long-overdue test isolation fix that retires the "(pre-existing test failures, unrelated)" caveat we've been carrying in every release PR since #80.

### Added

- **`chameleon bundle build --force`** ([#89](https://github.com/trendvidia/chameleon/pull/89)). Truncates and overwrites an existing `--out` file. Without `--force`, the bundler errors on an existing path (the original safety default — typo'd paths can't clobber pinned release artifacts or files in active use). Unblocks the iterative "edit manifest → rebuild" loop without needing an `rm` step.
- **Bundle input on `chameleon reveal` / `get` / `explain`** ([#89](https://github.com/trendvidia/chameleon/pull/89)). The three resolution CLI subcommands can now read their chain spec + layer FS from a bundle instead of loose `--chain` + `--layers` paths. Three mutually-exclusive input forms:
  - `--bundle=PATH` — file on disk.
  - `--bundle=-` — stdin (pipe straight from a secret-manager CLI).
  - `--bundle-env=VAR` — env var by NAME. The CLI reads the variable's value in-process so the bundle bytes never appear on the process command line (unlike `--bundle="$VAR"`, which would leak via `ps` / `/proc/$pid/cmdline`).

  Add `--base64` to any form when the bundle is base64-wrapped (matches `chameleon bundle build --base64` output). `--bundle*` flags are mutually exclusive with `--chain` / `--layers`; combining them errors at flag-parse time.

### Fixed

- **`TestExplainCmd_MissingRequiredFlag` / `TestGetCmd_MissingRequiredFlag` isolation** ([#89](https://github.com/trendvidia/chameleon/pull/89)). Both tests now pin `CHAMELEON_USER_CONFIG` to a non-existent path so the developer's `~/.chameleon` app entry can't silently fill in `--schema` and skip the required-flag check. Eliminates the long-running "two unrelated pre-existing test failures" caveat that's been in every release PR since #80.

## [0.18.0] — 2026-05-23

Minor release — chameleon learns to **bundle** configuration for distribution. A new end-to-end pipeline produces a per-module zip artifact (chain spec + narrowed, re-encrypted layers) suitable for stuffing into a single secret-manager value. A matching consume-side public API (`bundle.Open` / `bundle.OpenBase64`) returns a `Bundle{FS, Chain, Audit}` that plugs straight into the existing `chameleon.Load` — no special "bundle mode" at runtime. Per-module allow/deny over secret-bearing field paths means a compromised bundle only leaks the surface area that module was authorized to see, and per-env recipient sets keep env isolation in the ciphertext of a single env-agnostic bundle.

### Added

- **Distribution bundles** ([#80](https://github.com/trendvidia/chameleon/pull/80) → [#85](https://github.com/trendvidia/chameleon/pull/85)).
  - **`chameleon bundle build`** — decrypts a chain's layers, narrows each to a per-module allowlist of `pxf.Secret` field paths declared in a `BundleManifest`, re-encrypts with module-specific recipients, and packages chain spec + layers as a zip. `--base64` wraps the output for paste-into-secret-manager-field workflows.
  - **`bundle.Open(r)` / `bundle.OpenBase64(r)`** — new public package. Returns `Bundle{FS, Chain, Audit}` that plugs straight into `chameleon.Load` — the runtime sees the bundle as just another `fs.FS` source.
  - **`sops.Encrypter` + `sops.DefaultEncrypter()`** — symmetric to the existing `Decrypter`. Backed by getsops/sops/v3's encrypt package (via the trendvidia/sops fork's stable top-level encrypt API).
  - **`BundleManifest`** proto adds `Module` entries with `allow` / `deny` schema-path globs, per-env `recipients_by_env` (so a single env-agnostic bundle still preserves env isolation in ciphertext), and a `default` `Policy` (`ALLOW_ALL` / `DENY_ALL`) controlling fallthrough.

  Manifest patterns are validated against `internal/secretcatalog` at bundle-build time — a typo'd allow-glob fails the bundler instead of silently producing an over-restricted bundle.

## [0.17.0] — 2026-05-16

Minor release — `chameleon` learns a second `Source` kind:
`kind = "env"` reads process environment variables at resolve
time and emits a synthetic PXF layer the existing parse + merge
pipeline consumes like any other layer. Downstream consumers
that previously bridged chameleon-resolved values into env-var-
driven binaries via sidecar scripts can now declare an env
layer in the chain spec and delete the script.

### Added

- **`source.Env(desc, ...EnvOption)` + `kind = "env"` chain spec
  support** ([#78](https://github.com/trendvidia/chameleon/pull/78)).
  At `Open()` time the source walks the resolved message's
  descriptor, derives an env-var name per scalar leaf (dotted
  field path → `UPPER_SNAKE` with optional source-level prefix
  via `source.WithPrefix("FOO_")`), coerces the env value to the
  field's typed value, builds a partial `dynamicpb.Message`, and
  marshals it as a PXF document the resolver merges normally.

  Discovery rules:
    - Recurses into singular nested message fields (so
      `anthropic.api_key` under `AppConfig.Anthropic` works).
    - Skips `repeated` and `map` fields — env vars are flat;
      structured overrides belong in pxf files.
    - Treats `pxf.Secret`, `google.protobuf.Duration`, and
      `google.protobuf.Timestamp` as scalar leaves sourced from
      a single env var rather than descending into
      `seconds`/`nanos`/`value` sub-fields.

  Coercion covers string, bool, int{32,64}, uint{32,64},
  float{32,64}, bytes (base64), `pxf.Secret`, `Duration` (Go
  duration syntax), and `Timestamp` (RFC 3339). Parse failure
  surfaces as a loud `Open` error citing the field path; an
  unset env var is silent — that's "no override," not an error.

  Example chain spec:

  ```pxf
  sources = {
    "default": { kind = "fs"  options = { "root": "./layers" } }
    "env":     { kind = "env" options = { "prefix": "STEWARD_" } }
  }

  layers = [
    { id = "base"        path = "base.pxf"        for_each = ONCE }
    { id = "env-layer"   path = "env/${env}.pxf"  for_each = PER_ENV }
    { id = "env-runtime" path = "(env)"           source = "env" for_each = ONCE }
  ]
  ```

  Setting `STEWARD_ANTHROPIC_API_KEY=sk-...` resolves into
  `cfg.Anthropic.ApiKey` with the same `pxf.Secret` semantics as
  if the value had come from a sops-encrypted file.

- **`cmd/chameleon` recognises `kind = "env"`**
  ([#78](https://github.com/trendvidia/chameleon/pull/78)). The
  CLI's `buildSources` now takes a `MessageDescriptor` and
  dispatches env-kind entries to `source.Env`. `get` / `reveal` /
  `explain` pass the descriptor they already compile.

- **Chain validation rejects encrypted env layers**
  ([#78](https://github.com/trendvidia/chameleon/pull/78)). A
  `LayerEntry` that references an env-kind source with
  `encrypted = true` fails at `buildSources` time — sops has
  nothing to decrypt for env vars, and failing fast beats a
  confusing runtime "no sops metadata" error.

- **`docs/proposals/`** ([#77](https://github.com/trendvidia/chameleon/pull/77)).
  New directory for ADR-style design memos that need review
  before code lands. The env-source design lives there as
  `0001-env-source.md` with a "What landed" section closing the
  loop between proposed and shipped.

### Deferred

Documented as followups in
[`docs/proposals/0001-env-source.md`](docs/proposals/0001-env-source.md):

- `[(chameleon.env_skip) = true]` field annotation for opting a
  field out of env override. Needs `pxf/annotations.proto`
  change + schema-wide regen across consumers.
- `chameleon explain` provenance customization for env-sourced
  fields (cite env var name instead of file path).
- `chameleon reveal` flagging absence of at-rest encryption
  when a `Secret` resolves from an env layer.
- Enum coercion (currently rejected as unsupported field kind).

## [0.16.0] — 2026-05-14

Minor release — `chameleon` learns per-user, per-app flag
defaults from a centralized `$HOME/.chameleon` PXF config file.
Scripts can now invoke `chameleon get` / `chameleon reveal` /
`chameleon explain` / `chameleon verify` from any working
directory with `--app=<name>` (or no flags at all, if the file
declares a default) instead of repeating the full
`--schema` / `--include` / `--message` / `--chain` / `--layers` /
`--env` / `--decrypter` surface every invocation.

### Added

- **`$HOME/.chameleon` user config**
  ([#75](https://github.com/trendvidia/chameleon/pull/75)). New
  `chameleon.v1.UserConfig` proto message
  (`proto/chameleon/v1/user_config.proto`) backs the file; it is
  parsed via `protowire-go/encoding/pxf.Unmarshal` so the format
  is dogfooded PXF — no TOML/YAML/JSON dependency. Centralized
  (one file per user, all apps inside) rather than per-project,
  so callers don't need to `cd` into a project tree to pick up
  defaults.

  Example:

  ```pxf
  @type chameleon.v1.UserConfig

  default_app = "steward"

  apps = {
    "steward": {
      root      = "/Users/bene/projects/src/github.com/trendvidia/website"
      schema    = "proto/steward/config/v1/config.proto"
      include   = ["proto"]
      message   = "steward.config.v1.AppConfig"
      chain     = "config/chain.pxf"
      layers    = "config/layers"
      env       = "dev"
      decrypter = "sops"
    }
  }
  ```

  After this lands:

  ```sh
  chameleon get | pxf query '.server.addr'    # default app
  chameleon reveal --field=paddle.api_key     # default app
  chameleon get --app=worker --env=prod       # different app + env override
  ```

- **`--app=<name>` flag on `explain`, `get`, `reveal`, `verify`**
  ([#75](https://github.com/trendvidia/chameleon/pull/75)).
  Selects which `apps[<name>]` entry from `$HOME/.chameleon`
  supplies flag defaults for this invocation. Resolution rules:
  (1) `--app=foo` explicit → `apps["foo"]` (error listing
  available if absent); (2) `--app` omitted and `default_app`
  set → that one; (3) single-entry `apps` and no `default_app`
  → use it (single-app shortcut); (4) otherwise error listing
  available apps. CLI flags always beat config values; the Go
  defaults `--layers="."` and `--decrypter="none"` are treated
  as "unset" by the merge so the app's values still take effect
  when the CLI invocation omits those flags.

  Subcommands without overlapping flag surface (`lint`,
  `migrate`, `hash`, `version`, `completion`) are intentionally
  out of scope.

## [0.15.0] — 2026-05-14

Minor release — closes the `get` / `reveal` story for secret
handling in scripts. `chameleon get` now renders Secret-typed
fields with an unambiguous `<redacted>` sentinel (was empty
string), and the new `chameleon reveal` subcommand is the
explicit, opt-in, single-value path for getting plaintext into a
shell variable.

### Changed

- **`chameleon get` renders `pxf.Secret` fields as `<redacted>`**
  ([#72](https://github.com/trendvidia/chameleon/pull/72)).
  `parse.Move` clears `pxf.Secret.value` to `""` on the resolver's
  output (the plaintext is moved into a memguard-backed
  `secret.Map` instead), so previously `chameleon get` emitted

  ```pxf
  api_key = ""
  password = ""
  ```

  for every Secret field — indistinguishable from "operator
  deliberately set the value to empty," "field is absent," and
  "field is a redacted secret." `get` now walks the resolved
  message before marshalling and writes `<redacted>` into every
  `pxf.Secret.value`, so the output is

  ```pxf
  api_key = "<redacted>"
  password = "<redacted>"
  ```

  unambiguously signalling "deliberately suppressed; plaintext
  lives in `res.Secrets`, not here." Plain string scalars are
  untouched.

  *This is a user-visible output change* — scripts that grep for
  `value = ""` to detect redacted secrets must switch to
  `<redacted>`. Hence the minor bump.

### Added

- **`chameleon reveal --field=<dotted.path>`**
  ([#73](https://github.com/trendvidia/chameleon/pull/73)).
  The deliberate, opt-in, single-value path for getting secret
  plaintext into a script:

  ```sh
  export PADDLE_API_KEY=$(chameleon reveal \
      --field=paddle.api_key \
      --schema=../proto/steward/config/v1/config.proto \
      --include=../proto \
      --message=steward.config.v1.AppConfig \
      --chain=chain.pxf --layers=layers --env=dev \
      --decrypter=sops)
  ```

  Surface mirrors `get` with three deltas: `--field` is
  required and singular (no tree dumps; forces the caller to
  name the target); the named field is validated against the
  message descriptor before the resolver runs (typo'd paths and
  non-Secret targets error with clean diagnostics instead of
  conflating into "secret not set"); stdout is exactly the
  plaintext bytes with no trailing newline (critical for
  `$(...)` substitution where a trailing `\n` would corrupt the
  captured value).

  For plain (non-Secret) scalars, use `chameleon get | pxf
  query`. Reveal refuses non-Secret paths to avoid surprising
  callers who expected the safety properties of the Secret
  pipeline.

## [0.14.4] — 2026-05-14

Additive patch — new `chameleon get` subcommand emits the resolved
config as a PXF document, composing with `pxf query` for dotted-
path extraction in shell scripts.

### Added

- **`chameleon get`** ([#70](https://github.com/trendvidia/chameleon/pull/70)).
  `explain` prints a human-readable provenance trace (value +
  sensitivity + layer tags on one line); `get` prints the
  resolved message itself, marshalled via
  `protowire-go/encoding/pxf.MarshalOptions{TypeURL: ...}.Marshal`.
  Same flag surface as `explain` (schema / message / chain /
  layers / env / include / ctx / decrypter), minus `--field`
  (pxf query owns dotted-path extraction), plus `--emit-type`
  (default `true`) controlling whether the output is prefixed
  with `@type <message>` for query-time descriptor binding.
  `--emit-type=false` strips the directive for callers
  concatenating outputs or supplying their own `@type`.

  ```sh
  chameleon get \
      --schema    ../proto/steward/config/v1/config.proto \
      --include   ../proto \
      --message   steward.config.v1.AppConfig \
      --chain     chain.pxf --layers layers --env dev \
      --decrypter sops \
    | pxf query '.server.addr'
  # value = ":8080"
  ```

  Secret fields surface with empty `value` and intact `hint` /
  `fingerprint` metadata — `parse.Move` clears plaintext into a
  memguard-backed `secret.Map` *before* the resolver returns, so
  `pxf.Marshal` can never observe plaintext on the message. A
  dedicated reveal API (with explicit naming and an opt-in
  posture matching 1Password's `op read`) is the right surface
  for scripts that need secret plaintext, and is not part of
  this release.

## [0.14.3] — 2026-05-14

Bug-fix + additive patch — `chameleon explain` is now usable
against real chain specs that declare named sources, anchors
declared source roots relative to `chain.pxf`, and can decrypt
sops-encrypted layers via the ambient sops/age/KMS stack.

### Fixed

- **`chameleon explain` registers every layer-referenced source
  name** ([#68](https://github.com/trendvidia/chameleon/pull/68)).
  Before this release `explainCmd` hardcoded
  `WithSource("default", ...)` and ignored `spec.sources`
  entirely, so any chain spec that used named sources errored
  immediately:

  ```
  chameleon explain: resolver: layer references unregistered
  source: layer="secrets-dev" source="secrets"
  ```

  Replaced with a two-pass `buildSources` helper: (1) every
  source declared in `spec.sources` is registered (kind=fs
  supported; other kinds error since `explain` only knows fs),
  and (2) every source NAME referenced by a layer not declared
  in pass 1 is auto-registered against `--layers`. The
  auto-registration handles the common dev shape — a single
  layers dir with logical name prefixes — without requiring a
  `sources` block in chain.pxf.

- **Declared relative source roots anchor at `chain.pxf`'s
  directory, not at `--layers`**
  ([#68](https://github.com/trendvidia/chameleon/pull/68)). A
  chain.pxf that declares `root = "./layers"` next to itself
  and is invoked as `chameleon explain --chain config/chain.pxf
  --layers config/layers` previously double-joined to
  `layers/layers/base.pxf`, producing
  `resolver: required layer missing: layer="base"
  path="base.pxf"`. Spec authors write declared roots relative
  to chain.pxf (that's how they look at the path next to the
  file on disk), so `buildSources` now anchors relative roots
  at `filepath.Dir(--chain)`. The `--layers` flag remains the
  anchor for the auto-registration pass and for declared
  sources with an empty root, preserving the historical
  single-source case.

### Added

- **`chameleon explain --decrypter <none|sops>`**
  ([#68](https://github.com/trendvidia/chameleon/pull/68)). The
  resolver eagerly rejects encrypted layers when no Decrypter
  is configured — before attempting to open the file — so a
  chain spec with an optional encrypted layer for a different
  environment (e.g. `secrets-prod.sops.pxf`, missing in dev)
  surfaced as `resolver: encrypted layer without Decrypter`
  during a dev `explain` run. `--decrypter sops` wires
  `chsops.Default()`, sourcing credentials from the ambient
  environment (AWS_*, GOOGLE_APPLICATION_CREDENTIALS,
  `~/.config/sops/age/keys.txt`, …) — same surface as a
  production binary's `WithDecrypter` wiring, so the dev
  workflow exercises the real decrypt path. `none` (the
  default) preserves the historical behavior so any spec that
  worked before continues to work; unknown values error rather
  than silently falling back to `none` (a typo'd
  `--decrypter=spos` must not quietly disable decryption).

  Combined invocation against a real chain.pxf with a prod-
  only encrypted layer:

  ```sh
  chameleon explain \
      --schema    ../proto/steward/config/v1/config.proto \
      --include   ../proto \
      --message   steward.config.v1.AppConfig \
      --chain     chain.pxf --layers layers --env dev \
      --decrypter sops
  ```

  prints the full provenance trace; the encrypted prod layer
  silently skips because its `.sops.pxf` doesn't exist for
  env=dev.

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

[Unreleased]: https://github.com/trendvidia/chameleon/compare/v0.25.1...HEAD
[0.25.1]: https://github.com/trendvidia/chameleon/compare/v0.25.0...v0.25.1
[0.25.0]: https://github.com/trendvidia/chameleon/compare/v0.24.0...v0.25.0
[0.24.0]: https://github.com/trendvidia/chameleon/compare/v0.23.0...v0.24.0
[0.23.0]: https://github.com/trendvidia/chameleon/compare/v0.22.1...v0.23.0
[0.22.1]: https://github.com/trendvidia/chameleon/compare/v0.22.0...v0.22.1
[0.22.0]: https://github.com/trendvidia/chameleon/compare/v0.21.0...v0.22.0
[0.21.0]: https://github.com/trendvidia/chameleon/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/trendvidia/chameleon/compare/v0.19.1...v0.20.0
[0.19.1]: https://github.com/trendvidia/chameleon/compare/v0.19.0...v0.19.1
[0.19.0]: https://github.com/trendvidia/chameleon/compare/v0.18.0...v0.19.0
[0.18.0]: https://github.com/trendvidia/chameleon/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/trendvidia/chameleon/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/trendvidia/chameleon/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/trendvidia/chameleon/compare/v0.14.4...v0.15.0
[0.14.4]: https://github.com/trendvidia/chameleon/compare/v0.14.3...v0.14.4
[0.14.3]: https://github.com/trendvidia/chameleon/compare/v0.14.2...v0.14.3
[0.14.2]: https://github.com/trendvidia/chameleon/compare/v0.14.1...v0.14.2
[0.14.1]: https://github.com/trendvidia/chameleon/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/trendvidia/chameleon/compare/v0.13.0...v0.14.0
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
