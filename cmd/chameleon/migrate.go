// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/getsops/sops/v3/encrypt"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/proto"

	"github.com/trendvidia/chameleon/chain"
	chsops "github.com/trendvidia/chameleon/sops"
)

func migrateCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	var (
		chainPath = fs.String("chain", "", "path to chain.pxf (required)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		env       = fs.String("env", "", "environment to expand (defaults to chain spec's default_environment)")
		ctxKVs    = fs.String("ctx", "", "comma-separated key=value pairs for ${ctx.*} substitutions")
		dryRun    = fs.Bool("dry-run", false, "print per-layer diff without writing or re-encrypting")
		only      = fs.String("only", "", "limit migration to a single LayerEntry id (e.g. base, env)")

		// Transform spec.
		transform = fs.String("transform", "", "transform kind: add-field | delete-field | rename-field")
		tPath     = fs.String("path", "", "dotted field path (add-field, delete-field)")
		tFrom     = fs.String("from", "", "source dotted path (rename-field)")
		tTo       = fs.String("to", "", "destination dotted path (rename-field)")
		tValue    = fs.String("value", "", "string value for add-field")
		tInt      = fs.String("int", "", "integer value for add-field")
		tBool     = fs.String("bool", "", "bool value for add-field (true|false)")
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon migrate — bulk-edit layer files in a chain spec.

Walks the chain spec, decrypts each encrypted layer, applies the
transform at the PXF AST level, re-encrypts using the same key set,
and writes back.

Transforms (dotted paths supported for nested blocks):
    add-field    --path=<dotted> --value|--int|--bool=<v>
    delete-field --path=<dotted>
    rename-field --from=<dotted> --to=<dotted>   (same parent only)

Examples:
    chameleon migrate --chain=chain.pxf --layers=./layers --env=dev \
        --transform=add-field --path=feature_flag --bool=true

    chameleon migrate --chain=chain.pxf --layers=./layers --env=dev \
        --transform=rename-field --from=db.user --to=db.username

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *chainPath == "" {
		fs.Usage()
		return fmt.Errorf("--chain is required")
	}
	t, err := parseTransform(*transform, *tPath, *tFrom, *tTo, *tValue, *tInt, *tBool)
	if err != nil {
		fs.Usage()
		return err
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

	dec := chsops.Default()
	enc := chsops.DefaultEncrypter()
	var (
		changed   int
		skipped   int
		unchanged int
	)
	for _, ref := range refs {
		if *only != "" && ref.EntryID != *only {
			continue
		}
		status, err := migrateOne(context.Background(), out, ref, *layersDir, t, *dryRun, dec, enc, spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR\t%s\t%s\t%v\n", ref.EntryID, ref.Path, err)
			return err
		}
		switch status {
		case migrateChanged:
			changed++
		case migrateUnchanged:
			unchanged++
		case migrateSkipped:
			skipped++
		}
	}
	verb := "migrated"
	if *dryRun {
		verb = "would migrate"
	}
	fmt.Fprintf(out, "summary: %s %d layer(s); unchanged %d; skipped %d\n",
		verb, changed, unchanged, skipped)
	return nil
}

type migrateStatus int

const (
	migrateChanged migrateStatus = iota
	migrateUnchanged
	migrateSkipped
)

// migrateOne loads a single layer, applies the transform, and (unless
// dry-run) writes the result back. Encrypted layers are decrypted via
// sops and re-encrypted under the env's configured key (resolved via
// chain.EffectiveKeyName against the chain spec); envs without a
// key_name fall back to preserving the existing ciphertext's key set.
func migrateOne(ctx context.Context, w io.Writer, ref chain.LayerRef, layersDir string, t transform, dryRun bool, dec chsops.Decrypter, enc chsops.Encrypter, spec proto.Message) (migrateStatus, error) {
	tag := fmt.Sprintf("%s\t%s", ref.EntryID, ref.Path)

	if ref.Source != "" && ref.Source != "default" {
		fmt.Fprintf(w, "SKIP\t%s\t(non-default source %q not supported)\n", tag, ref.Source)
		return migrateSkipped, nil
	}

	full := filepath.Join(layersDir, ref.Path)
	raw, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) && !ref.Required {
			fmt.Fprintf(w, "SKIP\t%s\t(optional file missing)\n", tag)
			return migrateSkipped, nil
		}
		return migrateSkipped, fmt.Errorf("read %q: %w", full, err)
	}

	// Decrypt if encrypted; otherwise treat raw as PXF.
	var plain []byte
	if ref.Encrypted {
		// chsops.Decrypter wipes the input ciphertext after a successful
		// decrypt — security feature: bytes on disk → memory → wipe.
		// migrate needs the original ciphertext later (as the reference
		// for UsingSameKeysAs, which extracts metadata from it). Pass a
		// disposable copy so raw stays intact.
		rawForDec := append([]byte{}, raw...)
		buf, err := dec.Decrypt(ctx, rawForDec, chsops.FormatProtowire)
		if err != nil {
			return migrateSkipped, fmt.Errorf("decrypt %q: %w", full, err)
		}
		// Copy out of memguard; we need the bytes alive through parse +
		// format + re-encrypt. Destroy the locked buffer when done.
		plain = append([]byte{}, buf.Bytes()...)
		buf.Destroy()
		defer wipeBytes(plain)
	} else {
		plain = raw
	}

	doc, err := pxf.Parse(plain)
	if err != nil {
		return migrateSkipped, fmt.Errorf("parse %q: %w", full, err)
	}
	changed, err := t.apply(doc)
	if err != nil {
		return migrateSkipped, fmt.Errorf("transform %q: %w", full, err)
	}
	if !changed {
		fmt.Fprintf(w, "UNCHANGED\t%s\t(%s already satisfied)\n", tag, t.describe())
		return migrateUnchanged, nil
	}
	newPlain := pxf.FormatDocument(doc)

	if dryRun {
		fmt.Fprintf(w, "WOULD-CHANGE\t%s\t(%s)\n", tag, t.describe())
		// Compact diff: show the chars that differ. For now just print
		// the new file size; a fuller diff lands in a follow-up.
		fmt.Fprintf(w, "  before: %d bytes\n  after:  %d bytes\n", len(plain), len(newPlain))
		return migrateChanged, nil
	}

	// Write back.
	var newBytes []byte
	if ref.Encrypted {
		var keyName string
		if spec != nil {
			n, err := chain.EffectiveKeyName(spec, ref.Env)
			if err != nil {
				return migrateSkipped, fmt.Errorf("resolve key_name for env %q: %w", ref.Env, err)
			}
			keyName = n
		}
		if keyName != "" {
			recipients, err := chsops.RecipientsFromKeyName(keyName)
			if err != nil {
				return migrateSkipped, fmt.Errorf("re-encrypt %q: %w", full, err)
			}
			ciph, err := enc.Encrypt(ctx, newPlain, chsops.FormatProtowire, recipients)
			if err != nil {
				return migrateSkipped, fmt.Errorf("re-encrypt %q: %w", full, err)
			}
			newBytes = ciph
		} else {
			// No env.key_name set — preserve the existing ciphertext's
			// key set so chain specs that haven't migrated to named
			// keys still round-trip cleanly.
			ciph, err := encrypt.UsingSameKeysAsWithContext(ctx, newPlain, raw, "protowire")
			if err != nil {
				return migrateSkipped, fmt.Errorf("re-encrypt %q: %w", full, err)
			}
			newBytes = ciph
		}
	} else {
		newBytes = newPlain
	}
	// 0o644 is conventional for layer files; encrypted bytes are
	// ciphertext (sops-protected), plaintext layers are not secret.
	if err := os.WriteFile(full, newBytes, 0o644); err != nil { //nolint:gosec // see comment above
		return migrateSkipped, fmt.Errorf("write %q: %w", full, err)
	}
	fmt.Fprintf(w, "CHANGED\t%s\t(%s)\n", tag, t.describe())
	return migrateChanged, nil
}

// wipeBytes overwrites the slice with zeros. Best-effort; the Go
// compiler may elide it if it proves the slice unreachable. Used as a
// last-step hygiene after the plaintext copy of an encrypted layer's
// bytes is no longer needed.
func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// memguardWriter shim reused from chameleon/sops/decrypt.go is not
// imported here (it's internal). The migrate command works with []byte
// because:
//   - Decryption is per-layer and bounded; brief plaintext residency
//     on the regular heap during the migrate run is acceptable for a
//     CLI tool (vs. a long-running service).
//   - The bytes are wiped via wipeBytes() once the migrate-one
//     iteration completes.
//
// If migrate ever runs in a context where in-memory residency matters
// more (e.g. as part of a long-running daemon), the same memguard
// adapter chameleon/sops uses could be lifted into this package.
var _ = bytes.NewReader // keep the bytes import if a future commit uses it
