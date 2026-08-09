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
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
)

// revealCmd implements `chameleon reveal` — print the plaintext of
// a single Secret-typed field to stdout. Companion to `chameleon
// get`: get redacts secrets; reveal is the explicit, opt-in,
// single-value path for scripts that need plaintext.
//
//	export PADDLE_API_KEY=$(chameleon reveal --field=paddle.api_key \
//	  --schema=app.proto --message=app.v1.AppConfig \
//	  --chain=chain.pxf --layers=./layers --env=dev --decrypter=sops)
//
// Surface follows `get` (same --schema / --message / --chain /
// --layers / --env / --include / --ctx / --decrypter flags) with
// three deltas:
//
//   - --field is required and singular. No tree dumps, no bulk
//     extraction. Forces the caller to name what they're after.
//   - The named field MUST resolve to a `pxf.Secret`-typed path in
//     the schema. Plain scalars route through `chameleon get | pxf
//     query`. We error early on type mismatch with a clearer
//     message than "secret not found."
//   - Stdout is exactly the plaintext bytes — no trailing newline,
//     no metadata, no @type directive. Suitable for $(...)
//     command substitution where a trailing \n would corrupt the
//     captured value.
//
// The plaintext window on the regular Go heap is bounded by the
// write+Destroy pair: Open returns a memguard LockedBuffer, write
// streams its bytes to stdout, Destroy wipes the buffer. The
// upstream PXF-lexer window (immutable Go string between token
// construction and the next GC) is the same one all chameleon
// consumers inherit; closing that requires protowire-go-side work
// and is not scope for this command.
func revealCmd(stdin io.Reader, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("reveal", flag.ContinueOnError)
	var (
		schema    = fs.String("schema", "", ".proto file describing the consumer's config (required)")
		message   = fs.String("message", "", "fully-qualified message name, e.g. myapp.v1.AppConfig (required)")
		chainPath = fs.String("chain", "", "path to chain.pxf (required unless a bundle input is provided)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		env       = fs.String("env", "", "environment to resolve (defaults to chain spec's default_environment)")
		ctxKVs    = fs.String("ctx", "", "comma-separated key=value pairs for ${ctx.*} substitutions, e.g. region=eu,tenant=acme")
		decrypter = fs.String("decrypter", "none", `decrypter for encrypted layers: "none" (error if any encrypted layer is hit) or "sops" (use ambient sops/age/KMS credentials)`)
		field     = fs.String("field", "", "dotted path of the pxf.Secret field to reveal, e.g. paddle.api_key (required)")
		appName   = fs.String("app", "", "named application entry to load defaults from in $HOME/.chameleon; CLI flags override its values")
	)
	bflags := registerBundleFlags(fs)
	var includePaths []string
	fs.Func("include", "directory to search for proto imports (repeatable, like protoc -I)", func(s string) error {
		includePaths = append(includePaths, s)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon reveal — print the plaintext of a single Secret-typed field.

Usage:
    chameleon reveal --field=paddle.api_key \
                     --schema=app.proto --message=app.v1.AppConfig \
                     --chain=chain.pxf --layers=./layers --env=dev \
                     --decrypter=sops

Stdout is exactly the plaintext bytes — no trailing newline, no
metadata. Suitable for $(...) command substitution:

    export FOO=$(chameleon reveal --field=paddle.api_key ...)

For plain (non-Secret) scalars, use chameleon get | pxf query
instead — reveal refuses non-Secret paths to avoid surprising
callers who expected the safety properties of the Secret pipeline.

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	// --field is reveal-specific (no analog in any app default) so
	// applyAppDefaults intentionally doesn't touch it. The user
	// still has to name the secret each invocation — by design.
	userCfg, err := loadUserConfig()
	if err != nil {
		return err
	}
	app, err := selectApp(userCfg, *appName)
	if err != nil {
		return err
	}
	applyAppDefaults(app, schema, message, chainPath, layersDir, env, decrypter, &includePaths)

	if *schema == "" || *message == "" || *field == "" {
		fs.Usage()
		return fmt.Errorf("--schema, --message, and --field are required")
	}
	if !bflags.active() && *chainPath == "" {
		fs.Usage()
		return fmt.Errorf("--chain (or --bundle / --bundle-env) is required")
	}
	if bflags.active() {
		if err := rejectChainLayerConflict(fs); err != nil {
			return err
		}
	}

	desc, err := compileSchema(*schema, *message, includePaths)
	if err != nil {
		return err
	}

	// Validate --field before resolving. Catches typos and non-
	// Secret targets up-front with clear errors rather than
	// surfacing as "secret <field> not set" after the resolver runs.
	if err := assertSecretField(desc, *field); err != nil {
		return err
	}

	var (
		spec    proto.Message
		sources map[string]source.Source
	)
	if bflags.active() {
		b, err := bflags.open(stdin)
		if err != nil {
			return err
		}
		spec = b.Chain
		sources, err = buildBundleSources(spec, desc, b.FS)
		if err != nil {
			return err
		}
	} else {
		spec, err = chain.LoadSpec(*chainPath)
		if err != nil {
			return err
		}
		chainDir, err := filepath.Abs(filepath.Dir(*chainPath))
		if err != nil {
			return fmt.Errorf("resolve chain dir: %w", err)
		}
		sources, err = buildSources(spec, desc, chainDir, *layersDir)
		if err != nil {
			return err
		}
	}

	ctxMap, err := parseCtx(*ctxKVs)
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
	opts = append(opts, decrypterOption(dec))
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

	s := res.Secrets.Get(*field)
	if s == nil {
		return fmt.Errorf("secret %q not set by any layer in env=%q", *field, *env)
	}
	buf, err := s.Open()
	if err != nil {
		return fmt.Errorf("open secret %q: %w", *field, err)
	}
	defer buf.Destroy()

	_, err = out.Write(buf.Bytes())
	return err
}

// assertSecretField walks desc by dotted path and returns nil if
// the path lands on a `pxf.Secret`-typed field. Errors with a
// clear diagnostic if any path segment doesn't exist or if the
// terminal field isn't Secret-typed — the two common operator
// mistakes (typo'd path; targeting a plain scalar). Distinguishing
// here keeps the post-resolve error surface clean: a `nil` return
// from res.Secrets.Get always means "valid Secret path that no
// layer set," never "you spelled the path wrong."
//
// secretFullName is defined in get.go in the same package.
func assertSecretField(desc protoreflect.MessageDescriptor, path string) error {
	if path == "" {
		return fmt.Errorf("--field is empty")
	}
	parts := strings.Split(path, ".")
	cur := desc
	for i, name := range parts {
		fd := cur.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return fmt.Errorf("--field %q: no field %q on message %q",
				path, name, cur.FullName())
		}
		if i == len(parts)-1 {
			if fd.Kind() != protoreflect.MessageKind || fd.Message() == nil ||
				fd.Message().FullName() != secretFullName {
				return fmt.Errorf("--field %q: not a pxf.Secret field "+
					"(use `chameleon get | pxf query` for plain scalars)", path)
			}
			return nil
		}
		// Non-terminal segment: must be a singular message to descend.
		// Repeated and map fields aren't navigable by bare-name path
		// here — pxf query syntax supports `foo[0]` / `foo[\"k\"]`,
		// but this command only handles unambiguous singular paths
		// because each invocation yields exactly one plaintext.
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
			return fmt.Errorf("--field %q: %q is not a singular message — "+
				"reveal only supports dotted paths through singular messages",
				path, name)
		}
		cur = fd.Message()
	}
	// Unreachable: the loop returns at the terminal segment.
	return fmt.Errorf("--field %q: empty path component", path)
}
