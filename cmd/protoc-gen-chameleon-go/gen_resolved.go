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
	gf.P("//")
	gf.P("// prefix scopes secret.Map lookups when this wrapper is reached")
	gf.P("// through a parent's chained accessor (e.g. cfg.Primary() returns")
	gf.P("// an EndpointResolved with prefix=\"primary.\"). Top-level")
	gf.P("// construction via ", ctorName, " sets prefix=\"\".")
	gf.P("type ", typeName, " struct {")
	gf.P("\tm       *", rootIdent)
	gf.P("\tsecrets *", mapIdent)
	gf.P("\tprefix  string")
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

	// Phase 2b PR1: emit chain methods for scalar-message fields whose
	// target is itself wrapper-bearing. Lets consumers chain like
	// cfg.Primary().AuthToken() in place of cfg.PrimaryAuthToken().
	// Both shapes coexist; the chain form is the path forward, the
	// flat form stays for back-compat with Phase 2a consumers.
	emitChainMethods(gf, typeName, root)
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
	gf.P("\treturn r.secrets.Get(r.prefix + ", joinFragmentRefs(gf, frags, strconvPkg), ")")
	gf.P("}")
}

// emitChainMethods emits one chained-access method per scalar-message
// field on parent whose target message contains a Secret transitively.
// The method returns a new wrapper for that target with the path
// prefix extended by the field's proto name. The target's wrapper is
// emitted elsewhere by emitOneResolvedWrapper — Phase 2a already
// emits a wrapper per top-level message, and at the .proto grammar
// level a "nested" message reachable via scalar access IS just
// another top-level message declaration (`message Endpoint { ... }`
// next to AppConfig), so it gets a wrapper too.
//
// Container fields (repeated / map<*, msg>) are deliberately skipped
// in this PR — chained access through them requires accessor helper
// types (Replicas().At(i).AuthToken()) and lands in a follow-up.
// The flat-method emit on the parent (e.g. ReplicasAuthTokenAt) still
// covers those paths.
func emitChainMethods(gf *protogen.GeneratedFile, parentTypeName string, parent *protogen.Message) {
	for _, f := range parent.Fields {
		if !isScalarMessageField(f) {
			continue
		}
		target := f.Message
		if !messageContainsSecret(target) {
			continue
		}
		emitOneChainMethod(gf, parentTypeName, f, target)
		gf.P()
	}
}

// isScalarMessageField reports whether f is a single (not repeated,
// not map) message-typed field. Used by the chain-method emitter to
// pick which subfields get chained access in PR1.
func isScalarMessageField(f *protogen.Field) bool {
	if f.Desc.IsMap() || f.Desc.IsList() {
		return false
	}
	return f.Desc.Kind() == protoreflect.MessageKind && f.Message != nil
}

// messageContainsSecret reports whether msg has a pxf.Secret field
// transitively, with a cycle guard. Used to decide whether to emit a
// chain method (or, in the wrapper emitter, whether to wrap at all).
func messageContainsSecret(msg *protogen.Message) bool {
	return messageContainsSecretWith(msg, map[protoreflect.FullName]bool{})
}

func messageContainsSecretWith(msg *protogen.Message, seen map[protoreflect.FullName]bool) bool {
	if msg == nil {
		return false
	}
	if seen[msg.Desc.FullName()] {
		return false
	}
	seen[msg.Desc.FullName()] = true
	defer delete(seen, msg.Desc.FullName())

	for _, f := range msg.Fields {
		switch {
		case f.Desc.IsMap():
			valFd := f.Desc.MapValue()
			if valFd.Kind() != protoreflect.MessageKind {
				continue
			}
			if isSecret(valFd.Message()) {
				return true
			}
			if mv := mapValueMessage(f); mv != nil && messageContainsSecretWith(mv, seen) {
				return true
			}
		case f.Desc.IsList():
			if f.Desc.Kind() != protoreflect.MessageKind {
				continue
			}
			if isSecret(f.Desc.Message()) {
				return true
			}
			if messageContainsSecretWith(f.Message, seen) {
				return true
			}
		case f.Desc.Kind() == protoreflect.MessageKind:
			if isSecret(f.Desc.Message()) {
				return true
			}
			if messageContainsSecretWith(f.Message, seen) {
				return true
			}
		}
	}
	return false
}

// emitOneChainMethod writes one chained-access method for a single
// scalar-message field whose target is wrapper-bearing.
//
//	func (r *AppConfigResolved) Primary() *EndpointResolved {
//		return &EndpointResolved{
//			m:       r.m.GetPrimary(),
//			secrets: r.secrets,
//			prefix:  r.prefix + "primary.",
//		}
//	}
//
// GetX rather than direct field access: protogen's GetX accessor
// is nil-safe (returns zero-value when the parent is nil) which
// matches the wrapper's "never panic on absent layer" posture.
func emitOneChainMethod(gf *protogen.GeneratedFile, parentTypeName string, f *protogen.Field, target *protogen.Message) {
	methodName := f.GoName
	targetType := target.GoIdent.GoName + "Resolved"
	protoName := string(f.Desc.Name())

	gf.P("// ", methodName, " returns a ", targetType, " scoped to the ",
		strconv.Quote(protoName+"."), " path prefix. Use the typed methods")
	gf.P("// on the returned wrapper to reach Secrets without touching")
	gf.P("// *pxfv1.Secret.Value on the underlying proto.")
	gf.P("func (r *", parentTypeName, ") ", methodName, "() *", targetType, " {")
	gf.P("\treturn &", targetType, "{")
	gf.P("\t\tm:       r.m.Get", methodName, "(),")
	gf.P("\t\tsecrets: r.secrets,")
	gf.P("\t\tprefix:  r.prefix + ", strconv.Quote(protoName+"."), ",")
	gf.P("\t}")
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
