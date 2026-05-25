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
//	chameleon get --schema=app.proto --message=app.v1.AppConfig \
//	              --chain=chain.pxf --layers=./layers --env=dev
//	    Resolve a config and print the result as a PXF document.
//	    Pipe into `pxf query` to extract individual values in
//	    scripts. Same flag surface as `explain`. Secret-typed
//	    fields render as `<redacted>`.
//
//	chameleon reveal --field=paddle.api_key \
//	                 --schema=app.proto --message=app.v1.AppConfig \
//	                 --chain=chain.pxf --layers=./layers --env=dev \
//	                 --decrypter=sops
//	    Print the plaintext of a single Secret-typed field to
//	    stdout (no trailing newline). The deliberate, opt-in,
//	    explicit-naming complement to `get`'s redacted output —
//	    suitable for $(...) command substitution.
//
//	chameleon verify --chain=chain.pxf --layers=./layers --env=dev
//	    Check @header.content_sha256 of every layer the chain spec
//	    resolves to. Encrypted layers are skipped (sops already
//	    authenticates them). Pass --strict to fail on layers without
//	    a hash.
//
//	chameleon migrate --chain=chain.pxf --transform=add-field ...
//	    Walk a chain spec and apply a field-level transform
//	    (add-field / delete-field / rename-field) to every layer.
//
//	chameleon hash <file>
//	    Print the canonical body SHA-256 of a single layer file —
//	    the value to paste into @header.content_sha256.
//
//	chameleon version
//	    Print chameleon's module version, Go toolchain, and commit.
//
//	chameleon completion <bash|zsh|fish>
//	    Print a shell completion script. Source the output in your
//	    shell init file for subcommand + flag completion.
//
//	chameleon lint ./...
//	    Run the go/analysis-based linter that flags reads / writes of
//	    pxfv1.Secret.Value (cleared by chameleon.parse.Move). Pass
//	    --help after `lint` to see all analyzer driver flags.
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
		if err := explainCmd(os.Stdin, os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon explain:", err)
			os.Exit(1)
		}
	case "get":
		if err := getCmd(os.Stdin, os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon get:", err)
			os.Exit(1)
		}
	case "reveal":
		if err := revealCmd(os.Stdin, os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon reveal:", err)
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
	case "version", "--version":
		if err := versionCmd(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon version:", err)
			os.Exit(1)
		}
	case "completion":
		if err := completionCmd(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon completion:", err)
			os.Exit(1)
		}
	case "lint":
		os.Exit(lintCmd(os.Stdout, os.Stderr, os.Args[2:]))
	case "bundle":
		if err := bundleCmd(os.Stdout, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "chameleon bundle:", err)
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
    explain     Resolve a config and print per-field provenance.
    get         Resolve a config and print it as a PXF document.
    reveal      Print the plaintext of a single Secret-typed field.
    verify      Check @header.content_sha256 of every layer in a chain.
    migrate     Bulk-edit layer files in a chain spec (add/rename/delete fields).
    hash        Print the canonical body SHA-256 of a single layer file.
    lint        Flag pxfv1.Secret.Value reads / writes in consumer code.
    bundle      Build a per-module distribution bundle (decrypt → narrow → re-encrypt → zip).
    version     Print chameleon version, Go toolchain, and commit.
    completion  Print shell completion script (bash|zsh|fish).
    help        Show this message.

Run 'chameleon <subcommand> --help' for subcommand flags.`)
}
