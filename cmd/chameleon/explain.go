// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
)

func explainCmd(out io.Writer, args []string) error {
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
	// includePaths collects --include flag values. Repeatable so callers
	// can pass multiple proto search roots (mirrors protoc -I). Each
	// path is added to protocompile's SourceResolver.ImportPaths and is
	// searched in order; the schema file's own directory is prepended
	// implicitly so a schema can be referenced by its on-disk path
	// without --include even when it has no non-WKT imports.
	var includePaths []string
	fs.Func("include", "directory to search for proto imports (repeatable, like protoc -I)", func(s string) error {
		includePaths = append(includePaths, s)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon explain — resolve a config and print per-field provenance.

Usage:
    chameleon explain --schema=app.proto --message=app.v1.AppConfig \
                      --chain=chain.pxf --layers=./layers --env=dev

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

	// 1) Load consumer schema (compile the .proto file at runtime).
	desc, err := compileSchema(*schema, *message, includePaths)
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
	//
	// Source registration comes from two channels:
	//   - the chain spec's declared `sources` map (kind=fs supported;
	//     other kinds error since `explain` only knows fs);
	//   - implicit defaulting for any layer source name that wasn't
	//     declared in the spec — point it at --layers so the simple
	//     "single root, multiple logical names" case (the common
	//     dev shape) just works.
	sources, err := buildSources(spec, *layersDir)
	if err != nil {
		return err
	}
	sample := dynamicpb.NewMessage(desc)
	opts := make([]resolver.Option, 0, len(sources)+1)
	for name, src := range sources {
		opts = append(opts, resolver.WithSource(name, src))
	}
	opts = append(opts, resolver.WithTrace())
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

	if res.Trace == nil {
		return fmt.Errorf("trace not produced (resolver returned but Trace is nil)")
	}

	// 5) Print.
	if *field != "" {
		printOne(out, *field, res)
		return nil
	}
	printAll(out, res)
	return nil
}

// compileSchema reads schemaPath and uses protocompile to find the
// named message descriptor. includePaths are additional import roots
// (protoc -I equivalent) used to resolve the schema's transitive
// imports. The schema file's own directory is prepended implicitly
// so the schema is always findable by its basename.
func compileSchema(schemaPath, fullName string, includePaths []string) (protoreflect.MessageDescriptor, error) {
	// Verify the file exists up-front so callers get a clean error
	// instead of protocompile's "could not resolve path" message
	// pointing at the same file from inside the import-search loop.
	if _, err := os.Stat(schemaPath); err != nil {
		return nil, fmt.Errorf("read schema %s: %w", schemaPath, err)
	}
	// Search the schema's own directory first (so the schema resolves
	// by basename), then any --include paths the caller passed (so
	// transitive imports like pxf/secret.proto resolve). All paths
	// are absolutized so protocompile's import-relative-to-import-path
	// joining produces stable results regardless of the caller's CWD.
	schemaDir, err := filepath.Abs(filepath.Dir(schemaPath))
	if err != nil {
		return nil, fmt.Errorf("resolve schema dir: %w", err)
	}
	importPaths := []string{schemaDir}
	for _, p := range includePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("resolve --include %q: %w", p, err)
		}
		importPaths = append(importPaths, abs)
	}

	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{ImportPaths: importPaths},
		),
	}
	// Compile by basename so the schema resolves against the first
	// import path (its own directory). Passing the full schemaPath
	// here would force protocompile to find it via path-prefix
	// matching, which is awkward when --include points elsewhere.
	files, err := comp.Compile(context.Background(), filepath.Base(schemaPath))
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
func printOne(out io.Writer, path string, res *resolver.Resolution[*dynamicpb.Message]) {
	e := res.Trace.Get(path)
	val := readField(res.Config.ProtoReflect(), path)
	fmt.Fprintln(out, format(path, val, e))
}

// printAll renders provenance for every traced field path, sorted.
func printAll(out io.Writer, res *resolver.Resolution[*dynamicpb.Message]) {
	paths := res.Trace.Paths()
	sort.Strings(paths)
	for _, p := range paths {
		val := readField(res.Config.ProtoReflect(), p)
		fmt.Fprintln(out, format(p, val, res.Trace.Get(p)))
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

// buildSources derives the source.Source map to register with the
// resolver from the chain spec. Two-pass:
//
//  1. Every source declared in spec.GetSources() — interpret its
//     `kind` + `options`. Only `kind="fs"` is supported today; the
//     spec's `options["root"]` is the FS root (resolved relative to
//     layersDir when not absolute). Unknown kinds error so callers
//     don't silently get the wrong behavior.
//
//  2. Every source NAME referenced by a layer that wasn't declared
//     in (1). Auto-register as fs at layersDir — this is the common
//     dev shape ("a single layers dir with logical name prefixes
//     like base / env-dev / secrets-dev, all on disk together").
//
// The auto-registration handles chain specs that use source names
// purely as logical labels without declaring backing sources — the
// pre-this-patch behavior covered "default" only; this generalises
// to any name. Specs that DO declare sources opt into the strict
// kind-checked path.
//
// Returning the bare map (rather than a slice of resolver.Option)
// keeps the helper testable without driving a live resolver. The
// caller threads each entry through resolver.WithSource.
func buildSources(spec proto.Message, layersDir string) (map[string]source.Source, error) {
	cs, ok := spec.(*chameleonv1.ChainSpec)
	if !ok {
		return nil, fmt.Errorf("chain spec: unexpected type %T", spec)
	}

	sources := make(map[string]source.Source)

	// Pass 1: declared sources.
	for name, ref := range cs.GetSources() {
		kind := ref.GetKind()
		if kind != "fs" {
			return nil, fmt.Errorf(
				"chain source %q: kind %q not supported by `chameleon explain` "+
					"(only \"fs\"). Run via the consumer's runtime to exercise "+
					"other kinds.",
				name, kind,
			)
		}
		root := ref.GetOptions()["root"]
		if root == "" {
			root = layersDir
		} else if !filepath.IsAbs(root) {
			root = filepath.Join(layersDir, root)
		}
		sources[name] = source.FS(os.DirFS(root))
	}

	// Pass 2: undeclared source names referenced by layers — default
	// to layersDir so they resolve like the historical single-source
	// path.
	for _, layer := range cs.GetLayers() {
		name := layer.GetSource()
		if name == "" {
			name = "default"
		}
		if _, registered := sources[name]; registered {
			continue
		}
		sources[name] = source.FS(os.DirFS(layersDir))
	}

	// Belt + suspenders: if neither pass registered anything (spec
	// has no layers AND no declared sources), at least register
	// "default" so the resolver can give the more useful "spec has
	// no layers" error rather than "unregistered source" noise.
	if len(sources) == 0 {
		sources["default"] = source.FS(os.DirFS(layersDir))
	}

	return sources, nil
}
