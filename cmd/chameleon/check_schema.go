// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// errSchemaViolations is returned when the schema carries at least one
// PXF bind-time violation. The violations themselves are printed to
// the command's output; this only carries the count, so main's
// "chameleon check-schema: …" line stays a summary rather than
// repeating the list.
var errSchemaViolations = errors.New("schema is not PXF-conformant")

// checkSchemaCmd implements `chameleon check-schema`. It is the only
// subcommand that asks a question about a schema alone: every other
// one that takes --schema also needs a chain, layers and an
// environment, because it is on its way to resolving something.
//
// The bind-time rules are a property of a file closure — reserved
// names, (pxf.key) / (pxf.default) / (pxf.required) placement — so
// they can be answered without any of that. resolver.New answers the
// same question for a program that constructs a Resolver
// (chameleon#134); this answers it for a repo that only authors
// schemas and hands them to someone else, which is where the
// import-closure widening in protowire-go#71 bites hardest: the file
// that breaks a downstream build is often not one that downstream
// owns.
//
// --schema is repeatable and takes a directory, because that repo owns
// many .proto files and "is every file I own conformant?" is the
// question it actually has (chameleon#153). Enumerating them in a
// shell loop answers a different one N times: it recompiles the shared
// closure per file and reports a violation in a shared import once per
// importing root.
func checkSchemaCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("check-schema", flag.ContinueOnError)
	message := fs.String("message", "", "optional: also assert this message exists, e.g. myapp.v1.AppConfig")
	var schemas []string
	fs.Func("schema", ".proto file, or directory to search for .proto files (repeatable; required)", func(s string) error {
		// An empty value is not "no value": it resolves to the working
		// directory, which is a directory, which would now be searched.
		// `--schema=$PROTO_DIR` with PROTO_DIR unset has to stay the
		// error it was before directories were accepted, rather than a
		// green run over every .proto in the tree.
		if s == "" {
			return errors.New("empty path")
		}
		schemas = append(schemas, s)
		return nil
	})
	var excludes []string
	fs.Func("exclude", "skip files and directories whose NAME matches this pattern while searching a --schema directory (repeatable)", func(s string) error {
		if s == "" {
			return errors.New("empty pattern")
		}
		// A pattern is tested against one element name at a time, and an
		// element name never holds a separator, so a pattern that does
		// can only ever match nothing — refused here rather than left to
		// prune nothing quietly. os.IsPathSeparator rather than a
		// comparison against filepath.Separator, because the platforms
		// diverge in both directions: Windows takes `/` as a separator
		// too, and everywhere else `\` is [filepath.Match]'s escape
		// character, so `a\*b` — a literal star — has to stay legal.
		for i := range len(s) {
			if os.IsPathSeparator(s[i]) {
				return fmt.Errorf("matches one path element, so it cannot contain %q", s[i:i+1])
			}
		}
		// Syntax is deliberately NOT checked here. filepath.Match only
		// reports ErrBadPattern once its scan reaches the bad syntax,
		// and it stops scanning at the first chunk that fails the name,
		// so no probe name settles the question: Match("vendor*[0-9",
		// "x") is (false, nil) because "vendor" already failed. The
		// verdict comes from the match site instead — see [matchesAny].
		excludes = append(excludes, s)
		return nil
	})
	var includePaths []string
	fs.Func("include", "directory to search for proto imports (repeatable, like protoc -I)", func(s string) error {
		// Same reason: an empty --include is the working directory,
		// which silently becomes an import root and renames every root
		// underneath it.
		if s == "" {
			return errors.New("empty path")
		}
		includePaths = append(includePaths, s)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon check-schema — check .proto files against the PXF bind-time rules.

Reports every violation in the schemas' transitive import closure:
PXF-reserved names, and invalid (pxf.key) / (pxf.default) /
(pxf.required) placement. No chain, no layers and no environment are
needed — the rules depend on the descriptor alone, so one run covers
every environment at once.

The closure is the point: a misplaced annotation in a shared
common.proto fails every schema that imports it, whether or not the
importing message refers to the offending type. Run this in the repo
that OWNS the shared schema, not only in the ones that consume it.

--schema is repeatable and also takes a directory, which is what a
schema-only repo's CI wants: every root is compiled once, together, so
a violation in a shared import is reported once rather than once per
importing file.

A directory is searched recursively for *.proto, following symlinks —
both linked directories and linked files. A subtree symlinked into
place is part of the tree you named, so it is checked; the OK line
counts what was reached only that way. A file reachable both directly
and through a link is checked once, and a link pointing at an ancestor
stops rather than looping. Note that a glob is NOT a shorter spelling
of this: no shell expands
`+"`"+`--schema=proto/*.proto`+"`"+`, because the pattern has to match the whole
word, flag name included — bash passes it through literally, zsh and
fish abort. Pass the directory, or have your script build the repeated
flags.

--exclude skips files and directories by NAME while searching. It
matches one path element at a time, so --exclude=third_party prunes
any directory of that name at whatever depth, and needs no `+"`"+`**`+"`"+`; the
usual pattern syntax works within an element (--exclude='*_test.proto').
For the same reason a pattern cannot contain a path separator. Nothing
is excluded unless you ask, and what a pattern pruned is counted on the
OK line — a well-formed pattern can take more than you meant, and the
run is green either way.

Two things --exclude does NOT do. It does not touch --include, so an
excluded file stays importable — which is the point for a vendored
tree that the files you own legitimately import. And it does not apply
to a .proto named directly by --schema, nor to the --schema directory
itself: what you name explicitly is checked.

--message is optional. It does not change what is checked — the rules
are per file closure, not per message — it only additionally asserts
that the named message exists somewhere in the closure the roots span,
which is worth having in CI when the schema and the consumer's message
name are meant to move together.

Usage:
    chameleon check-schema --schema=config.proto
    chameleon check-schema --schema=config.proto --message=myapp.v1.AppConfig --include=./proto
    chameleon check-schema --schema=./proto --include=./proto
    chameleon check-schema --schema=./proto --include=./proto --exclude=third_party
    chameleon check-schema --schema=a.proto --schema=b.proto

Exit status is 0 when clean, 1 when a schema has violations or cannot
be compiled.

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(schemas) == 0 {
		fs.Usage()
		return errors.New("--schema is required")
	}
	// The schemas are named by --schema, so a positional argument is
	// always a mistake — and never a harmless one: flag parsing stops
	// at the first non-flag argument, so `--schema=a.proto b.proto
	// --include=./lib` silently drops the --include and then fails the
	// compile on an import it could have resolved. Repeatable --schema
	// makes that shape more tempting, not less: it is exactly what a
	// shell glob produces if the flag is written with a space.
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q (schemas are named by --schema, one flag each)", fs.Arg(0))
	}

	search, err := resolveSchemaRoots(schemas, includePaths, excludes)
	if err != nil {
		return err
	}
	roots := search.roots
	files, err := compileRoots(roots, search.importPaths)
	if err != nil {
		return err
	}

	report := checkRoots(roots, files)
	for _, r := range report.violations {
		fmt.Fprintln(out, r.line(len(roots) > 1))
	}

	// --message is an additional assertion, not a narrowing, so it is
	// checked after the violation report — a typo'd message name must
	// not swallow the command's primary output — and before the success
	// line, so the typo is not buried under it either. It is asked once
	// of the union closure rather than once per root: the question is
	// "does this message exist in what I just checked", and answering
	// it per root would fail every root that does not happen to reach
	// the file declaring it.
	if *message != "" {
		if _, err := findMessageAcross(files, *message); err != nil {
			return err
		}
	}

	if n := len(report.violations); n > 0 {
		return fmt.Errorf("%w: %d violation(s)", errSchemaViolations, n)
	}
	// The closure size is worth printing rather than a bare "OK": the
	// whole reason this command exists is that the checked set is
	// larger than the files named, so the count is what tells an author
	// how much of it this run actually spoke for. A directory search
	// prints both numbers, because they answer different questions —
	// how much was asked, and how much was covered — and the second
	// alone cannot say whether --include is wrong.
	//
	// Which shape is printed follows what --schema named, not how many
	// roots came out of it: see [schemaSearch.named].
	if search.named != "" {
		fmt.Fprintf(out, "OK\t%s\t%d file(s) checked\n", search.named, report.closure)
		return nil
	}
	// Both trailing fields appear only when they have something to
	// report, so a run with neither --exclude nor a symlink prints the
	// line it always did. Their absence is not a claim about the tree,
	// only that this run pruned nothing and crossed nothing.
	extra := ""
	if search.skipped > 0 {
		extra += fmt.Sprintf("\t%d skipped", search.skipped)
	}
	if search.linked > 0 {
		extra += fmt.Sprintf("\t%d linked", search.linked)
	}
	fmt.Fprintf(out, "OK\t%d root(s)\t%d file(s) checked%s\n", len(roots), report.closure, extra)
	return nil
}

// schemaSearch is what the --schema / --include / --exclude values
// resolved to: the files to compile, the import paths to compile them
// against, and the two facts the OK line needs that the root list
// alone cannot supply.
type schemaSearch struct {
	roots       []schemaRoot
	importPaths []string

	// skipped counts the .proto files --exclude pruned out of the
	// directory searches. Reported rather than merely applied: a
	// well-formed pattern can prune more than the operator meant —
	// `--exclude=v*` takes v1/ along with vendor/ — and the run is
	// green either way, so without this the only signal is a file
	// count nobody has a baseline for (chameleon#166).
	skipped int

	// linked counts the .proto files the directory searches reached by
	// crossing a symlink. The walk following links at all is what
	// chameleon#167 changed; before it, those files were silently not
	// checked, and a count is what makes the new behaviour visible in a
	// tree where the links are not obvious.
	linked int

	// named is the one --schema value when exactly one was given and
	// it named a file, and "" otherwise. It decides the OK line's
	// shape. Deliberately NOT "how many roots survived": exclusion can
	// prune a directory search down to one root, and letting that flip
	// the output to the single-file form means a CI job matching
	// `N root(s)` stops matching exactly when a pattern over-prunes.
	// What the operator asked for does not move under it.
	named string
}

// schemaRoot is one .proto file this run was asked to check, as
// opposed to one it merely reached through an import.
type schemaRoot struct {
	// path is the absolute path on disk.
	path string
	// name is what protocompile compiles it under, and therefore what
	// violations in it are reported as. See [resolveSchemaRoots].
	name string
	// arg is the --schema value it came from — the file itself, or the
	// directory it was found under. Diagnostics about the run as a
	// whole quote this rather than name, because it is what the
	// operator typed; one that has to distinguish two files found under
	// the same argument quotes path instead.
	arg string
}

// resolveSchemaRoots turns the --schema values into the concrete list
// of files to compile, plus the import paths to compile them against.
//
// A value that names a directory is searched recursively for *.proto;
// a value that names a regular file is taken as-is. Symlinks are not
// followed, so a subtree symlinked into place is not searched.
//
// excludes prune that search by element name — see [protoFilesUnder].
// They apply to the walk only: a file named directly by --schema is
// checked whatever the patterns say, because naming it is the more
// specific instruction, and the import paths are untouched, so an
// excluded file remains resolvable as somebody else's import. That
// combination is what makes a vendored duplicate tractable
// (chameleon#159): the second copy of a shared file stops being a root
// — which is what made protocompile fail on duplicate symbols — while
// staying importable by the files that legitimately import it.
//
// Glob expansion is deliberately NOT implemented here: `**` means
// different things in bash and fish and nothing in sh, so a glob this
// command expanded itself would disagree with the same string typed at
// a prompt. No shell does it for you either — pathname expansion
// matches the whole word, so `--schema=proto/*.proto` matches nothing
// in bash (and is passed through literally) and aborts in zsh and
// fish. The directory form is what replaces the glob; repeated flags
// are for a script that builds them.
//
// Naming rules, which decide what violations are reported as:
//
//   - a file lying under an --include path is named relative to it,
//     longest match first. --include is the operator stating the
//     protoc -I contract — "imports around here are written relative
//     to this" — so it outranks the directory the file was found
//     under. --schema=./proto/svc --include=./proto names a file
//     "svc/a.proto", not "a.proto", because that is how its siblings
//     import it. Naming it "a.proto" would make protocompile compile
//     it twice, once as a root and once as a sibling's import, and
//     the run would die on duplicate symbols rather than report
//     anything.
//   - otherwise, a file found under a directory --schema is named
//     relative to that directory, which is also added as an import
//     path.
//   - otherwise — a file named directly by --schema and lying under no
//     import root — it is named by its basename, with its own
//     directory added as an import path. This is what the single-file
//     form has always done, and it is also what a lone --schema=a.proto
//     keeps regardless of the rules above: with one root nothing else
//     is compiled to collide with it.
func resolveSchemaRoots(schemas, includePaths, excludes []string) (schemaSearch, error) {
	// candidate is a file some --schema value named, before duplicates
	// are folded together and before it is given its compile name.
	type candidate struct {
		path string // absolute
		arg  string
		dir  string // absolute directory --schema it was found under, "" if named directly
	}

	// Import paths are assembled in protoc's search order at the end:
	// the directories of files named directly first (so a basename
	// resolves), then directories handed to --schema, then --include.
	pruned := map[string]bool{}
	linked := 0
	var (
		cands          []candidate
		seenPath       = map[string]bool{}
		fileDirs       []string
		dirImports     []string
		includeImports []string
	)
	// The same file named twice — `--schema=proto --schema=proto/a.proto`,
	// or a repeated flag — is one root, not two, and has to be folded
	// together before anything downstream counts candidates: whether
	// there is one root or several is what decides how roots are named.
	// First mention wins, so the name follows the argument order the
	// operator wrote.
	addCand := func(path, arg, dir string) {
		if seenPath[path] {
			return
		}
		seenPath[path] = true
		cands = append(cands, candidate{path: path, arg: arg, dir: dir})
	}
	for _, arg := range schemas {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return schemaSearch{}, fmt.Errorf("resolve --schema %q: %w", arg, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return schemaSearch{}, fmt.Errorf("read schema %s: %w", arg, err)
		}
		if st.IsDir() {
			found, err := protoFilesUnder(abs, excludes)
			if err != nil {
				return schemaSearch{}, fmt.Errorf("search --schema %q: %w", arg, err)
			}
			if len(found.paths) == 0 {
				return schemaSearch{}, emptySearchError(arg, abs, excludes)
			}
			linked += found.linked
			// What the patterns took out of this directory. The
			// unfiltered walk is redone rather than counted during the
			// first, because pruning a directory with fs.SkipDir is
			// what makes the filtered walk cheap and it cannot report
			// what it never descended into. Only paid when --exclude
			// was passed, and it lists paths rather than compiling
			// anything.
			//
			// Paths, not a count: a file this walk pruned may still be
			// named directly by another --schema, and then it is
			// checked and must not be reported as skipped. Resolved
			// against the final root set below, once every argument
			// has been seen.
			if len(excludes) > 0 {
				all, err := protoFilesUnder(abs, nil)
				if err != nil {
					return schemaSearch{}, fmt.Errorf("search --schema %q: %w", arg, err)
				}
				kept := make(map[string]bool, len(found.paths))
				for _, f := range found.paths {
					kept[f] = true
				}
				for _, f := range all.paths {
					if !kept[f] {
						pruned[f] = true
					}
				}
			}
			dirImports = appendUnique(dirImports, abs)
			for _, f := range found.paths {
				addCand(f, arg, abs)
			}
			continue
		}
		// Regular file, or something that is neither — a directory is
		// handled above, so anything left that is not a regular file
		// (a device, a dangling symlink target) would reach
		// protocompile as "read <x>: is a directory"-shaped noise.
		if !st.Mode().IsRegular() {
			return schemaSearch{}, fmt.Errorf("read schema %s: not a regular file", arg)
		}
		addCand(abs, arg, "")
	}
	for _, p := range includePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return schemaSearch{}, fmt.Errorf("resolve --include %q: %w", p, err)
		}
		includeImports = appendUnique(includeImports, abs)
	}

	// Longest match first, so a file under both ./proto and
	// ./proto/myapp is named relative to the latter — the same rule
	// protoc applies when several -I paths match. Kept in two lists
	// because an --include outranks a directory --schema whatever the
	// lengths are; see the naming rules above.
	longestFirst := func(a, b string) int { return len(b) - len(a) }
	includePrefixes := slices.Clone(includeImports)
	slices.SortFunc(includePrefixes, longestFirst)
	dirPrefixes := slices.Clone(dirImports)
	slices.SortFunc(dirPrefixes, longestFirst)

	multi := len(cands) > 1
	var (
		roots  []schemaRoot
		byName = map[string]string{} // compile name -> the absolute path holding it
	)
	for _, c := range cands {
		var prefix string
		switch {
		case c.dir != "":
			if prefix = firstContaining(includePrefixes, c.path); prefix == "" {
				prefix = c.dir
			}
		case multi:
			if prefix = firstContaining(includePrefixes, c.path); prefix == "" {
				prefix = firstContaining(dirPrefixes, c.path)
			}
		}

		var name string
		if prefix != "" {
			rel, err := filepath.Rel(prefix, c.path)
			if err != nil {
				return schemaSearch{}, fmt.Errorf("name %s relative to %s: %w", c.path, prefix, err)
			}
			name = filepath.ToSlash(rel)
		} else {
			name = filepath.Base(c.path)
			// The file's own directory has to be searchable for the
			// basename to resolve.
			fileDirs = appendUnique(fileDirs, filepath.Dir(c.path))
		}

		if prev, clash := byName[name]; clash {
			return schemaSearch{}, fmt.Errorf(
				"%s and %s both compile as %q: pass --include=<common root> so they can be told apart",
				prev, c.path, name)
		}
		byName[name] = c.path
		roots = append(roots, schemaRoot{path: c.path, name: name, arg: c.arg})
	}

	// Deduplicated across all three lists, not just within each: a
	// directory is commonly both the --schema and the --include, which
	// would otherwise have protocompile search it twice for every
	// import it cannot resolve.
	var importPaths []string
	for _, p := range slices.Concat(fileDirs, dirImports, includeImports) {
		importPaths = appendUnique(importPaths, p)
	}

	// The single-file form is exactly one --schema naming a file. Not
	// "one root": a directory search that exclusion narrowed to one
	// root was still a directory search, and saying so is the point of
	// #166.
	named := ""
	if len(schemas) == 1 && len(dirImports) == 0 {
		named = schemas[0]
	}

	// A pruned file that another --schema named directly is checked, so
	// it is not skipped — the count is what the run did not look at,
	// not what the patterns matched.
	skipped := 0
	for path := range pruned {
		if !seenPath[path] {
			skipped++
		}
	}
	return schemaSearch{roots: roots, importPaths: importPaths, skipped: skipped, linked: linked, named: named}, nil
}

// emptySearchError explains a directory --schema that produced no
// roots. Blaming the directory is right when it holds no .proto files
// at all, and wrong once --exclude has pruned every one of them: the
// operator reads "no .proto files found under this directory", looks
// at a directory plainly full of them, and debugs the path instead of
// the pattern. That is the same misdiagnosis [protoFilesUnder] refuses
// to create for the walk root, and an over-broad pattern reaches it by
// the other door. The unfiltered walk is redone rather than counted
// during the first one, because this runs at most once, on the way out
// of a run that has already failed.
func emptySearchError(arg, abs string, excludes []string) error {
	if len(excludes) > 0 {
		if all, err := protoFilesUnder(abs, nil); err == nil && len(all.paths) > 0 {
			return fmt.Errorf("--schema %q: --exclude pruned all %d .proto file(s) under this directory", arg, len(all.paths))
		}
	}
	return fmt.Errorf("--schema %q: no .proto files found under this directory", arg)
}

// appendUnique appends p to dst unless it is already there. The lists
// it guards are import roots — a handful, typically one — so a linear
// scan beats carrying a parallel set, and it keeps the order the
// caller built.
func appendUnique(dst []string, p string) []string {
	if slices.Contains(dst, p) {
		return dst
	}
	return append(dst, p)
}

// firstContaining returns the first directory in dirs that contains
// path, or "" if none does. dirs is ordered longest first by the
// caller, so the most specific import root wins.
func firstContaining(dirs []string, path string) string {
	for _, d := range dirs {
		if under(d, path) {
			return d
		}
	}
	return ""
}

// protoFiles is the result of searching one --schema directory.
type protoFiles struct {
	// paths are the .proto files found, as walked — through the
	// symlink rather than resolved past it, because that is the name
	// the operator wrote and the one its siblings import it by.
	paths []string
	// linked counts how many of them were reachable ONLY by crossing a
	// symlink — that is, how many the search would have missed before
	// #167. A file reachable both ways is not counted, so the number
	// does not move with whether a link's name sorts before its
	// target's. Reported on the OK line, where it is the signal that
	// the walk left the tree the operator named.
	linked int
}

// protoFilesUnder returns every *.proto file under dir, recursively, in
// lexical order (os.ReadDir sorts, so a run's output does not depend on
// directory iteration order).
//
// Symlinks are followed, directories and files alike (chameleon#167).
// [filepath.WalkDir] does not, which meant a subtree symlinked into
// place — a normal monorepo layout, and this command exists for the
// shared-schema repo most likely to have one — was silently skipped,
// with the file count on the OK line the only hint and no baseline to
// read it against.
//
// Following them brings two obligations the non-following walk did not
// have, both handled here rather than left to protocompile:
//
//   - Cycles. A link pointing at an ancestor would recurse forever, so
//     each directory is entered once, keyed by its resolved path.
//   - One file, one name. A file reachable both directly and through a
//     link would otherwise be compiled twice under two names, which is
//     the duplicate-symbol failure the naming rules in
//     [resolveSchemaRoots] exist to prevent. Files are deduplicated by
//     resolved path, first walked path winning.
//
// A dangling link names nothing and is skipped; it is a broken tree,
// but not one this command can say anything useful about.
//
// excludes are [filepath.Match] patterns tested against one path
// element at a time — a directory's or a file's own name, never a path
// with separators in it. Matching per element is what makes a subtree
// exclusion writable without a `**`: "third_party" prunes any
// directory of that name at any depth, because the walk stops there
// rather than because the pattern spans the rest of the path. Within
// an element the usual Match syntax applies, so "*_test.proto" skips
// those files wherever they are, and a malformed one stops the walk
// instead of quietly matching nothing — see [matchesAny] for why that
// verdict can only be reached here. A pattern is tested against the
// link's own name, not its target's: the name is what the operator
// sees in the tree.
//
// dir itself is never tested. Excluding the directory the operator
// just named is a contradiction, and reading it as one would turn
// `--schema=./third_party --exclude=third_party` into "no .proto files
// found" rather than into the obvious intent.
func protoFilesUnder(dir string, excludes []string) (protoFiles, error) {
	w := &protoWalk{
		excludes: excludes,
		dirs:     map[string]bool{},
		files:    map[string]bool{},
	}
	if err := w.walk(dir, false); err != nil {
		return protoFiles{}, err
	}
	return protoFiles{paths: w.found, linked: w.linked}, nil
}

// protoWalk carries the state a symlink-following search needs and a
// [filepath.WalkDir] callback cannot hold: which real directories have
// been entered, and which real files have already been collected.
type protoWalk struct {
	excludes []string
	dirs     map[string]bool // resolved directories entered, the cycle guard
	files    map[string]bool // resolved files collected, the one-name guard
	found    []string
	linked   int
}

// walk reads one directory and recurses. viaLink says whether getting
// here already crossed a symlink, so a file below a linked directory
// counts as linked without the leaf having to be one itself.
func (w *protoWalk) walk(dir string, viaLink bool) error {
	// Resolved rather than cleaned, because the cycle guard below has
	// to recognise two paths that are the same directory. A failure
	// here is real and reported: a dangling link never reaches this
	// point — the symlink branch below Stats before recursing — so
	// what is left is an unreadable or vanished directory, and
	// swallowing it would surface as "no .proto files found under this
	// directory", sending the operator to check the path they typed
	// rather than the one that broke.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if w.dirs[real] {
		return nil
	}
	w.dirs[real] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// Two passes, real entries before symlinks, so a file reachable
	// both ways is attributed to the path that does not cross a link.
	// One pass in name order would make it depend on whether the link
	// happens to sort before its target — `alias` does, `real` does not
	// — and `linked` is only worth printing if it means "files the
	// search would not have found before #167".
	//
	// For a tree with no symlinks the first pass is the whole walk, in
	// exactly the order os.ReadDir gives, so nothing about the existing
	// behaviour moves.
	for _, e := range entries {
		if e.Type()&iofs.ModeSymlink != 0 {
			continue
		}
		skip, err := matchesAny(w.excludes, e.Name())
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if err := w.walk(path, viaLink); err != nil {
				return err
			}
			continue
		}
		w.add(path, viaLink)
	}
	for _, e := range entries {
		if e.Type()&iofs.ModeSymlink == 0 {
			continue
		}
		skip, err := matchesAny(w.excludes, e.Name())
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// Lstat said "symlink"; Stat follows it and says what it
		// actually is. Both cases are wanted — decision 3 of #167.
		st, err := os.Stat(path)
		if err != nil {
			continue // dangling
		}
		switch {
		case st.IsDir():
			if err := w.walk(path, true); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			w.add(path, true)
		}
	}
	return nil
}

// add collects path if it is a .proto the search has not already
// reached by another route.
func (w *protoWalk) add(path string, viaLink bool) {
	if !strings.HasSuffix(path, ".proto") {
		return
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return
	}
	if w.files[real] {
		return
	}
	w.files[real] = true
	w.found = append(w.found, path)
	if viaLink {
		w.linked++
	}
}

// matchesAny reports whether name matches any of the patterns, and is
// also where a malformed pattern is caught, because it is the only
// place that can be: [filepath.Match] reports ErrBadPattern once its
// scan reaches the bad syntax, and it stops scanning at the first
// chunk that fails the name, so the same pattern is well-formed or not
// depending on what it is tested against — Match("vendor*[0-9", "x")
// is (false, nil) while Match("vendor*[0-9", "vendor") is
// ErrBadPattern. No probe at flag-parse time settles that.
//
// The error is returned rather than read as "no match": swallowing it
// is how an operator's typo becomes a tree checked unpruned under a
// green OK line, the silent no-op --exclude already refuses for a
// pattern with a separator in it. It surfaces wherever it could have
// mattered, because Match reaches bad syntax only once the chunks
// before it have matched — a pattern whose leading chunk matches no
// name in the tree would have pruned nothing however it was completed,
// and one that does match reaches the bad chunk and fails the run.
//
// Every pattern is tried even after one has matched, so an earlier
// pattern claiming a name cannot hide a later one's syntax error.
func matchesAny(patterns []string, name string) (bool, error) {
	matched := false
	for _, p := range patterns {
		ok, err := filepath.Match(p, name)
		if err != nil {
			return false, fmt.Errorf("--exclude %q: %w", p, err)
		}
		matched = matched || ok
	}
	return matched, nil
}

// under reports whether path is strictly inside dir. Both must be
// absolute and cleaned. Prefix comparison alone would call /a/bc a
// child of /a/b; equality is excluded as well, because an import root
// is a directory and a file is never inside itself — an --include that
// names a .proto file by mistake would otherwise become that file's
// naming prefix and compile it under the name ".".
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// compileRoots compiles every root in one pass, so a shared import is
// parsed and linked once no matter how many roots reach it — the cost
// the shell-loop workaround pays per file. The returned slice is
// aligned with roots.
func compileRoots(roots []schemaRoot, importPaths []string) (linker.Files, error) {
	names := make([]string, len(roots))
	for i, r := range roots {
		names[i] = r.name
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{ImportPaths: importPaths},
		),
	}
	files, err := comp.Compile(context.Background(), names...)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", compileErrorSubject(roots), err)
	}
	if len(files) != len(roots) {
		return nil, fmt.Errorf("compile %s: got %d file(s) for %d root(s)",
			compileErrorSubject(roots), len(files), len(roots))
	}
	return files, nil
}

// compileErrorSubject names what failed to compile: the one root's own
// argument when there is one, or a count when there are many — the
// underlying protocompile error already names the offending file, so
// listing every root here would bury it.
func compileErrorSubject(roots []schemaRoot) string {
	if len(roots) == 1 {
		return roots[0].arg
	}
	return fmt.Sprintf("%d schema root(s)", len(roots))
}

// rootedViolation is one violation plus the roots whose closure
// reaches it. Both halves are printed: the violation is what to fix,
// and the roots are its blast radius.
type rootedViolation struct {
	v     pxf.Violation
	roots []string
}

func (r rootedViolation) line(annotate bool) string {
	s := r.v.String()
	if !annotate {
		return s
	}
	// A root that is itself the offending file is dropped from the
	// list: it is already named at the head of the line, and "via
	// itself" reads as though something else were meant. When that
	// leaves nobody, the violation is purely the root's own and needs
	// no annotation at all.
	via := make([]string, 0, len(r.roots))
	for _, root := range r.roots {
		if root != r.v.File {
			via = append(via, root)
		}
	}
	if len(via) == 0 {
		return s
	}
	return s + "\t(via " + strings.Join(via, ", ") + ")"
}

type schemaReport struct {
	// violations are deduplicated across roots and ordered the way
	// [pxf.ValidateFile] orders one root's — by declaring file, then by
	// element. Each one's roots are in the order the operator named
	// them.
	violations []rootedViolation
	// closure is the number of distinct files the roots span, imports
	// included and counted once.
	closure int
}

// checkRoots validates every root and folds the results together.
//
// Deduplication is the reason this command exists in a multi-root
// form: three schemas importing one bad common.proto is one thing to
// fix, and printing it three times makes the report's length a
// function of the import graph rather than of the damage. The roots
// that reach it are kept and printed instead, which is the part the
// repetition was actually carrying.
func checkRoots(roots []schemaRoot, files linker.Files) schemaReport {
	var (
		rep  schemaReport
		seen = map[pxf.Violation]int{} // violation -> index in rep.violations
		fset = map[string]struct{}{}
	)
	for i, r := range roots {
		fd := files[i]
		collectClosure(fd, fset)
		for _, v := range pxf.ValidateFile(fd) {
			idx, dup := seen[v]
			if !dup {
				seen[v] = len(rep.violations)
				rep.violations = append(rep.violations, rootedViolation{v: v, roots: []string{r.name}})
				continue
			}
			// Same violation from another root. Roots are visited in
			// order and each appends at most once, so no dedup is
			// needed here.
			rep.violations[idx].roots = append(rep.violations[idx].roots, r.name)
		}
	}
	// pxf.ValidateFile documents its result as sorted by declaring file
	// and then by element, and the single-root form inherited that.
	// Folding several roots together loses it: first-sighting order is
	// a function of which root happened to reach a file first, which is
	// the property this report exists to keep out of the output.
	// Stable, so the element order ValidateFile already fixed within
	// one file survives.
	slices.SortStableFunc(rep.violations, func(a, b rootedViolation) int {
		return cmp.Or(
			strings.Compare(a.v.File, b.v.File),
			strings.Compare(a.v.Element, b.v.Element),
		)
	})
	rep.closure = len(fset)
	return rep
}

// collectClosure adds every distinct file in fd's transitive import
// closure, fd included, to seen. Deduplicated by path, for the same
// reason upstream's walk is: A importing B and C, both importing D,
// must not count D twice — and across roots, two schemas sharing an
// import must not count it twice either.
func collectClosure(fd protoreflect.FileDescriptor, seen map[string]struct{}) {
	var walk func(protoreflect.FileDescriptor)
	walk = func(f protoreflect.FileDescriptor) {
		// Placeholders are skipped for the same reason upstream's walk
		// skips them: one stands in for an import that did not resolve,
		// declares nothing and imports nothing. Counting it would claim
		// a file ValidateFile never inspected.
		if f == nil || f.IsPlaceholder() {
			return
		}
		if _, dup := seen[f.Path()]; dup {
			return
		}
		seen[f.Path()] = struct{}{}
		imports := f.Imports()
		for i, n := 0, imports.Len(); i < n; i++ {
			walk(imports.Get(i).FileDescriptor)
		}
	}
	walk(fd)
}

// findMessageAcross looks fullName up in the closure of every root,
// stopping at the first hit. [findMessage] asks one file's resolver,
// which is the right question when there is one schema; with several
// roots the message need only exist somewhere in what was checked.
func findMessageAcross(files linker.Files, fullName string) (protoreflect.MessageDescriptor, error) {
	var firstErr error
	for _, f := range files {
		d, err := linker.ResolverFromFile(f).FindDescriptorByName(protoreflect.FullName(fullName))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		md, ok := d.(protoreflect.MessageDescriptor)
		if !ok {
			return nil, fmt.Errorf("%q is not a message", fullName)
		}
		return md, nil
	}
	return nil, fmt.Errorf("find message %q: %w", fullName, firstErr)
}
