# Contributing to chameleon

Thanks for your interest in contributing! chameleon is schema-driven
layered configuration for Go: a `.proto` schema, PXF layer files that fall
through an environment chain, and sops-encrypted secrets held in memguard
enclaves at runtime. See the [README](README.md) for the full picture.

## Getting Started

```bash
git clone https://github.com/trendvidia/chameleon.git
cd chameleon
make check     # gofumpt check, vet, unit tests
make ci        # everything CI runs through make
make help      # every target
```

You need Go at the version in `go.mod` or newer, and GNU Make 3.82 or
newer. macOS ships 3.81, which silently drops failures in per-module
loops, so the Makefile refuses to run under it: `brew install make` and
use `gmake`.

The repository holds three Go modules — the library at the root, the sops
backend in `sops/`, and the CLI in `cmd/chameleon/` — wired together by
`go.work`. The make targets walk all three; a bare `go test ./...` at the
root covers only the library. [docs/RELEASING.md](docs/RELEASING.md)
explains why they are split.

## What Makes a Good PR

- **One concern per PR.** Bug fix, feature, or refactor — pick one.
- **Tests with the change.** Anything that touches secret handling needs a
  test that the plaintext does not leak, not only that the value is right.
- **A changelog fragment.** Add `changelog.d/<issue-or-pr>.<category>.md`;
  [changelog.d/README.md](changelog.d/README.md) has the format. CI fails a
  PR without one unless a maintainer labels it `skip-changelog`.
- **Formatting is gofumpt, through the pinned golangci-lint.** Run
  `make fmt`. A standalone `gofumpt` can disagree with the pinned version
  and CI will reject the difference.
- **No new dependency in the library module without a reason.** Consumers
  inherit its `go.mod`. Key-management SDKs in particular belong in
  `sops/`, and a test fails if they reach the root module.

## Reporting Bugs

Open a GitHub issue with a reproducer — a Go test, or a schema plus the
layer files that misbehave — and the release tag you observed it on. For
security-sensitive reports, see [`SECURITY.md`](SECURITY.md) instead;
those should never go through public issues.

## Code of Conduct

Participation is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
