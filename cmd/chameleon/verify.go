// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/internal/header"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func verifyCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	var (
		chainPath = fs.String("chain", "", "path to chain.pxf (required)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		env       = fs.String("env", "", "environment to expand (defaults to chain spec's default_environment)")
		ctxKVs    = fs.String("ctx", "", "comma-separated key=value pairs for ${ctx.*} substitutions, e.g. region=eu,tenant=acme")
		strict    = fs.Bool("strict", false, "fail if any layer is missing @header.content_sha256 (default: such layers are skipped)")
		appName   = fs.String("app", "", "named application entry to load chain/layers/env defaults from in $HOME/.chameleon; CLI flags override its values")
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon verify — check @header.content_sha256 of every layer in a chain.

Expands the chain spec against --env / --ctx, opens each resolved
layer file, and compares the file's body SHA-256 against the value in
the @header.content_sha256 field. The "body" is the bytes after the
closing } of the @header preamble (or the whole file if no header).

Encrypted layers are skipped — sops already authenticates their
ciphertext, and decryption is out of scope for this command.

Usage:
    chameleon verify --chain=chain.pxf --layers=./layers --env=dev

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// Verify only needs the chain/layers/env subset of the app
	// surface; schema/message/include/decrypter aren't read here.
	// Inline the three-field merge rather than threading nil
	// pointers through applyAppDefaults.
	userCfg, err := loadUserConfig()
	if err != nil {
		return err
	}
	app, err := selectApp(userCfg, *appName)
	if err != nil {
		return err
	}
	if app != nil {
		if *chainPath == "" {
			*chainPath = resolveAppPath(app, app.GetChain())
		}
		if *layersDir == "" || *layersDir == "." {
			if app.GetLayers() != "" {
				*layersDir = resolveAppPath(app, app.GetLayers())
			}
		}
		if *env == "" {
			*env = app.GetEnv()
		}
	}

	if *chainPath == "" {
		fs.Usage()
		return fmt.Errorf("--chain is required")
	}

	spec, err := chain.LoadSpec(*chainPath)
	if err != nil {
		return err
	}
	ctxMap, err := parseCtx(*ctxKVs)
	if err != nil {
		return err
	}
	refs, err := chain.Expand(spec, *env, ctxMap)
	if err != nil {
		return err
	}

	var mismatched, missingHash int
	for _, ref := range refs {
		status := verifyOne(out, ref, *layersDir, *strict)
		switch status {
		case statusMismatch:
			mismatched++
		case statusMissingHash:
			missingHash++
		}
	}
	if mismatched > 0 {
		return fmt.Errorf("%d layer(s) failed content_sha256 verification", mismatched)
	}
	if *strict && missingHash > 0 {
		return fmt.Errorf("--strict: %d layer(s) missing @header.content_sha256", missingHash)
	}
	return nil
}

type verifyStatus int

const (
	statusOK verifyStatus = iota
	statusMismatch
	statusMissingHash
	statusSkippedMissing
	statusSkippedEncrypted
	statusSkippedNonDefault
	statusSkippedNoHeader
	statusError
)

// verifyOne prints a one-line result for a single layer ref and
// returns the status code for caller aggregation.
func verifyOne(w io.Writer, ref chain.LayerRef, layersDir string, strict bool) verifyStatus {
	tag := fmt.Sprintf("%s\t%s", ref.EntryID, ref.Path)

	if ref.Encrypted {
		fmt.Fprintf(w, "SKIP\t%s\t(encrypted; sops authenticates)\n", tag)
		return statusSkippedEncrypted
	}
	if ref.Source != "" && ref.Source != "default" {
		fmt.Fprintf(w, "SKIP\t%s\t(non-default source %q not supported)\n", tag, ref.Source)
		return statusSkippedNonDefault
	}

	full := filepath.Join(layersDir, ref.Path)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) && !ref.Required {
			fmt.Fprintf(w, "SKIP\t%s\t(optional file missing)\n", tag)
			return statusSkippedMissing
		}
		fmt.Fprintf(w, "ERROR\t%s\t%v\n", tag, err)
		return statusError
	}

	doc, err := pxf.Parse(data)
	if err != nil {
		fmt.Fprintf(w, "ERROR\t%s\tparse: %v\n", tag, err)
		return statusError
	}
	var match *pxf.Directive
	for i := range doc.Directives {
		if doc.Directives[i].Name == header.DirectiveName {
			match = &doc.Directives[i]
			break
		}
	}
	if match == nil {
		level := "SKIP"
		if strict {
			level = "MISS"
		}
		fmt.Fprintf(w, "%s\t%s\t(no @header)\n", level, tag)
		return statusMissingHash
	}
	parsed, err := header.Parse(match.Body)
	if err != nil {
		fmt.Fprintf(w, "ERROR\t%s\t@header parse: %v\n", tag, err)
		return statusError
	}
	want, ok := parsed.ContentSHA256()
	if !ok {
		level := "SKIP"
		if strict {
			level = "MISS"
		}
		fmt.Fprintf(w, "%s\t%s\t(no content_sha256)\n", level, tag)
		return statusMissingHash
	}
	got, err := header.BodyHash(data)
	if err != nil {
		fmt.Fprintf(w, "ERROR\t%s\t%v\n", tag, err)
		return statusError
	}
	if got != want {
		fmt.Fprintf(w, "FAIL\t%s\twant=%s got=%s\n", tag, want, got)
		return statusMismatch
	}
	fmt.Fprintf(w, "OK\t%s\n", tag)
	return statusOK
}

func hashCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("hash", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon hash <file> — print the canonical body SHA-256 of a layer file.

Output is the value an author should paste into the file's
@header.content_sha256 field. Hashes the bytes after the closing } of
the @header preamble (or the whole file if no header is present), so
adding or editing the @header itself does not change the hash.

Usage:
    chameleon hash base.pxf`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one file argument required")
	}
	path := fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	got, err := header.BodyHash(data)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, got)
	return nil
}
