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
	"strings"

	"filippo.io/age"
	"github.com/trendvidia/sops/v3/age/keypb"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// keystoreCmd dispatches `chameleon keystore <subcommand>`. The
// dispatcher exists so future helpers (`keystore list`,
// `keystore remove`) drop in without rewriting the entry point.
func keystoreCmd(out io.Writer, args []string) error {
	if len(args) < 1 {
		keystoreUsage(out)
		return errors.New("keystore: subcommand required")
	}
	switch args[0] {
	case "add":
		return keystoreAddCmd(out, args[1:])
	case "-h", "--help", "help":
		keystoreUsage(out)
		return nil
	default:
		keystoreUsage(out)
		return fmt.Errorf("keystore: unknown subcommand %q", args[0])
	}
}

func keystoreUsage(out io.Writer) {
	fmt.Fprintln(out, `chameleon keystore — manage the PXF-formatted SOPS age key file.

Usage:
    chameleon keystore <subcommand> [flags]

Subcommands:
    add         Add a named age key entry to the keystore (generating one if needed).

Run 'chameleon keystore <subcommand> --help' for subcommand flags.`)
}

// keystoreAddCmd implements `chameleon keystore add` — appends a
// new named entry to the PXF-formatted age key file pointed at by
// --file (or $SOPS_AGE_KEY_FILE if unset). Either:
//
//   - Generates a fresh X25519 identity if --identity is unset, OR
//   - Adopts an operator-supplied identity (e.g. one already on a
//     password manager or hardware token, exported as a Bech32
//     `AGE-SECRET-KEY-...` string).
//
// The new entry's recipient is printed to stdout so it can be
// piped into another tool (`chameleon keystore add --name=prod
// | tee prod.recipient`). The identity itself stays inside the
// keystore file — never echoed.
//
// Refuses to overwrite an existing entry unless --force, since
// silently clobbering a key would destroy access to anything
// encrypted under it. Refuses to write outside .pxf to keep the
// sops fork's parser/writer in lockstep with the file format.
func keystoreAddCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("keystore add", flag.ContinueOnError)
	var (
		name     = fs.String("name", "", "entry name in the keystore (required)")
		file     = fs.String("file", "", "keystore file path (defaults to $SOPS_AGE_KEY_FILE)")
		identity = fs.String("identity", "", "age private key (AGE-SECRET-KEY-...); if empty, a fresh X25519 identity is generated")
		setDflt  = fs.Bool("default", false, "set this entry as the keystore's default key")
		force    = fs.Bool("force", false, "overwrite an existing entry with the same name (destroys access to anything encrypted under the previous key)")
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon keystore add — add a named age key entry to the PXF keystore.

Generates a fresh X25519 identity when --identity is unset; otherwise adopts
the supplied identity. The keystore file is created if it doesn't exist.
The new entry's age recipient is printed to stdout (the private identity
stays inside the keystore file).

Examples:
    # Generate a new key named "prod-2026" in the default keystore.
    chameleon keystore add --name=prod-2026

    # Adopt an existing identity, mark it as the file's default.
    chameleon keystore add --name=ops --identity=AGE-SECRET-KEY-1... --default

    # Override the keystore path explicitly.
    chameleon keystore add --name=ci --file=/etc/chameleon/keys.pxf

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *name == "" {
		fs.Usage()
		return errors.New("--name is required")
	}

	path, err := resolveKeystorePath(*file)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(path, keypb.FileExtension) {
		return fmt.Errorf("keystore file %q must have %s extension (sops fork dispatches PXF parser on suffix)", path, keypb.FileExtension)
	}

	id, recipient, err := acquireIdentity(*identity)
	if err != nil {
		return err
	}

	store, err := loadOrInitKeystore(path)
	if err != nil {
		return err
	}
	if _, exists := store.Keys[*name]; exists && !*force {
		return fmt.Errorf("keystore %q already has an entry named %q; pass --force to overwrite (destroys access to anything encrypted under the previous key)", path, *name)
	}
	if store.Keys == nil {
		store.Keys = make(map[string]string)
	}
	store.Keys[*name] = id
	if *setDflt {
		store.Default = *name
	}

	if err := writeKeystoreAtomic(path, store); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", recipient)
	return nil
}

// resolveKeystorePath returns the explicit --file value when set,
// otherwise $SOPS_AGE_KEY_FILE. Errors when neither is available
// — keystore writes need a target path; defaulting silently to
// ~/.config/sops/age/keys.pxf would put a private key in a
// surprise location.
func resolveKeystorePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("SOPS_AGE_KEY_FILE"); env != "" {
		return env, nil
	}
	return "", errors.New("keystore path required: pass --file=PATH or set SOPS_AGE_KEY_FILE")
}

// acquireIdentity returns the (identity, recipient) pair to write.
// If provided is non-empty it's parsed as an existing X25519
// identity; otherwise a fresh one is generated. Hybrid / plugin
// identities (post-quantum, hardware-token-backed) aren't
// supported here yet — the keypb format permits them but their
// recipient can only be derived through the plugin's IPC protocol,
// out of scope for this first-cut helper.
func acquireIdentity(provided string) (idStr, recipient string, err error) {
	if provided != "" {
		id, perr := age.ParseX25519Identity(provided)
		if perr != nil {
			return "", "", fmt.Errorf("parse --identity: %w (only X25519 AGE-SECRET-KEY-... is supported here)", perr)
		}
		return provided, id.Recipient().String(), nil
	}
	id, gerr := age.GenerateX25519Identity()
	if gerr != nil {
		return "", "", fmt.Errorf("generate X25519 identity: %w", gerr)
	}
	return id.String(), id.Recipient().String(), nil
}

// loadOrInitKeystore reads the existing keystore or returns an
// empty one when the file doesn't exist yet. Other read errors
// (malformed PXF, permission denied) surface verbatim so the
// operator notices before the write clobbers the bad state.
func loadOrInitKeystore(path string) (*keypb.AgeKeyFile, error) {
	store, err := keypb.ReadFile(path)
	if err == nil {
		return store, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return &keypb.AgeKeyFile{Keys: map[string]string{}}, nil
	}
	// keypb.ReadFile wraps the underlying os error; unwrap to check
	// for "missing file" through the wrap.
	if strings.Contains(err.Error(), "no such file") {
		return &keypb.AgeKeyFile{Keys: map[string]string{}}, nil
	}
	return nil, err
}

// writeKeystoreAtomic marshals store and writes it to path via a
// temp file + rename so a crash or partial write can't corrupt the
// keystore. 0o600 on the temp file keeps the private keys readable
// only by the running user; the rename inherits those permissions.
func writeKeystoreAtomic(path string, store *keypb.AgeKeyFile) error {
	data, err := pxf.Marshal(store)
	if err != nil {
		return fmt.Errorf("marshal keystore: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".keys-*.pxf.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp file to %q: %w", path, err)
	}
	return nil
}
