// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// transform is one step a `chameleon migrate` invocation applies to a
// parsed pxf.Document. apply mutates doc and reports whether the
// document changed. Returns (false, nil) when the document already
// satisfies the transform (e.g. add-field on a path that already
// exists). describe returns a one-line summary for dry-run output.
type transform interface {
	apply(doc *pxf.Document) (changed bool, err error)
	describe() string
}

// resolvePath walks dotted segments through nested Blocks under root
// and returns:
//
//   - parent: pointer to the []Entry that should contain the leaf
//     segment. Always non-nil when err == nil.
//   - idx: index in *parent of the Assignment whose Key matches the
//     leaf segment, or -1 if absent.
//   - err: returned when an intermediate Block doesn't exist (the
//     caller cannot proceed without auto-creating it, which v2 does
//     not do).
//
// Empty segs is invalid; the caller is responsible for splitting the
// dotted path. Single-segment paths target a top-level entry.
func resolvePath(root *[]pxf.Entry, segs []string) (parent *[]pxf.Entry, idx int, err error) {
	if len(segs) == 0 {
		return nil, -1, errors.New("empty path")
	}
	cur := root
	for i := 0; i < len(segs)-1; i++ {
		seg := segs[i]
		var found *pxf.Block
		for _, e := range *cur {
			if b, ok := e.(*pxf.Block); ok && b.Name == seg {
				found = b
				break
			}
		}
		if found == nil {
			return nil, -1, fmt.Errorf("intermediate block %q not found (path %q)", seg, strings.Join(segs, "."))
		}
		cur = &found.Entries
	}
	leaf := segs[len(segs)-1]
	for i, e := range *cur {
		if a, ok := e.(*pxf.Assignment); ok && a.Key == leaf {
			return cur, i, nil
		}
	}
	return cur, -1, nil
}

func splitPath(p string) []string { return strings.Split(p, ".") }

// addField appends "key = value" at the resolved location if absent.
// Idempotent: returns (false, nil) when the key already exists.
// Nested paths require all intermediate Blocks to already exist
// (auto-creation lands later).
type addField struct {
	path  string
	value pxf.Value
	raw   string // original CLI value string, for describe()
}

func (t *addField) describe() string {
	return fmt.Sprintf("add-field %s = %s", t.path, t.raw)
}

func (t *addField) apply(doc *pxf.Document) (bool, error) {
	segs := splitPath(t.path)
	parent, idx, err := resolvePath(&doc.Entries, segs)
	if err != nil {
		return false, fmt.Errorf("add-field: %w", err)
	}
	if idx >= 0 {
		return false, nil
	}
	*parent = append(*parent, &pxf.Assignment{
		Key:   segs[len(segs)-1],
		Value: t.value,
	})
	return true, nil
}

// deleteField removes an assignment at the resolved path.
// Idempotent: returns (false, nil) when the key is already absent.
type deleteField struct {
	path string
}

func (t *deleteField) describe() string {
	return fmt.Sprintf("delete-field %s", t.path)
}

func (t *deleteField) apply(doc *pxf.Document) (bool, error) {
	segs := splitPath(t.path)
	parent, idx, err := resolvePath(&doc.Entries, segs)
	if err != nil {
		// Intermediate block missing → field is effectively absent.
		// Treat that as an idempotent skip rather than a hard error so
		// `chameleon migrate --transform=delete-field` can be run
		// against a fleet of layers without per-layer guards.
		return false, nil
	}
	if idx < 0 {
		return false, nil
	}
	*parent = append((*parent)[:idx], (*parent)[idx+1:]...)
	return true, nil
}

// renameField renames an assignment's key. v2 requires from and to to
// share the same parent container — same top-level scope, or same
// dotted prefix for nested paths (e.g. "db.user" → "db.username" is
// allowed; "db.user" → "auth.username" is not). Idempotent on the
// from side: missing source → skip. On the to side, a collision is a
// hard error to avoid silently dropping data.
type renameField struct {
	from string
	to   string
}

func (t *renameField) describe() string {
	return fmt.Sprintf("rename-field %s -> %s", t.from, t.to)
}

func (t *renameField) apply(doc *pxf.Document) (bool, error) {
	fromSegs := splitPath(t.from)
	toSegs := splitPath(t.to)
	if len(fromSegs) != len(toSegs) {
		return false, fmt.Errorf("rename-field: from and to must have the same depth (%q vs %q)", t.from, t.to)
	}
	for i := 0; i < len(fromSegs)-1; i++ {
		if fromSegs[i] != toSegs[i] {
			return false, fmt.Errorf("rename-field: cross-container rename not supported (%q vs %q)", t.from, t.to)
		}
	}
	parent, idx, err := resolvePath(&doc.Entries, fromSegs)
	if err != nil {
		// Source parent missing → source field absent → idempotent skip.
		return false, nil
	}
	if idx < 0 {
		return false, nil
	}
	toLeaf := toSegs[len(toSegs)-1]
	for i, e := range *parent {
		if i == idx {
			continue
		}
		if a, ok := e.(*pxf.Assignment); ok && a.Key == toLeaf {
			return false, fmt.Errorf("rename-field: destination %q already exists; refusing to overwrite", t.to)
		}
	}
	(*parent)[idx].(*pxf.Assignment).Key = toLeaf
	return true, nil
}

// parseTransform builds a transform from the CLI flag set.
//
// Supported forms:
//
//	--transform=add-field    --path=X --value=Y | --int=N | --bool=B
//	--transform=delete-field --path=X
//	--transform=rename-field --from=X --to=Y
func parseTransform(kind, path, from, to, strVal, intVal, boolVal string) (transform, error) {
	switch kind {
	case "":
		return nil, errors.New("--transform is required (add-field | delete-field | rename-field)")
	case "add-field":
		if path == "" {
			return nil, errors.New("add-field: --path is required")
		}
		val, raw, err := pickValue(strVal, intVal, boolVal)
		if err != nil {
			return nil, fmt.Errorf("add-field: %w", err)
		}
		return &addField{path: path, value: val, raw: raw}, nil
	case "delete-field":
		if path == "" {
			return nil, errors.New("delete-field: --path is required")
		}
		if strVal != "" || intVal != "" || boolVal != "" {
			return nil, errors.New("delete-field: --value / --int / --bool are not allowed")
		}
		return &deleteField{path: path}, nil
	case "rename-field":
		if from == "" || to == "" {
			return nil, errors.New("rename-field: --from and --to are required")
		}
		if strVal != "" || intVal != "" || boolVal != "" {
			return nil, errors.New("rename-field: --value / --int / --bool are not allowed")
		}
		if path != "" {
			return nil, errors.New("rename-field: use --from and --to, not --path")
		}
		return &renameField{from: from, to: to}, nil
	default:
		return nil, fmt.Errorf("unknown transform %q; supported: add-field, delete-field, rename-field", kind)
	}
}

// pickValue picks the single non-empty value flag and produces a
// pxf.Value of the corresponding kind. Returns the value + a raw
// string for describe().
func pickValue(strVal, intVal, boolVal string) (pxf.Value, string, error) {
	set := 0
	for _, s := range []string{strVal, intVal, boolVal} {
		if s != "" {
			set++
		}
	}
	switch set {
	case 0:
		return nil, "", errors.New("one of --value / --int / --bool is required")
	case 1:
	default:
		return nil, "", errors.New("exactly one of --value / --int / --bool may be set")
	}
	switch {
	case strVal != "":
		return &pxf.StringVal{Value: strVal}, fmt.Sprintf("%q", strVal), nil
	case intVal != "":
		return &pxf.IntVal{Raw: intVal}, intVal, nil
	case boolVal != "":
		switch strings.ToLower(boolVal) {
		case "true":
			return &pxf.BoolVal{Value: true}, "true", nil
		case "false":
			return &pxf.BoolVal{Value: false}, "false", nil
		default:
			return nil, "", fmt.Errorf("--bool must be true or false; got %q", boolVal)
		}
	}
	return nil, "", errors.New("unreachable")
}
