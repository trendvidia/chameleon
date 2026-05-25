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

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// secretFullName is the canonical FullName of the pxf.Secret well-
// known type. Mirrors the same constant in parse/move.go; kept
// duplicated here rather than exported because (a) `get` is the
// only caller in cmd/chameleon and (b) the parse package is the
// authority on the type and shouldn't grow a public surface to
// service a sibling CLI command's redaction needs.
const secretFullName protoreflect.FullName = "pxf.Secret"

// redactedSentinel is the placeholder written into pxf.Secret.value
// for marshalled output. Indicates "deliberately suppressed —
// plaintext lives in res.Secrets, not on this message," so the
// reader doesn't confuse a redacted secret with an absent or
// empty-string scalar. Surrounding `<...>` chosen so it can't
// collide with a legal config value (no real secret would
// literally contain that string).
const redactedSentinel = "<redacted>"

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
func getCmd(stdin io.Reader, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	var (
		schema    = fs.String("schema", "", ".proto file describing the consumer's config (required)")
		message   = fs.String("message", "", "fully-qualified message name, e.g. myapp.v1.AppConfig (required)")
		chainPath = fs.String("chain", "", "path to chain.pxf (required unless a bundle input is provided)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		env       = fs.String("env", "", "environment to resolve (defaults to chain spec's default_environment)")
		ctxKVs    = fs.String("ctx", "", "comma-separated key=value pairs for ${ctx.*} substitutions, e.g. region=eu,tenant=acme")
		decrypter = fs.String("decrypter", "none", `decrypter for encrypted layers: "none" (error if any encrypted layer is hit) or "sops" (use ambient sops/age/KMS credentials)`)
		appName   = fs.String("app", "", "named application entry to load defaults from in $HOME/.chameleon; CLI flags override its values")
		// emitType controls whether the output starts with a `@type
		// <fully-qualified-name>\n\n` directive. Default on because
		// pxf query uses @type for descriptor resolution and a self-
		// describing document is the more useful default for the
		// scripting case. --no-emit-type for callers that want a
		// bare message body (e.g. concatenating multiple gets, or
		// piping into a tool that supplies its own @type).
		emitType = fs.Bool("emit-type", true, "prefix output with `@type <message>` directive")
	)
	bflags := registerBundleFlags(fs)
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

	userCfg, err := loadUserConfig()
	if err != nil {
		return err
	}
	app, err := selectApp(userCfg, *appName)
	if err != nil {
		return err
	}
	applyAppDefaults(app, schema, message, chainPath, layersDir, env, decrypter, &includePaths)

	if *schema == "" || *message == "" {
		fs.Usage()
		return fmt.Errorf("--schema and --message are required")
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

	if err := redactSecrets(res.Config); err != nil {
		return fmt.Errorf("redact secrets: %w", err)
	}

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

// redactSecrets walks msg and writes redactedSentinel into the
// `value` field of every pxf.Secret-typed message reached. Mirrors
// parse.Move's walker but inverse: instead of extracting plaintext
// into a memguard map, it writes a visible placeholder so the
// marshalled PXF document distinguishes "deliberately suppressed"
// from "absent" or "empty string." parse.Move has already cleared
// the value to "" before this runs (it's invoked on the resolver's
// post-Resolve message); this step overwrites that "" with the
// sentinel.
//
// Errors only on a structural anomaly — a Secret message whose
// descriptor lacks a `value` field. A nil msg is a no-op.
func redactSecrets(msg proto.Message) error {
	if msg == nil {
		return nil
	}
	return redactWalk(msg.ProtoReflect())
}

func redactWalk(m protoreflect.Message) error {
	var firstErr error
	m.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if err := redactMap(m, fd); err != nil {
				firstErr = err
				return false
			}
		case fd.IsList():
			if err := redactList(m, fd); err != nil {
				firstErr = err
				return false
			}
		case fd.Kind() == protoreflect.MessageKind:
			sub := m.Mutable(fd).Message()
			if fd.Message().FullName() == secretFullName {
				if err := writeRedacted(sub); err != nil {
					firstErr = err
					return false
				}
				return true
			}
			if err := redactWalk(sub); err != nil {
				firstErr = err
				return false
			}
		}
		return true
	})
	return firstErr
}

func redactList(parent protoreflect.Message, fd protoreflect.FieldDescriptor) error {
	if fd.Kind() != protoreflect.MessageKind {
		return nil
	}
	mdesc := fd.Message()
	if mdesc == nil {
		return nil
	}
	isSecret := mdesc.FullName() == secretFullName
	list := parent.Mutable(fd).List()
	for i, n := 0, list.Len(); i < n; i++ {
		sub := list.Get(i).Message()
		if isSecret {
			if err := writeRedacted(sub); err != nil {
				return err
			}
			continue
		}
		if err := redactWalk(sub); err != nil {
			return err
		}
	}
	return nil
}

func redactMap(parent protoreflect.Message, fd protoreflect.FieldDescriptor) error {
	valFd := fd.MapValue()
	if valFd.Kind() != protoreflect.MessageKind {
		return nil
	}
	mdesc := valFd.Message()
	if mdesc == nil {
		return nil
	}
	isSecret := mdesc.FullName() == secretFullName
	mp := parent.Mutable(fd).Map()
	var rangeErr error
	mp.Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
		sub := v.Message()
		if isSecret {
			if err := writeRedacted(sub); err != nil {
				rangeErr = err
				return false
			}
			return true
		}
		if err := redactWalk(sub); err != nil {
			rangeErr = err
			return false
		}
		return true
	})
	return rangeErr
}

func writeRedacted(secMsg protoreflect.Message) error {
	valueFd := secMsg.Descriptor().Fields().ByName("value")
	if valueFd == nil {
		return errors.New("redact: pxf.Secret descriptor missing `value` field")
	}
	secMsg.Set(valueFd, protoreflect.ValueOfString(redactedSentinel))
	return nil
}
