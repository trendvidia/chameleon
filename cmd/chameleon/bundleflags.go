// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/trendvidia/chameleon/bundle"
)

// bundleFlags collects the three "where does the bundle come from"
// flags plus the --base64 toggle. Shared by reveal / get / explain
// so the bundle-input surface is identical across all three
// resolution-style subcommands.
//
// The three input forms are mutually exclusive:
//
//   - `--bundle=PATH`     read from a file at PATH
//   - `--bundle=-`        read from stdin
//   - `--bundle-env=VAR`  read from the env var named VAR
//
// `--bundle-env` reads the variable by NAME, not by shell-expanded
// value. The `--bundle="$VAR"` shape leaks the bundle bytes into
// the process command line (visible via `ps`, /proc/$pid/cmdline),
// which is bad for material that may contain ciphertext metadata
// downstream attackers can correlate. `--bundle-env=VAR` only
// surfaces the variable's name on the command line; the bytes are
// read in-process.
type bundleFlags struct {
	path   *string
	env    *string
	base64 *bool
}

// registerBundleFlags wires the bundle-input flags onto fs and
// returns a handle the subcommand uses to resolve the configured
// source at runtime.
func registerBundleFlags(fs *flag.FlagSet) *bundleFlags {
	return &bundleFlags{
		path:   fs.String("bundle", "", `path to a chameleon distribution bundle, or "-" for stdin (mutually exclusive with --bundle-env, --chain, --layers)`),
		env:    fs.String("bundle-env", "", "name of an env var holding the bundle bytes (mutually exclusive with --bundle, --chain, --layers)"),
		base64: fs.Bool("base64", false, "treat the bundle input as base64-encoded (matches `chameleon bundle build --base64` output)"),
	}
}

// active reports whether any bundle-input flag was set. Callers use
// this to choose between the bundle path and the legacy
// --chain/--layers path.
func (b *bundleFlags) active() bool {
	return b != nil && (*b.path != "" || *b.env != "")
}

// open resolves the configured bundle source and returns the opened
// Bundle. Returns (nil, nil) when no bundle flag was set so callers
// can fall through to --chain / --layers.
//
// stdin is the reader to use when the source is `--bundle=-`; main
// passes os.Stdin, tests pass a buffer.
func (b *bundleFlags) open(stdin io.Reader) (*bundle.Bundle, error) {
	if !b.active() {
		return nil, nil
	}
	if *b.path != "" && *b.env != "" {
		return nil, errors.New("--bundle and --bundle-env are mutually exclusive")
	}

	reader, cleanup, err := b.reader(stdin)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if *b.base64 {
		return bundle.OpenBase64(reader)
	}
	return bundle.Open(reader)
}

// reader returns the io.Reader for the configured source and a
// cleanup closure (e.g. file Close) the caller must invoke after
// the open completes. Factored out so the file-handle cleanup
// stays right next to the file open, even though bundle.Open reads
// synchronously.
func (b *bundleFlags) reader(stdin io.Reader) (io.Reader, func(), error) {
	switch {
	case *b.path == "-":
		return stdin, func() {}, nil
	case *b.path != "":
		f, err := os.Open(*b.path)
		if err != nil {
			return nil, nil, fmt.Errorf("open bundle %s: %w", *b.path, err)
		}
		return f, func() { _ = f.Close() }, nil
	case *b.env != "":
		val, ok := os.LookupEnv(*b.env)
		if !ok {
			return nil, nil, fmt.Errorf("env var %q is not set", *b.env)
		}
		if val == "" {
			return nil, nil, fmt.Errorf("env var %q is empty", *b.env)
		}
		return strings.NewReader(val), func() {}, nil
	}
	// active() guarded this earlier; reaching here is a programmer error.
	return nil, nil, errors.New("bundleflags: no source configured")
}

// rejectChainLayerConflict errors if --chain or --layers was
// explicitly set alongside a bundle-input flag. Falling defaults
// (--layers's "." default for example) don't count — only explicit
// user choices, via flag.Visit.
//
// Conflating bundle inputs with on-disk inputs is asking for
// surprise (which chain wins? which layers FS gets used?); the
// cleanest behavior is to fail loud at flag-parse time.
func rejectChainLayerConflict(fs *flag.FlagSet) error {
	var explicit []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "chain" || f.Name == "layers" {
			explicit = append(explicit, f.Name)
		}
	})
	if len(explicit) == 0 {
		return nil
	}
	return fmt.Errorf("--%s cannot be combined with bundle inputs (--bundle / --bundle-env)",
		strings.Join(explicit, ", --"))
}
