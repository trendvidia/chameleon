// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package pathfmt is the chameleon path-formatting convention used by
// parse.Move (when writing keys into a secret.Map), by interpolate
// (when reporting OnApplied callbacks for repeated/map elements), and
// implicitly mirrored by the protoc-gen-chameleon-go plugin's emitted
// Go expressions.
//
// Paths are dotted: `db_password`, `primary.auth_token`. Repeated
// elements append `[i]`. Map entries append `[key]` where the key is
// quoted for strings (`["acme"]`) and bare for numerics/bools
// (`[42]`, `[true]`).
//
// Three callers must agree byte-for-byte for `secret.Map.Get` lookups
// to work — drift between any two breaks consumer code silently. This
// package is the single source of truth.
package pathfmt

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// MapKey renders a protoreflect.MapKey for path inclusion. String keys
// are double-quoted; numeric and bool keys appear bare.
func MapKey(k protoreflect.MapKey) string {
	switch v := k.Interface().(type) {
	case string:
		return strconv.Quote(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}
