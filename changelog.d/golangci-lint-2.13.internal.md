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
