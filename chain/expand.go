// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package chain materializes a [chameleon.v1.ChainSpec] into the
// ordered list of layer files the resolver loads and merges.
//
// Inputs: a parsed ChainSpec, a leaf environment name, and a context
// map (caller-supplied `${ctx.*}` values).
//
// Output: an ordered slice of [LayerRef] in merge order (low → high).
// Each LayerRef is one concrete file to load — a [LayerEntry] in the
// spec may produce zero (template variable unresolved), one (default
// `for_each = ONCE`), or many (`for_each = PER_ENV`, one per ancestor).
//
// Validation performed:
//
//   - Leaf environment exists in the spec.
//   - Environment ancestry is acyclic and references only declared envs.
//   - Caller-supplied context keys are declared in `context_keys`
//     (rejects typos like "regions" vs "region").
//   - Context values match `allowed_values` if specified.
//   - Required context keys are present (or have a default).
//   - Required layer entries resolve to non-empty paths.
//
// The package accepts any [proto.Message] with the
// chameleon.v1.ChainSpec shape — either the typed
// *chameleonv1.ChainSpec (preferred; what [LoadSpec] / [ParseSpec]
// return) or a dynamicpb-backed message with the same field numbers.
// Non-typed inputs round-trip through proto.Marshal/Unmarshal once
// at entry; the per-call cost is one extra alloc cycle.
package chain

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// LayerRef identifies one concrete layer file in the resolved chain.
// The resolver passes Source + Path through the registered Source
// implementation to fetch bytes; encrypted=true triggers the Decrypter.
type LayerRef struct {
	EntryID   string // LayerEntry.id; surfaces in diagnostics + Trace.
	Source    string // Logical source name; "default" if the entry omitted it.
	Path      string // Resolved path with all ${env} / ${ctx.*} substituted.
	Env       string // Environment this materialization is bound to.
	Required  bool   // LayerEntry.required — error if file missing.
	Encrypted bool   // LayerEntry.encrypted — route through Decrypter.
}

// ErrUnknownEnvironment indicates the caller-supplied env (or
// default_environment) is not declared in the spec.
var ErrUnknownEnvironment = errors.New("chain: unknown environment")

// ErrCyclicAncestry indicates the spec's environments form a cycle.
var ErrCyclicAncestry = errors.New("chain: cyclic environment ancestry")

// ErrUnknownContextKey indicates a caller-supplied context key is not
// declared in the spec's context_keys.
var ErrUnknownContextKey = errors.New("chain: unknown context key")

// ErrMissingRequiredContext indicates a required context key was not
// supplied and has no default.
var ErrMissingRequiredContext = errors.New("chain: required context key missing")

// ErrContextValueNotAllowed indicates a caller-supplied context value
// doesn't appear in the declared allowed_values.
var ErrContextValueNotAllowed = errors.New("chain: context value not allowed")

// ErrUnresolvedRequiredLayer indicates a layer entry marked
// `required = true` had a template variable that didn't resolve.
var ErrUnresolvedRequiredLayer = errors.New("chain: required layer has unresolved template variable")

// Options tunes Expand. The zero value enforces strict validation —
// suitable for production. Tests / migration paths may relax.
type Options struct {
	// AllowUnknownContextKeys silently ignores caller-supplied context
	// keys not in the spec's context_keys. Off by default — strict mode
	// catches typos at the cost of some flexibility.
	AllowUnknownContextKeys bool
}

// Expand is shorthand for Options{}.Expand.
func Expand(spec proto.Message, env string, ctx map[string]string) ([]LayerRef, error) {
	return Options{}.Expand(spec, env, ctx)
}

// Expand materializes spec into an ordered list of LayerRefs for the
// given (env, ctx). Empty env falls back to default_environment.
func (o Options) Expand(spec proto.Message, env string, ctx map[string]string) ([]LayerRef, error) {
	if spec == nil {
		return nil, errors.New("chain: nil spec")
	}
	cs, err := toChainSpec(spec)
	if err != nil {
		return nil, err
	}

	// 1. Resolve effective environment.
	if env == "" {
		env = cs.GetDefaultEnvironment()
		if env == "" {
			return nil, errors.New("chain: no environment supplied and no default_environment in spec")
		}
	}

	// 2. Parse environments map.
	envs, declOrder, err := readEnvironments(cs)
	if err != nil {
		return nil, err
	}
	_ = declOrder // reserved for diagnostics ordering

	// 3. Build ancestry chain (root → leaf, leaf last).
	ancestry, err := ancestryChain(envs, env)
	if err != nil {
		return nil, err
	}

	// 4. Normalize and validate context.
	normCtx, err := o.normalizeContext(cs, ctx)
	if err != nil {
		return nil, err
	}

	// 5. Walk layer entries; produce LayerRefs in declared order.
	refs, err := expandLayers(cs, ancestry, normCtx)
	if err != nil {
		return nil, err
	}
	return refs, nil
}

// toChainSpec accepts any proto.Message with the chameleon.v1.ChainSpec
// shape and returns a typed *chameleonv1.ChainSpec. Typed inputs are
// returned directly; dynamicpb-backed messages round-trip through
// proto.Marshal/Unmarshal. Mismatched schemas fail fast on the
// FullName check (cheaper than serialization).
func toChainSpec(spec proto.Message) (*chameleonv1.ChainSpec, error) {
	if cs, ok := spec.(*chameleonv1.ChainSpec); ok {
		return cs, nil
	}
	const want = "chameleon.v1.ChainSpec"
	if got := string(spec.ProtoReflect().Descriptor().FullName()); got != want {
		return nil, fmt.Errorf("chain: expected %s, got %s", want, got)
	}
	data, err := proto.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("chain: marshal spec: %w", err)
	}
	cs := &chameleonv1.ChainSpec{}
	if err := proto.Unmarshal(data, cs); err != nil {
		return nil, fmt.Errorf("chain: unmarshal spec into typed form: %w", err)
	}
	return cs, nil
}

// readEnvironments returns (name → extends) and the declared order of
// names. `extends == ""` means root. Duplicate names error.
func readEnvironments(cs *chameleonv1.ChainSpec) (map[string]string, []string, error) {
	envList := cs.GetEnvironments()
	envs := make(map[string]string, len(envList))
	order := make([]string, 0, len(envList))
	for i, e := range envList {
		name := e.GetName()
		if name == "" {
			return nil, nil, fmt.Errorf("chain: environment at index %d missing name", i)
		}
		if _, dup := envs[name]; dup {
			return nil, nil, fmt.Errorf("chain: duplicate environment %q", name)
		}
		envs[name] = e.GetExtends()
		order = append(order, name)
	}
	if len(envs) == 0 {
		return nil, nil, errors.New("chain: spec declares no environments")
	}
	return envs, order, nil
}

// ancestryChain returns the resolution order for leaf: root first,
// leaf last. Errors on cycles or references to undeclared envs.
func ancestryChain(envs map[string]string, leaf string) ([]string, error) {
	if _, ok := envs[leaf]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEnvironment, leaf)
	}
	var chain []string
	visited := make(map[string]bool)
	cur := leaf
	for cur != "" {
		if visited[cur] {
			return nil, fmt.Errorf("%w involving %q", ErrCyclicAncestry, cur)
		}
		visited[cur] = true
		chain = append([]string{cur}, chain...)
		parent := envs[cur]
		if parent != "" {
			if _, ok := envs[parent]; !ok {
				return nil, fmt.Errorf("chain: %q extends undeclared env %q", cur, parent)
			}
		}
		cur = parent
	}
	return chain, nil
}

// normalizeContext applies defaults, validates allowed_values, and
// (unless relaxed) rejects unknown keys.
func (o Options) normalizeContext(cs *chameleonv1.ChainSpec, caller map[string]string) (map[string]string, error) {
	out := make(map[string]string)
	declared := make(map[string]bool)

	for i, ck := range cs.GetContextKeys() {
		name := ck.GetName()
		if name == "" {
			return nil, fmt.Errorf("chain: context_key at index %d missing name", i)
		}
		declared[name] = true

		defaultVal := ck.GetDefault()
		required := ck.GetRequired()
		allowed := ck.GetAllowedValues()

		val, present := caller[name]
		if !present {
			if required && defaultVal == "" {
				return nil, fmt.Errorf("%w: %q", ErrMissingRequiredContext, name)
			}
			if defaultVal != "" {
				out[name] = defaultVal
			}
			continue
		}

		if len(allowed) > 0 {
			ok := false
			for _, a := range allowed {
				if a == val {
					ok = true
					break
				}
			}
			if !ok {
				return nil, fmt.Errorf("%w: key=%q value=%q", ErrContextValueNotAllowed, name, val)
			}
		}
		out[name] = val
	}

	if !o.AllowUnknownContextKeys {
		for k := range caller {
			if !declared[k] {
				return nil, fmt.Errorf("%w: %q", ErrUnknownContextKey, k)
			}
		}
	} else {
		// Pass through unknown keys for downstream consumers that may
		// want them (e.g. custom path-template extensions).
		for k, v := range caller {
			if !declared[k] {
				out[k] = v
			}
		}
	}

	return out, nil
}

// expandLayers walks LayerEntry list, applying for_each and template
// substitution, producing ordered LayerRefs.
func expandLayers(cs *chameleonv1.ChainSpec, ancestry []string, ctx map[string]string) ([]LayerRef, error) {
	layers := cs.GetLayers()
	if len(layers) == 0 {
		return nil, errors.New("chain: spec declares no layers")
	}

	leaf := ancestry[len(ancestry)-1]
	var refs []LayerRef

	for i, entry := range layers {
		id := entry.GetId()
		path := entry.GetPath()
		required := entry.GetRequired()
		encrypted := entry.GetEncrypted()
		source := entry.GetSource()
		if source == "" {
			source = "default"
		}

		if id == "" {
			return nil, fmt.Errorf("chain: layer entry at index %d missing id", i)
		}
		if path == "" {
			return nil, fmt.Errorf("chain: layer %q missing path", id)
		}

		envIter := []string{leaf}
		if entry.GetForEach() == chameleonv1.LayerEntry_PER_ENV {
			envIter = ancestry
		}

		for _, e := range envIter {
			vars := map[string]string{"env": e}
			for k, v := range ctx {
				vars["ctx."+k] = v
			}
			resolved, ok, err := substitute(path, vars)
			if err != nil {
				return nil, fmt.Errorf("chain: layer %q: %w", id, err)
			}
			if !ok {
				if required {
					return nil, fmt.Errorf("%w: layer=%q env=%q template=%q", ErrUnresolvedRequiredLayer, id, e, path)
				}
				continue
			}
			refs = append(refs, LayerRef{
				EntryID:   id,
				Source:    source,
				Path:      resolved,
				Env:       e,
				Required:  required,
				Encrypted: encrypted,
			})
		}
	}
	return refs, nil
}

// substitute replaces `${name}` and `${ctx.x}` tokens against vars.
// Returns (resolved, true) on success; (_, false) when any referenced
// variable is unset (signals "skip this materialization"); (_, false,
// err) only on malformed templates (unclosed `${`).
func substitute(template string, vars map[string]string) (string, bool, error) {
	var b strings.Builder
	for {
		i := strings.Index(template, "${")
		if i < 0 {
			b.WriteString(template)
			return b.String(), true, nil
		}
		b.WriteString(template[:i])
		end := strings.IndexByte(template[i+2:], '}')
		if end < 0 {
			return "", false, fmt.Errorf("malformed template: unclosed `${` at %q", template[i:])
		}
		end += i + 2
		name := template[i+2 : end]
		val, ok := vars[name]
		if !ok || val == "" {
			return "", false, nil
		}
		b.WriteString(val)
		template = template[end+1:]
	}
}
