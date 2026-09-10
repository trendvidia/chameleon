// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/trendvidia/protocompile"
	"github.com/trendvidia/protocompile/linker"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/crypt"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/chameleon/resolver"
	chsops "github.com/trendvidia/chameleon/sops"
	"github.com/trendvidia/chameleon/source"
)

func explainCmd(stdin io.Reader, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	var (
		schema    = fs.String("schema", "", ".proto file describing the consumer's config (required)")
		message   = fs.String("message", "", "fully-qualified message name, e.g. myapp.v1.AppConfig (required)")
		chainPath = fs.String("chain", "", "path to chain.pxf (required unless a bundle input is provided)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		env       = fs.String("env", "", "environment to resolve (defaults to chain spec's default_environment)")
		field     = fs.String("field", "", "if set, print provenance for this dotted field path only; otherwise print all")
		ctxKVs    = fs.String("ctx", "", "comma-separated key=value pairs for ${ctx.*} substitutions, e.g. region=eu,tenant=acme")
		decrypter = fs.String("decrypter", "none", `decrypter for encrypted layers: "none" (error if any encrypted layer is hit) or "sops" (use ambient sops/age/KMS credentials)`)
		appName   = fs.String("app", "", "named application entry to load defaults from in $HOME/.chameleon; CLI flags override its values")
	)
	bflags := registerBundleFlags(fs)
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

	// Merge in $HOME/.chameleon defaults under explicit CLI flags.
	// A missing file is a no-op; the required-flag check below
	// produces the same error it always did.
	userCfg, err := loadUserConfig()
	if err != nil {
		return err
	}
	app, err := selectApp(userCfg, *appName)
	if err != nil {
		return err
	}
	applyAppDefaults(fs, app, schema, message, chainPath, layersDir, env, decrypter, &includePaths)

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

	// 1) Load consumer schema (compile the .proto file at runtime).
	desc, err := compileSchema(*schema, *message, includePaths)
	if err != nil {
		return err
	}

	// 2) Parse chain spec — either from the bundle or from --chain.
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
		// Source registration for the on-disk path comes from two
		// channels (see buildSources): the chain spec's declared
		// `sources` map (kind=fs supported), and implicit defaulting
		// for any source name not declared, pointing at --layers.
		chainDir, err := filepath.Abs(filepath.Dir(*chainPath))
		if err != nil {
			return fmt.Errorf("resolve chain dir: %w", err)
		}
		sources, err = buildSources(spec, desc, chainDir, *layersDir)
		if err != nil {
			return err
		}
	}

	// 3) Build context map.
	ctxMap, err := parseCtx(*ctxKVs)
	if err != nil {
		return err
	}
	dec, err := selectDecrypter(*decrypter)
	if err != nil {
		return err
	}
	sample := dynamicpb.NewMessage(desc)
	opts := make([]resolver.Option, 0, len(sources)+2)
	for name, src := range sources {
		opts = append(opts, resolver.WithSource(name, src))
	}
	opts = append(opts, resolver.WithTrace(), decrypterOption(dec))
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
		return noDecrypterError(err, fs, *decrypter)
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
// named message descriptor. See [compileSchemaFiles] for how
// includePaths are treated.
func compileSchema(schemaPath, fullName string, includePaths []string) (protoreflect.MessageDescriptor, error) {
	files, err := compileSchemaFiles(schemaPath, includePaths)
	if err != nil {
		return nil, err
	}
	return findMessage(files, fullName)
}

// findMessage looks fullName up in everything files[0] can see: the
// schema file itself, its imports, and their public imports — which is
// exactly the set a message reference inside the schema could name.
// That walk is [findVisibleDescriptor].
//
// Not [linker.Files.AsResolver]: that searches only the files named on
// the command line, and compileSchemaFiles names one. A message
// declared in an imported common.proto would come back "not found"
// from a closure that plainly contains it.
func findMessage(files linker.Files, fullName string) (protoreflect.MessageDescriptor, error) {
	d, err := findVisibleDescriptor(files[0], protoreflect.FullName(fullName))
	if err != nil {
		return nil, fmt.Errorf("find message %q: %w", fullName, err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%q is not a message", fullName)
	}
	return md, nil
}

// compileSchemaFiles compiles schemaPath together with its transitive
// imports. includePaths are additional import roots (protoc -I
// equivalent); the schema file's own directory is prepended implicitly
// so the schema is always findable by its basename.
//
// The returned files[0] is schemaPath's own FileDescriptor — which is
// what a caller validating a schema wants, since the PXF bind-time
// rules are a property of a file closure rather than of any one
// message. Callers looking up a specific message use AsResolver.
func compileSchemaFiles(schemaPath string, includePaths []string) (linker.Files, error) {
	// Verify the file exists up-front so callers get a clean error
	// instead of protocompile's "could not resolve path" message
	// pointing at the same file from inside the import-search loop.
	// Regular-file, not merely exists: a directory otherwise reaches
	// protocompile and comes back as "read <dir>: is a directory",
	// which is the message this check exists to avoid, and a directory
	// is the likely wrong argument now that check-schema takes a
	// --schema a caller may reasonably think means "a proto tree".
	st, err := os.Stat(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", schemaPath, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("read schema %s: not a regular file", schemaPath)
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
	return files, nil
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

// selectDecrypter maps the --decrypter flag value to a Decrypter
// instance (or nil for "none"). "sops" returns chsops.Default(),
// which reads credentials from the ambient environment (AWS_*,
// GOOGLE_APPLICATION_CREDENTIALS, ~/.config/sops/age/keys.txt, …)
// — same surface as a production binary's WithDecrypter wiring,
// so the dev workflow exercises the real path. Unknown values
// error rather than silently falling back to "none", since a
// typo'd flag should not silently disable decryption.
//
// nil means "the operator asked for no decryption", which is not the
// same as "nothing configured": this binary links chameleon/sops for
// bundle/migrate/rotate-key, so the crypt registry would happily
// auto-select it. Callers pass the nil through to
// [decrypterOption] rather than omitting the option.
func selectDecrypter(name string) (crypt.Decrypter, error) {
	switch name {
	case "", "none":
		return nil, nil
	case "sops":
		return chsops.Default(), nil
	default:
		return nil, fmt.Errorf(`--decrypter: unknown value %q (want "none" or "sops")`, name)
	}
}

// decrypterOption turns a [selectDecrypter] result into the resolver
// option that matches it: the chosen Decrypter, or an explicit opt-out
// for --decrypter=none so an encrypted layer fails with
// resolver.ErrNoDecrypter instead of being decrypted with ambient
// credentials the operator declined to use.
func decrypterOption(dec crypt.Decrypter) resolver.Option {
	if dec == nil {
		return resolver.WithoutDecrypter()
	}
	return resolver.WithDecrypter(dec)
}

// noDecrypterError re-words the resolver's encrypted-layer refusal for
// someone holding a command line rather than a Go program
// (trendvidia/chameleon#141). The resolver's own diagnostic ends in
// "decryption disabled (WithoutDecrypter)", which is correct for its
// audience and useless for this one: a CLI user never called
// WithoutDecrypter, is not told they did anything, and the identifier
// appears in no --help output.
//
// Whether --decrypter was typed on the command line (flagSet) is the
// only distinction that changes what the operator needs to hear.
// Defaulting to it is a surprise and gets the instruction; asking for
// it and being refused is the flag working, and gets a statement.
//
// decrypter is the flag's value, echoed as typed rather than named
// "none": --decrypter= is what --decrypter=$DECRYPTER expands to with
// the variable unset, and it wires WithoutDecrypter exactly as "none"
// does, so assuming the value would report one the operator never
// passed. The echo is also the answer to "why did the decrypter in my
// ~/.chameleon not apply" — applyAppDefaults leaves a typed flag
// alone, empty value included.
//
// Anything else passes through untouched. In particular the
// no-backend-registered case cannot arise from this binary — the CLI
// always passes either WithDecrypter or WithoutDecrypter, so the
// registry is never consulted — and if it somehow did, the resolver's
// Go-facing wording is the better of the two.
func noDecrypterError(err error, fs *flag.FlagSet, decrypter string) error {
	var nde *resolver.NoDecrypterError
	if !errors.As(err, &nde) || !nde.Disabled {
		return err
	}
	if flagSet(fs, "decrypter") {
		return &cliError{
			msg: fmt.Sprintf("layer %q is encrypted: --decrypter=%s was passed on the command line",
				nde.LayerID, decrypter),
			err: err,
		}
	}
	return &cliError{
		msg: fmt.Sprintf(`layer %q is encrypted: --decrypter is "none" (the fail-closed default); `+
			`pass --decrypter=sops to decrypt it, or set decrypter = "sops" on this app in %s`,
			nde.LayerID, userConfigPath()),
		err: err,
	}
}

// cliError carries a message written for a CLI user over a cause
// written for a Go caller. Only the message is printed; the cause
// stays reachable, so errors.Is against resolver.ErrNoDecrypter keeps
// working for the tests and for any caller that wraps main.
type cliError struct {
	msg string
	err error
}

func (e *cliError) Error() string { return e.msg }
func (e *cliError) Unwrap() error { return e.err }

// buildSources derives the source.Source map to register with the
// resolver from the chain spec. Two-pass:
//
//  1. Every source declared in spec.GetSources() — interpret its
//     `kind` + `options`. Supported kinds:
//
//     "fs" is filesystem-backed. `options["root"]` is the FS root,
//     resolved relative to chainDir (the chain.pxf's own directory)
//     when not absolute. When no root is supplied, fall back to
//     layersDir so a `{ kind = "fs" }` entry behaves like the
//     historical single-source case.
//
//     "env" is process-environment-backed. `options["prefix"]`
//     optionally narrows which env vars participate (matches only
//     names that begin with the prefix; remainder is compared to the
//     uppercased dotted field path). Requires the resolved message
//     descriptor so the source can walk the schema and coerce values
//     at Open time.
//
//     Unknown kinds error so callers don't silently get the wrong
//     behavior — that covers every kind above, not just the last
//     one. (Kinds are paragraphs rather than sub-bullets because
//     gofmt's doc-comment printer flattens nested lists to a single
//     indent, which silently reattaches this sentence to "env".)
//
//  2. Every source NAME referenced by a layer that wasn't declared
//     in (1). Auto-register as fs at layersDir — this is the
//     common dev shape ("a single layers dir with logical name
//     prefixes like base / env-dev / secrets-dev, all on disk
//     together") and is the only place --layers is consulted.
//
// In addition: every LayerEntry that references an env-kind source
// is checked for `encrypted = true` and rejected. Sops decryption
// has nothing to bite on for env vars; failing fast here is more
// helpful than a confusing runtime error inside the decrypter.
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
func buildSources(spec proto.Message, desc protoreflect.MessageDescriptor, chainDir, layersDir string) (map[string]source.Source, error) {
	cs, ok := spec.(*chameleonv1.ChainSpec)
	if !ok {
		return nil, fmt.Errorf("chain spec: unexpected type %T", spec)
	}

	sources := make(map[string]source.Source)
	envKinds := make(map[string]bool) // source name → is env-kind

	// Pass 1: declared sources.
	for name, ref := range cs.GetSources() {
		kind := ref.GetKind()
		switch kind {
		case "fs":
			root := ref.GetOptions()["root"]
			switch {
			case root == "":
				root = layersDir
			case !filepath.IsAbs(root):
				root = filepath.Join(chainDir, root)
			}
			sources[name] = source.FS(os.DirFS(root))
		case "env":
			if desc == nil {
				return nil, fmt.Errorf("chain source %q: kind=env requires a message descriptor; pass --schema + --message", name)
			}
			var opts []source.EnvOption
			if prefix := ref.GetOptions()["prefix"]; prefix != "" {
				opts = append(opts, source.WithPrefix(prefix))
			}
			sources[name] = source.Env(desc, opts...)
			envKinds[name] = true
		default:
			return nil, fmt.Errorf(
				"chain source %q: kind %q not supported (want \"fs\" or \"env\")",
				name, kind,
			)
		}
	}

	// Reject encrypted=true on layers that reference an env source.
	// sops has nothing to decrypt; failing here beats a runtime
	// "couldn't find sops metadata" further down the pipeline.
	for _, layer := range cs.GetLayers() {
		if !envKinds[layer.GetSource()] {
			continue
		}
		if layer.GetEncrypted() {
			return nil, fmt.Errorf(
				"chain layer %q: source %q is kind=env; encrypted=true is meaningless (env vars are plaintext)",
				layer.GetId(), layer.GetSource(),
			)
		}
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

// buildBundleSources is the bundle-input counterpart to buildSources.
// Same two-pass shape (declared sources, then undeclared layer-
// referenced names), but every fs source — declared or implicit —
// resolves to bundleFS instead of an on-disk dir:
//
//   - kind=fs: source.FS(bundleFS), ignoring options["root"]. The
//     bundler packs one logical FS root per module (it takes one
//     --layers dir on the build side); collapsing all fs sources
//     in the chain onto that root is the correct round-trip.
//   - kind=env: source.Env(desc, opts...). Identical to buildSources
//     — env sources don't read from disk, so they behave the same
//     regardless of how layer files are sourced.
//   - any other kind: rejected with the same error shape.
//
// Same encrypted-on-env-source rejection rule as buildSources, for
// the same reason (sops has nothing to decrypt for env vars).
//
// Without this helper, callers using a --bundle input would only
// see whatever single-source map they constructed inline, missing
// every declared source the chain spec carries — chains with an
// env-runtime layer (the common shape for STEWARD_*-style runtime
// overrides) would error mid-resolve with "unregistered source".
func buildBundleSources(spec proto.Message, desc protoreflect.MessageDescriptor, bundleFS iofs.FS) (map[string]source.Source, error) {
	cs, ok := spec.(*chameleonv1.ChainSpec)
	if !ok {
		return nil, fmt.Errorf("chain spec: unexpected type %T", spec)
	}

	sources := make(map[string]source.Source)
	envKinds := make(map[string]bool)

	for name, ref := range cs.GetSources() {
		kind := ref.GetKind()
		switch kind {
		case "fs":
			sources[name] = source.FS(bundleFS)
		case "env":
			if desc == nil {
				return nil, fmt.Errorf("chain source %q: kind=env requires a message descriptor; pass --schema + --message", name)
			}
			var opts []source.EnvOption
			if prefix := ref.GetOptions()["prefix"]; prefix != "" {
				opts = append(opts, source.WithPrefix(prefix))
			}
			sources[name] = source.Env(desc, opts...)
			envKinds[name] = true
		default:
			return nil, fmt.Errorf(
				"chain source %q: kind %q not supported (want \"fs\" or \"env\")",
				name, kind,
			)
		}
	}

	for _, layer := range cs.GetLayers() {
		if !envKinds[layer.GetSource()] {
			continue
		}
		if layer.GetEncrypted() {
			return nil, fmt.Errorf(
				"chain layer %q: source %q is kind=env; encrypted=true is meaningless (env vars are plaintext)",
				layer.GetId(), layer.GetSource(),
			)
		}
	}

	// Undeclared source names referenced by layers — default to the
	// bundle's FS so a logical label the chain doesn't explicitly
	// declare still resolves against bundled content.
	for _, layer := range cs.GetLayers() {
		name := layer.GetSource()
		if name == "" {
			name = "default"
		}
		if _, registered := sources[name]; registered {
			continue
		}
		sources[name] = source.FS(bundleFS)
	}

	if len(sources) == 0 {
		sources["default"] = source.FS(bundleFS)
	}

	return sources, nil
}
