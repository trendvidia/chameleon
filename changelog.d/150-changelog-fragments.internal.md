- **Changelog entries are now one file per change, in [`changelog.d/`](changelog.d/).** `CHANGELOG.md` is written only at release time, by `make changelog VERSION=x.y.z`, which folds the fragments into a new section and deletes them. Nothing about the published format changes — the assembled output is byte-identical to what was there before, because a fragment's entire content *is* the entry, verbatim markdown, tables and code blocks included.

  **Why:** two PRs that both touch `CHANGELOG.md` conflict, every time, on a file where the conflict carries no information — you keep both sides and move on. That happened three times in one afternoon across #143, #146 and #148. A fragment per change cannot conflict, because no two PRs write the same file.

  CI requires a PR to add a fragment; label a PR `skip-changelog` when a change genuinely has nothing to tell a consumer. `make changelog-check` validates the filenames and is part of `make ci`.

  **One rule the old format did not need:** a fragment must not say "the entry below" or "described above". Order is derived from the fragment IDs at release time and shifts as other PRs land, so positional references break silently — six of the sixteen migrated entries pointed at the wrong thing after assembly, and were rewritten to name their target instead. `changelog.d/README.md` has the details.
