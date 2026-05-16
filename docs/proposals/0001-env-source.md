# Proposal 0001 — `kind = "env"` source

**Status:** draft.
**Trigger:** trendvidia/steward needed to override `anthropic.api_key` (a Secret) and a pair of LLM perf knobs (`max_tokens`, synth timeout) at runtime. Without an env-aware layer in chameleon, the consumer ended up with `scripts/sync-worker-env.sh` — a sidecar that resolves chameleon and rewrites `.env`. That script *is* a second source of truth, and the consumer is stuck reconciling it.

## Context

Today every layer in a chain is a PXF document fetched through a `Source` (`source.Source` interface — content-agnostic byte producer). The `fs` source covers the static / committed case well. What's missing is the case where the operator wants to override a single resolved field at runtime — typical reasons:

- A perf knob (`max_tokens`, request timeout) that an SRE wants to tune without editing files.
- A credential injected by the orchestrator (Kubernetes `Secret` → env, ECS task definition, Docker Compose `environment:`) that the operator doesn't want copied into a pxf file at all.
- A debug-only override on a developer's laptop that should not become a committed artifact.

Steward-side workaround today: a shell script reads chameleon, then rewrites `.env` so the worker (which doesn't link chameleon) sees the same values. Net effect — the env values *are* the chameleon values, just laundered through a script. That's the cost we want to eliminate.

## Proposal

Add `kind = "env"` to `chameleon.v1.SourceRef`. An env-source layer reads process environment variables at resolve time and emits them as a synthetic PXF document that flows through the existing parse/merge/Secret pipeline unchanged. Operators declare it as just another `LayerEntry`, typically last (highest precedence).

Chain spec example:

```
sources = {
  "default": { kind = "fs"  options = { "root": "./layers" } }
  "secrets": { kind = "fs"  options = { "root": "./layers/secrets" } }
  "env":     { kind = "env" options = { "prefix": "STEWARD_" } }
}

layers = [
  { id = "base"        path = "base.pxf"     for_each = ONCE     required = true }
  { id = "env-layer"   path = "env/${env}.pxf" for_each = PER_ENV }
  { id = "secrets-dev" path = "${env}.pxf"   source = "secrets"  for_each = ONCE }
  { id = "env-runtime" path = ""             source = "env"      for_each = ONCE }
]
```

The env layer's `path` field is unused for `kind = "env"` (kept to satisfy `LayerEntry.path = required`; the resolver could special-case it as empty-allowed for env sources, or accept a meaningless sentinel).

Net result: `STEWARD_ANTHROPIC_API_KEY` set in the worker's environment ends up in `cfg.Anthropic.ApiKey` with the same Secret semantics as if it had come from `secrets/dev.pxf`. The bridging script disappears.

## Design points (need decisions)

### 1. Field-to-env-name mapping

**Option A (recommended): implicit, with optional prefix.** Dotted field path → uppercase + underscores. `anthropic.api_key` → `ANTHROPIC_API_KEY`, or `STEWARD_ANTHROPIC_API_KEY` with `prefix = "STEWARD_"` in source options.

Pros: zero per-field config; the env-var name is derivable from the schema path. Operators can predict it without a lookup.
Cons: collisions if the schema has, say, `foo.bar_baz` and `foo_bar.baz` — both map to `FOO_BAR_BAZ`. Vanishingly rare for our schemas; resolver can reject ambiguous mappings at chain-load time.

**Option B: explicit, per-field annotation.** Add a proto option `[(chameleon.env_var) = "STEWARD_ANTHROPIC_KEY"]` on each field that participates.

Pros: visible at the schema; immune to collisions; supports non-derivable names (e.g. matching an existing convention like `ANTHROPIC_API_KEY` without prefix).
Cons: every field that wants env override needs an annotation; schema authors easily forget; the schema becomes the registry of "what's tunable."

**Recommendation: ship A; reserve the annotation for special cases later.** Convention-over-configuration aligns with how operators already think about env vars.

### 2. Type coercion

Env values are strings; AppConfig fields are typed. Coercion rules:

| Proto kind | Env interpretation |
|---|---|
| string | as-is |
| int32 / int64 / uint32 / uint64 | `strconv.ParseInt` / `ParseUint` |
| float32 / float64 | `strconv.ParseFloat` |
| bool | `strconv.ParseBool` (`true`/`false`/`1`/`0`) |
| `google.protobuf.Duration` | `time.ParseDuration` (`"120s"`, `"3m"`) |
| `google.protobuf.Timestamp` | RFC 3339 |
| `bytes` | base64 (matches how secrets are typically held) |
| `pxf.Secret` | string body becomes the secret's `value` — loses at-rest encryption (env vars are plaintext) but that's the operator's explicit choice |
| repeated / map / nested message | **not supported in v1** — env vars are flat; structured overrides should go through pxf files |

**Failure mode:** parse failure is a hard error at resolve time (loudly cite the field + env var). Silent fallback to the next layer is tempting but hides typos until the misconfigured field is actually consumed.

### 3. Field discovery

**Option A (recommended): reflective.** At resolve time, the env source walks the schema's message descriptor for the root config type and probes each field's mapped env var.

Pros: zero per-field config; new fields automatically participate.
Cons: the env source needs the schema descriptor — requires plumbing it into the `Source` constructor, which today is content-agnostic.

**Option B: explicit opt-in.** Only fields with an `[(chameleon.env) = true]` annotation are probed.

Pros: visible; safer-by-default (a new field doesn't gain a new env-var attack surface unintentionally).
Cons: more friction; schema authors must remember.

**Option C: hybrid — reflective with opt-out.** Default-on, with `[(chameleon.env_skip) = true]` for fields that must never be env-overridable (e.g. audit-critical settings).

**Recommendation: ship C.** Defaults match operator intuition; opt-out covers the rare "absolutely must come from a committed file" case.

## Implementation surface (sketch — no code)

- **proto:** `SourceRef.kind` already accepts arbitrary strings; no proto change. Optional later: `pxf/annotations.proto` gets `env_skip`, `env_var` field options.
- **`source/env.go`:** new `EnvSource` type. Unlike `FSSource`, it can't satisfy the pure byte-producer contract without the schema; either (a) extend `Source` with a richer "produce a parsed layer" method that env can use exclusively, or (b) pass the schema descriptor through source options/constructor and have env emit a synthetic PXF byte stream that the existing pipeline parses normally.
- **`resolver/`:** chain-load validation rejects an env layer with `encrypted = true` (sops decrypt doesn't apply); rejects an env source on an `fs`-shaped path template that isn't empty.
- **`cmd/chameleon/get`:** no change for resolution; `explain` should cite the env layer with the env-var name as the provenance string, not a file path.
- **`cmd/chameleon/reveal`:** env-sourced Secrets reveal as the raw env value; flag the absence of at-rest encryption in the provenance line.
- **Tests:** the existing `source/source_test.go` style (an in-memory `fstest.MapFS` substitute) becomes a `t.Setenv`-driven test for `EnvSource`. Parse-coercion failures get table-driven coverage.

The cleanest implementation is probably (b) — synthesize PXF from env, let the existing pipeline do the rest. It keeps `Source` content-agnostic in spirit (env source still produces *bytes*; it just generates them from process state) and the parse/Secret/merge path doesn't need to learn a second materialization model.

## Out of scope (this proposal)

- **Two-way binding.** Chameleon doesn't write env vars; it reads them once at resolve time.
- **Watch / reload.** Env vars don't change inside a running process; the resolver runs once at boot.
- **Per-field source precedence.** A field that pulls from one env var while sibling fields pull from a file at a different precedence. Layer-level precedence handles every concrete case we've hit; per-field would be a separate proposal.
- **Repeated / map / nested message fields from env.** Would need a serialization convention (JSON-in-env? dotted-flat?). Defer until a real consumer asks.
- **`kind = "vault"` / `"ssm"` / `"secrets-manager"`.** Same `Source` extension model could host them; out of scope here, but the env-source PR should not paint chameleon into a corner that blocks them.

## Alternatives considered

- **Keep env handling in consumers.** Status quo. Every downstream (steward, future consumers) writes its own sync bridge. Two sources of truth, perpetual drift risk.
- **Generate sync scripts from chameleon (`chameleon emit-env`).** Mechanical, but still leaves env vars as a parallel artifact that has to be regenerated whenever the chain changes. Worse posture than just teaching chameleon to read env directly.
- **A separate "overlays" mechanism, distinct from layers.** Splits the conceptual model (layers + overlays); we'd end up explaining the same precedence rules twice. Layers already model "later wins"; env is just another layer.

## Open questions

- Should the env source emit a `pxf.required`-style error when a field's env var is empty (vs. unset)? Today an unset env var is "no override"; an explicit empty string is ambiguous. Default: treat empty as unset (most ergonomic), but reserve a flag in source options if a consumer needs distinction.
- Coercion error display: should `chameleon explain` surface the *attempted* coercion in the trace, so the operator sees "tried to coerce `not-a-number` to int32 for field foo.bar via env FOO_BAR; rejected"?
- Interaction with `chameleon migrate`: when a field is renamed in the schema, do we also rewrite consumer references to its old derived env-var name? Probably "warn, don't rewrite" since env vars live outside the repo. Worth confirming.
