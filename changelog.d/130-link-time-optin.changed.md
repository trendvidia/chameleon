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
