// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
)

func explainCmd(args []string) error {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	var (
		schema    = fs.String("schema", "", ".proto file describing the consumer's config (required)")
		message   = fs.String("message", "", "fully-qualified message name, e.g. myapp.v1.AppConfig (required)")
		chainPath = fs.String("chain", "", "path to chain.pxf (required)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		env       = fs.String("env", "", "environment to resolve (defaults to chain spec's default_environment)")
		field     = fs.String("field", "", "if set, print provenance for this dotted field path only; otherwise print all")
		ctxKVs    = fs.String("ctx", "", "comma-separated key=value pairs for ${ctx.*} substitutions, e.g. region=eu,tenant=acme")
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon explain — resolve a config and print per-field provenance.

Usage:
    chameleon explain --schema=app.proto --message=app.v1.AppConfig \
                      --chain=chain.pxf --layers=./layers --env=dev

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *schema == "" || *message == "" || *chainPath == "" {
		fs.Usage()
		return fmt.Errorf("--schema, --message, and --chain are required")
	}

	// 1) Load consumer schema (compile the .proto file at runtime).
	desc, err := compileSchema(*schema, *message)
	if err != nil {
		return err
	}

	// 2) Parse chain spec.
	spec, err := chain.LoadSpec(*chainPath)
	if err != nil {
		return err
	}

	// 3) Build context map.
	ctxMap, err := parseCtx(*ctxKVs)
	if err != nil {
		return err
	}

	// 4) Resolve with trace enabled.
	sample := dynamicpb.NewMessage(desc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(os.DirFS(*layersDir))),
		resolver.WithTrace(),
	)
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

	if res.Trace == nil {
		return fmt.Errorf("trace not produced (resolver returned but Trace is nil)")
	}

	// 5) Print.
	if *field != "" {
		printOne(*field, res)
		return nil
	}
	printAll(res)
	return nil
}

// compileSchema reads schemaPath and uses protocompile to find the
// named message descriptor.
func compileSchema(schemaPath, fullName string) (protoreflect.MessageDescriptor, error) {
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", schemaPath, err)
	}
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(
				map[string]string{schemaPath: string(data)},
			)},
		),
	}
	files, err := comp.Compile(context.Background(), schemaPath)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", schemaPath, err)
	}
	d, err := files.AsResolver().FindDescriptorByName(protoreflect.FullName(fullName))
	if err != nil {
		return nil, fmt.Errorf("find message %q: %w", fullName, err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%q is not a message", fullName)
	}
	return md, nil
}

func parseCtx(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("malformed ctx pair %q (want key=value)", kv)
		}
		out[strings.TrimSpace(kv[:eq])] = strings.TrimSpace(kv[eq+1:])
	}
	return out, nil
}

// printOne renders provenance for a single dotted field path.
func printOne(path string, res *resolver.Resolution[*dynamicpb.Message]) {
	e := res.Trace.Get(path)
	val := readField(res.Config.ProtoReflect(), path)
	fmt.Println(format(path, val, e))
}

// printAll renders provenance for every traced field path, sorted.
func printAll(res *resolver.Resolution[*dynamicpb.Message]) {
	paths := res.Trace.Paths()
	sort.Strings(paths)
	for _, p := range paths {
		val := readField(res.Config.ProtoReflect(), p)
		fmt.Println(format(p, val, res.Trace.Get(p)))
	}
}

// readField walks msg by dotted path and returns a string rendering
// of the leaf scalar. Returns "" for unknown paths or unsupported
// kinds; the trace entry's Sensitive flag drives the actual display.
func readField(msg protoreflect.Message, path string) string {
	parts := strings.Split(path, ".")
	cur := msg
	for i, name := range parts {
		fd := cur.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return ""
		}
		if i == len(parts)-1 {
			if fd.Kind() == protoreflect.MessageKind || fd.IsList() || fd.IsMap() {
				return "<message/list/map>"
			}
			return cur.Get(fd).String()
		}
		if fd.Kind() != protoreflect.MessageKind {
			return ""
		}
		cur = cur.Get(fd).Message()
	}
	return ""
}

func format(path, value string, e resolver.TraceEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-40s = ", path)
	if e.Sensitive {
		b.WriteString("<sensitive>")
	} else {
		fmt.Fprintf(&b, "%q", value)
	}
	var tags []string
	if e.FromDefault {
		tags = append(tags, "default")
	} else if e.LayerID != "" {
		tags = append(tags, e.LayerID)
	}
	if e.Interpolated {
		tags = append(tags, "interpolated")
	}
	if e.Sensitive {
		tags = append(tags, "sensitive")
	}
	if len(tags) > 0 {
		fmt.Fprintf(&b, "  [%s]", strings.Join(tags, " | "))
	}
	return b.String()
}
