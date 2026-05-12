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

### Loading the chain spec

The CLI tool below uses `chain.LoadSpec(path)` — the same helper is available to library consumers if you don't want to compile the ChainSpec descriptor yourself:

```go
spec, err := chain.LoadSpec("config/chain.pxf")
```

`chain.ParseSpec(data)` does the same against in-memory bytes (handy for `embed.FS`).

### Complex setups

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
parse.MoveInto      pxf.Secret values into memguard Enclaves
                    (scalar-shorthand: routed during decode via
                    pxf.OnSecretField; block-form: post-decode walk)
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

#### Optional `@header` preamble

A layer file may declare an `@header chameleon.v1.LayerHeader { ... }` block before the schema-typed body. Fields are checked against the chain entry that loaded the file; mismatch is a hard error at resolve time. Every field is optional — author-omitted fields are not checked.

```pxf
@header chameleon.v1.LayerHeader
{
  id          = "env"      # must equal LayerEntry.id from the chain spec
  environment = "prod"     # must equal the env this materialization is bound to
  encrypted   = true       # must equal LayerEntry.encrypted
  notes       = "rotated 2026-04"  # informational, ignored at resolve time
}

# the rest is the consumer's schema
base_url = "mybigproject.com"
```

What it catches: a `prod.pxf` file accidentally moved to `env/dev.pxf` (env mismatch), a plaintext file dropped into an encrypted slot (encrypted mismatch), the wrong layer file at the wrong path (id mismatch). What it doesn't carry: any resolution semantics. Layer content is still just the schema-typed body; the header is sanity-check metadata only. Files without `@header` parse exactly as before.

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

## Observability

### Metrics (OpenTelemetry)

Pass `resolver.WithMeterProvider(mp)` to record OTel metrics. Two families:

**Resolve-time** (one observation per `Resolve` call):

| Instrument | Type | Attributes |
|---|---|---|
| `chameleon.resolve.duration` | Histogram (seconds) | `chameleon.env` |
| `chameleon.resolve.errors` | Counter | `chameleon.env` |
| `chameleon.resolve.layers` | Histogram | `chameleon.env` |
| `chameleon.resolve.secrets` | Histogram | `chameleon.env` |
| `chameleon.merge.clobbered_metadata` | Counter | `chameleon.env` |

**Runtime** (one observation per `Secret.Open()` / `Destroy()` pair):

| Instrument | Type | Attributes |
|---|---|---|
| `chameleon.secret.opens` | Counter | `chameleon.path`, `chameleon.env` |
| `chameleon.secret.active` | UpDownCounter (gauge) | `chameleon.path` |
| `chameleon.secret.open.duration` | Histogram (seconds) | `chameleon.path` |

```go
r, _ := resolver.New[*AppConfig](sample, spec,
    resolver.WithSource("default", source.FS(layersFS)),
    resolver.WithMeterProvider(otel.GetMeterProvider()),
)
```

Without `WithMeterProvider`, instruments use the noop provider — every record is a cheap no-op.

### Trace (per-field provenance)

`resolver.WithTrace()` populates `Resolution.Trace` with one entry per dotted field path: which layer set it, whether a default applied, whether interpolation rewrote it, whether it's sensitive. Used by the `chameleon explain` CLI (below) and powerful for debugging "why does this field have this value?"

`Resolution.Trace.ClobberedMetadata` also lists any WKT-metadata losses detected during merge — e.g., a higher layer's `db_password = "y"` silently drops a lower layer's `hint`. See [WKT metadata clobber](#wkt-metadata-clobber-warning) below.

### Debugging: pairing resolve-time provenance with runtime state

Two production-debugging questions, two distinct answers:

**"Where did this field's value come from?"** — resolve-time, answered by `chameleon explain` (below) and `Resolution.Trace`. Tells you which layer set the value, whether a default applied, whether interpolation rewrote it.

**"Who's holding this secret right now?"** — runtime, answered by the `chameleon.secret.*` metrics. Tells you whether any goroutine is currently inside an `Open()` → `Destroy()` window, how often the secret is opened, how long it's typically held.

Worked example: suspecting a memguard buffer leak for `db.password`.

```promql
# Is anything holding it open right now?
chameleon_secret_active{chameleon_path="db.password"} > 0

# For how long has the gauge been non-zero?
max_over_time(chameleon_secret_active{chameleon_path="db.password"}[5m]) > 0

# Sustained opens that never returned?
rate(chameleon_secret_opens{chameleon_path="db.password"}[5m])
  - rate(chameleon_secret_open_duration_count{chameleon_path="db.password"}[5m]) > 0
```

The last query catches leaked enclaves: opens-per-second minus close-durations-per-second should average zero in steady state. A sustained positive delta means a code path opened the secret but never called `Destroy()` on the returned `OpenedBuffer`. Pair with `pprof` goroutine dumps to find the offending caller.

### WKT metadata clobber warning

WKTs (`pxf.Secret`, `Timestamp`, `BigInt`, wrapper types) are atomic — a higher layer's whole-value write replaces the lower layer's value entirely, including any metadata. For `pxf.Secret` specifically, that means an `env/dev.pxf` writing `db_password = "y"` silently drops the `hint = "Postgres primary"` that `base.pxf` had set.

Chameleon detects this and surfaces it three ways:

- **`Resolution.Trace.ClobberedMetadata`** (when `WithTrace()`): a slice of `ClobberRecord{Path, Type, LostFields, ClobberingLayerID}` per loss.
- **`chameleon.merge.clobbered_metadata`** OTel counter (when `WithMeterProvider()`): increments once per loss, attributed by `chameleon.env`.
- **`resolver.WithStrictMetadata()`**: opt-in option that promotes the warning to a hard `Resolve` error. Use when your authoring discipline says "always re-declare metadata if you override a Secret" and you want accidental drops to fail loud.

Currently only `pxf.Secret` has a metadata concept (`hint`, `fingerprint`). Other atomic WKTs replace whole without checks.

### `chameleon` CLI

Companion CLI installable via `go install github.com/trendvidia/chameleon/cmd/chameleon@latest`. Subcommands:

- `chameleon explain` — resolve a chain spec and print per-field provenance (which layer set the value, whether a default was applied, whether interpolation rewrote it, whether the field is sensitive).
- `chameleon verify` — check `@header.content_sha256` on every layer the chain resolves to. Encrypted layers are skipped (sops already authenticates them).
- `chameleon migrate` — bulk-edit layer files in a chain spec. Decrypts encrypted layers, applies an AST-level transform (add-field / delete-field / rename-field), re-encrypts preserving the original key set.
- `chameleon hash` — print the canonical body SHA-256 of a single layer file. Use this value as the `@header.content_sha256` on the layer.
- `chameleon version` — print the installed chameleon version, Go toolchain, and commit (from VCS-aware module info).
- `chameleon completion <bash|zsh|fish>` — print a shell completion script. Source the output in your shell init file for subcommand + flag completion (e.g. `chameleon completion fish > ~/.config/fish/completions/chameleon.fish`).

Example: `explain` against a checked-in chain spec:

```sh
chameleon explain \
    --schema=app.proto \
    --message=myapp.v1.AppConfig \
    --chain=chain.pxf \
    --layers=./layers \
    --env=dev
```

Output:

```
api_url        = "api.dev.mybigproject.com"  [base | interpolated]
base_url       = "dev.mybigproject.com"      [env]
db_password    = <sensitive>                  [secrets]
region         = "us"                         [default]
```

Each field shows its final value (or `<sensitive>` for `pxf.Secret` fields) and tags identifying the layer that set it, whether the value came from a `(pxf.default)`, and whether interpolation rewrote it.

`--field=<dotted.path>` prints provenance for one field. `--ctx=region=eu,tenant=acme` supplies context for chain expansion.

### `chameleon verify` CLI

Walks the chain spec and checks each layer's `@header.content_sha256` against the file's actual body hash. Non-zero exit on mismatch:

```sh
chameleon verify --chain=chain.pxf --layers=./layers --env=dev
# OK    base    base.pxf
# OK    env     env/dev.pxf
# SKIP  secrets dev.sops.pxf  (encrypted; sops authenticates)
```

Pass `--strict` to fail if any layer lacks a `content_sha256` (default behavior is to skip).

The canonical bytes hashed are the schema-typed body — everything after the closing `}` of the `@header` preamble. This means adding, editing, or removing fields inside `@header` (including `content_sha256` itself) does not invalidate the hash. Use `chameleon hash <file>` to compute the value an author should paste into `@header.content_sha256`:

```sh
chameleon hash base.pxf
# f38075764f27b34bdcc46845d5ad8c873a1b852667526651ac7b4b40c130ffdf
```

Encrypted layers are skipped — sops already authenticates the ciphertext.

## Custom decryption (programmatic key injection)

`sops.Default()` sources keys from the ambient environment (`SOPS_AGE_KEY`, etc.). For programmatic key injection — say, fetching keys from a secret manager at runtime — implement the `sops.Decrypter` interface yourself:

```go
type myDecrypter struct{ ageKey string }

func (d *myDecrypter) Decrypt(ctx context.Context, ciphertext []byte, format sops.Format) (*memguard.LockedBuffer, error) {
    // Set SOPS_AGE_KEY for this call's duration, or pre-decrypt with
    // the age library + wrap in a memguard buffer manually.
    // Then call back into sops's decrypt.Data, OR roll your own.
    ...
}

r, _ := resolver.New[*AppConfig](sample, spec,
    resolver.WithSource("default", source.FS(layersFS)),
    resolver.WithDecrypter(&myDecrypter{ageKey: fetchFromVault()}),
)
```

The `Decrypter` interface is one method (`Decrypt`) — straightforward to wrap any encryption backend.

## Codegen

The included `protoc-gen-chameleon-go` plugin emits a sidecar `*.chameleon.go` per `.proto` with one accessor per `pxf.Secret` field — including those reached through `repeated` and `map<*, V>` containers.

### Installing the plugin

```sh
go install github.com/trendvidia/chameleon/cmd/protoc-gen-chameleon-go@latest
```

`github.com/trendvidia/*` is currently a private module set, so `go install` only succeeds if `go` is configured to fetch it privately. Either:

- Set `GOPRIVATE=github.com/trendvidia/*` and add a git rewrite that uses your credentials, e.g.
  ```sh
  go env -w GOPRIVATE=github.com/trendvidia/*
  git config --global url."git@github.com:trendvidia/".insteadOf "https://github.com/trendvidia/"
  ```
- Or export a `GH_TOKEN` / `GITHUB_TOKEN` with `repo` scope before `go install`.

Without one of those, `go install` fails with `403 Forbidden` against the proxy or a `terminal prompts disabled` git error. Same setup applies to consumers that `import` chameleon directly.

### Wiring into buf

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

- **Plaintext-in-heap window — proto-field path closed; transient lexer + hook string remains.** For every `pxf.Secret` value (scalar shorthand AND block form), the chameleon resolver wires protowire-go's `UnmarshalOptions.OnSecretField` hook (scalar shorthand from v0.76.0; block form from v0.77.0) so the value **never lands on `*pxfv1.Secret.Value` on the proto message**. From decode through `parse.MoveInto`, the proto-field path is sealed. What remains is a brief transient on the regular Go heap: the PXF lexer materializes the token's value as an immutable Go string (built via `strings.Builder` in `protowire-go/encoding/pxf/lexer.go`), passes it to the hook closure, and `secret.FromString` copies it into a memguard `Enclave` via a `[]byte` conversion. `NewBufferFromBytes` wipes the `[]byte`, but the original Go string is immutable and remains live on the heap until the lexer's next `advance()` overwrites the token state AND the GC reclaims the backing array. The window is **bounded by one lexer step + one GC pacing**, not the duration of the resolve. Hint and fingerprint metadata still flow through the proto message and are folded onto the Map's Secret entry by `parse.MoveInto`'s walker; both are diagnostic, not sensitive. The walker remains as a defensive sweep that catches orphans (paths a higher layer's `null` cleared) and enriches hook-routed entries with the block-form metadata. **Closing the last transient window** requires either a byte-slice hook variant (caller-controlled scratch buffer the lexer can wipe) or a lexer-level Secret-aware path that skips the string materialization entirely — both are deeper protowire-go work; tracked for a follow-up release.

- **WKT atomic merge** — `pxf.Secret` / `Timestamp` / `BigInt` / etc. replace whole, never deep-merge. A higher layer's `db_password = "new"` discards a lower layer's `hint`; re-declare metadata explicitly if you want it preserved. The atomic semantic also applies to "empty" Secret blocks: `pw { hint = "h" }` or `pw { }` (no `value`) **drops** any lower-layer secret value, matching "replaces the Secret as a unit." To preserve the value while updating only the hint, the higher layer must re-supply value (`pw { value = "..." hint = "h" }`). To intentionally clear, `pw = null` is the more explicit primitive.

- **Sops `context.Context` threading — closed via the `trendvidia/sops/v3` fork.** Chameleon's `Decrypter.Decrypt` threads `ctx` through `decrypt.DataIntoWriterContext`; in-flight KMS round-trips abort on `ctx` deadline / cancellation natively for AWS / GCP / Azure / Vault. HuaweiCloud KMS is approximated via a goroutine shim (see fork [#10](https://github.com/trendvidia/sops/issues/10)). The `chameleon migrate` CLI uses the corresponding `encrypt.UsingSameKeysAsWithContext` so re-encrypts honor ctx the same way. Upstream [getsops/sops#2179](https://github.com/getsops/sops/issues/2179) tracks the merge candidate; until then, chameleon consumes via the `replace` directive — see [Compatibility → Replace directives](#replace-directives--required-in-your-gomod).

- **Forked dependencies require explicit `replace` directives in consumer `go.mod`.** Chameleon depends on two upstream-incompatible forks (`trendvidia/sops/v3`, `trendvidia/protobuf-go`); Go's `replace` directive is non-transitive so the chameleon repo's `replace` does NOT propagate to consumers. See **Compatibility → Replace directives** above for the exact lines to copy into your own `go.mod`.

## Compatibility

| Dep | Version |
|---|---|
| Go | 1.26+ |
| `github.com/trendvidia/protowire-go` | `>= v0.77.0` (block-form `OnSecretField` routing; v0.76.0+ covers scalar shorthand) |
| `github.com/getsops/sops/v3` | `>= v3.13.0` upstream surface, **consumed via `github.com/trendvidia/sops/v3 v3.13.2` fork** (see below) |
| `github.com/awnumar/memguard` | `>= v0.22.5` |

### Replace directives — required in your `go.mod`

Go module `replace` directives **only apply to the main module's build** — they do **NOT** propagate from a dependency's `go.mod` to your binary's resolution. Chameleon depends on two forks whose `replace` directives you must copy into your own `go.mod` for the build to succeed against the function set chameleon calls:

```go
// In your project's go.mod:
replace (
    github.com/getsops/sops/v3       => github.com/trendvidia/sops/v3 v3.13.2
    google.golang.org/protobuf       => github.com/trendvidia/protobuf-go v1.36.12
)
```

If you omit these, `go build` fails — chameleon calls into functions that don't exist on the upstream packages.

| Replace | Required by | Why the fork | Tracking upstream |
|---|---|---|---|
| **`github.com/trendvidia/sops/v3 v3.13.2`** | chameleon's `sops` wrapper, `chameleon migrate` | `decrypt.DataIntoWriterContext`, the `encrypt` package, end-to-end `context.Context` across AWS / GCP / Azure / Vault. PXF store + comment directives. | upstream [getsops/sops#2179](https://github.com/getsops/sops/issues/2179) for the ctx-threading work; rest are smaller standalone PRs. |
| **`github.com/trendvidia/protobuf-go v1.36.12`** | protowire-go's `dynamicpb` fast path | unexported `SetUnsafe` / `AppendUnsafe` / map `SetUnsafe` on `*dynamicpb.Message`. Saves the per-call typecheck box-alloc on the PXF unmarshal hot path. | upstream protobuf-go has not yet accepted; the fork tracks the upstream `v1.36` line. |

Both forks keep their upstream module paths, so any code that imports the upstream namespace compiles against the fork unchanged. The fork repos publish CHANGELOGs against the upstream tag they ride on.

When upstream eventually merges the relevant parts, drop the matching `replace` from your `go.mod` and bump the chameleon minimum to the merged upstream tag.

## Test status

15 packages, ~150 tests, all green under `go test ./... -race`. Plus fuzz harnesses for the two parsers chameleon owns:

```sh
go test ./interpolate/ -fuzz=FuzzApply -fuzztime=30s
go test ./chain/ -fuzz=FuzzExpand -fuzztime=30s
```

Both seed with deliberately ugly inputs (malformed templates, invalid UTF-8, deep paths). Property: `Apply` / `Expand` must never panic; errors are fine.

A goroutine-leak guard test runs Load+Destroy in a tight 500-iteration loop and verifies the goroutine count doesn't grow.

## Repo layout

```
chain/                  ChainSpec expansion + LoadSpec/ParseSpec runtime helpers
cmd/chameleon/          CLI: explain (per-field provenance) / verify (@header.content_sha256) / migrate (bulk layer edits) / hash
cmd/chameleon-lint/     go/analysis-style linter flagging *pxfv1.Secret.Value reads
cmd/protoc-gen-chameleon-go/   protoc plugin emitting typed Secret accessors
defaults/               (pxf.default) post-merge pass
examples/buf/           working buf.gen.yaml + sample schema
examples/simple/        chain.pxf + base/dev layers (no Go code)
interpolate/            ${field.path} post-merge expansion
internal/header/        @header preamble scanner + LayerHeader sanity checks
internal/pathfmt/       single source of truth for dotted-path format
internal/upstream/      cross-repo contract with protowire-go (not built)
merge/                  presence-aware proto-message overlay
parse/                  Move(): pxf.Secret values -> memguard Enclaves
proto/chameleon/v1/     ChainSpec / LayerHeader proto definitions
resolver/               public Resolver[T] + Resolution[T] + OTel metrics
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
