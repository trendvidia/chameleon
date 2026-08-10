- **`chameleon check-schema` reports what `--exclude` skipped, and stops letting a pattern change the `OK` line's shape** ([#166](https://github.com/trendvidia/chameleon/issues/166)). Two ways a well-formed pattern could quietly narrow a green run:

  ```sh
  # before
  chameleon check-schema --schema=./proto --exclude=vendor
  # OK    2 root(s)    2 file(s) checked
  chameleon check-schema --schema=./proto --exclude='v*'      # also takes v1/
  # OK    ./proto    1 file(s) checked

  # now
  # OK    2 root(s)    2 file(s) checked    1 skipped
  # OK    1 root(s)    1 file(s) checked    2 skipped
  ```

  **The skipped count** is new. It is of files the run did *not* look at, not of files the patterns matched: one pruned from the walk and then named directly by another `--schema` is checked, and is not counted. The field is absent when nothing was skipped, so a run without `--exclude` prints exactly the line it always did.

  **The `OK` line's shape now follows what `--schema` named**, not how many roots came out of it. A directory search that exclusion narrowed to a single root used to flip to the single-file form — so a CI job matching `N root(s)` stopped matching at exactly the moment a pattern over-pruned, which is the failure it was watching for. One `--schema` naming one file keeps the single-file form; a directory, or more than one `--schema`, keeps the `N root(s)` form however many roots survive.

  **If you match on stdout.** The `N root(s)` form is now reachable with `N` = 1, and a `\t<n> skipped` field can follow the file count. Anchoring on the whole line is what breaks; matching the fields you care about is not. Pruning *every* `.proto` under a directory remains an error rather than a count, unchanged.
