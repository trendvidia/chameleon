# chameleon

Schema-driven layered configuration for Go. Define your config shape in proto, declare an environment-ancestry chain spec describing how layers fall through, get back a strongly-typed Go struct plus memguard-backed secrets at runtime.

Built on [protowire-go](https://github.com/trendvidia/protowire-go) (PXF text format with three-state field presence) and [getsops/sops/v3](https://github.com/getsops/sops) (encrypted layer files).

## Why

You have a config schema that's mostly the same across `prod` / `sandbox` / `uat` / `dev`, with a few per-environment tweaks (URLs, credentials, feature flags). You want:

- One source of truth for the schema (`.proto`).
- Layers per environment that fall through to a base, with explicit nulling, scalar defaults, and `${field.path}` interpolation.
- Hierarchy declared by ops, not hardcoded — `dev` extends `sandbox` extends `prod` is data, and the consumer can supply additional context (`region=eu`) that influences which layers load.
- Secrets encrypted at rest with sops, decrypted into memory-locked enclaves at runtime, never reachable as a plain `string` on the resolved struct.

## Quick start

```go
import (
    "context"
    "os"

    "github.com/trendvidia/chameleon"
    "github.com/trendvidia/chameleon/resolver"
    "github.com/trendvidia/chameleon/source"

    myappv1 "example.com/myapp/proto/myapp/v1"
)

// 1) Load + parse your ChainSpec however you prefer (file, embed.FS, etc).
spec, err := loadChainSpec("chain.pxf")

// 2) One-call simple case (no encryption, single layer dir):
res, err := chameleon.Load[*myappv1.AppConfig](
    &myappv1.AppConfig{},
    spec,
    os.DirFS("./layers"),
    "dev",
)
if err != nil { /* handle */ }
defer res.Destroy()

cfg := res.Config           // typed *AppConfig
pw  := mypb.AppConfigSecret_DbPassword(res.Secrets) // typo-safe accessor (codegen)
buf, _ := pw.Open(); defer buf.Destroy()
useDBPassword(buf.Bytes())
```

For complex setups (multiple sources, sops-encrypted layers, request context) use the full `resolver.New(...)` API:

```go
r, err := resolver.New[*myappv1.AppConfig](
    &myappv1.AppConfig{},
    spec,
    resolver.WithSource("default", source.FS(os.DirFS("./layers"))),
    resolver.WithSource("secrets", source.FS(os.DirFS("./layers/secrets"))),
    resolver.WithDecrypter(sops.Default()),
    resolver.WithTrace(),  // opt-in per-field provenance
)
res, err := r.Resolve(ctx, resolver.Request{
    Environment: "dev",
    Context:     map[string]string{"region": "eu"},
})
defer res.Destroy()
```

## The resolver pipeline

```
chain.Expand        ChainSpec + env + ctx -> ordered LayerRefs
source + sops       layer bytes (sops streams plaintext into memguard)
pxf.UnmarshalFull   typed message + per-layer presence
merge.Merge         presence-aware overlay; WKTs treated as atomic
defaults.Apply      (pxf.default) for fields no layer set
interpolate.Apply   ${field.path} expansion with cycle detection
validate.Validate   (pxf.required) check on the merged result
parse.Move          pxf.Secret values into per-field memguard Enclaves
```

Per-stage docs live in each package's godoc.

## Concepts

### Chain spec

A `chameleon.v1.ChainSpec` declares:
- **`environments`** — single-parent ancestry (`uat extends sandbox extends prod`).
- **`layers`** — ordered list of layer entries. Each entry has a path template (`base.pxf`, `env/${env}.pxf`, `region/${ctx.region}.pxf`) and a strategy: `ONCE` (single materialization) or `PER_ENV` (one per ancestor, root-first).
- **`context_keys`** — declared inputs the caller may supply. Validates against `allowed_values`, applies defaults, enforces `required`.
- **`sources`** — logical names mapping to backing implementations (filesystem, encrypted bucket, etc.) referenced by `LayerEntry.source`.

Templates with unresolved variables silently skip unless `required = true`. Nothing fancier — no expression language, no conditionals.

### Layer files

Each layer is a PXF document conforming to your schema. Typical structure: `base.pxf` → all defaults; `env/<env>.pxf` → per-environment overrides; `region/<ctx.region>.pxf` → optional regional tweaks; encrypted layers via sops.

```pxf
# base.pxf
base_url     = "mybigproject.com"
api_url      = "api.${base_url}"      # interpolated post-merge
db_password  = "PLACEHOLDER"          # overridden by encrypted layer

# env/dev.pxf
base_url     = "dev.mybigproject.com" # api_url's template (in base, untouched) re-resolves
```

For "tweak" patterns where a higher layer derives its value from a base, use a *separate* root variable rather than self-reference:

```pxf
# base.pxf
domain      = "mybigproject.com"
base_url    = "https://${domain}"
api_url     = "https://api.${domain}"

# env/dev.pxf — overriding domain re-resolves both templates above
domain      = "dev.mybigproject.com"
```

`base_url = "dev.${base_url}"` would error with `interpolate: cycle` — interpolation runs post-merge against the final state, so a self-reference resolves to its own template. Always introduce an intermediate root variable for compose-from-base patterns.

### Secrets

Fields typed `pxf.Secret` (the WKT defined in [protowire/proto/pxf/secret.proto](https://github.com/trendvidia/protowire/blob/main/proto/pxf/secret.proto)) are recognized by the codec. Author them in PXF as scalar shorthand or block form:

```pxf
db_password = "supersecret"
db_password { value = "supersecret"  hint = "Postgres primary" }
```

After `Resolve`, the plaintext lives only in `Resolution.Secrets` (a `*secret.Map` keyed by dotted field path), backed by memguard `Enclave`s (mlocked + encrypted in process). The proto field's `Value` string is cleared.

Access via the codegen-emitted accessor (typo-safe):

```go
pw := mypb.AppConfigSecret_DbPassword(res.Secrets)
buf, _ := pw.Open()
defer buf.Destroy()
useSecret(buf.Bytes())
```

Or by string path (no codegen required):

```go
pw := res.Secrets.Get("db_password")
```

`Resolution.Destroy()` wipes every Enclave. Idempotent and concurrent-safe.

## Codegen

The included `protoc-gen-chameleon-go` plugin emits a sidecar `*.chameleon.go` per `.proto` with one accessor per `pxf.Secret` field — including those reached through `repeated` and `map<*, V>` containers.

Run alongside `protoc-gen-go`:

```yaml
# buf.gen.yaml
version: v2
plugins:
  - local: protoc-gen-go
    out: gen/go
    opt: paths=source_relative
  - local: protoc-gen-chameleon-go
    out: gen/go
    opt: paths=source_relative
```

See `examples/buf/` for a working setup.

**What's emitted** — given:

```proto
message AppConfig {
  pxf.Secret db_password = 1;
  Endpoint primary = 2;
  repeated Endpoint replicas = 3;
}
message Endpoint {
  pxf.Secret auth_token = 1;
}
```

```go
// app.chameleon.go (generated)
func AppConfigSecret_DbPassword(s *secret.Map) *secret.Secret { /* */ }
func AppConfigSecret_Primary_AuthToken(s *secret.Map) *secret.Secret { /* */ }
func AppConfigSecret_Replicas_AuthToken(s *secret.Map, i int) *secret.Secret { /* */ }
```

Renaming a `.proto` field invalidates the function name; stale call sites fail to compile.

## PXF authoring gotchas

The PXF text format is whitespace-significant inside message blocks:

- **Field separators inside `{ ... }` are newlines or spaces, NOT commas.** This errors:
  ```
  endpoints = [
    { url = "u" port = 80 }       # OK (space-separated)
    { url = "u", port = 80 }      # ERROR — comma not a separator inside blocks
  ]
  ```
- **List literals separate with newlines or commas** — both fine. `tags = ["a", "b"]` and `tags = ["a"\n  "b"]` are equivalent.
- **Map keys** with valid identifiers render unquoted on encode (`acme: "x"`, not `"acme": "x"`).

Worth knowing because the PXF parser's error message ("expected identifier, string, or integer, got `,`") doesn't make the cause obvious.

## Limitations & known issues

- **`protoc-gen-chameleon-go` sidecar mode** doesn't hide the `*pxfv1.Secret.Value` field on the proto-generated struct. `cfg.DbPassword.Value` compiles and runs — but reads `""` after `Resolve`, because `parse.Move` clears it post-merge. The plaintext lives only in the memguard `Enclave`. Discipline-based protection rather than type-system. A field-replacement plugin variant would close this at compile time but requires implementing `proto.Message` for a hand-rolled type — not yet done.

- **Plaintext-in-heap window** between PXF parse and `parse.Move`. Go strings are immutable; the bytes that briefly land on `pxf.Secret.Value` cannot be wiped (only the post-Move memguard representation can). Documented as best-effort.

- **WKT atomic merge** — `pxf.Secret` / `Timestamp` / `BigInt` / etc. replace whole, never deep-merge. So a higher layer's `db_password = "new"` discards a lower layer's `hint`. Re-declare metadata explicitly if you want it preserved.

- **Sops API has no `ctx.Context` hook** — slow KMS round-trips can't be interrupted mid-decrypt. The chameleon Decrypter checks `ctx.Err()` before invoking sops but cannot cancel in-flight calls.

- **Forked `protobuf-go`** — `protowire-go` `replace`s `google.golang.org/protobuf` with `trendvidia/protobuf-go` for the `dynamicpb` fast path. Transitive consumers of chameleon inherit that. See protowire-go's README for the rationale.

## Compatibility

| Dep | Version |
|---|---|
| Go | 1.25+ |
| `github.com/trendvidia/protowire-go` | `>= v0.71.0` |
| `github.com/getsops/sops/v3` | `>= v3.13.0` |
| `github.com/awnumar/memguard` | `>= v0.22.5` |

## Test status

13 packages, ~140 tests, all green under `go test ./... -race`.

## Repo layout

```
chain/                  ChainSpec expansion (env ancestry + layer template materialization)
cmd/protoc-gen-chameleon-go/   protoc plugin emitting typed Secret accessors
defaults/               (pxf.default) post-merge pass
examples/buf/           working buf.gen.yaml + sample schema
examples/simple/        chain.pxf + base/dev layers (no Go code)
interpolate/            ${field.path} post-merge expansion
internal/upstream/      cross-repo contract with protowire-go (not built)
merge/                  presence-aware proto-message overlay
parse/                  Move(): pxf.Secret values -> memguard Enclaves
proto/chameleon/v1/     ChainSpec / LayerHeader proto definitions
resolver/               public Resolver[T] + Resolution[T]
secret/                 Secret + thread-safe Map runtime
sops/                   sops/v3 wrapper with memguard streaming
source/                 Source interface + fs.FS impl
validate/               (pxf.required) post-merge pass
wkt/                    well-known type predicate (atomic-merge boundary)
```

## See also

- [protowire spec](https://github.com/trendvidia/protowire) — PXF format, well-known types
- [protowire-go](https://github.com/trendvidia/protowire-go) — Go codec
- [sops](https://github.com/getsops/sops) — secret encryption at rest
- [memguard](https://github.com/awnumar/memguard) — in-memory secret protection

## License

MIT
