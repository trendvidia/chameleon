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
//	chameleon verify --chain=chain.pxf --layers=./layers --env=dev
//	    Check @header.content_sha256 of every layer the chain spec
//	    resolves to. Encrypted layers are skipped (sops already
//	    authenticates them). Pass --strict to fail on layers without
//	    a hash.
//
//	chameleon hash <file>
//	    Print the canonical body SHA-256 of a single layer file —
//	    the value to paste into @header.content_sha256.
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
		if err := explainCmd(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon explain:", err)
			os.Exit(1)
		}
	case "verify":
		if err := verifyCmd(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon verify:", err)
			os.Exit(1)
		}
	case "migrate":
		if err := migrateCmd(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon migrate:", err)
			os.Exit(1)
		}
	case "hash":
		if err := hashCmd(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon hash:", err)
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
    verify     Check @header.content_sha256 of every layer in a chain.
    migrate    Bulk-edit layer files in a chain spec (add/rename/delete fields).
    hash       Print the canonical body SHA-256 of a single layer file.
    help       Show this message.

Run 'chameleon <subcommand> --help' for subcommand flags.`)
}
