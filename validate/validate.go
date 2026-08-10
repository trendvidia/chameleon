// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package validate runs chameleon's post-merge (pxf.required) check.
// After merge + interpolation + defaults have produced the final
// configuration, [Validate] walks the schema and errors if any
// (pxf.required) field is still absent in the presence union.
//
// "Absent" is checked against the same presence map that defaults
// consults — so a default-filled required field validates as
// satisfied (defaults pass marks the path present after applying).
//
// Scope:
//
//   - Top-level required fields are always checked.
//   - Nested-message required fields are checked only when the parent
//     message is live (has at least one sub-field set by some layer).
//     If a parent is absent, requiring its inner fields is moot —
//     callers should put (pxf.required) on the parent itself if the
//     whole nested message is mandatory.
//   - WKT subtrees are skipped (atomic-value boundary).
//   - Repeated and map fields can be required; the check is "the path
//     was mentioned by some layer", not "the list is non-empty".
package validate

import (
	"errors"
	"fmt"
	"sort"

	"github.com/trendvidia/chameleon/wkt"
	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ErrMissingRequired is returned when one or more (pxf.required) fields
// are absent from the presence union. The error message lists every
// missing path so callers see all problems at once, not one per call.
var ErrMissingRequired = errors.New("validate: required fields missing")

// Validate walks msg and errors if any (pxf.required) field is not
// present in the presence union. Idempotent and read-only.
func Validate(msg proto.Message, present map[string]struct{}) error {
	if msg == nil {
		return fmt.Errorf("validate: nil message")
	}
	if present == nil {
		present = map[string]struct{}{}
	}
	var missing []string
	walk(msg.ProtoReflect(), present, "", &missing)
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%w: %v", ErrMissingRequired, missing)
}

func walk(m protoreflect.Message, present map[string]struct{}, prefix string, missing *[]string) {
	if wkt.Atomic(m.Descriptor()) {
		return
	}
	fields := m.Descriptor().Fields()
	for i, n := 0, fields.Len(); i < n; i++ {
		fd := fields.Get(i)
		path := joinPath(prefix, string(fd.Name()))

		if pxf.IsRequired(fd) {
			if _, ok := present[path]; !ok {
				*missing = append(*missing, path)
			}
		}

		if fd.IsList() || fd.IsMap() {
			continue
		}
		if fd.Kind() == protoreflect.MessageKind {
			md := fd.Message()
			if md == nil || wkt.Atomic(md) {
				continue
			}
			if !m.Has(fd) {
				continue
			}
			walk(m.Mutable(fd).Message(), present, path, missing)
		}
	}
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
