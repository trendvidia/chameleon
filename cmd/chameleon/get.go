// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// getCmd implements `chameleon get` — resolve a config against the
// given chain spec + layer dir and print the full resolved message
// as a PXF document. Suitable for piping into `pxf query` to extract
// individual values in scripts:
//
//	chameleon get --schema=app.proto --message=app.v1.AppConfig \
//	              --chain=chain.pxf --layers=./layers --env=dev \
//	  | pxf query 'server.addr'
//
// Surface mirrors `chameleon explain` except: no provenance trace, no
// --field filter (pxf query owns dotted-path extraction), and the
// output is the marshalled resolved message rather than a per-field
// rendering.
//
// Secret fields surface with empty `value` and intact `hint` /
// `fingerprint` metadata — chameleon.parse.Move clears plaintext
// from the proto message into a memguard-backed secret.Map *before*
// the resolver hands the message back, so pxf.Marshal can never
// observe the plaintext on the message. Scripts that need secret
// plaintext should use a dedicated, explicitly-named API rather
// than this command.
func getCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	var (
		schema    = fs.String("schema", "", ".proto file describing the consumer's config (required)")
		message   = fs.String("message", "", "fully-qualified message name, e.g. myapp.v1.AppConfig (required)")
		chainPath = fs.String("chain", "", "path to chain.pxf (required)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		env       = fs.String("env", "", "environment to resolve (defaults to chain spec's default_environment)")
		ctxKVs    = fs.String("ctx", "", "comma-separated key=value pairs for ${ctx.*} substitutions, e.g. region=eu,tenant=acme")
		decrypter = fs.String("decrypter", "none", `decrypter for encrypted layers: "none" (error if any encrypted layer is hit) or "sops" (use ambient sops/age/KMS credentials)`)
		// emitType controls whether the output starts with a `@type
		// <fully-qualified-name>\n\n` directive. Default on because
		// pxf query uses @type for descriptor resolution and a self-
		// describing document is the more useful default for the
		// scripting case. --no-emit-type for callers that want a
		// bare message body (e.g. concatenating multiple gets, or
		// piping into a tool that supplies its own @type).
		emitType = fs.Bool("emit-type", true, "prefix output with `@type <message>` directive")
	)
	var includePaths []string
	fs.Func("include", "directory to search for proto imports (repeatable, like protoc -I)", func(s string) error {
		includePaths = append(includePaths, s)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon get — resolve a config and print it as a PXF document.

Usage:
    chameleon get --schema=app.proto --message=app.v1.AppConfig \
                  --chain=chain.pxf --layers=./layers --env=dev

Pipe into pxf query to extract individual values:

    chameleon get ... | pxf query 'server.addr'

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *schema == "" || *message == "" || *chainPath == "" {
		fs.Usage()
		return fmt.Errorf("--schema, --message, and --chain are required")
	}

	desc, err := compileSchema(*schema, *message, includePaths)
	if err != nil {
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

	chainDir, err := filepath.Abs(filepath.Dir(*chainPath))
	if err != nil {
		return fmt.Errorf("resolve chain dir: %w", err)
	}
	sources, err := buildSources(spec, chainDir, *layersDir)
	if err != nil {
		return err
	}

	dec, err := selectDecrypter(*decrypter)
	if err != nil {
		return err
	}

	sample := dynamicpb.NewMessage(desc)
	opts := make([]resolver.Option, 0, len(sources)+1)
	for name, src := range sources {
		opts = append(opts, resolver.WithSource(name, src))
	}
	if dec != nil {
		opts = append(opts, resolver.WithDecrypter(dec))
	}
	r, err := resolver.New[*dynamicpb.Message](sample, spec, opts...)
	if err != nil {
		return err
	}
	res, err := r.Resolve(context.Background(), resolver.Request{
		Environment:         *env,
		Context:             ctxMap,
		AllowUnknownContext: true,
	})
	if err != nil {
		return err
	}
	defer res.Destroy()

	mo := pxf.MarshalOptions{}
	if *emitType {
		mo.TypeURL = *message
	}
	body, err := mo.Marshal(res.Config)
	if err != nil {
		return fmt.Errorf("marshal pxf: %w", err)
	}
	_, err = out.Write(body)
	return err
}
