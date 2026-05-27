// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// allowAllAttrs is the attribute-selector init that matches what
// Compile produces when a manifest sets no attribute_* fields:
// non-secrets pass through unchanged. Tests that exercise only
// secret-side narrowing use this so the new attribute filter
// doesn't accidentally clear non-secret structural fields.
var allowAllAttrs = selector{defaultPolicy: chameleonv1.BundleManifest_ALLOW_ALL}

// buildTestDescriptor synthesizes a schema with all four
// Secret-bearing shapes the narrow walker needs to cover:
//
//	message Secret { string value = 1; ... }     // FullName: pxf.Secret (matches WKT)
//	message Endpoint {
//	  string url = 1;
//	  Secret auth_token = 2;
//	}
//	message AppConfig {
//	  Secret db_password = 1;                    // scalar Secret
//	  Endpoint primary = 2;                      // nested message containing Secret
//	  repeated Secret backup_keys = 3;           // repeated Secret
//	  map<string, Secret> tenant_keys = 4;       // map<K, Secret>
//	}
//
// Package is "pxf" so the local Secret message gets FullName
// "pxf.Secret" — matching secretcatalog.SecretFullName so the
// narrow walker recognizes it as the WKT.
func buildTestDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	syntax := "proto3"
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test.proto"),
		Package: proto.String("pxf"),
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Secret"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("value"), Number: proto.Int32(1), Type: &str, Label: &optional},
					{Name: proto.String("hint"), Number: proto.Int32(2), Type: &str, Label: &optional},
					{Name: proto.String("fingerprint"), Number: proto.Int32(3), Type: &str, Label: &optional},
				},
			},
			{
				Name: proto.String("Endpoint"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("url"), Number: proto.Int32(1), Type: &str, Label: &optional},
					{Name: proto.String("auth_token"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
				},
			},
			{
				Name: proto.String("AppConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("db_password"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
					{Name: proto.String("primary"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Endpoint"), Label: &optional},
					{Name: proto.String("backup_keys"), Number: proto.Int32(3), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &repeated},
					{Name: proto.String("tenant_keys"), Number: proto.Int32(4), Type: &msg, TypeName: proto.String(".pxf.AppConfig.TenantKeysEntry"), Label: &repeated},
				},
				NestedType: []*descriptorpb.DescriptorProto{mapEntry("TenantKeysEntry", str, msg, ".pxf.Secret")},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	appConfig := fd.Messages().ByName("AppConfig")
	if appConfig == nil {
		t.Fatal("AppConfig descriptor not found")
	}
	return appConfig
}

// mapEntry constructs the synthetic entry message protobuf uses to
// represent `map<K, V>` — a 2-field message tagged as a map entry.
func mapEntry(name string, keyType, valType descriptorpb.FieldDescriptorProto_Type, valTypeName string) *descriptorpb.DescriptorProto {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	return &descriptorpb.DescriptorProto{
		Name: proto.String(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("key"), Number: proto.Int32(1), Type: &keyType, Label: &optional},
			{Name: proto.String("value"), Number: proto.Int32(2), Type: &valType, TypeName: proto.String(valTypeName), Label: &optional},
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
}

// populated returns an AppConfig with every Secret-bearing field
// set, so tests can assert which specific fields Narrow cleared.
func populated(t *testing.T, md protoreflect.MessageDescriptor) *dynamicpb.Message {
	t.Helper()
	cfg := dynamicpb.NewMessage(md)
	fields := md.Fields()

	// db_password = Secret{value: "shh"}
	dbField := fields.ByName("db_password")
	dbSecret := dynamicpb.NewMessage(dbField.Message())
	dbSecret.Set(dbSecret.Descriptor().Fields().ByName("value"), protoreflect.ValueOfString("shh"))
	cfg.Set(dbField, protoreflect.ValueOfMessage(dbSecret))

	// primary = Endpoint{url: "x", auth_token: Secret{value: "tok"}}
	primField := fields.ByName("primary")
	endpoint := dynamicpb.NewMessage(primField.Message())
	endpoint.Set(endpoint.Descriptor().Fields().ByName("url"), protoreflect.ValueOfString("x"))
	authField := endpoint.Descriptor().Fields().ByName("auth_token")
	authSecret := dynamicpb.NewMessage(authField.Message())
	authSecret.Set(authSecret.Descriptor().Fields().ByName("value"), protoreflect.ValueOfString("tok"))
	endpoint.Set(authField, protoreflect.ValueOfMessage(authSecret))
	cfg.Set(primField, protoreflect.ValueOfMessage(endpoint))

	// backup_keys = [Secret{value: "k1"}, Secret{value: "k2"}]
	bkField := fields.ByName("backup_keys")
	bkList := cfg.Mutable(bkField).List()
	for _, v := range []string{"k1", "k2"} {
		s := dynamicpb.NewMessage(bkField.Message())
		s.Set(s.Descriptor().Fields().ByName("value"), protoreflect.ValueOfString(v))
		bkList.Append(protoreflect.ValueOfMessage(s))
	}

	// tenant_keys = {"acme": Secret{value: "a"}, "globex": Secret{value: "g"}}
	tkField := fields.ByName("tenant_keys")
	tkMap := cfg.Mutable(tkField).Map()
	valFd := tkField.MapValue()
	for k, v := range map[string]string{"acme": "a", "globex": "g"} {
		s := dynamicpb.NewMessage(valFd.Message())
		s.Set(s.Descriptor().Fields().ByName("value"), protoreflect.ValueOfString(v))
		tkMap.Set(protoreflect.ValueOfString(k).MapKey(), protoreflect.ValueOfMessage(s))
	}

	return cfg
}

// has reports whether the given field is currently set on cfg.
func has(cfg *dynamicpb.Message, fieldName string) bool {
	return cfg.Has(cfg.Descriptor().Fields().ByName(protoreflect.Name(fieldName)))
}

// nestedHas reports whether `outer.inner` is set on cfg.
func nestedHas(cfg *dynamicpb.Message, outer, inner string) bool {
	outerFd := cfg.Descriptor().Fields().ByName(protoreflect.Name(outer))
	if !cfg.Has(outerFd) {
		return false
	}
	nested := cfg.Get(outerFd).Message()
	return nested.Has(nested.Descriptor().Fields().ByName(protoreflect.Name(inner)))
}

// allowFilter is a Filter-allowing-everything helper for the
// baseline (un-narrowed) population test.
func allowFilter(t *testing.T) *Filter {
	t.Helper()
	f, err := Compile(nil, "test", Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNarrow_NilFilterIsNoop(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)
	if err := Narrow(cfg, nil); err != nil {
		t.Fatalf("Narrow with nil filter: %v", err)
	}
	if !has(cfg, "db_password") || !has(cfg, "backup_keys") || !nestedHas(cfg, "primary", "auth_token") {
		t.Errorf("nil filter should leave everything intact")
	}
}

func TestNarrow_AllowAllKeepsEverything(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)
	if err := Narrow(cfg, allowFilter(t)); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"db_password", "backup_keys", "tenant_keys"} {
		if !has(cfg, fn) {
			t.Errorf("%s should survive ALLOW_ALL filter", fn)
		}
	}
	if !nestedHas(cfg, "primary", "auth_token") {
		t.Errorf("primary.auth_token should survive ALLOW_ALL filter")
	}
}

func TestNarrow_DenyAllClearsEverySecret(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)

	// Build a filter with no allows + manifest default DENY_ALL.
	// Have to construct via Compile so the predicate resolves
	// correctly; use a fake manifest entry that names this module
	// but has no allow patterns.
	// Secret selector defaults to DENY_ALL via zero-value; attribute
	// selector explicitly ALLOW_ALL so non-secret structural fields
	// (primary, primary.url) survive — the historical semantics this
	// test was written for.
	f := &Filter{moduleName: "test", attribute: allowAllAttrs}
	if err := Narrow(cfg, f); err != nil {
		t.Fatal(err)
	}
	if has(cfg, "db_password") {
		t.Errorf("db_password should be cleared (no allow, default DENY_ALL via zero-value Filter)")
	}
	if has(cfg, "backup_keys") {
		t.Errorf("backup_keys should be cleared")
	}
	if has(cfg, "tenant_keys") {
		t.Errorf("tenant_keys should be cleared")
	}
	if nestedHas(cfg, "primary", "auth_token") {
		t.Errorf("primary.auth_token should be cleared")
	}
	// Non-secret field on the nested Endpoint should survive — narrowing
	// only clears Secrets, not the structural messages that contained them.
	primFd := md.Fields().ByName("primary")
	if !cfg.Has(primFd) {
		t.Errorf("primary message should survive (non-secret structural field)")
	}
	primary := cfg.Get(primFd).Message()
	if !primary.Has(primary.Descriptor().Fields().ByName("url")) {
		t.Errorf("primary.url (non-secret) should survive")
	}
}

func TestNarrow_AllowSpecificPath(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)

	// Whitelist: only db_password and primary.auth_token survive.
	f := &Filter{
		moduleName: "test",
		secret:     selector{allow: []string{"db_password", "primary.auth_token"}},
		attribute:  allowAllAttrs,
	}
	if err := Narrow(cfg, f); err != nil {
		t.Fatal(err)
	}
	if !has(cfg, "db_password") {
		t.Errorf("db_password should survive (in allow)")
	}
	if !nestedHas(cfg, "primary", "auth_token") {
		t.Errorf("primary.auth_token should survive (in allow)")
	}
	if has(cfg, "backup_keys") {
		t.Errorf("backup_keys should be cleared (not in allow)")
	}
	if has(cfg, "tenant_keys") {
		t.Errorf("tenant_keys should be cleared (not in allow)")
	}
}

func TestNarrow_DenyOverridesAllow(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)

	// allow="**" with deny on backup_keys — everything survives
	// except the backup_keys list.
	f := &Filter{
		moduleName: "test",
		secret: selector{
			allow: []string{"**"},
			deny:  []string{"backup_keys"},
		},
		attribute: allowAllAttrs,
	}
	if err := Narrow(cfg, f); err != nil {
		t.Fatal(err)
	}
	if !has(cfg, "db_password") {
		t.Errorf("db_password should survive")
	}
	if has(cfg, "backup_keys") {
		t.Errorf("backup_keys should be cleared (denied)")
	}
}

func TestNarrow_GlobInsideNested(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)

	// `primary.*` matches primary.auth_token (one segment under primary).
	// `db_password` is NOT under primary, gets dropped.
	f := &Filter{
		moduleName: "test",
		secret:     selector{allow: []string{"primary.*"}},
		attribute:  allowAllAttrs,
	}
	if err := Narrow(cfg, f); err != nil {
		t.Fatal(err)
	}
	if !nestedHas(cfg, "primary", "auth_token") {
		t.Errorf("primary.auth_token should survive (matches primary.*)")
	}
	if has(cfg, "db_password") {
		t.Errorf("db_password should be cleared")
	}
}

func TestNarrow_NilMessageIsNoop(t *testing.T) {
	if err := Narrow(nil, nil); err != nil {
		t.Errorf("Narrow(nil, nil) should be a no-op, got: %v", err)
	}
}

// TestNarrow_AttributeDenyClearsNonSecretField — the new attribute
// selector lets a module strip non-secret fields (here, the
// non-secret `primary.url` string) while leaving secrets alone.
func TestNarrow_AttributeDenyClearsNonSecretField(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)

	f := &Filter{
		moduleName: "test",
		secret:     selector{defaultPolicy: chameleonv1.BundleManifest_ALLOW_ALL},
		attribute: selector{
			deny:          []string{"primary.url"},
			defaultPolicy: chameleonv1.BundleManifest_ALLOW_ALL,
		},
	}
	if err := Narrow(cfg, f); err != nil {
		t.Fatal(err)
	}

	primFd := md.Fields().ByName("primary")
	primary := cfg.Get(primFd).Message()
	if primary.Has(primary.Descriptor().Fields().ByName("url")) {
		t.Errorf("primary.url should be cleared by attribute_deny")
	}
	// Secrets unaffected.
	if !nestedHas(cfg, "primary", "auth_token") {
		t.Errorf("primary.auth_token (secret) should survive — only attribute filter matched primary.url")
	}
	if !has(cfg, "db_password") {
		t.Errorf("db_password (secret) should survive — secret filter is ALLOW_ALL here")
	}
}

// TestNarrow_AttributeAllowOnlyKeepsWhitelistedNonSecrets — when
// attribute_allow is set, the implicit DENY_ALL strips every other
// non-secret field. Secrets are governed independently by the
// secret selector.
func TestNarrow_AttributeAllowOnlyKeepsWhitelistedNonSecrets(t *testing.T) {
	md := buildTestDescriptor(t)
	cfg := populated(t, md)

	f := &Filter{
		moduleName: "test",
		secret:     selector{defaultPolicy: chameleonv1.BundleManifest_ALLOW_ALL},
		attribute: selector{
			allow:         []string{"primary", "primary.url"},
			defaultPolicy: chameleonv1.BundleManifest_DENY_ALL,
		},
	}
	if err := Narrow(cfg, f); err != nil {
		t.Fatal(err)
	}

	primFd := md.Fields().ByName("primary")
	if !cfg.Has(primFd) {
		t.Fatalf("primary should survive (in attribute_allow)")
	}
	primary := cfg.Get(primFd).Message()
	if !primary.Has(primary.Descriptor().Fields().ByName("url")) {
		t.Errorf("primary.url should survive (in attribute_allow)")
	}
	// Secrets still pass through (secret filter is ALLOW_ALL).
	if !has(cfg, "db_password") {
		t.Errorf("db_password (secret) should survive")
	}
}
