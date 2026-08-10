// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// ErrSchemaViolations is returned when the schema carries at least one
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
func checkSchemaCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("check-schema", flag.ContinueOnError)
	var (
		schema  = fs.String("schema", "", ".proto file to check (required)")
		message = fs.String("message", "", "optional: also assert this message exists, e.g. myapp.v1.AppConfig")
	)
	var includePaths []string
	fs.Func("include", "directory to search for proto imports (repeatable, like protoc -I)", func(s string) error {
		includePaths = append(includePaths, s)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon check-schema — check a .proto against the PXF bind-time rules.

Reports every violation in the schema's transitive import closure:
PXF-reserved names, and invalid (pxf.key) / (pxf.default) /
(pxf.required) placement. No chain, no layers and no environment are
needed — the rules depend on the descriptor alone, so one run covers
every environment at once.

The closure is the point: a misplaced annotation in a shared
common.proto fails every schema that imports it, whether or not the
importing message refers to the offending type. Run this in the repo
that OWNS the shared schema, not only in the ones that consume it.

--message is optional. It does not change what is checked — the rules
are per file closure, not per message — it only additionally asserts
that the named message exists, which is worth having in CI when the
schema and the consumer's message name are meant to move together.

Usage:
    chameleon check-schema --schema=config.proto
    chameleon check-schema --schema=config.proto --message=myapp.v1.AppConfig --include=./proto

Exit status is 0 when clean, 1 when the schema has violations or
cannot be compiled.

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *schema == "" {
		fs.Usage()
		return errors.New("--schema is required")
	}

	files, err := compileSchemaFiles(*schema, includePaths)
	if err != nil {
		return err
	}
	// files[0] is the schema's own file; ValidateFile walks it plus its
	// transitive imports.
	fd := files[0]

	// --message is checked before the violations are reported, so a
	// typo'd message name is not buried under a conformant-schema
	// success line.
	if *message != "" {
		d, err := files.AsResolver().FindDescriptorByName(protoreflect.FullName(*message))
		if err != nil {
			return fmt.Errorf("find message %q: %w", *message, err)
		}
		if _, ok := d.(protoreflect.MessageDescriptor); !ok {
			return fmt.Errorf("%q is not a message", *message)
		}
	}

	violations := pxf.ValidateFile(fd)
	if len(violations) == 0 {
		// The closure size is worth printing rather than a bare "OK":
		// the whole reason this command exists is that the checked set
		// is larger than the file named, and a "1 file" result on a
		// schema the author believes imports something is the cheapest
		// possible signal that --include is wrong.
		fmt.Fprintf(out, "OK\t%s\t%d file(s) checked\n", *schema, closureSize(fd))
		return nil
	}
	for _, v := range violations {
		fmt.Fprintln(out, v.String())
	}
	return fmt.Errorf("%w: %d violation(s)", errSchemaViolations, len(violations))
}

// closureSize counts the distinct files in fd's transitive import
// closure, fd included. Deduplicated by path, for the same reason
// upstream's walk is: A importing B and C, both importing D, must not
// count D twice.
func closureSize(fd protoreflect.FileDescriptor) int {
	seen := map[string]struct{}{}
	var walk func(protoreflect.FileDescriptor)
	walk = func(f protoreflect.FileDescriptor) {
		if f == nil {
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
	return len(seen)
}
