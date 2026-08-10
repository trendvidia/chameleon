// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
		schemas = append(schemas, s)
		return nil
	})
	var includePaths []string
	fs.Func("include", "directory to search for proto imports (repeatable, like protoc -I)", func(s string) error {
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
importing file. A directory is searched recursively for *.proto. Glob
expansion is left to your shell — `+"`"+`--schema=proto/*.proto`+"`"+` works
because the shell expands it into repeated flags.

--message is optional. It does not change what is checked — the rules
are per file closure, not per message — it only additionally asserts
that the named message exists somewhere in the closure the roots span,
which is worth having in CI when the schema and the consumer's message
name are meant to move together.

Usage:
    chameleon check-schema --schema=config.proto
    chameleon check-schema --schema=config.proto --message=myapp.v1.AppConfig --include=./proto
    chameleon check-schema --schema=./proto --include=./proto
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

	roots, importPaths, err := resolveSchemaRoots(schemas, includePaths)
	if err != nil {
		return err
	}
	files, err := compileRoots(roots, importPaths)
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
	// how much of it this run actually spoke for. With several roots
	// both numbers are printed, because they answer different
	// questions — how much was asked, and how much was covered — and
	// the second alone cannot say whether --include is wrong.
	if len(roots) == 1 {
		fmt.Fprintf(out, "OK\t%s\t%d file(s) checked\n", roots[0].arg, report.closure)
		return nil
	}
	fmt.Fprintf(out, "OK\t%d root(s)\t%d file(s) checked\n", len(roots), report.closure)
	return nil
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
	// directory it was found under. Diagnostics quote this rather than
	// name, because it is what the operator typed.
	arg string
}

// resolveSchemaRoots turns the --schema values into the concrete list
// of files to compile, plus the import paths to compile them against.
//
// A value that names a directory is searched recursively for *.proto;
// a value that names a regular file is taken as-is. Glob expansion is
// deliberately NOT implemented here: `**` means different things in
// bash and fish and nothing in sh, so a glob this command expanded
// itself would disagree with the same string typed at a prompt. A
// shell that expands `proto/*.proto` hands back repeated --schema
// flags, which this already accepts.
//
// Naming rules, which decide what violations are reported as:
//
//   - a file found under a directory --schema is named relative to
//     that directory, which is also added as an import path — the
//     protoc -I contract, and what makes a file's name here match how
//     its siblings import it.
//   - a file named directly by --schema is named by its basename, with
//     its own directory added as an import path. This is what the
//     single-file form has always done.
//   - EXCEPT when there are several roots and the file lies under a
//     directory --schema or an --include path: then the first rule
//     wins, longest prefix first. Two roots naming the same file
//     differently is not cosmetic — protocompile would compile it
//     twice, once as a root and once as the other root's import, and
//     report its violations under both names. That cannot happen with
//     one root (nothing else is compiled to collide with it), which is
//     why the single-file form keeps its unqualified name.
func resolveSchemaRoots(schemas, includePaths []string) ([]schemaRoot, []string, error) {
	type candidate struct {
		path   string // absolute
		arg    string
		prefix string // absolute dir the name is taken relative to, "" for basename
	}

	// Import roots contributed by directory --schema values and by
	// --include, longest first so the naming rule below can prefer the
	// most specific one.
	var prefixes []string
	addPrefix := func(p string) {
		if !slices.Contains(prefixes, p) {
			prefixes = append(prefixes, p)
		}
	}

	// Import paths are assembled in protoc's search order at the end:
	// the directories of files named directly first (so a basename
	// resolves), then directories handed to --schema, then --include.
	var (
		cands          []candidate
		fileDirs       []string
		dirImports     []string
		includeImports []string
	)
	for _, arg := range schemas {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve --schema %q: %w", arg, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, nil, fmt.Errorf("read schema %s: %w", arg, err)
		}
		if st.IsDir() {
			found, err := protoFilesUnder(abs)
			if err != nil {
				return nil, nil, fmt.Errorf("search --schema %q: %w", arg, err)
			}
			if len(found) == 0 {
				return nil, nil, fmt.Errorf("--schema %q: no .proto files found under this directory", arg)
			}
			addPrefix(abs)
			if !slices.Contains(dirImports, abs) {
				dirImports = append(dirImports, abs)
			}
			for _, f := range found {
				cands = append(cands, candidate{path: f, arg: arg, prefix: abs})
			}
			continue
		}
		// Regular file, or something that is neither — a directory is
		// handled above, so anything left that is not a regular file
		// (a device, a dangling symlink target) would reach
		// protocompile as "read <x>: is a directory"-shaped noise.
		if !st.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("read schema %s: not a regular file", arg)
		}
		cands = append(cands, candidate{path: abs, arg: arg})
	}
	for _, p := range includePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve --include %q: %w", p, err)
		}
		addPrefix(abs)
		if !slices.Contains(includeImports, abs) {
			includeImports = append(includeImports, abs)
		}
	}

	// Longest prefix wins, so a file under both ./proto and
	// ./proto/myapp is named relative to the latter — the same rule
	// protoc applies when several -I paths match.
	longest := append([]string(nil), prefixes...)
	sort.Slice(longest, func(i, j int) bool { return len(longest[i]) > len(longest[j]) })

	multi := len(cands) > 1
	var (
		roots   []schemaRoot
		byPath  = map[string]int{}
		byName  = map[string]schemaRoot{}
		dirSeen = map[string]bool{}
	)
	for _, c := range cands {
		prefix := c.prefix
		if prefix == "" && multi {
			for _, p := range longest {
				if under(p, c.path) {
					prefix = p
					break
				}
			}
		}
		var name string
		if prefix != "" {
			rel, err := filepath.Rel(prefix, c.path)
			if err != nil {
				return nil, nil, fmt.Errorf("name %s relative to %s: %w", c.path, prefix, err)
			}
			name = filepath.ToSlash(rel)
		} else {
			name = filepath.Base(c.path)
			// The file's own directory has to be searchable for the
			// basename to resolve.
			if dir := filepath.Dir(c.path); !dirSeen[dir] {
				dirSeen[dir] = true
				fileDirs = append(fileDirs, dir)
			}
		}

		// The same file named twice — `--schema=proto --schema=proto/a.proto`,
		// or a repeated flag — is one root, not two. First mention wins,
		// so the name is a function of the argument order the operator
		// wrote rather than of map iteration.
		if _, dup := byPath[c.path]; dup {
			continue
		}
		if prev, clash := byName[name]; clash {
			return nil, nil, fmt.Errorf(
				"--schema %q and %q both compile as %q: pass --include=<common root> so they can be told apart",
				prev.path, c.path, name)
		}
		r := schemaRoot{path: c.path, name: name, arg: c.arg}
		byPath[c.path] = len(roots)
		byName[name] = r
		roots = append(roots, r)
	}

	importPaths := make([]string, 0, len(fileDirs)+len(dirImports)+len(includeImports))
	importPaths = append(importPaths, fileDirs...)
	importPaths = append(importPaths, dirImports...)
	importPaths = append(importPaths, includeImports...)
	return roots, importPaths, nil
}

// protoFilesUnder returns every *.proto file under dir, recursively,
// in lexical order (what WalkDir already guarantees) so a run's output
// does not depend on directory iteration order.
func protoFilesUnder(dir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(dir, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		found = append(found, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// under reports whether path is inside dir. Both must be absolute and
// cleaned. Prefix comparison alone would call /a/bc a child of /a/b.
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	// violations are deduplicated across roots and ordered by first
	// sighting, roots in the order the operator named them.
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
