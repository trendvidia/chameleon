// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Command chameleon is the chameleon library's companion CLI.
//
// Subcommands:
//
//	chameleon explain --schema=app.proto --message=app.v1.AppConfig \
//	                  --chain=chain.pxf --layers=./layers --env=dev
//	    Resolve a config against the given chain spec + layer dir
//	    and print per-field provenance (which layer set the value,
//	    whether it came from a default, whether interpolation
//	    rewrote it, whether it's sensitive).
//
// More subcommands may follow (`validate`, `diff`); for now `explain`
// is the only one.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "explain":
		if err := explainCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon explain:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, `chameleon — companion CLI for the chameleon layered-config library

Usage:
    chameleon <subcommand> [flags]

Subcommands:
    explain    Resolve a config and print per-field provenance.
    help       Show this message.

Run 'chameleon <subcommand> --help' for subcommand flags.`)
}
