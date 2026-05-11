# Roadmap

Each milestone groups issues that ship together. Detailed designs live in the linked issues; this file is the index.

## Milestone 1 — v0.6.0 (chameleon-internal, cheap)

All three reuse the existing OTel meter plumbing. No cross-repo coordination.

- **[#2 — merge: warn when a higher layer clobbers WKT metadata](https://github.com/trendvidia/chameleon/issues/2)** — detect when `base.pxf`'s `db_password { value="x" hint="..." }` gets clobbered by `env/dev.pxf`'s `db_password = "y"`. Surface via `Resolution.Trace` + new OTel counter `chameleon.merge.clobbered_metadata`. Opt-in strict mode upgrades to error.
- **[#3 — secret: per-Open() observability](https://github.com/trendvidia/chameleon/issues/3)** — three new instruments (`chameleon.secret.opens` counter, `.active` gauge, `.open.duration` histogram). Wraps memguard's `LockedBuffer` so chameleon can track active holders, not just resolve-time counts.
- **[#4 — docs: pair resolve-time provenance with runtime observability](https://github.com/trendvidia/chameleon/issues/4)** — README "Debugging" section that distinguishes `chameleon explain` (compile-time: which layer set X) from per-Open metrics (runtime: who's holding X right now). Ships with #3.

Plus upstream: **[getsops/sops#2179 — decrypt API lacks context.Context](https://github.com/getsops/sops/issues/2179)** filed; no chameleon code change required, but pre-existing limitation graduates from "out of scope" to "tracked upstream."

## Milestone 2 — v0.7.0 (new tooling)

- **[#5 — cmd/chameleon migrate: bulk-edit encrypted layers](https://github.com/trendvidia/chameleon/issues/5)** — `chameleon migrate --transform=add-field --path=db.password --kind=secret` decrypts every layer in a chain spec, applies the transform at the PXF AST level, re-encrypts. Adds an encrypt wrapper to chameleon's `sops` package alongside the existing decrypt wrapper.
- **[#6 phase 1 — chameleon-lint: flag `cfg.X.Value` reads at CI time](https://github.com/trendvidia/chameleon/issues/6)** — Go AST analyzer that catches the `pxfv1.Secret.Value` footgun in consumer code without waiting for the full field-replacement codegen. 95% of the protection for 5% of the cost.

## Milestone 3 — v1.0 prep (cross-repo, weeks of work)

- **[#6 phase 2 — protoc-gen-chameleon-go field-replacement mode](https://github.com/trendvidia/chameleon/issues/6)** — `--mode=replace` emits a struct that has no `Value` field exposed. `cfg.DbPassword.Value` becomes a compile error. Requires implementing `proto.Message` manually for the replacement type.
- **[#7 — protowire-go Secret-direct decode path](https://github.com/trendvidia/chameleon/issues/7)** — protowire-go v0.73.0 adds an `OnSecretField` decoder hook so chameleon can route bytes from the codec directly into a memguard Enclave, closing the plaintext-in-heap window between `pxf.UnmarshalFull` and `parse.Move`. Pairs with #6 phase 2 — together they ship "no public Value field, no transient string allocation."

---

## Other deferred items

### P2: Typed `ChainSpec` access via generated bindings

`chain/expand.go` accesses `ChainSpec` fields by name via `protoreflect.FieldDescriptors.ByName(...)`. Typos in `expand.go` itself return `nil` silently rather than failing at compile time. Once `protoc-gen-go` is wired against `proto/chameleon/v1/chain.proto`, swap the descriptor-driven access for typed field access. **Why deferred:** requires setting up codegen for chameleon's own `.proto` files. **Impact when done:** type safety in `chain/expand.go`, no user-visible API change.

### Resolver-time `content_sha256` verification

The `chameleon verify` CLI checks `@header.content_sha256` on demand. The resolver itself parses the field but doesn't run the check at every `Resolve` — that would need a warning channel (the spec says "warn on mismatch, don't reject") and chameleon has intentionally avoided a logger ABI (see "Logging hooks" under non-goals below). A `resolver.WithContentSHA256Check` option could surface as either a hard-error mode or via the OTel meter; neither is yet pressing. **Why deferred:** out-of-band CLI verification covers the typical "did this file get tampered with?" workflow without inflating the resolver's hot path.

---

## Skipped — low value vs. cost

### Real-sops integration test with programmatic encryption

The chameleon `sops.Decrypter` wrapper is plumbing around `getsops/sops/v3/decrypt.Data`. Its non-trivial logic (memguard copy, ciphertext wipe, error prefix propagation) is tested at the unit level. Adding a "real sops file with a known age key" round-trip test would only verify `decrypt.Data` works against real bytes — sops's job, not chameleon's. Programmatic encryption at test setup requires unstable sops internals (`EncryptTree`, `Tree`); couples the test suite to a non-stable API for marginal gain.

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

### Sops `context.Context` mid-call cancellation — now tracked upstream

Was "out of scope" because no upstream path existed. Filed [getsops/sops#2179](https://github.com/getsops/sops/issues/2179) proposing `decrypt.DataWithContext`. Chameleon's `Decrypter.Decrypt` continues to check `ctx.Err()` pre-call; once sops merges the upstream change, chameleon threads `ctx` through and the limitation closes without further chameleon design work.
