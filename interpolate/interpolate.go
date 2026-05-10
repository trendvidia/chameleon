// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package interpolate runs chameleon's post-merge string-interpolation
// pass. After all layers have been merged into a single typed message,
// [Apply] scans every user-defined string-scalar field and replaces
// `${field.path}` placeholders with the resolved value of that path.
//
// Scope:
//
//   - Top-level scalar string fields, recursively into nested
//     user-defined messages, repeated string elements, and
//     map<*, string> values.
//   - Field paths use dotted access into the same merged message
//     (e.g., `${endpoints.primary.url}`). No bracket access, no map
//     lookups inside paths.
//   - WKT subtrees (pxf.Secret, Timestamp, BigInt, etc.) are NOT
//     traversed: their inner fields are not interpolated, and they
//     cannot be referenced from inside a `${...}` lookup. This keeps
//     plaintext sensitive values out of the template namespace and
//     keeps the interpolation API decoupled from value-type
//     internals.
//
// Resolution semantics:
//
//   - Templates can reference other templates transitively
//     (`a -> b -> c`). The resolver walks the chain on demand.
//   - Cycles are detected and errored — `a -> b -> a` returns
//     ErrCycle.
//   - Unknown field references error with ErrUnknownReference.
//   - Resolved values are NOT rescanned for more `${...}` tokens —
//     once a placeholder is substituted, the resulting bytes are
//     literal. This prevents injection: a value containing `${`
//     does not become a template.
//   - Empty/unset fields resolve to the empty string. The
//     interpolator does not error on missing values; only on
//     missing field declarations.
//
// Apply runs in the resolver pipeline AFTER merge and BEFORE
// parse.Move, so:
//   - Templates see fully merged values from all layers.
//   - pxf.Secret values are still in the proto message but skipped
//     by the walker, so secrets are never templated and cannot leak
//     into other fields via interpolation.
package interpolate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/trendvidia/chameleon/internal/pathfmt"
	"github.com/trendvidia/chameleon/wkt"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ErrCycle indicates a `${...}` reference graph has a cycle.
var ErrCycle = errors.New("interpolate: cycle in template references")

// ErrUnknownReference indicates a `${field.path}` references a field
// that doesn't exist in the message schema.
var ErrUnknownReference = errors.New("interpolate: unknown reference")

// ErrMalformedTemplate indicates a `${` without a matching `}`.
var ErrMalformedTemplate = errors.New("interpolate: malformed template")

// ErrInvalidReference indicates a path passes through a non-message
// field on the way to its target, or terminates on a non-string field.
var ErrInvalidReference = errors.New("interpolate: invalid reference")

// Option configures Apply.
type Option func(*options)

type options struct {
	onApplied func(path string)
}

// OnApplied registers a callback that fires once per scalar string
// field whose value contained a `${...}` token and was rewritten. The
// path argument is the dotted field path (matching parse.Move's
// format), so callers can correlate against secret.Map and
// pxf.Result.PresentFields. Used by the resolver's Trace plumbing.
func OnApplied(fn func(path string)) Option {
	return func(o *options) { o.onApplied = fn }
}

// Apply scans msg for string-scalar fields containing `${...}` tokens
// and rewrites them in place with resolved values. Safe to call on a
// message with no templates (no-op fast path).
//
// On error, msg may have been partially mutated — Apply does not
// roll back successful per-field rewrites. Callers that need
// transactional semantics should clone before Apply and discard on
// error.
func Apply(msg proto.Message, opts ...Option) error {
	if msg == nil {
		return errors.New("interpolate: nil message")
	}
	o := options{}
	for _, fn := range opts {
		fn(&o)
	}
	w := walker{root: msg.ProtoReflect(), onApplied: o.onApplied}
	return w.message(w.root, "")
}

type walker struct {
	root      protoreflect.Message
	onApplied func(path string)
}

func (w *walker) message(m protoreflect.Message, prefix string) error {
	// WKT subtrees are skipped — their internals are not user-
	// templatable and including them would expose Secret.value to
	// callers of the interpolation namespace.
	if wkt.Atomic(m.Descriptor()) {
		return nil
	}
	desc := m.Descriptor()
	fields := desc.Fields()
	for i, n := 0, fields.Len(); i < n; i++ {
		fd := fields.Get(i)
		if !m.Has(fd) {
			continue
		}
		if err := w.field(m, fd, prefix); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) field(parent protoreflect.Message, fd protoreflect.FieldDescriptor, prefix string) error {
	path := joinFieldPath(prefix, string(fd.Name()))
	switch {
	case fd.IsMap():
		return w.mapField(parent, fd, path)
	case fd.IsList():
		return w.listField(parent, fd, path)
	case fd.Kind() == protoreflect.MessageKind:
		return w.message(parent.Mutable(fd).Message(), path)
	case fd.Kind() == protoreflect.StringKind:
		v := parent.Get(fd).String()
		if !hasTemplate(v) {
			return nil
		}
		resolved, err := w.expand(v, nil)
		if err != nil {
			return err
		}
		parent.Set(fd, protoreflect.ValueOfString(resolved))
		if w.onApplied != nil {
			w.onApplied(path)
		}
		return nil
	}
	return nil
}

func (w *walker) listField(parent protoreflect.Message, fd protoreflect.FieldDescriptor, path string) error {
	list := parent.Mutable(fd).List()
	switch fd.Kind() {
	case protoreflect.MessageKind:
		for i, n := 0, list.Len(); i < n; i++ {
			if err := w.message(list.Get(i).Message(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case protoreflect.StringKind:
		for i, n := 0, list.Len(); i < n; i++ {
			v := list.Get(i).String()
			if !hasTemplate(v) {
				continue
			}
			resolved, err := w.expand(v, nil)
			if err != nil {
				return err
			}
			list.Set(i, protoreflect.ValueOfString(resolved))
			if w.onApplied != nil {
				w.onApplied(fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	return nil
}

func (w *walker) mapField(parent protoreflect.Message, fd protoreflect.FieldDescriptor, path string) error {
	m := parent.Mutable(fd).Map()
	valFd := fd.MapValue()
	switch valFd.Kind() {
	case protoreflect.MessageKind:
		var rangeErr error
		m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			if err := w.message(v.Message(), fmt.Sprintf("%s[%s]", path, pathfmt.MapKey(k))); err != nil {
				rangeErr = err
				return false
			}
			return true
		})
		return rangeErr
	case protoreflect.StringKind:
		// Build the keyset first; mutating during Range is unsafe.
		type kv struct {
			k protoreflect.MapKey
			v string
		}
		var pending []kv
		m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			s := v.String()
			if hasTemplate(s) {
				pending = append(pending, kv{k, s})
			}
			return true
		})
		for _, p := range pending {
			resolved, err := w.expand(p.v, nil)
			if err != nil {
				return err
			}
			m.Set(p.k, protoreflect.ValueOfString(resolved))
			if w.onApplied != nil {
				w.onApplied(fmt.Sprintf("%s[%s]", path, pathfmt.MapKey(p.k)))
			}
		}
	}
	return nil
}

// expand walks a template string, substituting `${...}` tokens with
// resolved field values. stack tracks paths currently being resolved
// for cycle detection.
func (w *walker) expand(template string, stack []string) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(template, "${")
		if i < 0 {
			b.WriteString(template)
			return b.String(), nil
		}
		b.WriteString(template[:i])
		end := strings.IndexByte(template[i+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("%w: unclosed `${` in %q", ErrMalformedTemplate, template[i:])
		}
		end += i + 2
		path := strings.TrimSpace(template[i+2 : end])
		resolved, err := w.resolve(path, stack)
		if err != nil {
			return "", err
		}
		b.WriteString(resolved)
		template = template[end+1:]
	}
}

// resolve looks up path against the root message and recursively
// expands its value if it contains further `${...}` tokens.
func (w *walker) resolve(path string, stack []string) (string, error) {
	for _, p := range stack {
		if p == path {
			return "", fmt.Errorf("%w: %s -> %s", ErrCycle, strings.Join(stack, " -> "), path)
		}
	}
	raw, err := w.lookup(path)
	if err != nil {
		return "", err
	}
	if !hasTemplate(raw) {
		return raw, nil
	}
	return w.expand(raw, append(stack, path))
}

// lookup descends path against w.root and returns the string value at
// the leaf. Errors if any segment is unknown, intermediate segments
// aren't messages, or the leaf isn't a string-scalar.
func (w *walker) lookup(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidReference)
	}
	parts := strings.Split(path, ".")
	cur := w.root
	for i, name := range parts {
		fd := cur.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return "", fmt.Errorf("%w: %q in path %q", ErrUnknownReference, name, path)
		}
		if i == len(parts)-1 {
			if fd.Kind() != protoreflect.StringKind {
				return "", fmt.Errorf("%w: %q is not a string scalar", ErrInvalidReference, path)
			}
			return cur.Get(fd).String(), nil
		}
		if fd.Kind() != protoreflect.MessageKind {
			return "", fmt.Errorf("%w: cannot descend through non-message field %q in %q", ErrInvalidReference, name, path)
		}
		md := fd.Message()
		if md == nil {
			return "", fmt.Errorf("%w: nil message descriptor at %q", ErrInvalidReference, name)
		}
		if wkt.Atomic(md) {
			return "", fmt.Errorf("%w: cannot descend into well-known type %q at %q", ErrInvalidReference, md.FullName(), name)
		}
		cur = cur.Get(fd).Message()
	}
	return "", fmt.Errorf("%w: empty path traversal", ErrInvalidReference)
}

func hasTemplate(s string) bool { return strings.Contains(s, "${") }

// joinFieldPath is the package-private path joiner. Distinct name from
// the lookup-side joinPath used inside expand/resolve so the two
// concerns stay textually separable.
func joinFieldPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}


