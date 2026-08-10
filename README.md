# chameleon

Schema-driven layered configuration for Go. Define your config shape in proto, declare an environment-ancestry chain spec describing how layers fall through, get back a strongly-typed Go struct plus memguard-backed secrets at runtime.

Built on [protowire-go](https://github.com/trendvidia/protowire-go) (PXF text format with three-state field presence) and [trendvidia/sops/v4](https://github.com/trendvidia/sops) (encrypted layer files; soft-fork of [getsops/sops](https://github.com/getsops/sops) carrying chameleon-required additions).

## Why

You have a config schema that's mostly the same across `prod` / `sandbox` / `uat` / `dev`, with a few per-environment tweaks (URLs, credentials, feature flags). You want:

- One source of truth for the schema (`.proto`).
- Layers per environment that fall through to a base, with explicit nulling, schema defaults (scalars and well-known types like `Duration`), and `${field.path}` interpolation.
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
import _ "github.com/trendvidia/chameleon/sops"  // only if some layer is encrypted

r, err := resolver.New[*myappv1.AppConfig](
    &myappv1.AppConfig{},
    spec,
    resolver.WithSource("default", source.FS(os.DirFS("./layers"))),
    resolver.WithSource("secrets", source.FS(os.DirFS("./layers/secrets"))),
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

### Templates and interpolation

chameleon recognizes `${...}` in **two distinct stages**. They share the syntax for ops-readable consistency but run on different code paths and can't cross-reference each other.

#### Chain-time path templates

Substituted by `chain.Expand` when materializing the chain spec into a concrete layer set — *before* any layer is loaded.

```pxf
layers = [
  { id = "base", path = "base.pxf" },                                # no template
  { id = "env",  path = "env/${env}.pxf", for_each = PER_ENV },      # → env/prod.pxf
  { id = "reg",  path = "regions/${ctx.region}/db.pxf",              # → regions/eu/db.pxf
                 required = true },
]
```

- **`${env}`** resolves to the leaf environment name being resolved (`Request.Environment`).
- **`${ctx.<key>}`** resolves to caller-supplied context values; the key must be declared in `chain.context_keys`.
- An unset `${ctx.*}` reference **silently skips the layer** unless `required = true`. Set `required` on layers whose absence would be a bug — it converts the silent skip into a hard error.
- The bundler (`chameleon bundle build`) **rejects** chain specs containing `${ctx.*}` outright: bundling is build-time and has no runtime context to substitute, so silently-skipped layers would produce a bundle that diverges from runtime resolution.

#### Post-merge field interpolation

Run by `interpolate.Apply` after all layers have merged. Operates on the typed message, not on raw PXF text.

```pxf
# base.pxf
domain     = "mybigproject.com"
base_url   = "https://${domain}"
api_url    = "https://api.${domain}"

# env/dev.pxf — overriding `domain` re-resolves both templates above
domain     = "dev.mybigproject.com"
```

- **Scope.** Top-level string scalars, recursively into nested user-defined messages, repeated string elements, and `map<*, string>` values. `bytes`, numerics, and bools are not interpolated.
- **References.** Dotted access into the merged message (`${endpoints.primary.url}`). No bracket access, no map-key lookups, no array indices inside `${...}`.
- **Transitive resolution.** `a → b → c` resolves on demand; the walker doesn't pre-compute. A `MaxResolveDepth` cap (defense added in v0.10.0) prevents pathological chains from blowing the stack.
- **Cycles.** Detected and surfaced as `interpolate.ErrCycle`. `base_url = "dev.${base_url}"` is a self-reference that errors — interpolation runs against the *final merged state*, so `base_url`'s template resolves to itself. Use an intermediate root variable for compose-from-base patterns (see the `domain` example above).
- **Unknown references** error with `interpolate.ErrUnknownReference`.
- **WKT exclusion.** `pxf.Secret`, `google.protobuf.Timestamp`, `google.protobuf.Duration`, and other well-known type subtrees are deliberately not traversed. Their inner fields don't participate in interpolation, and they cannot be referenced from inside a `${...}` lookup. This keeps secret plaintext out of the template namespace and decouples interpolation from value-type internals.

#### The two stages don't cross-reference

You cannot use `${env}` inside a config string value — `interpolate.Apply` would look up `env` as a field path in the merged message and error with `ErrUnknownReference`. You cannot use `${field.path}` inside a chain spec layer path — `chain.Expand` only recognizes `${env}` and `${ctx.*}` and would treat anything else as a literal path component.

If you need the env name surfaced inside the resolved config, declare an explicit field for it in your schema and have each env's layer set it; then `${env_name}`-style references work normally through the post-merge interpolator.

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

`Resolution.Trace.Decrypter` names the backend the resolver picked out of the crypt registry for this call, and is set only when it picked one — empty if no encrypted layer was reached, empty if you named the `Decrypter` yourself. That is the "who decrypted my layers" answer for the auto-selection path, which is the one nobody wrote down: see [Runtime decryption](#runtime-decryption).

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

- `chameleon explain` — resolve a chain spec and print per-field provenance (which layer set the value, whether a default was applied, whether interpolation rewrote it, whether the field is sensitive). Accepts on-disk `--chain` + `--layers` or a `--bundle` input (file / stdin / env var, optionally `--base64`).
- `chameleon get` — resolve a chain spec and print the result as a PXF document. Same input flags as `explain`; secret fields render as `<redacted>`. Pipe into `pxf query` to extract individual values.
- `chameleon reveal --field=<path>` — print the plaintext of a single Secret-typed field to stdout (no trailing newline; suitable for `$(...)` substitution). Same input flags as `explain`.
- `chameleon bundle build` — produce a per-module distribution bundle (chain spec + narrowed, re-encrypted layers as a zip). `--base64` for paste-into-secret-manager-field output, `--force` to truncate-overwrite an existing `--out` file. See "[Distribution bundles](#distribution-bundles)".
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

## Working with secrets

The reference material covers what a `pxf.Secret` field looks like ([### Secrets](#secrets)) and the memguard accessor API. This section is the end-to-end **operator flow** — from "I need to add a new secret" through to "it's in production, accessed safely, and rotates cleanly."

### Recommended layer layout

Separate plaintext config from encrypted secrets at the file level. `LayerEntry.encrypted = true` selects per-entry decryption, so a typical repo carries:

```
config/
  chain.pxf            # plaintext
  base.pxf             # plaintext defaults — NO secrets here
  env/
    prod.pxf           # plaintext per-env config
    sandbox.pxf
    dev.pxf
  secrets/
    prod.pxf           # sops-encrypted per-env secrets
    sandbox.pxf
    dev.pxf
```

Chain spec wires the secrets layer as `encrypted = true`:

```pxf
layers = [
  { id = "base",    path = "base.pxf",           for_each = ONCE },
  { id = "env",     path = "env/${env}.pxf",     for_each = PER_ENV },
  { id = "secrets", path = "secrets/${env}.pxf", for_each = PER_ENV, encrypted = true },
]
```

**Why separate.** Mixing encrypted and plaintext fields in the same file is supported but discourages the explicit "this is a secret, route through KMS" choice. Gating decryption behind a single dedicated layer keeps the decrypt cost predictable and makes "should this go in `base.pxf` or `secrets/prod.pxf`?" a question operators ask out loud.

**Never put per-env secrets in `base.pxf`.** `base.pxf` loads for every environment including `dev` — a dev runtime with decrypt creds for `base.pxf` would see prod secrets. Per-env secrets belong in per-env layers, full stop.

### Encrypting layers

Use the `sops` CLI directly:

```sh
# First time — encrypt with one or more recipients in place.
sops -e -i --age "age1examplerecipient..." secrets/prod.pxf

# Editing an already-encrypted file — sops decrypts to $EDITOR + re-encrypts on save.
sops secrets/prod.pxf
```

For team setups, list multiple recipients (one per operator's age key or KMS principal) so anyone authorized can edit. A `.sops.yaml` in the repo root centralises the recipient list per path pattern; check it in alongside the encrypted files so `sops <file>` finds the right keys without flags.

PXF's sops format is recognized by `.pxf` extension on sops versions shipping protowire support; older sops may need `--input-type protowire --output-type protowire`.

#### Naming the encryption key per environment

For deployments that use **different** age keys per environment (so a `dev` runtime physically can't decrypt `prod`'s layers, even if it gets hold of the ciphertext), declare the key by name in the chain spec:

```pxf
environments = [
  { name = "prod"    key_name = "prod-key" }
  { name = "sandbox" extends  = "prod" key_name = "sandbox-key" }
  { name = "dev"     extends  = "sandbox" }    # inherits "sandbox-key"
]
```

Names are looked up against the configured age key source (`SOPS_AGE_KEY_FILE`, the env-var twin `SOPS_AGE_KEY` / `SOPS_AGE_KEY_CMD`, or the standardized default `$HOME/.config/sops/age/keys.pxf` — see [Runtime decryption](#runtime-decryption) for the full source list). A PXF multi-key file holds one labelled age identity per env:

```pxf
keys = {
  "prod-key":    "AGE-SECRET-KEY-1..."
  "sandbox-key": "AGE-SECRET-KEY-1..."
}
```

Use `chameleon keystore add` to populate it without hand-editing — generates a fresh X25519 identity (or adopts one you already have via `--identity=AGE-SECRET-KEY-...`) and writes the entry atomically:

```sh
chameleon keystore add --name=prod-key             # generates a new key
chameleon keystore add --name=sandbox-key --default  # also mark as the file default
```

The recipient is printed to stdout (pipeable); the identity stays inside the keystore file.

A child env with empty `key_name` inherits the nearest non-empty ancestor's value, mirroring how `extends` already composes layer state — so `dev` above encrypts to `sandbox-key`.

Three consumers honor `key_name` automatically:

- **`chameleon migrate`** uses the env's `key_name` when re-encrypting a layer after a schema transform.
- **`chameleon bundle build`** unions the `key_name`-derived recipient into each layer's bundle recipient set, alongside any `BundleManifest.recipients_by_env` entry. A typo'd or missing `key_name` fails at bundle-build time, not at runtime decrypt.
- **`chameleon rotate-key`** re-encrypts every encrypted layer to the chain spec's current key set. See [Rotation](#rotation) for the workflow.

Envs without a `key_name` fall back to whatever recipient set the encrypted file already carries (for `migrate`) or the `BundleManifest` recipient list (for `bundle build`) — unmigrated chains keep working unchanged.

### Runtime decryption

Decryption is opt-in **at link time**. The resolver depends on the `crypt.Decrypter` interface, never on an implementation, so a program whose layers are all plaintext links no key-management code at all. A program with encrypted layers blank-imports a backend, which registers itself — the same model `database/sql` uses for drivers:

```go
import (
    "github.com/trendvidia/chameleon/resolver"
    _ "github.com/trendvidia/chameleon/sops"   // registers the "sops" backend
)

r, err := resolver.New[*AppConfig](sample, spec,
    resolver.WithSource("default", source.FS(os.DirFS("./config"))),
)
```

With exactly one backend linked in, the resolver picks it up the first time an encrypted layer appears. Nothing else is needed — and if you forget the import, the error says so:

```
resolver: encrypted layer without Decrypter: layer="secrets": crypt: no decryption
backend registered; link one with `import _ "github.com/trendvidia/chameleon/sops"`,
or pass resolver.WithDecrypter
```

Three escape hatches. `resolver.WithDecrypter(sops.Default())` passes an implementation explicitly — use it for a `Decrypter` you constructed yourself (see [Custom decryption](#custom-decryption-programmatic-key-injection)), or to pin one in a build that links several. `resolver.WithDecrypterNamed("sops")` picks a registered backend by name, which is what a build with more than one needs: auto-selection is only defined for the single-backend case, and several linked in without a choice is an error, not a coin flip. `resolver.WithoutDecrypter()` says no: an encrypted layer returns `ErrNoDecrypter` even when the build has a backend registered. The three are mutually exclusive.

> **When you need `WithoutDecrypter`.** Registration follows the *module graph*, not your configuration — the same trade `database/sql` makes. A dependency that imports `chameleon/sops` for its own reasons registers the backend in your build too, so "I passed no decrypter" stopped being a reliable way to mean "do not decrypt". `crypt.Registered()` reports what a build ended up with. This is why `chameleon get/explain/reveal --decrypter=none` is wired to `WithoutDecrypter` rather than to a nil `Decrypter` — and why `resolver.WithDecrypter(nil)` is an error rather than a silent fall-through to auto-selection. Note that [`chameleon.Load`](#quick-start) takes no options and so cannot opt out: a program that needs encrypted layers refused must use `resolver.New` directly.

> **Why link-time.** Until v0.22.1 the resolver imported `chameleon/sops` directly, so every consumer linked the whole sops key-management tree — AWS, Azure, GCP, Vault, Huawei KMS and their transitive weight — whether or not a single layer was encrypted. A program resolving two plaintext layers measured **68 MB**; the same program on v0.23.0 measures **11.7 MB**, of which 7.0 MB is the protobuf runtime any protobuf program pays ([#130](https://github.com/trendvidia/chameleon/issues/130)).

`sops.Default()` — and the registered backend, which is the same wiring — reads keys from the ambient environment, exactly the same lookup the `sops` CLI does:

| Backend | Credential source |
|---|---|
| age | `SOPS_AGE_KEY_FILE`, `SOPS_AGE_KEY` (inline), `SOPS_AGE_KEY_CMD`, `$HOME/.config/sops/age/keys.pxf` (standardized default, PXF only). Each has a CLI twin (`--age-key-file` etc.) that takes precedence. SSH side channel for decrypt: `SOPS_AGE_SSH_PRIVATE_KEY_FILE` / `_CMD`. |
| AWS KMS | Standard AWS SDK chain (env vars, IAM role, EC2 / EKS metadata) |
| GCP KMS | `GOOGLE_APPLICATION_CREDENTIALS` or workload identity |
| Azure Key Vault | `AZURE_CLIENT_ID` / managed identity |
| PGP | gpg-agent |

**Recommended posture.** Cloud KMS in production — the runtime gets decrypt capability via its IAM role, no plaintext keys deployed. age for development and CI test fixtures — flat-file private keys, no cloud round-trip cost. If you need programmatic key injection (fetching keys per-tenant, swapping providers per-deployment), implement `sops.Decrypter` yourself; see [Custom decryption](#custom-decryption-programmatic-key-injection).

### Accessing secrets in code: three rules

1. **Always use the codegen accessor** (`<Root>Secret_<Path>`), not `cfg.Field.Value`. `parse.Move` clears `.Value` to `""` after resolve — reading it returns an empty string, never the actual secret. The `chameleon lint` analyzer catches `.Value` reads at CI time.
2. **Always `defer buf.Destroy()`** at the same scope as `Open()`. Forgetting it leaks the mlocked page until process exit; the `chameleon.secret.active` OTel gauge surfaces leaked enclaves under monitoring.
3. **Never round-trip a secret through `string`, `log`, or template rendering.** `buf.Bytes()` lives in mlocked memory; passing it to `fmt.Printf`, `log.Print`, or any `%s` formatter copies plaintext to the regular heap where the GC may not zero it for arbitrarily long. Pass `buf.Bytes()` straight to the consumer (DB driver, HTTP client, file write) without intermediate string conversion.

```go
pw := mypb.AppConfigSecret_DbPassword(res.Secrets)
buf, err := pw.Open()
if err != nil { return err }
defer buf.Destroy()

// Right: pass bytes directly to the driver.
db, err := sql.Open("postgres", buildConnStr(buf.Bytes()))

// Wrong — never do this.
log.Printf("connecting with password %s", string(buf.Bytes()))
```

### Rotation

**Data rotation** (changing a secret's value) is just a new encrypted layer push:

```sh
sops secrets/prod.pxf   # edit in $EDITOR, save, sops re-encrypts
git commit -am "rotate db password"
git push                # next resolve picks up the new value
```

**Key rotation** (replacing the age key that encrypts a layer) has two shapes:

- **Per-file**, using sops directly — useful for ad-hoc rotation of a single file:

  ```sh
  sops rotate --in-place --add-age "age1new..." --rm-age "age1old..." secrets/prod.pxf
  ```

- **Chain-wide**, using `chameleon rotate-key` — the right tool when the chain spec declares per-env named keys (see [Naming the encryption key per environment](#naming-the-encryption-key-per-environment)). It walks every encrypted layer, resolves the recipient set from `Environment.key_name`, and re-encrypts each file in place:

  ```sh
  # 1. Add the new key to the keystore.
  chameleon keystore add --name=prod-key-2026
  # → prints the new recipient; identity stays inside SOPS_AGE_KEY_FILE.

  # 2. Edit chain.pxf: flip Environment.key_name from "prod-key-2025" to
  #    "prod-key-2026". Open a PR, get it reviewed, merge.

  # 3. Re-encrypt every encrypted layer to the new recipient set.
  chameleon rotate-key --chain=config/chain.pxf --layers=./config

  # 4. Once everything is verified, remove the old entry from
  #    SOPS_AGE_KEY_FILE (manual edit; `keystore remove` is planned).
  ```

  Step 3 is idempotent and supports `--dry-run` so you can see exactly which files would change before committing. The transition window between steps 1 and 3 is operator-paced — both keys are in the keystore, so old files stay readable while new writes (from `migrate`, `bundle build`, or `rotate-key` itself) pick up the new key.

  **Refusal rule.** `rotate-key` refuses to re-encrypt a layer that's reachable from any env without `key_name` set in the chain spec — re-encrypting to the union of just-the-envs-with-key_name would silently lock out the others. Set `key_name` on every env that reaches the layer, or accept that the layer can't be rotated yet.

For bulk schema changes across every layer (adding / removing / renaming a Secret field everywhere), use `chameleon migrate` — it decrypts every layer in a chain spec, applies the transform at the PXF AST level, and re-encrypts under the env's current `key_name`. Avoids the "find every encrypted file and edit by hand" footgun on multi-env setups.

### Inspecting at resolve time

`chameleon explain` redacts secrets in its output, showing only provenance:

```
db_password    = <sensitive>                          [secrets | encrypted | sops]
```

`chameleon reveal --field=db_password` prints the plaintext of one named secret to stdout (no trailing newline) — suitable for `$(...)` substitution in scripts that need a real secret value:

```sh
PGPASSWORD=$(chameleon reveal --field=db_password --schema=... --chain=... --env=prod --decrypter=sops) \
  psql -h ...
```

`reveal` is the deliberate, explicitly-named complement to the always-redacted `get` / `explain` output. Don't use it in long-running processes — the library API gives you mlock + Destroy lifecycle that a stdout pipe can't.

Both `explain` and `reveal` (and `get`) also accept bundle input — useful when debugging what a deployed runtime actually sees, since the bundle ships through the same path the runtime resolved:

```sh
# Bundle on disk.
chameleon reveal --field=db_password --bundle=billing-worker.bundle \
                 --schema=... --decrypter=sops --env=prod

# Pipe from a secret-manager CLI (base64-wrapped).
gcloud secrets versions access latest --secret=billing-worker-bundle \
  | chameleon reveal --field=db_password --bundle=- --base64 \
                     --schema=... --decrypter=sops --env=prod

# Read straight from the env var a container already has.
chameleon reveal --field=db_password --bundle-env=CONFIG_BUNDLE --base64 \
                 --schema=... --decrypter=sops --env=prod
```

See "[Debugging a bundle from the CLI](#debugging-a-bundle-from-the-cli)" for the full surface and the security note on `--bundle-env=VAR` vs `--bundle="$VAR"`.

### Distribution

For deployments where the runtime can't easily reach the layer directory + sops decrypt credentials (FaaS, edge, locked-down containers), build a per-module bundle that ships chain + narrowed re-encrypted layers as a single secret-manager value:

```sh
chameleon bundle build --module=billing-worker --manifest=modules.pxf ... --base64 \
  | gcloud secrets versions add billing-worker-bundle --data-file=-
```

The `BundleManifest` declares per-module allow/deny over secret-bearing field paths — a compromised bundle leaks only the secrets that module was authorized to use, not the full chain. See [Distribution bundles](#distribution-bundles) for the full surface (consume side: `bundle.OpenBase64` → `chameleon.Load`).

### What never to do

- **Don't reference a `pxf.Secret` from inside a `${field.path}` interpolation.** The WKT exclusion catches this — the interpolation walker doesn't descend into Secret subtrees and `${db_password}` errors as `unknown reference`. Surfacing a secret as a substring of another field would defeat the memguard story.
- **Don't read `cfg.Field.Value` on a `pxf.Secret` field.** It's been cleared. Use the codegen accessor or `Secrets.Get(path)`.
- **Don't reuse a `LockedBuffer` after `Destroy()`.** Each Open + Destroy is one cycle. Need it again? Open it again — the accessors are idempotent.
- **Don't pin the same age / KMS key across all environments.** Per-env recipients let you revoke a single env's access cleanly (e.g. remove a leaked dev key without touching prod).
- **Don't commit `.sops.yaml` recipients without auditing them.** Anyone whose key is in the recipient list can decrypt the layer locally even without prod access. Quarterly audit + rotation hygiene applies.

## Custom decryption (programmatic key injection)

`sops.Default()` sources keys from the ambient environment (`SOPS_AGE_KEY`, etc.). For programmatic key injection — say, fetching keys from a secret manager at runtime — implement the `crypt.Decrypter` interface yourself. `chameleon/crypt` is a leaf package (standard library plus memguard), so a custom backend costs your binary nothing beyond what you wire into it:

```go
import "github.com/trendvidia/chameleon/crypt"

type myDecrypter struct{ ageKey string }

func (d *myDecrypter) Decrypt(ctx context.Context, ciphertext []byte, format crypt.Format) (*memguard.LockedBuffer, error) {
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

The `Decrypter` interface is one method (`Decrypt`) — straightforward to wrap any encryption backend. Pass it to `resolver.WithDecrypter`, or register it under your own name with `crypt.Register` from an `init` if you would rather select it with a blank import the way `chameleon/sops` does.

`sops.Decrypter`, `sops.Encrypter`, `sops.Format`, `sops.Recipients` and the `sops.Format*` constants still exist as aliases for their `crypt` counterparts, so code written against the old spellings compiles unchanged — but they are deprecated, and importing `chameleon/sops` to name a type is what put 56 MB of cloud SDKs in binaries that never decrypted anything. Switch the import to `chameleon/crypt`.

## Distribution bundles

A **bundle** is a zip artifact carrying a chain spec plus its layer files as a single addressable blob. Built per-module: each consumer module ships only the secrets it's authorized to see, so a compromised bundle leaks just that module's surface area — not every secret in the chain.

### What gets narrowed

A `BundleManifest` (PXF-encoded) declares one entry per module, with **two independent selectors** — one for `pxf.Secret`-bearing fields, one for non-secret (attribute) fields. Both use schema-form field paths with `*` (one segment) / `**` (zero or more) globs.

```pxf
modules = [{
  name = "billing-worker"

  # Secret selector — gates pxf.Secret-bearing fields.
  allow = ["db.*", "primary.auth_token"]    # only these secrets survive
  # deny = ["db.admin_password"]            # carve-outs after allow

  # Attribute selector — gates everything else (non-secret config).
  # Omit the attribute_* fields entirely and non-secrets pass through
  # unchanged (the historical "secrets-only narrowing" behavior).
  attribute_deny = ["debug.*", "internal.*"]

  recipients = { age = ["age1examplerecipient..."] }
}]
```

The two selectors are evaluated independently: a secret field's verdict doesn't depend on the attribute filter and vice versa. Default policies (when `default` / `attribute_default` aren't set explicitly):

| Patterns set | Selector default |
|---|---|
| No `allow` / `attribute_allow` patterns | Falls through to manifest `default` (else `ALLOW_ALL`) |
| `allow` set (whitelist intent) | Implicit `DENY_ALL` — only listed paths survive |
| `attribute_allow` set | Implicit `DENY_ALL` for non-secrets |

Common postures:

- **Secrets-only narrowing** (default) — set `allow` / `deny`, leave `attribute_*` empty. Matches pre-narrowing-of-non-secrets behavior.
- **Strip debug surface** — `attribute_deny = ["debug.*"]` with `attribute_default = ALLOW_ALL` (implicit). Drops debug-only fields, keeps everything else.
- **Hard whitelist** — `attribute_allow = ["public.*"]`. Only public surface ships; everything else (including non-secrets) is dropped.

Patterns are validated at build time against the consumer schema: `allow` / `deny` match against the `secretcatalog` enumeration of `pxf.Secret` paths, and `attribute_allow` / `attribute_deny` match against the parallel non-secret-field enumeration. A pattern matching nothing in its catalog is a bundler error — never silently produces an over-restricted bundle.

The bundler decrypts each layer, drops denied fields (secret and non-secret independently), then re-encrypts with the module's recipients. `recipients_by_env` opt-in encrypts each layer file to only the env that consumes it, so a single env-agnostic bundle still preserves env isolation in the ciphertext (a dev runtime physically can't decrypt the prod layer). When the chain spec declares per-env `key_name` (see [Naming the encryption key per environment](#naming-the-encryption-key-per-environment)), the bundler unions those recipients with `recipients_by_env`, so chains migrated to named keys don't need to duplicate the recipient list in the manifest.

### Building

```sh
chameleon bundle build \
    --manifest=modules.pxf --module=billing-worker \
    --schema=app.proto --message=app.v1.AppConfig \
    --chain=chain.pxf --layers=./layers \
    --decrypter=sops --out=billing-worker.bundle
```

For paste-into-a-secret-manager-field workflows, add `--base64`:

```sh
chameleon bundle build ... --base64 \
    | gcloud secrets versions add billing-worker-bundle --data-file=-
```

`--out=PATH` refuses to overwrite an existing file by default — typo'd paths can't clobber a pinned release artifact or a file in active use. For the iterative "edit manifest → rebuild" loop, add `--force` to truncate and overwrite:

```sh
chameleon bundle build ... --out=billing-worker.bundle --force
```

### Consuming

The runtime story stays the chameleon runtime story — the bundle's `chain.pxf` + layer FS plug into `chameleon.Load` exactly the way an on-disk layer directory would:

```go
import "github.com/trendvidia/chameleon/bundle"

// 1. Pull the bundle out of wherever you stored it.
raw, _ := secretsClient.Get(ctx, "billing-worker-bundle")

// 2. Open it. Use OpenBase64 for the base64-wrapped form.
b, err := bundle.OpenBase64(bytes.NewReader(raw))
if err != nil { return err }

// 3. Resolve normally.
res, err := chameleon.Load[*myappv1.AppConfig](
    &myappv1.AppConfig{}, b.Chain, b.FS, "prod",
)
if err != nil { return err }
defer res.Destroy()
```

`bundle.Open` reads raw zip bytes; `bundle.OpenBase64` decodes the base64 wrapping first. Both return a `Bundle{FS, Chain, Audit}` — `FS` and `Chain` go straight to `chameleon.Load`, `Audit` carries the produce-time manifest hash + module + timestamp for diagnostics.

### Debugging a bundle from the CLI

`chameleon explain`, `chameleon get`, and `chameleon reveal` all accept bundle input via three flags, so you can debug a bundle the same way you'd debug a loose chain + layer directory — no unzipping by hand. The chain spec inside the bundle replaces `--chain`; the bundle's `fs.FS` replaces `--layers`.

| Flag | Source |
|---|---|
| `--bundle=PATH` | Read the bundle bytes from a file at PATH |
| `--bundle=-` | Read the bundle bytes from stdin |
| `--bundle-env=VAR` | Read the bundle bytes from the env var named VAR |

Add `--base64` to any of the three when the bundle is base64-wrapped (matching `chameleon bundle build --base64` output).

```sh
# From a file on disk.
chameleon reveal --field=db_password \
    --schema=app.proto --message=app.v1.AppConfig \
    --bundle=billing-worker.bundle --decrypter=sops --env=prod

# Streamed through stdin — pipe straight from a secret-manager CLI.
gcloud secrets versions access latest --secret=billing-worker-bundle \
  | chameleon get --schema=app.proto --message=app.v1.AppConfig \
                  --bundle=- --base64 --decrypter=sops --env=prod

# From an env var the runtime already has.
chameleon explain --schema=app.proto --message=app.v1.AppConfig \
                  --bundle-env=CONFIG_BUNDLE --base64 --env=prod
```

`--bundle-env=VAR` reads the variable's *value* directly inside the CLI — the shell-expanded form `--bundle="$VAR"` would put the bundle bytes on the process command line where `ps` and `/proc/$pid/cmdline` would surface them to other users on the host. Prefer the by-name form for anything containing ciphertext.

`--bundle*` flags are mutually exclusive with `--chain` and `--layers`; combining them errors at flag-parse time.

Chains that declare a `kind=env` source for `STEWARD_*`-style runtime overrides at the top of the chain work transparently with `--bundle*` — the CLI walks the bundled chain spec's `sources` map and registers each kind alongside the bundle's FS. Same encrypted-on-env-source rejection rule the on-disk path applies.

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

- **`(pxf.default)` type coverage.** The post-merge defaults pass honors an annotation on any scalar field and on the atomic well-known types that PXF has a literal form for: `Duration` (`"250ms"`), `Timestamp` (RFC3339), eight of the nine `google.protobuf` wrappers, `pxf.BigInt`, `pxf.Decimal`, `pxf.BigFloat`. A default on a WKT field sets the **whole** value — atomicity governs merging, not defaulting. A default on any other message-typed field (`google.protobuf.Any`, `pxf.Secret`, `google.protobuf.Struct`, a user-defined message) is a resolve-time **error**, not a silent no-op. The one remaining silent case is `repeated` / `map` fields, where the annotation is ignored ([#131](https://github.com/trendvidia/chameleon/issues/131)).

  Three edges are worth knowing before you annotate a WKT field:

  - **`google.protobuf.BytesValue` is the ninth wrapper and the one that is not covered** — protowire-go's wrapper-default path has no bytes branch, so `[(pxf.default) = "aGVsbG8="]` on a `BytesValue` field is a resolve-time error (`unsupported default kind bytes`). The identical literal on a plain `bytes` scalar decodes fine; only the wrapper is affected.
  - **A default on a WKT is never interpolated.** Defaults run before interpolation so that a default may itself be a template (`"api.${base_url}"`), but interpolation stops at the same atomic boundary the merge does — so a `${...}` inside a default on a `google.protobuf.StringValue` reaches your resolved config verbatim. Use a plain `string` field when the default needs interpolation.
  - **Bool literals must be exactly `"true"`.** protowire-go compares the literal against `"true"`, so `"True"` / `"1"` / `"yes"` yield `false` with no error — and the path still counts as satisfying `(pxf.required)`. Every other literal kind reports a parse failure.

- **`(pxf.default)` inside a `oneof` yields to the layer's choice.** Setting any oneof member clears its siblings, so a default is skipped whenever some layer already selected a different member of the same oneof — otherwise filling `timeout`'s default would silently discard an operator's explicit `file = "/tmp/x"`. Fields from proto3 `optional` (a synthetic single-field oneof) are unaffected and default normally.

- **Sops `context.Context` threading — closed via the `trendvidia/sops/v4` fork.** Chameleon's `Decrypter.Decrypt` threads `ctx` through `decrypt.DataIntoWriterContext`; in-flight KMS round-trips abort on `ctx` deadline / cancellation natively for AWS / GCP / Azure / Vault. HuaweiCloud KMS is approximated via a goroutine shim (see fork [#10](https://github.com/trendvidia/sops/issues/10)). The `chameleon migrate` CLI uses the corresponding `encrypt.UsingSameKeysAsWithContext` so re-encrypts honor ctx the same way. Upstream [getsops/sops#2179](https://github.com/getsops/sops/issues/2179) tracks the merge candidate; chameleon consumes the fork directly under its `github.com/trendvidia/sops/v4` module path — no `replace` directive required.

- **Forked dependencies require an explicit `replace` directive in consumer `go.mod`.** Chameleon depends on `trendvidia/protobuf-go` (a soft fork of `google.golang.org/protobuf`); Go's `replace` directive is non-transitive so the chameleon repo's `replace` does NOT propagate to consumers. The other fork chameleon depends on — `trendvidia/sops/v4` — is consumed directly under its own module path and does NOT need a `replace`. See **Compatibility → Replace directives** above for the exact line to copy into your own `go.mod`.

## Compatibility

| Dep | Version |
|---|---|
| Go | 1.26+ |
| `github.com/trendvidia/protowire-go` | `>= v0.77.0` (block-form `OnSecretField` routing; v0.76.0+ covers scalar shorthand) |
| `github.com/trendvidia/sops/v4` | `>= v4.0.0` (soft-fork of `getsops/sops` consumed directly under its own module path — no `replace` directive needed) |
| `github.com/awnumar/memguard` | `>= v0.22.5` |

**Module graph vs. linked code.** Since v0.23.0 a plaintext consumer *links* no key-management code (see [Runtime decryption](#runtime-decryption)), but `go list -m all` still lists the KMS SDK modules: chameleon's own `go.mod` requires them for the `chameleon/sops` package and the CLI, and Go's module graph is per-module, not per-import. So dependency-review and SBOM tooling that reads `go.mod` still sees them; binary size, link time, and reachability-based scanners (`govulncheck`) do not. Removing them from the graph as well means splitting `chameleon/sops` into its own module — the open half of [#130](https://github.com/trendvidia/chameleon/issues/130).

### Replace directives — required in your `go.mod`

Go module `replace` directives **only apply to the main module's build** — they do **NOT** propagate from a dependency's `go.mod` to your binary's resolution. Chameleon depends on one fork whose `replace` directive you must copy into your own `go.mod` for the build to succeed against the function set chameleon calls:

```go
// In your project's go.mod:
replace google.golang.org/protobuf => github.com/trendvidia/protobuf-go v1.36.12
```

If you omit it, `go build` fails — chameleon (via protowire-go) calls into functions that don't exist on the upstream package.

| Replace | Required by | Why the fork | Tracking upstream |
|---|---|---|---|
| **`github.com/trendvidia/protobuf-go v1.36.12`** | protowire-go's `dynamicpb` fast path | unexported `SetUnsafe` / `AppendUnsafe` / map `SetUnsafe` on `*dynamicpb.Message`. Saves the per-call typecheck box-alloc on the PXF unmarshal hot path. | upstream protobuf-go has not yet accepted; the fork tracks the upstream `v1.36` line. |

The fork keeps the upstream module path (`google.golang.org/protobuf`), so any code that imports the upstream namespace compiles against the fork unchanged. The fork repo publishes its CHANGELOG against the upstream tag it rides on.

When upstream eventually merges the relevant parts, drop the `replace` from your `go.mod` and bump the chameleon minimum to the merged upstream tag.

The other fork chameleon depends on — `github.com/trendvidia/sops/v4` — is consumed directly under its own module path (it doesn't override `getsops/sops`), so no consumer-side `replace` is needed.

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
cmd/chameleon/          CLI: explain (per-field provenance) / verify (@header.content_sha256) / migrate (bulk layer edits) / hash / lint (go/analysis-style linter flagging *pxfv1.Secret.Value reads)
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
 