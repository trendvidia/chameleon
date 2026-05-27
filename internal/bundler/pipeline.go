// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/internal/secretcatalog"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/chameleon/sops"
)

// sops.Encrypter + sops.Recipients carry the per-layer re-encrypt
// contract. The bundler computes a sops.Recipients per layer from
// the module's recipients_by_env entry (or recipients fallback) so
// each layer file inside an env-agnostic bundle is encrypted only
// to the env that consumes it.

// BuildOptions is the input bundle for [Build]. Required fields
// cause Build to error if unset; optional fields fall back to
// documented defaults.
type BuildOptions struct {
	// Manifest is the *chameleonv1.BundleManifest authored by ops.
	// Required. proto.Message indirection keeps this package
	// independent of the generated Go binding's import path at the
	// signature level; the implementation type-asserts to the
	// concrete *chameleonv1.BundleManifest.
	Manifest proto.Message

	// Module names the target module — matches an entry in
	// Manifest.modules, or falls back to Manifest.default policy
	// (unknown-module fallthrough logs a warning to stderr via
	// the caller; Compile records the fallthrough in its
	// returned Filter). Required.
	Module string

	// Schema is the descriptor for the consumer's top-level config
	// message (e.g. AppConfig). Used for catalog enumeration
	// (manifest pattern validation) and tree narrowing. Required.
	Schema protoreflect.MessageDescriptor

	// ChainSpec is the resolved chain configuration as a
	// *chameleonv1.ChainSpec. Required; expanded via chain.Expand
	// once per declared environment to enumerate the layer set
	// the bundle must carry. Copied verbatim into the bundle so
	// the runtime can re-resolve.
	ChainSpec proto.Message

	// LayersFS is the source filesystem holding the chain spec's
	// referenced layer files. Required.
	LayersFS fs.FS

	// Decrypter reads sops-encrypted layers. Required when any
	// LayerEntry in ChainSpec is marked encrypted; ignored otherwise.
	Decrypter sops.Decrypter

	// Encrypter writes sops-encrypted layers in the output bundle.
	// Required when any layer's resolved sops.Recipients are non-empty.
	Encrypter sops.Encrypter

	// Output is the io.Writer the zip bundle is streamed to.
	// Required. The caller is responsible for any wrapping
	// concerns (file, multipart upload, in-memory buffer).
	Output io.Writer

	// ManifestSHA256 is recorded in the bundle's audit entry so
	// incident response can answer "which manifest version
	// produced this bundle". Optional; computed by the bundler
	// from Manifest's deterministic serialization if empty.
	ManifestSHA256 []byte

	// ManifestName is recorded in the audit entry alongside the
	// hash. Optional; the hash alone identifies the manifest, but
	// the name aids human readability.
	ManifestName string

	// Now overrides time.Now for the audit entry. Tests pass a
	// fixed value to keep bundle bytes deterministic; production
	// leaves this nil.
	Now func() time.Time
}

// Build runs the decrypt → narrow → re-encrypt → package pipeline.
//
// Layer encryption policy:
//
//   - Each unique resolved layer path is written ONCE to the
//     bundle, at its resolved path under the zip root, encrypted
//     to the UNION of recipient sets for every environment that
//     uses it.
//   - Per-env layers (paths that templated on ${env}) naturally
//     produce one resolved path per env and get that env's
//     recipients only — env isolation in ciphertext.
//   - Cross-env "base" layers (no ${env} template) get the union;
//     the BundleManifest comment documents this as the price of
//     sharing.
//
// The chain spec is copied verbatim. The runtime extracts the
// bundle as an fs.FS and resolves normally — same chain.Expand,
// same layer paths, same Decrypter call shape.
//
// Layer @header preambles are stripped during the pipeline:
// chameleon's runtime treats them as optional sanity checks, and
// the bundle's chain spec is authoritative for the layer
// metadata they encode (id, env, encrypted).
// BuildBase64 is the symmetric convenience for Build: it wraps
// opts.Output with base64.NewEncoder before running Build, then
// explicitly Closes the encoder so the trailing pad group flushes.
// Equivalent to:
//
//	enc := base64.NewEncoder(base64.StdEncoding, opts.Output)
//	opts.Output = enc
//	err := Build(ctx, opts)
//	if cerr := enc.Close(); err == nil { err = cerr }
//
// but factored as a single call so the close-error surfacing —
// which is load-bearing, truncating the pad group is a silent
// data-loss bug — happens consistently across callers. The
// matching reader is [bundle.OpenBase64].
func BuildBase64(ctx context.Context, opts BuildOptions) error {
	if opts.Output == nil {
		return fmt.Errorf("bundler: BuildOptions invalid: output is required")
	}
	enc := base64.NewEncoder(base64.StdEncoding, opts.Output)
	opts.Output = enc
	err := Build(ctx, opts)
	if cerr := enc.Close(); err == nil {
		err = cerr
	}
	return err
}

func Build(ctx context.Context, opts BuildOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}

	manifest := opts.Manifest.(*chameleonv1.BundleManifest)
	chainSpec := opts.ChainSpec.(*chameleonv1.ChainSpec)

	catalog := Catalog{
		Secrets:    secretcatalog.FindInMessage(opts.Schema),
		NonSecrets: secretcatalog.FindNonSecretFieldsInMessage(opts.Schema),
	}
	filter, err := Compile(manifest, opts.Module, catalog)
	if err != nil {
		return err
	}

	module, _ := lookupModule(manifest, opts.Module)

	layers, err := planLayers(chainSpec, module)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(opts.Output)
	for _, plan := range layers {
		if err := processLayer(ctx, zw, opts, filter, plan); err != nil {
			return err
		}
	}
	if err := writeChainSpec(zw, opts.ChainSpec); err != nil {
		return err
	}
	if err := writeAudit(zw, opts, manifest); err != nil {
		return err
	}
	return zw.Close()
}

// layerPlan is one (resolved path, env-union) pair the bundler
// will emit. Built up by planLayers from a per-env chain.Expand
// sweep so each unique path is processed exactly once.
type layerPlan struct {
	Path       string
	Encrypted  bool
	Required   bool
	Envs       []string // sorted; the envs that share this path
	Recipients sops.Recipients
}

// planLayers iterates every environment declared in chainSpec,
// calls chain.Expand for each, then collapses the per-env LayerRef
// lists into one layerPlan per unique resolved path. The
// recipient union is computed from the module's recipients_by_env
// entries for those envs.
//
// Rejects chain specs with any LayerEntry.path referencing
// ${ctx.*} — bundling is build-time and has no runtime context
// values. Without rejecting, optional ctx layers would silently
// drop out of the bundle (chain.Expand skips them) and the
// resulting bundle would diverge from any runtime that passes the
// context, surfacing as a baffling "where did my layer go" at
// runtime. The CLI surfaces this as a "remove ${ctx} references
// or migrate to ${env}" message.
func planLayers(chainSpec *chameleonv1.ChainSpec, module *chameleonv1.BundleManifest_Module) ([]*layerPlan, error) {
	for _, le := range chainSpec.GetLayers() {
		if strings.Contains(le.GetPath(), "${ctx.") {
			return nil, fmt.Errorf("bundler: layer entry %q path %q references ${ctx.*}; bundles cannot resolve runtime context, migrate the value into the chain spec or split into per-env entries", le.GetId(), le.GetPath())
		}
	}

	byPath := make(map[string]*layerPlan)
	for _, envEntry := range chainSpec.GetEnvironments() {
		envName := envEntry.GetName()
		refs, err := chain.Expand(chainSpec, envName, nil)
		if err != nil {
			return nil, fmt.Errorf("bundler: expand env %q: %w", envName, err)
		}
		for _, ref := range refs {
			if !path.IsAbs(ref.Path) && !strings.HasPrefix(ref.Path, "../") && ref.Path != ".." {
				// fine — relative, no escape
			} else {
				return nil, fmt.Errorf("bundler: layer %q (entry %q): zip-unsafe path", ref.Path, ref.EntryID)
			}
			plan, ok := byPath[ref.Path]
			if !ok {
				plan = &layerPlan{
					Path:      ref.Path,
					Encrypted: ref.Encrypted,
					Required:  ref.Required,
				}
				byPath[ref.Path] = plan
			} else if plan.Encrypted != ref.Encrypted {
				return nil, fmt.Errorf("bundler: layer %q referenced as both encrypted and plaintext across envs (%v)", ref.Path, append(plan.Envs, ref.Env))
			}
			plan.Envs = append(plan.Envs, ref.Env)
		}
	}

	plans := make([]*layerPlan, 0, len(byPath))
	for _, p := range byPath {
		sort.Strings(p.Envs)
		p.Envs = dedupe(p.Envs)
		if p.Encrypted {
			p.Recipients = unionRecipients(module, p.Envs)
		}
		plans = append(plans, p)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Path < plans[j].Path })
	return plans, nil
}

// processLayer reads ref.Path from opts.LayersFS, decrypts if
// encrypted, parses as opts.Schema, runs Narrow against the
// filter, re-marshals, re-encrypts to plan.Recipients if needed,
// and writes the result to zw at plan.Path.
func processLayer(ctx context.Context, zw *zip.Writer, opts BuildOptions, filter *Filter, plan *layerPlan) error {
	raw, err := fs.ReadFile(opts.LayersFS, plan.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !plan.Required {
			return nil
		}
		return fmt.Errorf("bundler: read %q: %w", plan.Path, err)
	}

	var plaintext []byte
	if plan.Encrypted {
		if opts.Decrypter == nil {
			return fmt.Errorf("bundler: layer %q is encrypted but no Decrypter was provided", plan.Path)
		}
		buf, err := opts.Decrypter.Decrypt(ctx, raw, sops.FormatForPath(plan.Path))
		if err != nil {
			return fmt.Errorf("bundler: decrypt %q: %w", plan.Path, err)
		}
		defer buf.Destroy()
		plaintext = buf.Bytes()
	} else {
		plaintext = raw
	}

	msg := dynamicpb.NewMessage(opts.Schema)
	if _, err := pxf.UnmarshalFull(plaintext, msg); err != nil {
		return fmt.Errorf("bundler: parse %q: %w", plan.Path, err)
	}
	if err := Narrow(msg, filter); err != nil {
		return fmt.Errorf("bundler: narrow %q: %w", plan.Path, err)
	}
	narrowed, err := pxf.MarshalOptions{}.Marshal(msg)
	if err != nil {
		return fmt.Errorf("bundler: re-marshal %q: %w", plan.Path, err)
	}

	var output []byte
	if plan.Encrypted {
		if opts.Encrypter == nil {
			return fmt.Errorf("bundler: layer %q is encrypted but no Encrypter was provided", plan.Path)
		}
		if plan.Recipients.IsEmpty() {
			return fmt.Errorf("bundler: layer %q is encrypted but module %q lists no recipients for envs %v", plan.Path, opts.Module, plan.Envs)
		}
		ct, err := opts.Encrypter.Encrypt(ctx, narrowed, sops.FormatForPath(plan.Path), plan.Recipients)
		if err != nil {
			return fmt.Errorf("bundler: encrypt %q: %w", plan.Path, err)
		}
		output = ct
	} else {
		output = narrowed
	}

	w, err := zw.Create(plan.Path)
	if err != nil {
		return fmt.Errorf("bundler: zip create %q: %w", plan.Path, err)
	}
	if _, err := w.Write(output); err != nil {
		return fmt.Errorf("bundler: zip write %q: %w", plan.Path, err)
	}
	return nil
}

// writeChainSpec emits the chain spec verbatim under the bundle's
// `chain.pxf` entry. The runtime reads this back and re-resolves
// against the bundle FS — same Decrypter call shape, same layer
// paths, no special "bundle mode".
func writeChainSpec(zw *zip.Writer, spec proto.Message) error {
	body, err := pxf.MarshalOptions{}.Marshal(spec)
	if err != nil {
		return fmt.Errorf("bundler: marshal chain spec: %w", err)
	}
	w, err := zw.Create(ChainEntryPath)
	if err != nil {
		return fmt.Errorf("bundler: zip create %s: %w", ChainEntryPath, err)
	}
	_, err = w.Write(body)
	return err
}

// writeAudit emits a small human-inspectable record of what
// produced this bundle. Format is freeform `key: value` lines —
// a typed BundleAudit proto would be nicer but is deferred to a
// follow-up so this PR doesn't drag a proto schema change.
//
// The hash on disk is sufficient to identify the manifest version
// even if ManifestName was unset; ManifestName is logged purely
// for human readability. The chameleon build version comes from
// debug.ReadBuildInfo (same source as `chameleon version`).
func writeAudit(zw *zip.Writer, opts BuildOptions, manifest *chameleonv1.BundleManifest) error {
	sum := opts.ManifestSHA256
	if len(sum) == 0 {
		body, err := proto.MarshalOptions{Deterministic: true}.Marshal(manifest)
		if err != nil {
			return fmt.Errorf("bundler: hash manifest: %w", err)
		}
		h := sha256.Sum256(body)
		sum = h[:]
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	var b strings.Builder
	fmt.Fprintf(&b, "module: %s\n", opts.Module)
	fmt.Fprintf(&b, "manifest_name: %s\n", opts.ManifestName)
	fmt.Fprintf(&b, "manifest_sha256: %x\n", sum)
	fmt.Fprintf(&b, "built_at: %s\n", now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "chameleon_version: %s\n", chameleonVersion())

	w, err := zw.Create(AuditEntryPath)
	if err != nil {
		return fmt.Errorf("bundler: zip create %s: %w", AuditEntryPath, err)
	}
	_, err = io.WriteString(w, b.String())
	return err
}

// chameleonVersion returns the module version compiled into the
// running binary. Empty / "(devel)" for non-release builds is
// expected and intentional — the audit captures whatever was real.
func chameleonVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	v := bi.Main.Version
	if v == "" {
		return "(devel)"
	}
	return v
}

// unionRecipients merges the per-env RecipientSets that apply to
// a shared layer. For a layer used by multiple envs, each env's
// runtime must be able to decrypt it, so we union; for a layer
// used by a single env, the union degenerates to that env's set.
//
// Falls back to module.recipients when recipients_by_env is empty
// (the "no env isolation in ciphertext, only plaintext narrowing"
// posture documented in BundleManifest.proto).
func unionRecipients(module *chameleonv1.BundleManifest_Module, envs []string) sops.Recipients {
	if module == nil {
		return sops.Recipients{}
	}
	byEnv := module.GetRecipientsByEnv()
	if len(byEnv) == 0 {
		fallback := module.GetRecipients()
		return sops.Recipients{
			KMS: append([]string(nil), fallback.GetKms()...),
			Age: append([]string(nil), fallback.GetAge()...),
			PGP: append([]string(nil), fallback.GetPgp()...),
		}
	}
	var out sops.Recipients
	for _, env := range envs {
		rs, ok := byEnv[env]
		if !ok {
			continue
		}
		out.KMS = append(out.KMS, rs.GetKms()...)
		out.Age = append(out.Age, rs.GetAge()...)
		out.PGP = append(out.PGP, rs.GetPgp()...)
	}
	out.KMS = dedupe(out.KMS)
	out.Age = dedupe(out.Age)
	out.PGP = dedupe(out.PGP)
	return out
}

func dedupe(ss []string) []string {
	if len(ss) <= 1 {
		return ss
	}
	sort.Strings(ss)
	out := ss[:1]
	for _, s := range ss[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// validate runs the cross-field checks that don't depend on
// reading any layer files. Errors here mean a caller misconfigured
// the BuildOptions; runtime-time errors (bad manifest patterns,
// missing layer files, decrypt failures) are surfaced later by
// the relevant pipeline step so the caller sees the failing
// layer's identity in the error.
func (o BuildOptions) validate() error {
	var errs []error
	if o.Manifest == nil {
		errs = append(errs, errors.New("manifest is required"))
	} else if _, ok := o.Manifest.(*chameleonv1.BundleManifest); !ok {
		errs = append(errs, fmt.Errorf("manifest must be *chameleonv1.BundleManifest, got %T", o.Manifest))
	}
	if o.Module == "" {
		errs = append(errs, errors.New("module is required"))
	}
	if o.Schema == nil {
		errs = append(errs, errors.New("schema is required"))
	}
	if o.ChainSpec == nil {
		errs = append(errs, errors.New("chain spec is required"))
	} else if _, ok := o.ChainSpec.(*chameleonv1.ChainSpec); !ok {
		errs = append(errs, fmt.Errorf("chain spec must be *chameleonv1.ChainSpec, got %T", o.ChainSpec))
	}
	if o.LayersFS == nil {
		errs = append(errs, errors.New("layers FS is required"))
	}
	if o.Output == nil {
		errs = append(errs, errors.New("output is required"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("bundler: BuildOptions invalid: %w", errors.Join(errs...))
	}
	return nil
}
