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
`sops/go.mod` also carries a `replace` to the library for anyone working in
that module without the workspace. `cmd/chameleon/go.mod` carries none, on
purpose: `go install pkg@version` refuses a module whose `go.mod` has a
`replace`, so the CLI module names the library and sops by their released
tags at all times, and a `cmd/chameleon` tag can be cut from any commit.
Outside the workspace the CLI therefore builds against the *last released*
library and sops, not the worktree — acceptable because those two are stable
and a CLI change rarely needs an unreleased change beneath it; when one does,
work in the workspace, which is how `make` and CI build anyway. Until v0.25.0
the CLI carried a pair of `replace`s that had to be dropped for every tag and
restored afterwards (#175/#176, #186/#188); that back-and-forth is gone.
Consumers ignore replaces either way — what they get is the `require` line,
which is why the tag order below matters.

`make test`, `make vet`, `make lint` and `make tidy` all iterate over the three
modules; `./...` alone stops at a module boundary and would quietly cover a
third of the repo.

## Cutting a release

1. **Land everything.** Confirm `make ci` is green and `git status` is clean.

   Changelog entries arrive as fragments in [`changelog.d/`](../changelog.d),
   one file per change — see its README. Nothing is written into
   `CHANGELOG.md` during the cycle, so there is no changelog to reconcile
   here; check that every change you expect to ship has a fragment, and
   write `changelog.d/_preamble.md` if this release wants the lead
   paragraph that v0.22.0 and v0.22.1 have.

2. **Assemble the changelog**, on its own commit so the release diff is
   readable:

   ```
   make changelog-preview VERSION=0.23.0   # read it first
   make changelog VERSION=0.23.0
   git commit -am "release: assemble v0.23.0 changelog"
   ```

   `VERSION` is bare — no leading `v`, which is the tag's alone. This folds
   the fragments into a new `## [0.23.0]` section, resets `## [Unreleased]`
   to its empty marker, and deletes the fragments it consumed. Do it
   **before** tagging — the tag must contain the changelog the release
   ships with. Assembling is refused if the version already has a section
   or if anything was written into `## [Unreleased]` by hand, so a second
   run cannot duplicate or discard; to fold in a late fragment, restore
   both sides (`git checkout -- CHANGELOG.md changelog.d`) and run once.

   `main` is protected, so this lands as a PR — and it is the one PR that
   deletes fragments instead of adding one. **Label it `skip-changelog`**
   or the changelog gate blocks it.

3. **Tag the library.**

   ```
   git tag v0.23.0 && git push origin v0.23.0
   ```

4. **Point sops at it, then tag sops.** Edit `sops/go.mod` so
   `require github.com/trendvidia/chameleon` names the tag you just pushed,
   commit, then:

   ```
   git tag sops/v0.23.0 && git push origin sops/v0.23.0
   ```

5. **Point the CLI at both, then tag the CLI.** Edit `cmd/chameleon/go.mod`
   so its two `require` lines name the library and sops tags from steps 3
   and 4, run `go mod tidy` there, commit, then:

   ```
   git tag cmd/chameleon/v0.23.0 && git push origin cmd/chameleon/v0.23.0
   ```

   The module carries no `replace` directives, and must not gain any: `go
   install pkg@version` refuses a module whose `go.mod` has one — "*it must
   not contain directives that would cause it to be interpreted differently
   than if it were the main module*" — so a `cmd/chameleon` tag cut with one
   in place makes **every** `go install github.com/trendvidia/chameleon/cmd/chameleon@...`
   fail, and a pushed tag cannot be taken back. Assert it before you tag, not
   after:

   ```
   test "$(go mod edit -json cmd/chameleon/go.mod | jq -r '.Replace')" = null \
     || { echo "cmd/chameleon/go.mod has replace directives"; exit 1; }
   ```

   Steps 4 and 5 may be one PR: both tags are pushed after the merge, so
   `cmd/chameleon`'s `require` naming a sops tag that does not exist yet is
   fine as long as `sops/vX.Y.Z` is pushed before `cmd/chameleon/vX.Y.Z`.
   Doing them together also keeps `main` green between them — with the CLI
   naming released tags only, its build list is consistent at every step
   (#187).

6. **Verify from outside the workspace** — the requires are the one thing the
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
  package, which is now the nested module. **Version pins keep their shape too**:
  you still write `@v0.23.0`, and Go maps it to this module's `cmd/chameleon/v0.23.0`
  tag automatically, the same way `go install golang.org/x/tools/gopls@v0.14.2`
  resolves the `gopls/v0.14.2` tag. The repo-tag spelling is not a version query
  and does not work on the command line. What *does* change is which commit a pin
  resolves to: `@v0.23.0` now names the CLI module's own tag, while an existing
  `@v0.22.1` still resolves through the root module (that revision predates the
  split). The verify-from-outside step above is where you confirm it against the
  proxy.
- **`protoc-gen-chameleon-go`**: unchanged, root-module tags, pins keep working.
