- **`chameleon check-schema --schema=<dir>` follows symlinks** ([#167](https://github.com/trendvidia/chameleon/issues/167)). A subtree symlinked into place — a normal monorepo layout, and this command exists for the shared-schema repo most likely to have one — was silently not searched, so the run reported `OK` over a narrower set than you named:

  ```sh
  $ find -L proto -name '*.proto' | wc -l
  4
  $ chameleon check-schema --schema=./proto
  # before
  OK    3 root(s)    3 file(s) checked
  # now
  OK    4 root(s)    4 file(s) checked    1 linked
  ```

  Linked **files** are resolved too, not only directories: the old walk collected a linked `.proto` by name without ever asking what it pointed at, so a *dangling* one went to the compiler and failed there, and one whose target the walk also reached directly was compiled twice. It is linked **directories** whose contents are new.

  The new `linked` field counts the files no path through the real tree reaches. It does not move with whether a link's name happens to sort before its target's, or with how many arguments cross a link to the same file, so it can never exceed the file count beside it.

  Three things following symlinks obliges, all handled rather than left to protocompile:

  - **A file reachable both directly and through a link is checked once**, under the path that crosses no link. Two paths to one file would otherwise compile it twice under two names, which is the duplicate-symbol failure the `--schema` naming rules exist to prevent — and preferring the real path matters as much as the deduplication does, because a root named through a link leaves a sibling's import of the real name compiling a second copy.
  - **A link pointing at an ancestor terminates.** Each directory is entered once, keyed by its resolved path. Terminating is all that guarantees: the ancestor's other subtrees still enter the search, because leaving the directory you named is what following a link means.
  - **A dangling link is skipped**, not fatal. It names nothing, and the rest of the tree is still worth checking.

  `--exclude` matches a link by its **own** name — `--exclude=linked` prunes it — because that is the name you see in the tree rather than the one it points at.

  **If you match on stdout:** a `\t<n> linked` field can now follow the file count (and the `skipped` field from [#166](https://github.com/trendvidia/chameleon/issues/166)), and a tree with symlinks will report higher counts than it did. That second part is the fix, not a side effect: the old numbers were answering a narrower question than the one asked.
