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
	"slices"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/trendvidia/chameleon/chain"
	"github.com/trendvidia/chameleon/crypt"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	chsops "github.com/trendvidia/chameleon/sops"
)

// rotateKeyCmd is the entry point for `chameleon rotate-key`. It
// walks every encrypted layer reachable from the chain spec,
// resolves each env's effective key_name through the sops library,
// and re-encrypts the layer to the union of those keys.
//
// The mental model: rotation = "make the on-disk recipient set
// match the chain spec." Typical workflow:
//
//  1. Add the new key to SOPS_AGE_KEY_FILE (manually for now;
//     `chameleon keystore add` lands next).
//  2. Edit chain.pxf: flip `Environment.key_name` to the new entry.
//  3. (Optional transition window) Old encrypted files are still
//     readable because the keystore holds both keys.
//  4. Run `chameleon rotate-key` to re-encrypt every file under the
//     new recipient set.
//  5. Remove the old key from SOPS_AGE_KEY_FILE.
//
// Steps 1, 2, 5 are operator ceremony; step 4 is what this command
// does.
//
// Refusal rule: a shared layer used by multiple envs is encrypted
// to the UNION of those envs' key_names. If any env in the layer's
// env set lacks `key_name` in the chain spec, rotation would lose
// access for that env — so we skip the layer with a clear message
// rather than silently locking someone out. The fix is to set
// `key_name` on every env that reaches the layer.
func rotateKeyCmd(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("rotate-key", flag.ContinueOnError)
	var (
		chainPath = fs.String("chain", "", "path to chain.pxf (required)")
		layersDir = fs.String("layers", ".", "directory holding layer files referenced by the chain spec")
		dryRun    = fs.Bool("dry-run", false, "print per-layer action without writing or re-encrypting")
	)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `chameleon rotate-key — re-encrypt every encrypted layer to the chain spec's current key_name.

Walks the chain spec, identifies each unique encrypted layer along with the
envs that reach it, resolves each env's effective key_name through the sops
library (SOPS_AGE_KEY_FILE, PXF multi-key format), and re-encrypts the layer
to the union of those keys.

Refuses to rotate a layer that's reachable from an env whose key_name is
unset — that would drop the env's read access. Set key_name on every env
that uses the layer, or remove the env's reach if it shouldn't be reading.

Examples:
    chameleon rotate-key --chain=config/chain.pxf --layers=./config
    chameleon rotate-key --chain=config/chain.pxf --layers=./config --dry-run

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

	spec, err := chain.LoadSpec(*chainPath)
	if err != nil {
		return err
	}

	plans, err := planEncryptedLayers(spec)
	if err != nil {
		return err
	}

	dec := chsops.Default()
	enc := chsops.DefaultEncrypter()

	var rotated, skipped int
	ctx := context.Background()
	for _, p := range plans {
		status, err := rotateOne(ctx, out, p, *layersDir, *dryRun, dec, enc, spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR\t%s\t%v\n", p.Path, err)
			return err
		}
		switch status {
		case rotateDone:
			rotated++
		case rotateSkipped:
			skipped++
		}
	}

	verb := "rotated"
	if *dryRun {
		verb = "would rotate"
	}
	fmt.Fprintf(out, "summary: %s %d layer(s); skipped %d\n", verb, rotated, skipped)
	return nil
}

type rotateStatus int

const (
	rotateDone rotateStatus = iota
	rotateSkipped
)

// layerRotatePlan is one (path, env-union, required) tuple the
// command processes. Built up by planEncryptedLayers from a per-env
// chain.Expand sweep so each unique path is processed exactly once,
// even if it's reachable from multiple envs.
type layerRotatePlan struct {
	Path     string
	Envs     []string // sorted, deduplicated
	Required bool
	Source   string
}

// planEncryptedLayers walks every declared environment in spec via
// chain.Expand and returns the unique encrypted layer paths plus
// the env set that reaches each. Plaintext layers are ignored —
// rotation is a sops concern.
//
// Rejects chain specs whose layer entries reference ${ctx.*}: like
// the bundler, rotation is build-time and has no caller context to
// substitute.
func planEncryptedLayers(spec proto.Message) ([]*layerRotatePlan, error) {
	cs, ok := spec.(*chameleonv1.ChainSpec)
	if !ok {
		return nil, fmt.Errorf("rotate-key: spec is %T, want *chameleonv1.ChainSpec", spec)
	}

	// Pre-Expand check: chain.Expand silently skips ctx-templated
	// entries when the caller context is unset, so the rejection
	// has to look at the spec's raw layer entries, not the
	// expanded refs.
	for _, le := range cs.GetLayers() {
		if strings.Contains(le.GetPath(), "${ctx.") {
			return nil, fmt.Errorf("layer entry %q path %q references ${ctx.*}; rotation is build-time and has no runtime context, migrate the value into the chain spec or split into per-env entries", le.GetId(), le.GetPath())
		}
	}

	byPath := make(map[string]*layerRotatePlan)
	for _, envEntry := range cs.GetEnvironments() {
		envName := envEntry.GetName()
		refs, err := chain.Expand(spec, envName, nil)
		if err != nil {
			return nil, fmt.Errorf("expand env %q: %w", envName, err)
		}
		for _, ref := range refs {
			if !ref.Encrypted {
				continue
			}
			plan, ok := byPath[ref.Path]
			if !ok {
				plan = &layerRotatePlan{
					Path:     ref.Path,
					Required: ref.Required,
					Source:   ref.Source,
				}
				byPath[ref.Path] = plan
			}
			plan.Envs = append(plan.Envs, ref.Env)
		}
	}

	plans := make([]*layerRotatePlan, 0, len(byPath))
	for _, p := range byPath {
		sort.Strings(p.Envs)
		p.Envs = slices.Compact(p.Envs)
		plans = append(plans, p)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Path < plans[j].Path })
	return plans, nil
}

// rotateOne reads the encrypted layer at plan.Path, decrypts it,
// resolves the recipient set from the chain spec's per-env
// key_name, and re-encrypts in place. Returns rotateSkipped on the
// soft-skip cases (missing optional file, non-default source, env
// in the plan's env set without key_name) and rotateDone otherwise.
func rotateOne(ctx context.Context, w io.Writer, plan *layerRotatePlan, layersDir string, dryRun bool, dec crypt.Decrypter, enc crypt.Encrypter, spec proto.Message) (rotateStatus, error) {
	if plan.Source != "" && plan.Source != "default" {
		fmt.Fprintf(w, "SKIP\t%s\t(non-default source %q not supported)\n", plan.Path, plan.Source)
		return rotateSkipped, nil
	}

	full := filepath.Join(layersDir, plan.Path)
	raw, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) && !plan.Required {
			fmt.Fprintf(w, "SKIP\t%s\t(optional file missing)\n", plan.Path)
			return rotateSkipped, nil
		}
		return rotateSkipped, fmt.Errorf("read %q: %w", full, err)
	}

	recipients, missing, err := resolveChainRecipients(spec, plan.Envs)
	if err != nil {
		return rotateSkipped, fmt.Errorf("resolve recipients for %q: %w", plan.Path, err)
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "SKIP\t%s\t(envs without key_name in chain spec: %s — set key_name to rotate, or accept current ciphertext)\n",
			plan.Path, strings.Join(missing, ","))
		return rotateSkipped, nil
	}
	if recipients.IsEmpty() {
		fmt.Fprintf(w, "SKIP\t%s\t(no recipients resolved; chain spec env set is empty)\n", plan.Path)
		return rotateSkipped, nil
	}

	if dryRun {
		fmt.Fprintf(w, "WOULD-ROTATE\t%s\t(envs: %s; age recipients: %d)\n",
			plan.Path, strings.Join(plan.Envs, ","), len(recipients.Age))
		return rotateDone, nil
	}

	// Decrypt. crypt.Decrypter wipes the input ciphertext on
	// success; pass a copy so the original bytes stay intact for
	// diagnostics if downstream steps fail.
	rawForDec := append([]byte{}, raw...)
	buf, err := dec.Decrypt(ctx, rawForDec, crypt.FormatProtowire)
	if err != nil {
		return rotateSkipped, fmt.Errorf("decrypt %q: %w", full, err)
	}
	plain := append([]byte{}, buf.Bytes()...)
	buf.Destroy()
	defer wipeBytes(plain)

	ciph, err := enc.Encrypt(ctx, plain, crypt.FormatProtowire, recipients)
	if err != nil {
		return rotateSkipped, fmt.Errorf("re-encrypt %q: %w", full, err)
	}
	// 0o644 mirrors migrate: encrypted bytes are ciphertext, not secret-at-rest.
	if err := os.WriteFile(full, ciph, 0o644); err != nil { //nolint:gosec // see comment above
		return rotateSkipped, fmt.Errorf("write %q: %w", full, err)
	}
	fmt.Fprintf(w, "ROTATED\t%s\t(envs: %s)\n", plan.Path, strings.Join(plan.Envs, ","))
	return rotateDone, nil
}

// resolveChainRecipients walks envs and returns the union of each
// env's effective key_name (resolved through the sops library), the
// list of envs lacking key_name (the caller treats these as a hard
// skip), and any resolution error.
//
// Mirrors the bundler's unionRecipients minus the manifest side
// (rotation doesn't consult BundleManifest). Duplicate recipients
// — typical when child envs inherit a parent's key_name — collapse
// to one entry.
func resolveChainRecipients(spec proto.Message, envs []string) (crypt.Recipients, []string, error) {
	var out crypt.Recipients
	var missing []string
	for _, env := range envs {
		keyName, err := chain.EffectiveKeyName(spec, env)
		if err != nil {
			return crypt.Recipients{}, nil, err
		}
		if keyName == "" {
			missing = append(missing, env)
			continue
		}
		r, err := chsops.RecipientsFromKeyName(keyName)
		if err != nil {
			return crypt.Recipients{}, nil, fmt.Errorf("env %q: %w", env, err)
		}
		out.Age = append(out.Age, r.Age...)
	}
	sort.Strings(out.Age)
	out.Age = slices.Compact(out.Age)
	return out, missing, nil
}
