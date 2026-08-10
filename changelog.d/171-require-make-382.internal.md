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

  **On macOS: `brew install make` and use `gmake`** — Homebrew deliberately does not displace `/usr/bin/make`. Linux and the CI Linux runner already ship ≥ 4.x. CI gained a step that prints and asserts the runner's version, because nothing in a log said what it was.
