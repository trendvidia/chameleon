# Roadmap

Things deliberately deferred from the v0.x line. Order is rough priority — items closer to the top are more likely to land first.

## Deferred — significant work, separate effort

### P2: Typed `ChainSpec` access via generated bindings

`chain/expand.go` accesses `ChainSpec` fields by name via `protoreflect.FieldDescriptors.ByName(...)`. Typos in `expand.go` itself return `nil` silently rather than failing at compile time. Once `protoc-gen-go` is wired against `proto/chameleon/v1/chain.proto`, swap the descriptor-driven access for typed field access.

**Why deferred:** requires setting up codegen for chameleon's own `.proto` files (`chain.proto`, `layer.proto`). Chameleon's testing pattern uses `bufbuild/protocompile` to compile schemas inline; there's no production code path that consumes generated chameleon-side bindings yet. Wiring codegen means deciding where the bindings live, how `go generate` is configured, and how downstream consumers handle the generated package.

**Impact when done:** type safety in `chain/expand.go`. No user-visible API change.

### `protoc-gen-chameleon-go` field-replacement variant

Current sidecar mode emits typed accessors *alongside* protoc-gen-go's output, but the proto-generated `*pxfv1.Secret.Value` field is still a publicly readable `string` on the struct. Reads return `""` after `Resolve` (because `parse.Move` clears it post-merge), but the footgun exists: a developer who writes `cfg.DbPassword.Value` gets a confusing empty string instead of a compile error.

The field-replacement variant would make the field type `*chameleon/secret.Secret` directly on the public struct, with no exposed `Value` accessor. That requires implementing `proto.Message` for a hand-rolled type — substantial work because `ProtoReflect` has many invariants the standard codegen handles.

**Why deferred:** the sidecar gives ~80% of the value (typo-safe paths, single source of truth) for ~20% of the cost. Field-replacement is the upgrade for environments where the empty-Value footgun causes real bugs in practice — a real but not-yet-pressing concern.

**Impact when done:** `cfg.DbPassword.Value` becomes a compile error. Plaintext-in-heap window between PXF parse and `parse.Move` closes (the custom protoreflect impl can route bytes straight from the lexer into a memguard Enclave with no transient `string` allocation).

## Skipped — low value vs. cost

### Real-sops integration test with programmatic encryption

The chameleon `sops.Decrypter` wrapper is plumbing around `getsops/sops/v3/decrypt.Data`. Its non-trivial logic is:
- copying decrypted plaintext into a memguard `LockedBuffer`
- wiping the ciphertext slice
- propagating the `sops:` error prefix

All three are tested at the unit level with the existing `decrypt_test.go`. Adding a "real sops file with a known age key" round-trip test would only verify that `decrypt.Data` works against real bytes — which is sops's job to guarantee, not chameleon's.

Programmatic encryption at test setup would require importing `getsops/sops/v3/cmd/sops/common` (unstable sops internals: `EncryptTree`, `Tree`, etc.). That couples chameleon's test suite to a non-stable API surface for marginal gain.

Production consumers wanting end-to-end confidence can drop their own sops-encrypted fixture into the test directory and exercise it under `-tags integration`. The shape of that test is two lines.

## Out of scope (intentional non-goals)

### File-watch / hot-reload

Resolved configs are immutable; if you need reload-on-change, wrap the `Resolver` with a watcher in your own code. Pulling fsnotify or similar into chameleon's surface would force a transitive dep on every consumer.

### Logging hooks

Distinct from metrics (which we DO support — see OTel below). Chameleon doesn't emit log lines; if you want a log message per Resolve, log around the call site. Adding a `slog.Logger` option to the resolver inflates the public surface for marginal value.

### Proto2 group support

PXF is proto3-only by spec; the codec never produces a `GroupKind` field. Chameleon's pipeline matches that scope — `case fd.Kind() == protoreflect.MessageKind` everywhere, no `GroupKind` branch. If a future PXF revision admits proto2, this gets revisited; until then, supported-surface clarity wins.

### Sops `context.Context` mid-call cancellation

`getsops/sops/v3` doesn't expose a `ctx` hook on its decrypt API. Chameleon's `Decrypter.Decrypt` checks `ctx.Err()` before invoking sops (so a pre-cancelled context short-circuits) but cannot cancel an in-flight KMS call. Filing an upstream PR against sops to add `ctx` support is the long-term fix; out of chameleon's scope to work around.
