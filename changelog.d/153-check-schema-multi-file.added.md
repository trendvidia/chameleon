- **`chameleon check-schema --schema` is repeatable and takes a directory** ([#153](https://github.com/trendvidia/chameleon/issues/153)). A repo that owns a shared schema owns many `.proto` files, and its question is "is every file I own conformant?" — not "is this one file's closure conformant?", asked N times:

  ```sh
  # before: one compile per file, and one report per importing file
  for f in proto/**/*.proto; do chameleon check-schema --schema="$f" || exit 1; done

  # now
  chameleon check-schema --schema=./proto --include=./proto
  # OK    5 root(s)    6 file(s) checked
  ```

  All roots compile **once, together**, so a shared import is parsed and linked once no matter how many roots reach it, and a violation in it is reported **once** — with the roots whose closure reaches it, so deduplication does not cost you the blast radius:

  ```
  common/v1/common.proto: field "common.v1.Shared.labels": invalid (pxf.default) = "nope": …    (via svc/a.proto, svc/b.proto, svc/c.proto)
  ```

  A directory is searched recursively for `*.proto`. Glob expansion is left to your shell — `--schema=proto/*.proto` works because the shell expands it into repeated flags, while `**` means different things in bash and fish and nothing in sh, so a glob this command expanded itself would disagree with the same string typed at a prompt.

  With several roots the `OK` line carries **both** counts — roots asked for, and distinct files covered. The file count stayed the cheapest signal that `--include` is wrong ([#144](https://github.com/trendvidia/chameleon/issues/144) made it load-bearing), and on its own it can no longer say how many roots produced it. `--message` is asked once of the union closure rather than once per root: with several roots the message need only exist somewhere in what was checked.

  Files found under a directory are named the way their siblings import them (protoc's `-I` contract), which is what lets one file's violations be recognised as one violation. Two *different* files that would compile under one name are rejected, naming both and suggesting `--include`, rather than being silently resolved to whichever import path came first.

  **The single-file form is unchanged** — same naming, same violations, same `OK <path> <n> file(s) checked` line. What used to be a hard error is now the feature: a directory passed to `--schema` was rejected with "not a regular file", and is now searched. An *empty* directory is still an error, since "OK, 0 files" is not an answer anyone wanted.
