- **`chameleon check-schema --exclude`** ([#159](https://github.com/trendvidia/chameleon/issues/159)). Prunes the `--schema` directory search by name. The case it exists for is a repo that vendors a second copy of a file its own files import: two copies of `pxf/annotations.proto` are two declarations of `pxf.required`, so compiling both as roots fails on duplicate symbols and the directory form added in [#153](https://github.com/trendvidia/chameleon/issues/153) could not be used on that tree at all.

  ```sh
  chameleon check-schema --schema=./proto --include=./proto
  # chameleon check-schema: compile 3 schema root(s): pxf/annotations.proto:5:8: symbol "pxf.required" already defined at third_party/pxf/annotations.proto:5:8

  chameleon check-schema --schema=./proto --include=./proto --exclude=third_party
  # OK    2 root(s)    3 file(s) checked
  ```

  (Which of the two copies that first line names as the offender, and which as the "already defined at", swaps between runs of the same command.)

  A pattern matches **one path element** at a time — a directory's or a file's own name — with `filepath.Match` syntax. That is what lets `--exclude=third_party` prune the directory at whatever depth it appears without needing a `**`: the walk stops there, rather than the pattern spanning the rest of the path. Within an element the usual syntax works (`--exclude='*_test.proto'`). A pattern containing a path separator cannot match an element, so it is rejected rather than silently matching nothing — as is an empty one. A malformed pattern fails the run as well, naming both the flag and the pattern; it is caught while searching rather than while parsing flags, because `filepath.Match` only reports bad syntax once its scan reaches it, so `--exclude='vendor*[0-9'` looks well-formed against every name that does not start with `vendor`. A pattern that prunes *every* `.proto` under the directory says so, instead of reporting the "no .proto files found" that would send you off to check the path.

  **What it does not do**, all three deliberate:

  - It does not touch `--include`. An excluded file stays importable, which is the point for a vendored tree — the second copy stops being a *root* while the files that legitimately import it still resolve.
  - It does not apply to a `.proto` named directly by `--schema`, nor to the `--schema` directory itself. Naming something explicitly is the more specific instruction, and reading it otherwise would turn `--schema=./third_party --exclude=third_party` into "no .proto files found".
  - Nothing is excluded by default. `third_party/`, `vendor/` and dot-directories are walked like any other until you ask for them to be skipped.

  Additive: a run with no `--exclude` behaves exactly as before. Shell completion for the flag ships for bash, zsh and fish.
