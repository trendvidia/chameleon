// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// emitResolvedWrappers writes a `<Root>Resolved` wrapper per top-level
// message that has at least one secretSite. The wrapper centralizes
// the (`*Message`, `*secret.Map`) pair and exposes typed-method
// accessors for every Secret reachable from that root.
//
// Design — Phase 2a of chameleon#6:
//
//   - The wrapper keeps an unexported `m *<Message>` and `secrets
//     *secret.Map`. The only escape hatch to the raw proto is
//     `Underlying() *<Message>` — chameleon-lint flags
//     `cfg.Underlying().X.Value` reads the same way it flags direct
//     `cfg.X.Value` reads, so the wrapper narrows the surface where
//     the footgun is reachable.
//
//   - One method per Secret site, named `<RootName>` stripped from
//     the function-style accessor (which the plugin still emits
//     alongside for callers that prefer the standalone form).
//     Example: function `AppConfigSecret_Primary_AuthToken(s, ...)` →
//     method `(*AppConfigResolved).PrimaryAuthToken(...)`.
//
//   - Repeated and map Secret containers carry their index/key
//     parameter on the method, mirroring the function-form
//     signature: `BackupKeyAt(i int) *secret.Secret`,
//     `TenantKey(key string) *secret.Secret`.
//
// What this PR does NOT do (deliberate scope; see CHANGELOG +
// chameleon#6 for the full picture):
//
//   - The wrapper does not implement `proto.Message`. For
//     serialization, callers still reach for the underlying proto
//     via `Underlying()`. Delegating ProtoReflect to the inner
//     message is a tractable follow-up but not a security gate.
//
//   - Non-Secret scalar / message / repeated / map fields aren't
//     mirrored as wrapper methods. Reading them goes through
//     `Underlying()`. A future PR could emit field-by-field getters
//     so `Underlying()` becomes unnecessary; the security win is
//     already in place once Secret access goes through the wrapper.
func emitResolvedWrappers(gf *protogen.GeneratedFile, sites []secretSite, secretIdent, mapIdent protogen.GoIdent, strconvPkg protogen.GoImportPath) {
	if len(sites) == 0 {
		return
	}

	// Group sites by root message; iterate roots in a deterministic
	// order (proto declaration order is preserved by the original
	// site collection, but use the GoIdent name as a stable secondary
	// for codegen determinism across protogen versions).
	type rootGroup struct {
		root  *protogen.Message
		sites []secretSite
	}
	byRoot := make(map[string]*rootGroup)
	var rootOrder []string
	for _, s := range sites {
		k := s.Root.GoIdent.GoName
		if g, ok := byRoot[k]; ok {
			g.sites = append(g.sites, s)
			continue
		}
		byRoot[k] = &rootGroup{root: s.Root, sites: []secretSite{s}}
		rootOrder = append(rootOrder, k)
	}
	sort.Strings(rootOrder)

	gf.P("// --- Resolved wrappers (opt-in via --chameleon-go_opt=with_resolved=true) ---")
	gf.P("//")
	gf.P("// One `<Root>Resolved` per top-level message with at least one")
	gf.P("// pxf.Secret in its tree. The wrapper holds the resolved")
	gf.P("// proto + the chameleon resolver's secret.Map together, and")
	gf.P("// exposes typed methods for every Secret reachable from the")
	gf.P("// root. Consumer code that goes through the wrapper avoids")
	gf.P("// touching `*pxfv1.Secret.Value` entirely; access non-Secret")
	gf.P("// fields via `Underlying()`.")
	gf.P()

	for _, k := range rootOrder {
		g := byRoot[k]
		emitOneResolvedWrapper(gf, g.root, g.sites, secretIdent, mapIdent, strconvPkg)
	}
}

func emitOneResolvedWrapper(gf *protogen.GeneratedFile, root *protogen.Message, sites []secretSite, secretIdent, mapIdent protogen.GoIdent, strconvPkg protogen.GoImportPath) {
	typeName := root.GoIdent.GoName + "Resolved"
	ctorName := "New" + typeName
	rootIdent := root.GoIdent

	gf.P("// ", typeName, " bundles a resolved ", rootIdent.GoName, " with the")
	gf.P("// chameleon resolver's secret.Map. Each method below returns a")
	gf.P("// *secret.Secret keyed by the dotted PXF path the parse.Move")
	gf.P("// walker assigned at resolve time — no direct access to")
	gf.P("// pxfv1.Secret.Value is necessary.")
	gf.P("type ", typeName, " struct {")
	gf.P("\tm       *", rootIdent)
	gf.P("\tsecrets *", mapIdent)
	gf.P("}")
	gf.P()

	gf.P("// ", ctorName, " constructs a wrapper around an already-resolved")
	gf.P("// ", rootIdent.GoName, " and the chameleon resolver's secret.Map.")
	gf.P("// Typical use:")
	gf.P("//")
	gf.P("// \tres, _ := r.Resolve(ctx, req)")
	gf.P("// \tdefer res.Destroy()")
	gf.P("// \tcfg := pkg.", ctorName, "(res.Config, res.Secrets)")
	gf.P("// \tbuf, _ := cfg.<SecretAccessor>().Open()")
	gf.P("// \tdefer buf.Destroy()")
	gf.P("func ", ctorName, "(m *", rootIdent, ", secrets *", mapIdent, ") *", typeName, " {")
	gf.P("\treturn &", typeName, "{m: m, secrets: secrets}")
	gf.P("}")
	gf.P()

	gf.P("// Underlying returns the raw resolved proto message. Use it to")
	gf.P("// access non-Secret fields. Reading `Underlying().<Secret>.Value`")
	gf.P("// is the chameleon-lint footgun this wrapper is meant to steer")
	gf.P("// you away from; use the typed methods below instead.")
	gf.P("func (r *", typeName, ") Underlying() *", rootIdent, " { return r.m }")
	gf.P()

	gf.P("// Secrets returns the underlying secret.Map. Exposed for callers")
	gf.P("// that need to iterate every resolved secret (e.g. telemetry).")
	gf.P("// Routine access to a specific secret should go through the")
	gf.P("// typed methods below.")
	gf.P("func (r *", typeName, ") Secrets() *", mapIdent, " { return r.secrets }")
	gf.P()

	for _, s := range sites {
		emitWrapperMethod(gf, typeName, s, secretIdent, strconvPkg)
		gf.P()
	}
}

// emitWrapperMethod writes one accessor method on the wrapper for
// site s. Naming convention:
//
//   - Scalar Secret at top of root: method name is the path
//     converted to PascalCase ("DbPassword"). No params.
//   - Nested scalar Secret: dotted path joined as PascalCase
//     ("PrimaryAuthToken" for primary.auth_token).
//   - Repeated Secret (path ends with a repeated container): name
//     gets an "At" suffix and takes an `i int` param
//     ("BackupKeyAt(i int)" for `backup_keys`). The trailing plural
//     "s" is dropped if present.
//   - Map<K, Secret>: no "At" suffix; takes a typed key param
//     ("TenantKey(key string)" for `tenant_keys`).
//   - Repeated container with a nested scalar Secret beneath it
//     (`endpoints[i].auth_token`): name combines container and leaf
//     ("EndpointsAuthTokenAt(i int)"). Index param.
func emitWrapperMethod(gf *protogen.GeneratedFile, typeName string, s secretSite, secretIdent protogen.GoIdent, strconvPkg protogen.GoImportPath) {
	params := assignParams(s.Path)
	frags := buildPathFragments(s.Path, params)
	docPath := dottedPath(s.Path)

	methodName := wrapperMethodName(s.Path)

	gf.P("// ", methodName, " returns the *secret.Secret at PXF path ", strconv.Quote(docPath), ",")
	gf.P("// or nil if absent or destroyed.")
	gf.P("func (r *", typeName, ") ", methodName, "(", paramsList(params), ") *", secretIdent, " {")
	gf.P("\treturn r.secrets.Get(", joinFragmentRefs(gf, frags, strconvPkg), ")")
	gf.P("}")
}

// wrapperMethodName builds the method name from a path.
//
// Convention:
//   - PascalCase each path segment, concatenate with no separator
//     (`primary.auth_token` → `PrimaryAuthToken`).
//   - If any segment along the path is a repeated container, append
//     an "At" suffix to signal the method takes one or more index
//     parameters. The "At" applies once regardless of how many
//     repeated steps the path has (`replicas[i].backup_tokens[j]` →
//     `ReplicasBackupTokensAt(i, j int)`, one suffix even though two
//     indices). Map-only paths get no suffix; their typed key param
//     already reads as the indexer.
//   - No singularization heuristic — `backup_keys` stays
//     `BackupKeysAt`, `tenant_keys` stays `TenantKeys`. Mechanical
//     transform beats English-aware guessing that would mistreat
//     non-English plurals.
func wrapperMethodName(path []pathStep) string {
	if len(path) == 0 {
		return ""
	}
	var b strings.Builder
	hasRepeated := false
	for _, step := range path {
		b.WriteString(toCamel(step.Name))
		if step.Container == containerRepeated {
			hasRepeated = true
		}
	}
	if hasRepeated {
		b.WriteString("At")
	}
	return b.String()
}

// formatSitePath returns a human-readable indented breakdown of a
// site's path. Used only by debug logging during plugin development.
// Kept here (rather than walk.go) to avoid bloating the AST surface.
func formatSitePath(s secretSite) string {
	parts := make([]string, 0, len(s.Path))
	for _, step := range s.Path {
		switch step.Container {
		case containerRepeated:
			parts = append(parts, fmt.Sprintf("%s[i]", step.Name))
		case containerMap:
			parts = append(parts, fmt.Sprintf("%s[k]", step.Name))
		default:
			parts = append(parts, step.Name)
		}
	}
	return strings.Join(parts, ".")
}

// silence unused-warning for the debug helper when the plugin is
// compiled without test verbose logging.
var _ = formatSitePath

// silence unused-warning if protoreflect is otherwise unreferenced
// after future refactors trim the wrapper emitter.
var _ protoreflect.Kind
