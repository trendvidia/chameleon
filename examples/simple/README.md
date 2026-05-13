# examples/simple

A chain-spec fixture — no Go code. The files are referenced by the
README walkthrough and by the `cmd/chameleon` smoke tests; nothing
here is meant to be `go run`-able on its own.

- `chain.pxf` — minimal `ChainSpec` with two environments and two layers.
- `layers/base.pxf` — base configuration.
- `layers/env/dev.pxf` — `dev`-environment overlay.

To explore them via the CLI:

```sh
chameleon explain --chain examples/simple/chain.pxf --env dev
```

For a runnable Go setup (consumer schema + `buf generate` + a
`resolver.Resolve` call), see [`examples/buf/`](../buf/).
