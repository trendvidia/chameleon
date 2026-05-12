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
// Design (chameleon#6 — current state):
//
//   - The wrapper keeps an unexported `m *<Message>` and `secrets
//     *secret.Map` plus a `prefix string` for chained accessors.
//     There is no public escape hatch to the raw proto. The wrapper
//     itself implements `proto.Message` by delegating Reset / String
//     / ProtoReflect to the inner message; reflection-driven APIs
//     (`proto.Marshal`, `proto.Equal`, `proto.Clone`) accept the
//     wrapper directly.
//
//   - One method per Secret site, named from the dotted PXF path via
//     wrapperMethodName (PascalCase segments concatenated, with an
//     "At" suffix when any segment is repeated). Example: PXF path
//     `primary.auth_token` → method
//     `(*AppConfigResolved).PrimaryAuthToken() *secret.Secret`. The
//     function-form accessor stays emitted alongside for callers
//     that prefer the standalone shape.
//
//   - Scalar-message subfields whose target message is wrapper-
//     bearing get a chain method that returns the nested wrapper:
//     `cfg.Primary() *EndpointResolved`. Repeated and map containers
//     of wrapper-bearing messages get a `<Parent><Field>Resolved`
//     accessor helper with Len / At(i) / Get(key) returning the
//     nested wrapper for each element. Pxf.Secret leaves in repeated
//     / map shapes get the flat indexed method form
//     (`BackupKeysAt(i int)`, `TenantKeys(key string)`).
//
//   - Every non-Secret, non-chain-bearing field of the underlying
//     message gets a delegating getter on the wrapper. Scalar
//     primitives, bytes, enums, repeated, map, and non-wrapper-
//     bearing message fields are all covered; bodies use the proto-
//     generated nil-safe `GetX()` accessor.
//
//   - A compile-time `var _ proto.Message = (*XResolved)(nil)`
//     assertion is emitted alongside each wrapper so the consumer's
//     `go build` fails immediately if a future emit path forgets one
//     of the three required delegating methods.
func emitResolvedWrappers(gf *protogen.GeneratedFile, sites []secretSite, secretIdent, mapIdent protogen.GoIdent, strconvPkg protogen.GoImportPath) error {
	if len(sites) == 0 {
		return nil
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

	// Validate every wrapper up-front before any code emit. A
	// reserved-name collision (e.g. user proto field named "secrets"
	// or "reset") would produce duplicate methods on the generated
	// wrapper — a hard compile error at the consumer. Surface the
	// collision here with a clear plugin-level error so the user
	// regenerates with a renamed field rather than chasing the
	// duplicate-method symptom at consumer build time.
	for _, k := range rootOrder {
		g := byRoot[k]
		if err := validateNoReservedNameCollisions(g.root, g.sites); err != nil {
			return err
		}
	}

	gf.P("// --- Resolved wrappers (opt-in via --chameleon-go_opt=with_resolved=true) ---")
	gf.P("//")
	gf.P("// One `<Root>Resolved` per top-level message with at least one")
	gf.P("// pxf.Secret in its tree. The wrapper holds the resolved")
	gf.P("// proto + the chameleon resolver's secret.Map together, and")
	gf.P("// exposes typed methods for every Secret reachable from the")
	gf.P("// root. Consumer code that goes through the wrapper IS a")
	gf.P("// proto.Message — pass it directly to proto.Marshal /")
	gf.P("// proto.Equal / proto.Clone / etc.")
	gf.P()

	for _, k := range rootOrder {
		g := byRoot[k]
		emitOneResolvedWrapper(gf, g.root, g.sites, secretIdent, mapIdent, strconvPkg)
	}
	return nil
}

// reservedWrapperMethodNames_FromGoName lists method names the
// codegen emits on every `<Root>Resolved` wrapper that are derived
// from a field's *Go* name (protogen.Field.GoName). Field getters
// and chain methods both use f.GoName; only chameleon-specific
// emitted methods need protection here, because protogen has
// already mangled any field GoName that would collide with a
// proto.Message-interface method (Reset, String — both confirmed
// to become "Reset_", "String_" when used as proto field names).
//
// Empirical note: protogen does NOT mangle `ProtoReflect` or
// `Secrets` (a field literally named `proto_reflect` or `secrets`
// keeps GoName "ProtoReflect" / "Secrets"). The validator catches
// those.
var reservedWrapperMethodNames_FromGoName = map[string]string{
	"ProtoReflect": "the proto.Message ProtoReflect method on the wrapper",
	"Secrets":      "the wrapper's secret.Map accessor",
}

// reservedWrapperMethodNames_FromPath lists method names the codegen
// emits for Secret accessors, derived from the proto-side path via
// wrapperMethodName(). That path doesn't go through protogen's
// GoName mangling, so we need the full set of proto.Message-
// interface methods plus the chameleon-specific Secrets accessor.
// Example collision: `pxf.Secret reset = 1;` would emit
// `func (r *AppConfigResolved) Reset() *secret.Secret { ... }`,
// duplicating the proto.Message Reset() delegation.
var reservedWrapperMethodNames_FromPath = map[string]string{
	"Reset":        "the proto.Message Reset method on the wrapper",
	"String":       "the proto.Message String method on the wrapper",
	"ProtoReflect": "the proto.Message ProtoReflect method on the wrapper",
	"Secrets":      "the wrapper's secret.Map accessor",
}

// validateNoReservedNameCollisions runs against a root's Secret sites
// AND the root's direct fields, computing every method name the emit
// would produce and checking each against the appropriate reserved
// set. Returns an error wrapping the first collision found, with a
// remediation hint so the user can fix the proto rather than chase a
// duplicate-method symptom in the generated file.
func validateNoReservedNameCollisions(root *protogen.Message, sites []secretSite) error {
	check := func(methodName, originDesc string, set map[string]string) error {
		if reason, isReserved := set[methodName]; isReserved {
			return fmt.Errorf(
				"protoc-gen-chameleon-go: %s in %s would shadow %s; "+
					"rename the proto field or opt out of with_resolved mode",
				originDesc, root.Desc.FullName(), reason,
			)
		}
		return nil
	}
	// Secret-method emit uses wrapperMethodName(path) — proto-side
	// names, no protogen mangling. Full reserved set applies.
	for _, s := range sites {
		if s.Root != root {
			continue
		}
		name := wrapperMethodName(s.Path)
		if err := check(name, fmt.Sprintf("Secret method %q (from path %q)", name, dottedPath(s.Path)), reservedWrapperMethodNames_FromPath); err != nil {
			return err
		}
	}
	// Field-getter and chain-method emits use f.GoName — protogen
	// has already mangled the proto.Message-interface collisions
	// (Reset, String). The remaining risk is the chameleon-specific
	// names protogen doesn't know about (ProtoReflect — surprisingly
	// not mangled — and Secrets).
	for _, f := range root.Fields {
		if err := check(f.GoName, fmt.Sprintf("field method %q", f.GoName), reservedWrapperMethodNames_FromGoName); err != nil {
			return err
		}
	}
	return nil
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

	// proto.Message implementation by delegation. The wrapper IS a
	// proto.Message; callers passing the resolved config to
	// proto.Marshal / proto.Equal / proto.Clone / any reflection-
	// driven API hand the wrapper directly. No raw-proto escape
	// hatch is emitted — typed accessors and the field-by-field
	// getters below cover every field shape, and the proto.Message
	// delegation here covers every reflection-driven use case.
	protoreflectMsg := protogen.GoImportPath("google.golang.org/protobuf/reflect/protoreflect").Ident("Message")

	gf.P("// Reset zeroes the underlying proto message. Implements the")
	gf.P("// proto.Message interface; delegates to the inner *", rootIdent.GoName, ".")
	gf.P("func (r *", typeName, ") Reset() { r.m.Reset() }")
	gf.P()
	gf.P("// String renders the underlying proto via its protoc-gen-go")
	gf.P("// implementation. Implements the proto.Message interface.")
	gf.P("// Note: protoc-gen-go's String output may include the inner")
	gf.P("// Secret.value field; chameleon clears those during resolve,")
	gf.P("// so the rendering shows the post-Move empty-value form.")
	gf.P("func (r *", typeName, ") String() string { return r.m.String() }")
	gf.P()
	gf.P("// ProtoReflect returns the underlying proto's reflection view.")
	gf.P("// Implements the proto.Message interface. The wrapper is fully")
	gf.P("// substitutable for *", rootIdent.GoName, " in proto.Marshal,")
	gf.P("// proto.Equal, and other reflection-driven APIs.")
	gf.P("func (r *", typeName, ") ProtoReflect() ", protoreflectMsg, " { return r.m.ProtoReflect() }")
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

	// Phase 2b chain methods. Scalar-message subfields (PR1) get a
	// method returning the nested wrapper; repeated / map containers
	// (PR2) get a method returning a list/map accessor helper whose
	// At(i)/Get(key) returns the nested wrapper with the correct
	// runtime prefix. Both shapes coexist with the existing flat
	// top-level methods for back-compat.
	emitChainMethods(gf, typeName, root, strconvPkg)

	// Field-by-field getters for every non-Secret, non-chain-bearing
	// field on this wrapper's message. Together with the typed
	// Secret methods + chain methods above, this gives consumers a
	// complete typed surface — no raw-proto access is required.
	emitFieldGetters(gf, typeName, root)

	// Compile-time guarantee: the wrapper implements proto.Message.
	// Catches drift if a future emit path forgets one of the three
	// required methods (Reset / String / ProtoReflect). The
	// assertion runs at the CONSUMER's go build, not at this
	// plugin's test compile — generated code is the unit of truth.
	protoMsg := protogen.GoImportPath("google.golang.org/protobuf/proto").Ident("Message")
	gf.P("var _ ", protoMsg, " = (*", typeName, ")(nil)")
	gf.P()
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
// Three cases: scalar message field → chain method returning the
// nested wrapper directly; repeated <UserMsg> → chain method
// returning a list-accessor helper (Len/At); map<K, UserMsg> → chain
// method returning a map-accessor helper (Len/Get). The pxf.Secret
// leaf itself is never wrapper-bearing — typed Secret access already
// goes through the parent's flat-method emit (DbPassword(),
// BackupKeysAt(i), TenantKeys(key)).
func emitChainMethods(gf *protogen.GeneratedFile, parentTypeName string, parent *protogen.Message, strconvPkg protogen.GoImportPath) {
	for _, f := range parent.Fields {
		switch {
		case isScalarMessageField(f):
			target := f.Message
			if !messageContainsSecret(target) {
				continue
			}
			emitOneChainMethod(gf, parentTypeName, f, target)
			gf.P()

		case isRepeatedUserMessageField(f):
			target := f.Message
			if !messageContainsSecret(target) {
				continue
			}
			emitListChainMethod(gf, parentTypeName, parent, f, target, strconvPkg)
			gf.P()

		case isMapUserMessageField(f):
			target := mapValueMessage(f)
			if target == nil || !messageContainsSecret(target) {
				continue
			}
			emitMapChainMethod(gf, parentTypeName, parent, f, target, strconvPkg)
			gf.P()
		}
	}
}

// isRepeatedUserMessageField reports whether f is `repeated UserMsg`
// (not repeated pxf.Secret — those are typed leaves handled by the
// flat-method emit). The wrapper-bearing decision is made by the
// caller via messageContainsSecret on f.Message.
func isRepeatedUserMessageField(f *protogen.Field) bool {
	if !f.Desc.IsList() {
		return false
	}
	if f.Desc.Kind() != protoreflect.MessageKind {
		return false
	}
	if isSecret(f.Desc.Message()) {
		return false
	}
	return f.Message != nil
}

// isMapUserMessageField reports whether f is `map<K, UserMsg>` (not
// map<K, pxf.Secret>). The wrapper-bearing decision is made by the
// caller via messageContainsSecret on mapValueMessage(f).
func isMapUserMessageField(f *protogen.Field) bool {
	if !f.Desc.IsMap() {
		return false
	}
	valFd := f.Desc.MapValue()
	if valFd.Kind() != protoreflect.MessageKind {
		return false
	}
	if isSecret(valFd.Message()) {
		return false
	}
	return f.Message != nil
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

// emitListChainMethod writes both the per-parent-per-field accessor
// helper type AND the parent-wrapper method returning it, for a
// `repeated <UserMsg>` field where UserMsg is wrapper-bearing:
//
//	type AppConfigReplicasResolved struct {
//	    m       []*Endpoint
//	    secrets *secret.Map
//	    prefix  string  // inherits parent's prefix
//	}
//
//	func (r *AppConfigReplicasResolved) Len() int { return len(r.m) }
//
//	func (r *AppConfigReplicasResolved) At(i int) *EndpointResolved {
//	    if i < 0 || i >= len(r.m) {
//	        return nil  // out-of-range yields nil; matches the
//	                    // chameleon-wide "absent never panics" posture.
//	    }
//	    return &EndpointResolved{
//	        m:       r.m[i],
//	        secrets: r.secrets,
//	        prefix:  r.prefix + "replicas[" + strconv.Itoa(i) + "].",
//	    }
//	}
//
//	func (r *AppConfigResolved) Replicas() *AppConfigReplicasResolved {
//	    return &AppConfigReplicasResolved{
//	        m:       r.m.GetReplicas(),
//	        secrets: r.secrets,
//	        prefix:  r.prefix,
//	    }
//	}
//
// One helper type per (parent, field) pair. Two parents that both
// have a `replicas []*Endpoint` get distinct helper types — slight
// duplication, but keeps the type names parent-scoped and avoids
// generic-Go gymnastics for a feature consumers don't ask for.
func emitListChainMethod(gf *protogen.GeneratedFile, parentTypeName string, parent *protogen.Message, f *protogen.Field, target *protogen.Message, strconvPkg protogen.GoImportPath) {
	helperType := parent.GoIdent.GoName + f.GoName + "Resolved"
	targetType := target.GoIdent.GoName + "Resolved"
	protoName := string(f.Desc.Name())
	targetIdent := target.GoIdent
	itoaIdent := strconvPkg.Ident("Itoa")

	// Helper type.
	gf.P("// ", helperType, " is the chained-access view of ",
		parentTypeName, ".", f.GoName, ". Len reports the element count;")
	gf.P("// At(i) returns the ", targetType, " for the i-th element with")
	gf.P("// a prefix scoped to ", strconv.Quote(protoName+"[i]."), " so")
	gf.P("// downstream Secret lookups produce the canonical PXF path.")
	gf.P("type ", helperType, " struct {")
	gf.P("\tm       []*", targetIdent)
	gf.P("\tsecrets *", protogen.GoImportPath(pluginState.secretPkg).Ident("Map"))
	gf.P("\tprefix  string")
	gf.P("}")
	gf.P()
	gf.P("// Len returns the element count.")
	gf.P("func (r *", helperType, ") Len() int { return len(r.m) }")
	gf.P()
	gf.P("// At returns the ", targetType, " at index i, or nil for an")
	gf.P("// out-of-range index. Matches chameleon's \"absent never panics\"")
	gf.P("// posture — callers checking Len first never see nil.")
	gf.P("func (r *", helperType, ") At(i int) *", targetType, " {")
	gf.P("\tif i < 0 || i >= len(r.m) {")
	gf.P("\t\treturn nil")
	gf.P("\t}")
	gf.P("\treturn &", targetType, "{")
	gf.P("\t\tm:       r.m[i],")
	gf.P("\t\tsecrets: r.secrets,")
	gf.P("\t\tprefix:  r.prefix + ", strconv.Quote(protoName+"["), " + ", itoaIdent, "(i) + ", strconv.Quote("]."), ",")
	gf.P("\t}")
	gf.P("}")
	gf.P()

	// Parent method.
	gf.P("// ", f.GoName, " returns a chained accessor over the ",
		strconv.Quote(protoName), " repeated field. Use Len/At on the")
	gf.P("// returned helper to reach Secrets without touching")
	gf.P("// *pxfv1.Secret.Value on the underlying proto.")
	gf.P("func (r *", parentTypeName, ") ", f.GoName, "() *", helperType, " {")
	gf.P("\treturn &", helperType, "{")
	gf.P("\t\tm:       r.m.Get", f.GoName, "(),")
	gf.P("\t\tsecrets: r.secrets,")
	gf.P("\t\tprefix:  r.prefix,")
	gf.P("\t}")
	gf.P("}")
}

// emitMapChainMethod writes both the per-parent-per-field accessor
// helper type AND the parent-wrapper method returning it, for a
// `map<K, UserMsg>` field where UserMsg is wrapper-bearing. Mirrors
// emitListChainMethod but with Get(key) instead of At(i) and the
// proper runtime key-formatting expression keyed off the proto map
// key kind.
func emitMapChainMethod(gf *protogen.GeneratedFile, parentTypeName string, parent *protogen.Message, f *protogen.Field, target *protogen.Message, strconvPkg protogen.GoImportPath) {
	helperType := parent.GoIdent.GoName + f.GoName + "Resolved"
	targetType := target.GoIdent.GoName + "Resolved"
	protoName := string(f.Desc.Name())
	targetIdent := target.GoIdent

	keyFd := f.Desc.MapKey()
	keyGoType := goTypeForMapKey(keyFd.Kind())
	keyFmtExpr := mapKeyRuntimeExpr(gf, keyFd.Kind(), "key", strconvPkg)

	// Helper type.
	gf.P("// ", helperType, " is the chained-access view of ",
		parentTypeName, ".", f.GoName, ". Len reports the entry count;")
	gf.P("// Get(key) returns the ", targetType, " for that key with a")
	gf.P("// prefix scoped to ", strconv.Quote(protoName+"[key]."), " so")
	gf.P("// downstream Secret lookups produce the canonical PXF path.")
	gf.P("type ", helperType, " struct {")
	gf.P("\tm       map[", keyGoType, "]*", targetIdent)
	gf.P("\tsecrets *", protogen.GoImportPath(pluginState.secretPkg).Ident("Map"))
	gf.P("\tprefix  string")
	gf.P("}")
	gf.P()
	gf.P("// Len returns the entry count.")
	gf.P("func (r *", helperType, ") Len() int { return len(r.m) }")
	gf.P()
	gf.P("// Get returns the ", targetType, " at the given key, or nil")
	gf.P("// if the key is absent. Matches chameleon's \"absent never")
	gf.P("// panics\" posture.")
	gf.P("func (r *", helperType, ") Get(key ", keyGoType, ") *", targetType, " {")
	gf.P("\tv, ok := r.m[key]")
	gf.P("\tif !ok {")
	gf.P("\t\treturn nil")
	gf.P("\t}")
	gf.P("\treturn &", targetType, "{")
	gf.P("\t\tm:       v,")
	gf.P("\t\tsecrets: r.secrets,")
	gf.P("\t\tprefix:  r.prefix + ", strconv.Quote(protoName+"["), " + ", keyFmtExpr, " + ", strconv.Quote("]."), ",")
	gf.P("\t}")
	gf.P("}")
	gf.P()

	// Parent method.
	gf.P("// ", f.GoName, " returns a chained accessor over the ",
		strconv.Quote(protoName), " map field. Use Len/Get on the")
	gf.P("// returned helper to reach Secrets without touching")
	gf.P("// *pxfv1.Secret.Value on the underlying proto.")
	gf.P("func (r *", parentTypeName, ") ", f.GoName, "() *", helperType, " {")
	gf.P("\treturn &", helperType, "{")
	gf.P("\t\tm:       r.m.Get", f.GoName, "(),")
	gf.P("\t\tsecrets: r.secrets,")
	gf.P("\t\tprefix:  r.prefix,")
	gf.P("\t}")
	gf.P("}")
}

// mapKeyRuntimeExpr returns the Go source for converting a typed map
// key variable into its path-segment string at runtime. Mirrors the
// per-kind dispatch in joinFragmentRefs so map chain accessors match
// the function-form path scheme byte-for-byte.
func mapKeyRuntimeExpr(gf *protogen.GeneratedFile, kind protoreflect.Kind, paramName string, strconvPkg protogen.GoImportPath) string {
	switch kind {
	case protoreflect.StringKind:
		return gf.QualifiedGoIdent(strconvPkg.Ident("Quote")) + "(" + paramName + ")"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return gf.QualifiedGoIdent(strconvPkg.Ident("FormatInt")) + "(int64(" + paramName + "), 10)"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return gf.QualifiedGoIdent(strconvPkg.Ident("FormatInt")) + "(" + paramName + ", 10)"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return gf.QualifiedGoIdent(strconvPkg.Ident("FormatUint")) + "(uint64(" + paramName + "), 10)"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return gf.QualifiedGoIdent(strconvPkg.Ident("FormatUint")) + "(" + paramName + ", 10)"
	case protoreflect.BoolKind:
		return gf.QualifiedGoIdent(strconvPkg.Ident("FormatBool")) + "(" + paramName + ")"
	default:
		return gf.QualifiedGoIdent(strconvPkg.Ident("Itoa")) + "(" + paramName + ")"
	}
}

// emitFieldGetters writes one delegating getter per field on parent
// where the field is neither a pxf.Secret leaf (already covered by
// the typed Secret-method emit) nor a wrapper-bearing message
// container (already covered by the chain-method emit). Lets
// consumers reach scalar primitives, bytes, enums, non-Secret
// messages, and non-Secret message containers through the typed
// wrapper surface — the wrapper has no raw-proto escape hatch by
// design.
//
//	func (r *AppConfigResolved) BaseUrl() string { return r.m.GetBaseUrl() }
//	func (r *AppConfigResolved) MaxReplicas() int32 { return r.m.GetMaxReplicas() }
//	func (r *AppConfigResolved) Metadata() *Metadata { return r.m.GetMetadata() }
//	func (r *AppConfigResolved) Labels() map[string]string { return r.m.GetLabels() }
//
// The plugin defers to protoc-gen-go's nil-safe GetX accessors so
// the wrapper never panics on absent fields, matching the
// chameleon-wide "absent never panics" posture.
func emitFieldGetters(gf *protogen.GeneratedFile, parentTypeName string, parent *protogen.Message) {
	for _, f := range parent.Fields {
		if isSecretField(f) || isWrapperBearingField(f) {
			continue
		}
		emitOneFieldGetter(gf, parentTypeName, f)
		gf.P()
	}
}

// isSecretField reports whether f's terminal type is pxf.Secret in
// any shape (scalar / repeated / map<*, Secret>). Those are leaves
// covered by the typed Secret-accessor methods (DbPassword,
// BackupKeysAt, TenantKeys) — emitting a generic getter for them
// would be a footgun (return *pxfv1.Secret with .Value exposed
// again).
func isSecretField(f *protogen.Field) bool {
	switch {
	case f.Desc.IsMap():
		vfd := f.Desc.MapValue()
		return vfd.Kind() == protoreflect.MessageKind && isSecret(vfd.Message())
	case f.Desc.IsList():
		return f.Desc.Kind() == protoreflect.MessageKind && isSecret(f.Desc.Message())
	case f.Desc.Kind() == protoreflect.MessageKind:
		return isSecret(f.Desc.Message())
	}
	return false
}

// isWrapperBearingField reports whether f targets a user message
// (scalar / repeated / map) that contains a Secret transitively.
// Those fields are covered by the chain-method emit
// (emitChainMethods): scalar → Primary() *EndpointResolved;
// repeated → Replicas() *AppConfigReplicasResolved with At(i);
// map → Sites() *AppConfigSitesResolved with Get(key).
func isWrapperBearingField(f *protogen.Field) bool {
	switch {
	case isScalarMessageField(f):
		return messageContainsSecret(f.Message)
	case isRepeatedUserMessageField(f):
		return messageContainsSecret(f.Message)
	case isMapUserMessageField(f):
		return messageContainsSecret(mapValueMessage(f))
	}
	return false
}

// emitOneFieldGetter writes a single delegating getter. The method
// name is the protoc-gen-go GoName (already PascalCase) and the body
// is `return r.m.Get<GoName>()` which is nil-safe on the proto type.
func emitOneFieldGetter(gf *protogen.GeneratedFile, parentTypeName string, f *protogen.Field) {
	goType := goTypeForField(gf, f)
	protoName := strconv.Quote(string(f.Desc.Name()))
	gf.P("// ", f.GoName, " returns the resolved ", protoName, " field.")
	gf.P("// Nil-safe via the proto-generated Get", f.GoName, " accessor.")
	gf.P("func (r *", parentTypeName, ") ", f.GoName, "() ", goType, " {")
	gf.P("\treturn r.m.Get", f.GoName, "()")
	gf.P("}")
}

// goTypeForField renders the Go type the field's getter should
// return. Mirrors protoc-gen-go's emit shape so the wrapper's
// getter signatures match what consumers expect from a vanilla
// proto type. For map / list fields, the element/value type is
// recursively rendered with the same rules.
func goTypeForField(gf *protogen.GeneratedFile, f *protogen.Field) string {
	switch {
	case f.Desc.IsMap():
		keyKind := f.Desc.MapKey().Kind()
		keyGo := goPrimitiveType(keyKind)
		valFd := f.Desc.MapValue()
		valGo := ""
		switch valFd.Kind() {
		case protoreflect.MessageKind:
			vm := mapValueMessage(f)
			if vm != nil {
				valGo = "*" + gf.QualifiedGoIdent(vm.GoIdent)
			} else {
				valGo = "*" + string(valFd.Message().Name())
			}
		case protoreflect.EnumKind:
			// map<*, enum> is a rare shape (proto doesn't even
			// support it everywhere). Render as int32 — the closest
			// well-typed default; if a real use case lands, swap
			// for the enum's GoIdent via a similar protogen lookup
			// to mapValueMessage.
			valGo = "int32"
		default:
			valGo = goPrimitiveType(valFd.Kind())
		}
		return "map[" + keyGo + "]" + valGo

	case f.Desc.IsList():
		return "[]" + scalarOrUserGoType(gf, f.Desc, f.Message, f.Enum)

	default:
		return scalarOrUserGoType(gf, f.Desc, f.Message, f.Enum)
	}
}

// scalarOrUserGoType renders a single Go type for one field-element
// kind. Message kind returns "*FQName"; enum kind returns the
// enum's FQName; primitives return the canonical Go type name.
func scalarOrUserGoType(gf *protogen.GeneratedFile, fd protoreflect.FieldDescriptor, msg *protogen.Message, enum *protogen.Enum) string {
	switch fd.Kind() {
	case protoreflect.MessageKind:
		if msg != nil {
			return "*" + gf.QualifiedGoIdent(msg.GoIdent)
		}
		return "*" + string(fd.Message().Name())
	case protoreflect.EnumKind:
		if enum != nil {
			return gf.QualifiedGoIdent(enum.GoIdent)
		}
		return "int32"
	default:
		return goPrimitiveType(fd.Kind())
	}
}

// goPrimitiveType returns the canonical Go type name for a
// protobuf primitive kind. Bytes → []byte; everything else maps
// to a Go scalar. Caller is responsible for not asking about
// Message/Enum/Group kinds.
func goPrimitiveType(kind protoreflect.Kind) string {
	switch kind {
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	case protoreflect.BytesKind:
		return "[]byte"
	}
	return "interface{}"
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
