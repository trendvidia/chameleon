// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/proto"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/internal/bundler"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/chameleon/sops"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// bundleCmd dispatches `chameleon bundle <subcommand>`. Currently the
// only subcommand is `build`; the wrapper exists so future
// additions (e.g. `bundle verify`, `bundle inspect`) drop in
// without rewriting the dispatch.
func bundleCmd(out io.Writer, args []string) error {
	if len(args) < 1 {
		bundleUsage(out)
		return errors.New("bundle: subcommand required")
	}
	switch args[0] {
	case "build":
		return bundleBuildCmd(out, args[1:])
	case "-h", "--help", "help":
		bundleUsage(out)
		return nil
	default:
		bundleUsage(out)
		return fmt.Errorf("bundle: unknown subcommand %q", args[0])
	}
}

func bundleUsage(out io.Writer) {
	fmt.Fprintln(out, `chameleon bundle — per-module distribution bundles.

Usage:
    chameleon bundle <subcommand> [flags]

Subcommands:
    build       Decrypt → narrow → re-encrypt → zip a chain's layers for one module.

Run 'chameleon bundle <subcommand> --help' for subcommand flags.`)
}

// bundleBuildCmd implements `chameleon bundle build` — the CLI shell
// around bundler.Build. Resolves the schema and chain spec the same
// way the other resolution subcommands (explain / get / reveal) do,
// reads the BundleManifest from PXF, and streams the resulting zip
// to --out (or stdout if --out=- / unset). With --base64, the output
// is base64-encoded so it can be pasted into a secret-manager
// console field or env var verbatim.
func bundleBuildCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("bundle build", flag.ContinueOnError)
	var (
		manifest  = fs.String("manifest", "", "path to a BundleManifest PXF file (required)")
		module    = fs.String("module", "", "module name — selects which entry in the manifest applies (required)")
		schema    = fs.String("schema", "", ".proto file describing the consumer's config (required)")
		message   = fs.String("message", "", "fully-qualified message name, e.g. myapp.v1.AppConfig (required)")
		chainPath = fs.String("chain", "", "path to chain.pxf (required)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		decrypter = fs.String("decrypter", "none", `decrypter for encrypted layers: "none" (error if any encrypted layer is hit) or "sops" (use ambient sops/age/KMS credentials)`)
		outPath   = fs.String("out", "-", `output path for the zip bundle; "-" writes to stdout`)
		b64       = fs.Bool("base64", false, "base64-encode the zip on the way out — paste-friendly for secret-manager fields")
		appName   = fs.String("app", "", "named application entry to load defaults from in $HOME/.chameleon; CLI flags override its values")
	)
	var includePaths []string
	fs.Func("include", "directory to search for proto imports (repeatable, like protoc -I)", func(s string) error {
		includePaths = append(includePaths, s)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon bundle build — produce a per-module distribution bundle.

Usage:
    chameleon bundle build \
        --manifest=modules.pxf --module=billing-worker \
        --schema=app.proto --message=app.v1.AppConfig \
        --chain=chain.pxf --layers=./layers \
        --decrypter=sops \
        --out=billing-worker.bundle

Pipe to a clipboard or secret-manager CLI with --base64:

    chameleon bundle build ... --base64 \
        | gcloud secrets versions add billing-worker-bundle --data-file=-

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	userCfg, err := loadUserConfig()
	if err != nil {
		return err
	}
	app, err := selectApp(userCfg, *appName)
	if err != nil {
		return err
	}
	// Reuse the same defaults applier the resolution commands use —
	// schema / message / chain / layers / decrypter / include are the
	// ones the user config carries. --manifest, --module, --out, and
	// --base64 are bundle-specific and stay CLI-only.
	envFallback := "" // bundle is env-agnostic; no --env to apply.
	applyAppDefaults(app, schema, message, chainPath, layersDir, &envFallback, decrypter, &includePaths)

	if *manifest == "" || *module == "" || *schema == "" || *message == "" || *chainPath == "" {
		fs.Usage()
		return fmt.Errorf("--manifest, --module, --schema, --message, and --chain are required")
	}

	manifestMsg, err := loadManifest(*manifest)
	if err != nil {
		return err
	}

	schemaDesc, err := compileSchema(*schema, *message, includePaths)
	if err != nil {
		return err
	}

	chainSpec, err := chain.LoadSpec(*chainPath)
	if err != nil {
		return err
	}

	layersAbs, err := filepath.Abs(*layersDir)
	if err != nil {
		return fmt.Errorf("resolve layers dir: %w", err)
	}
	layersFS := os.DirFS(layersAbs)

	dec, err := selectDecrypter(*decrypter)
	if err != nil {
		return err
	}

	// Buffer the bundle in memory so we control the write boundary —
	// writing directly to the final Writer means a half-built zip on
	// any post-Build failure, which is uglier than the small extra
	// allocation. Bundles are KB-to-low-MB; memory cost is fine.
	var buf bytes.Buffer
	if err := bundler.Build(context.Background(), bundler.BuildOptions{
		Manifest:     manifestMsg,
		Module:       *module,
		Schema:       schemaDesc,
		ChainSpec:    chainSpec,
		LayersFS:     layersFS,
		Decrypter:    dec,
		Encrypter:    sops.DefaultEncrypter(),
		Output:       &buf,
		ManifestName: filepath.Base(*manifest),
	}); err != nil {
		return err
	}

	sink, closeSink, err := openOutput(out, *outPath)
	if err != nil {
		return err
	}
	defer closeSink()

	if *b64 {
		enc := base64.NewEncoder(base64.StdEncoding, sink)
		if _, err := io.Copy(enc, &buf); err != nil {
			return fmt.Errorf("write base64 output: %w", err)
		}
		// The base64 encoder buffers up to three bytes of pad; Close
		// flushes the trailing group. Errors here MUST be surfaced
		// (truncated output is a silent data-loss bug).
		if err := enc.Close(); err != nil {
			return fmt.Errorf("flush base64 output: %w", err)
		}
		return nil
	}
	_, err = io.Copy(sink, &buf)
	return err
}

// openOutput resolves the --out flag into a writer + a close func.
// "-" routes to the command's `out` writer (typically stdout) with
// a no-op close. Any other value opens the named file with
// O_EXCL — we refuse to overwrite an existing bundle because the
// CLI doesn't know whether the previous file is in use by another
// process or pinned by a release tag.
func openOutput(out io.Writer, path string) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return out, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

// loadManifest reads a BundleManifest PXF file from disk. Sibling to
// chain.LoadSpec; lives here because it's the only consumer at the
// moment and pulling it into internal/bundler would create a
// proto-load surface the library doesn't otherwise need.
func loadManifest(path string) (proto.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	msg := &chameleonv1.BundleManifest{}
	if err := pxf.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return msg, nil
}
