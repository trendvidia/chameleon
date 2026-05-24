// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secretcatalog

import "strings"

// SchemaPath returns s.Path joined by dots with NO container bracket
// markers — pure field-name path through the schema. This is the form
// bundle manifest allow/deny patterns match against: filtering
// happens at the schema-field level, not at the runtime-data level
// (you whitelist `tenant_keys` as a whole, not `tenant_keys["acme"]`
// for a specific tenant). Example shapes:
//
//	"db_password"
//	"primary.auth_token"
//	"backup_keys"
//	"tenant_keys"
//	"replicas.auth_token"
func (s Site) SchemaPath() string {
	var b strings.Builder
	for i, step := range s.Path {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(step.Name)
	}
	return b.String()
}

// DottedPath returns s.Path in PXF dotted form including container
// markers (`[i]` for repeated, `[key]` / `[key2]` for maps). This is
// the operator-facing diagnostic shape — it mirrors the function
// signature the codegen plugin emits and the secret.Map key shape
// (with placeholder names rather than concrete index expressions).
// Example shapes:
//
//	"db_password"
//	"primary.auth_token"
//	"backup_keys[i]"
//	"tenant_keys[key]"
//	"replicas[i].auth_token"
//	"sites[key].replicas[j].auth_token"
func (s Site) DottedPath() string {
	var b strings.Builder
	repeatedCount := 0
	mapCount := 0
	for i, step := range s.Path {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(step.Name)
		switch step.Container {
		case ContainerRepeated:
			b.WriteByte('[')
			b.WriteString(repeatedParamName(repeatedCount))
			b.WriteByte(']')
			repeatedCount++
		case ContainerMap:
			b.WriteByte('[')
			b.WriteString(mapParamName(mapCount))
			b.WriteByte(']')
			mapCount++
		}
	}
	return b.String()
}

// repeatedParamName / mapParamName mirror the placeholder convention
// the codegen plugin uses for accessor parameter names so DottedPath
// reads the same as the generated function signatures.
func repeatedParamName(n int) string {
	switch n {
	case 0:
		return "i"
	case 1:
		return "j"
	case 2:
		return "k"
	case 3:
		return "l"
	}
	var b strings.Builder
	b.WriteByte('i')
	writeInt(&b, n)
	return b.String()
}

func mapParamName(n int) string {
	if n == 0 {
		return "key"
	}
	var b strings.Builder
	b.WriteString("key")
	writeInt(&b, n+1)
	return b.String()
}

func writeInt(b *strings.Builder, n int) {
	if n < 0 {
		b.WriteByte('-')
		n = -n
	}
	if n >= 10 {
		writeInt(b, n/10)
	}
	b.WriteByte(byte('0' + n%10))
}
