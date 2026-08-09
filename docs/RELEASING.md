# Releasing

Since v0.23.0 this repo publishes **three Go modules**. They are versioned
together — one release, three tags — but they must be tagged in dependency
order, because each one's `go.mod` names the version of the one below it.

| Module | Directory | Tag prefix | Depends on |
|---|---|---|---|
| `github.com/trendvidia/chameleon` | `.` | `vX.Y.Z` | — |
| `github.com/trendvidia/chameleon/sops` | `sops/` | `sops/vX.Y.Z` | the library |
| `github.com/trendvidia/chameleon/cmd/chameleon` | `cmd/chameleon/` | `cmd/chameleon/vX.Y.Z` | the library + sops |

The split exists so that a consumer of the library does not carry the cloud
key-management tree in its module graph — see
[#130](https://github.com/trendvidia/chameleon/issues/130) and the v0.23.0
CHANGELOG entry. `protoc-gen-chameleon-go` has no backend dependency and stays
in the library module, so it keeps the root module's tags.

## Local development

`go.work` at the repo root wires the three together, so `go build`, `go test`
and your editor resolve every cross-module import against the working tree.
The `replace` directives in `sops/go.mod` and `cmd/chameleon/go.mod` do the
same for anyone working in a single module without the workspace. Consumers
ignore both — what they get is the `require` line, which is why the tag order
below matters.

`make test`, `make vet`, `make lint` and `make tidy` all iterate over the three
modules; `./...` alone stops at a module boundary and would quietly cover a
third of the repo.

## Cutting a release

1. **Land everything**, CHANGELOG included. Confirm `make ci` is green and
   `git status` is clean.

2. **Tag the library.**

   ```
   git tag v0.23.0 && git push origin v0.23.0
   ```

3. **Point sops at it, then tag sops.** Edit `sops/go.mod` so
   `require github.com/trendvidia/chameleon` names the tag you just pushed,
   commit, then:

   ```
   git tag sops/v0.23.0 && git push origin sops/v0.23.0
   ```

4. **Point the CLI at both, then tag the CLI.** Edit `cmd/chameleon/go.mod` so
   its two `require` lines name the tags from steps 2 and 3, commit, then:

   ```
   git tag cmd/chameleon/v0.23.0 && git push origin cmd/chameleon/v0.23.0
   ```

5. **Verify from outside the workspace** — the requires are the one thing the
   workspace hides, so check them against the proxy rather than the worktree:

   ```
   cd $(mktemp -d) && go mod init verify
   GOFLAGS=-mod=mod go get github.com/trendvidia/chameleon/sops@v0.23.0
   go install github.com/trendvidia/chameleon/cmd/chameleon@v0.23.0
   ```

   A stale `require` surfaces here as a build against the wrong library
   version, and nowhere earlier.

## What consumers see

- **Library only** (the common case): `go get github.com/trendvidia/chameleon`.
  Nothing changed.
- **Library + encrypted layers**: also
  `go get github.com/trendvidia/chameleon/sops`, which is what `go get` does on
  its own the first time the blank import appears. It is a separate `require`
  line in their `go.mod` now, where it used to arrive for free.
- **The CLI**: `go install github.com/trendvidia/chameleon/cmd/chameleon@latest`
  is unchanged — `@latest` resolves the longest module path that provides the
  package, which is now the nested module. Version pins change shape: an
  existing `@v0.22.1` still resolves (that revision predates the split, so the
  root module still contained the package), but from v0.23.0 on a pin must name
  the module's own tag — `@cmd/chameleon/v0.23.0`, not `@v0.23.0`. Step 5 above
  is where you confirm that against the proxy.
- **`protoc-gen-chameleon-go`**: unchanged, root-module tags, pins keep working.
