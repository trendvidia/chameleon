# changelog.d — one file per change

Changelog entries live here as individual files until a release folds them
into [`CHANGELOG.md`](../CHANGELOG.md).

**Why:** two PRs that both edit `CHANGELOG.md` conflict, every time, on a
file where the conflict carries no information — you keep both sides and
move on. It happened three times in a single afternoon on the #134/#135
workstream. A fragment per change never conflicts, because no two PRs
write the same file.

## Adding one

Create `changelog.d/<id>.<category>.md`:

- **`<id>`** — the issue or PR number the change belongs to (`134`). Use a
  short slug (`releasing-doc`) when there genuinely isn't one. IDs order
  the entries within a category; numbered ones sort first, ascending.
- **`<category>`** — one of `added`, `changed`, `deprecated`, `removed`,
  `fixed`, `security`, `internal`.

The name must end in `.md` and the file must sit directly in this
directory — `make changelog-check` fails on anything else rather than
skipping it, because CI's "did this PR add a fragment?" gate counts any
added file here, and a `.txt` or a subdirectory would pass the gate and
then never be assembled into anything.

Nothing goes into `CHANGELOG.md` itself any more, not even under
`## [Unreleased]`: assembly replaces that section wholesale, so an entry
left there is destroyed at release time. `make changelog-check` fails on a
non-empty Unreleased section for that reason.

**The file's entire content is the entry, verbatim.** There is no wrapper
format, no YAML, no escaping. Write exactly the markdown you would have
written into `CHANGELOG.md` — a top-level `-` bullet, with any tables,
fenced code blocks and extra paragraphs indented two spaces to stay inside
it:

```markdown
- **`resolver.New` now rejects a schema whose PXF annotations cannot bind**
  ([#134](https://github.com/trendvidia/chameleon/issues/134)). One-sentence
  statement of what changed, then why it matters.

  What that catches which the old behaviour did not: a chain whose every
  layer is optional and absent binds nothing at all.

  **What to do.** If `New` starts failing after this upgrade, the schema was
  already invalid.
```

Write for the operator upgrading, not for the reviewer reading the diff:
what changed, what breaks, what to do about it. The existing entries in
`CHANGELOG.md` are the house style.

### Don't write "the entry below"

A fragment does not know where it will land. Order is derived at release
time from the IDs, and it changes as other PRs add fragments around
yours — so **positional cross-references break silently.** Six entries in
the v0.23.0 migration said "the entry below" or "described above"; every
one of them pointed somewhere else after assembly.

Name the thing instead:

```diff
- The entry below stopped consumers *linking* the KMS SDKs.
+ The link-time opt-in entry in this same section stopped consumers *linking* the KMS SDKs.

- the protowire-go v1.4.0 bump below moves the rejection to bind time
+ the protowire-go v1.4.0 bump ([#139](…/issues/139)) moves the rejection to bind time
```

Referring to an earlier paragraph *within your own fragment* is fine —
that text moves as one block.

## Release prose

`_preamble.md` is optional and holds the paragraph that sits between the
version heading and the first `###` — the "Patch release — dependency-only
bump…" summary, as in the v0.22.0 and v0.22.1 sections. It describes the
release rather than one change, so it carries no category and is not
bulleted.

## Assembling

```sh
make changelog-preview VERSION=0.23.0   # print the section, change nothing
make changelog VERSION=0.23.0           # fold into CHANGELOG.md, delete fragments
make changelog-check                    # validate every filename (CI runs this)
```

`make changelog` inserts the assembled section after `## [Unreleased]`,
resets Unreleased to its empty marker, and removes the fragments it
consumed — all of which lands in the release commit. See
[docs/RELEASING.md](../docs/RELEASING.md) for where that sits in the tag
order.

## Skipping

CI requires a PR to add a fragment. When a change genuinely has nothing to
tell a consumer — a refactor with no behaviour change, a CI-only edit, a
typo fix — label the PR **`skip-changelog`** and the check passes. The
label exists so that omitting an entry is a decision someone made, rather
than something everyone forgets.

The release PR wears the label too: it consumes fragments rather than
adding one, so the gate would otherwise block every release. See
[docs/RELEASING.md](../docs/RELEASING.md) step 2.
