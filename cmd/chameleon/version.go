// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"
)

func versionCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon version — print chameleon's module version, Go toolchain, and commit.

Usage:
    chameleon version`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(out, "chameleon: build info unavailable (built without module info)")
		return nil
	}

	version := bi.Main.Version
	if version == "" || version == "(devel)" {
		version = "(devel)"
	}

	var vcsRev, vcsTime, vcsModified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			vcsRev = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			vcsModified = s.Value
		}
	}

	fmt.Fprintf(out, "chameleon %s\n", version)
	fmt.Fprintf(out, "  go:        %s\n", bi.GoVersion)
	if vcsRev != "" {
		dirty := ""
		if vcsModified == "true" {
			dirty = " (dirty)"
		}
		fmt.Fprintf(out, "  commit:    %s%s\n", vcsRev, dirty)
	}
	if vcsTime != "" {
		fmt.Fprintf(out, "  built:     %s\n", vcsTime)
	}
	return nil
}
