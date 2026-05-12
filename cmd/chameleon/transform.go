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
// parsed pxf.Document. v1 supports: add-field. delete-field /
// rename-field / set-default land in follow-ups; the interface keeps
// adding them mechanical.
//
// apply mutates doc and reports whether the document changed.
// Returns (false, nil) when the document already satisfies the
// transform (e.g. add-field on a path that already exists, when
// strict=false). describe returns a human-readable one-line summary
// for dry-run output.
type transform interface {
	apply(doc *pxf.Document) (changed bool, err error)
	describe() string
}

// addField inserts a new top-level "key = value" assignment if the
// path doesn't already exist. v1 supports only top-level paths
// (no nested "db.password"); nested-path support is a follow-up.
type addField struct {
	path  string
	value pxf.Value
	// raw is the original CLI value string (for describe()).
	raw string
}

func (t *addField) describe() string {
	return fmt.Sprintf("add-field %s = %s", t.path, t.raw)
}

func (t *addField) apply(doc *pxf.Document) (bool, error) {
	if strings.Contains(t.path, ".") {
		return false, fmt.Errorf("add-field: nested paths not supported in v1; got %q", t.path)
	}
	for _, e := range doc.Entries {
		if a, ok := e.(*pxf.Assignment); ok && a.Key == t.path {
			// Already present; idempotent skip.
			return false, nil
		}
	}
	doc.Entries = append(doc.Entries, &pxf.Assignment{
		Key:   t.path,
		Value: t.value,
	})
	return true, nil
}

// parseTransform builds a transform from the CLI flag set. Returns
// nil + error if the combination of flags is invalid.
//
// Supported forms today:
//
//	--transform=add-field --path=X --value=Y      (string)
//	--transform=add-field --path=X --int=N        (integer)
//	--transform=add-field --path=X --bool=true    (boolean)
func parseTransform(kind, path, strVal, intVal, boolVal string) (transform, error) {
	if path == "" {
		return nil, errors.New("--path is required")
	}
	switch kind {
	case "add-field":
		val, raw, err := pickValue(strVal, intVal, boolVal)
		if err != nil {
			return nil, fmt.Errorf("add-field: %w", err)
		}
		return &addField{path: path, value: val, raw: raw}, nil
	case "":
		return nil, errors.New("--transform is required (e.g. add-field)")
	default:
		return nil, fmt.Errorf("unknown transform %q; supported: add-field", kind)
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
		// fallthrough to dispatch
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
