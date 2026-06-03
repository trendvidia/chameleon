// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secretcatalog

import (
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// secret-side traversal tests. The non_secret_test.go file already
// synthesizes an AppConfig schema we can reuse; this file pins
// FindInMessage / FindInFile and their handler fan-out (scalar
// message, repeated, map) plus the cycle-detection guard.

func TestIsSecret(t *testing.T) {
	// Nil-safe receiver per the comment on IsSecret.
	if IsSecret(nil) {
		t.Errorf("IsSecret(nil) = true, want false")
	}
	md := buildAppConfigDescriptor(t)
	// AppConfig is not a Secret.
	if IsSecret(md) {
		t.Errorf("IsSecret(AppConfig) = true, want false")
	}
	// AppConfig.db_password's value type IS the pxf.Secret.
	dbPassword := md.Fields().ByName("db_password")
	if !IsSecret(dbPassword.Message()) {
		t.Errorf("IsSecret(pxf.Secret) = false, want true")
	}
}

func TestFindInMessage_EnumeratesAllSecretSites(t *testing.T) {
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)

	paths := make([]string, 0, len(sites))
	for _, s := range sites {
		paths = append(paths, s.SchemaPath())
	}
	sort.Strings(paths)

	want := []string{
		"backup_keys",        // repeated Secret
		"db_password",        // scalar Secret
		"primary.auth_token", // scalar Secret inside nested message
		"tenant_keys",        // map<string, Secret>
	}
	sort.Strings(want)

	if len(paths) != len(want) {
		t.Fatalf("got %d sites, want %d\n got:  %q\n want: %q", len(paths), len(want), paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("site %d: got %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestFindInMessage_NonSecretFieldsExcluded(t *testing.T) {
	// The non-secret scalar/repeated/map fields (service_name,
	// regions, tags, primary.url) must not appear in the Secret
	// catalog — they belong to FindNonSecret*.
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)

	for _, s := range sites {
		p := s.SchemaPath()
		for _, bad := range []string{"service_name", "regions", "tags", "primary.url", "primary"} {
			if p == bad {
				t.Errorf("non-secret path %q must not appear in secret catalog", p)
			}
		}
	}
}

func TestFindInMessage_PathStepContainerCorrect(t *testing.T) {
	// Each Site's last PathStep.Container must match how the secret
	// is reached: scalar / repeated / map. This is load-bearing for
	// codegen — it picks the accessor template based on Container.
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)

	got := make(map[string]Container, len(sites))
	for _, s := range sites {
		last := s.Path[len(s.Path)-1]
		got[s.SchemaPath()] = last.Container
	}

	want := map[string]Container{
		"db_password":        ContainerScalar,
		"primary.auth_token": ContainerScalar,
		"backup_keys":        ContainerRepeated,
		"tenant_keys":        ContainerMap,
	}
	for path, c := range want {
		if got[path] != c {
			t.Errorf("Container for %q = %v, want %v", path, got[path], c)
		}
	}
}

func TestFindInMessage_MapKeyKindRecorded(t *testing.T) {
	// PathStep.MapKeyKind must be populated only when Container ==
	// Map, and must reflect the actual key kind. Tested with
	// AppConfig.tenant_keys whose key is string.
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)

	for _, s := range sites {
		last := s.Path[len(s.Path)-1]
		if s.SchemaPath() == "tenant_keys" {
			if last.Container != ContainerMap {
				t.Fatalf("tenant_keys Container = %v, want Map", last.Container)
			}
			if last.MapKeyKind != protoreflect.StringKind {
				t.Errorf("tenant_keys MapKeyKind = %v, want StringKind", last.MapKeyKind)
			}
			continue
		}
		// Non-map sites must have MapKeyKind unset (the zero value
		// of protoreflect.Kind, which is 0 — the invalid sentinel).
		if last.Container != ContainerMap && last.MapKeyKind != 0 {
			t.Errorf("%s Container=%v but MapKeyKind=%v (want zero)", s.SchemaPath(), last.Container, last.MapKeyKind)
		}
	}
}

func TestFindInMessage_DeterministicFieldOrder(t *testing.T) {
	// Per the FindInFile doc: "fields are visited in field-declaration
	// order". AppConfig declares db_password (field 1), primary
	// (field 2 → .auth_token), backup_keys (field 3), tenant_keys
	// (field 4), so secrets should emit in that order regardless of
	// run.
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)
	gotOrder := make([]string, len(sites))
	for i, s := range sites {
		gotOrder[i] = s.SchemaPath()
	}
	wantOrder := []string{"db_password", "primary.auth_token", "backup_keys", "tenant_keys"}
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("got %d sites, want %d", len(gotOrder), len(wantOrder))
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Errorf("site %d: got %q, want %q (declaration order broken)", i, gotOrder[i], wantOrder[i])
		}
	}
}

func TestFindInMessage_RootPreservedAcrossPath(t *testing.T) {
	// Every site's Root must point at the message FindInMessage was
	// called with — even for sites discovered inside nested
	// non-secret messages. Codegen relies on this to scope generated
	// accessors to the right top-level binding.
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)
	for _, s := range sites {
		if s.Root.FullName() != md.FullName() {
			t.Errorf("site %q has Root=%s, want %s", s.SchemaPath(), s.Root.FullName(), md.FullName())
		}
	}
}

func TestFindInFile_WalksAllTopLevelMessages(t *testing.T) {
	// Build a file with TWO top-level messages, both carrying a
	// scalar Secret field. FindInFile should walk each as its own
	// root and return the union; Site.Root must differ between the
	// two batches.
	fd := buildTwoRootFile(t)
	sites := FindInFile(fd)

	if len(sites) != 2 {
		t.Fatalf("got %d sites, want 2 (one per top-level message)", len(sites))
	}
	roots := map[string]bool{}
	for _, s := range sites {
		roots[string(s.Root.FullName())] = true
	}
	for _, want := range []string{"pxf.RootA", "pxf.RootB"} {
		if !roots[want] {
			t.Errorf("expected a site rooted at %s, got roots: %v", want, roots)
		}
	}
}

func TestFindNonSecretFieldsInFile_WalksAllTopLevelMessages(t *testing.T) {
	// Mirror of TestFindInFile_WalksAllTopLevelMessages for the
	// non-secret catalog. Both top-level messages should contribute
	// their non-secret fields.
	fd := buildTwoRootFile(t)
	sites := FindNonSecretFieldsInFile(fd)

	roots := map[string]bool{}
	for _, s := range sites {
		roots[string(s.Root.FullName())] = true
	}
	for _, want := range []string{"pxf.RootA", "pxf.RootB"} {
		if !roots[want] {
			t.Errorf("expected a non-secret site rooted at %s, got: %v", want, roots)
		}
	}
}

func TestFindInMessage_CycleBroken_DirectSelfReference(t *testing.T) {
	// Direct self-reference: Node { Node next = 1; Secret token = 2; }
	// Without cycle detection walkMessage would recurse infinitely
	// down `next`. The Secret site `token` must still be reported
	// exactly once.
	md := buildSelfReferentialDescriptor(t)
	sites := FindInMessage(md)

	if len(sites) != 1 {
		t.Fatalf("got %d sites, want 1 (token); cycle guard may have failed", len(sites))
	}
	if got := sites[0].SchemaPath(); got != "token" {
		t.Errorf("SchemaPath = %q, want %q", got, "token")
	}
}

func TestFindInMessage_CycleBroken_TwoNodeCycle(t *testing.T) {
	// A → B → A. Each carries a Secret. Both secrets must surface
	// exactly once and the walk must terminate.
	md := buildTwoNodeCycleDescriptor(t)
	sites := FindInMessage(md)

	have := map[string]int{}
	for _, s := range sites {
		have[s.SchemaPath()]++
	}
	if have["a_token"] != 1 {
		t.Errorf("a_token count = %d, want 1", have["a_token"])
	}
	if have["next.b_token"] != 1 {
		t.Errorf("next.b_token count = %d, want 1", have["next.b_token"])
	}
}

func TestFindInMessage_RepeatedScalar_NoSecrets(t *testing.T) {
	// A repeated scalar (e.g. repeated string) can't carry secrets.
	// handleListField must early-return without emitting a site.
	// Covered indirectly by FindInMessage of AppConfig — `regions`
	// must NOT appear in the secret catalog.
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)
	for _, s := range sites {
		if s.SchemaPath() == "regions" {
			t.Errorf("repeated scalar `regions` must not appear in secret catalog")
		}
	}
}

func TestFindInMessage_ListOfNonSecretMessage_RecursesIntoNestedSecret(t *testing.T) {
	// repeated Endpoint replicas — Endpoint isn't a Secret, so
	// handleListField must walk INTO it and discover the nested
	// auth_token Secret. Without this case, the recursion branch
	// in handleListField stays uncovered.
	md := buildContainerOfMessageDescriptor(t)
	sites := FindInMessage(md)
	have := map[string]bool{}
	for _, s := range sites {
		have[s.SchemaPath()] = true
		// Path's first step is the container; assert Container set.
		first := s.Path[0]
		switch first.Name {
		case "replicas":
			if first.Container != ContainerRepeated {
				t.Errorf("replicas Container = %v, want Repeated", first.Container)
			}
		case "sites":
			if first.Container != ContainerMap {
				t.Errorf("sites Container = %v, want Map", first.Container)
			}
			if first.MapKeyKind != protoreflect.StringKind {
				t.Errorf("sites MapKeyKind = %v, want StringKind", first.MapKeyKind)
			}
		}
	}
	if !have["replicas.auth_token"] {
		t.Errorf("expected replicas.auth_token; got: %v", have)
	}
	if !have["sites.auth_token"] {
		t.Errorf("expected sites.auth_token; got: %v", have)
	}
}

func TestFindNonSecretFieldsInMessage_ListAndMapOfMessage_BothEmittedAndRecursed(t *testing.T) {
	// Mirror of the secret-side container-of-message test, for the
	// non-secret traversal. Asserts the list/map handlers in
	// non_secret.go both emit the container site AND recurse into
	// the message — covering the previously-silent recurse branches
	// in handleNonSecretList and handleNonSecretMap.
	md := buildContainerOfMessageDescriptor(t)
	sites := FindNonSecretFieldsInMessage(md)
	have := map[string]bool{}
	for _, s := range sites {
		have[s.SchemaPath()] = true
	}
	if !have["replicas"] {
		t.Errorf("expected `replicas` container site; got: %v", have)
	}
	if !have["replicas.url"] {
		t.Errorf("expected nested `replicas.url`; non-secret list handler should recurse")
	}
	if !have["sites"] {
		t.Errorf("expected `sites` container site; got: %v", have)
	}
	if !have["sites.url"] {
		t.Errorf("expected nested `sites.url`; non-secret map handler should recurse")
	}
	// auth_token is a Secret — must NOT appear in the non-secret catalog.
	if have["replicas.auth_token"] || have["sites.auth_token"] {
		t.Errorf("Secret-typed fields must not appear in non-secret catalog: %v", have)
	}
}

func TestFindInMessage_MapScalarValue_NoSecrets(t *testing.T) {
	// A map<K, scalar> can't carry secrets. handleMapField must
	// early-return for map<string, string>. Same indirect check via
	// AppConfig — `tags` must NOT appear.
	md := buildAppConfigDescriptor(t)
	sites := FindInMessage(md)
	for _, s := range sites {
		if s.SchemaPath() == "tags" {
			t.Errorf("map<K, scalar> `tags` must not appear in secret catalog")
		}
	}
}

// buildTwoRootFile synthesizes a file with two top-level messages,
// each containing one scalar pxf.Secret field. Drives FindInFile /
// FindNonSecretFieldsInFile tests.
func buildTwoRootFile(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	syntax := "proto3"
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("twoRoot.proto"),
		Package: proto.String("pxf"),
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Secret"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("value"), Number: proto.Int32(1), Type: &str, Label: &optional},
				},
			},
			{
				Name: proto.String("RootA"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("token_a"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
					{Name: proto.String("label_a"), Number: proto.Int32(2), Type: &str, Label: &optional},
				},
			},
			{
				Name: proto.String("RootB"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("token_b"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
					{Name: proto.String("label_b"), Number: proto.Int32(2), Type: &str, Label: &optional},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build two-root descriptor: %v", err)
	}
	return fd
}

// buildSelfReferentialDescriptor synthesizes:
//
//	message Node {
//	  Node next = 1;     // direct self-reference
//	  Secret token = 2;
//	}
//
// Drives the direct-cycle test.
func buildSelfReferentialDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	syntax := "proto3"
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("selfref.proto"),
		Package: proto.String("pxf"),
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Secret"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("value"), Number: proto.Int32(1), Type: &str, Label: &optional},
				},
			},
			{
				Name: proto.String("Node"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("next"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Node"), Label: &optional},
					{Name: proto.String("token"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build self-ref descriptor: %v", err)
	}
	md := fd.Messages().ByName("Node")
	if md == nil {
		t.Fatal("Node descriptor not found")
	}
	return md
}

// buildContainerOfMessageDescriptor synthesizes:
//
//	message Endpoint {
//	  string url = 1;
//	  Secret auth_token = 2;
//	}
//	message Cluster {
//	  repeated Endpoint replicas = 1;
//	  map<string, Endpoint> sites = 2;
//	}
//
// Forces the list/map handlers to recurse into a non-secret message
// (Endpoint) and surface the inner Secret on the nested path.
func buildContainerOfMessageDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	syntax := "proto3"
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("container.proto"),
		Package: proto.String("pxf"),
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Secret"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("value"), Number: proto.Int32(1), Type: &str, Label: &optional},
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
				Name: proto.String("Cluster"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("replicas"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Endpoint"), Label: &repeated},
					{Name: proto.String("sites"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Cluster.SitesEntry"), Label: &repeated},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					mapEntryDesc("SitesEntry", str, msg, ".pxf.Endpoint"),
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build container-of-message descriptor: %v", err)
	}
	md := fd.Messages().ByName("Cluster")
	if md == nil {
		t.Fatal("Cluster descriptor not found")
	}
	return md
}

// buildTwoNodeCycleDescriptor synthesizes:
//
//	message A {
//	  Secret a_token = 1;
//	  B next = 2;
//	}
//	message B {
//	  Secret b_token = 1;
//	  A back = 2;
//	}
//
// Drives the indirect-cycle test (A → B → A).
func buildTwoNodeCycleDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	syntax := "proto3"
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("cycle.proto"),
		Package: proto.String("pxf"),
		Syntax:  &syntax,
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Secret"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("value"), Number: proto.Int32(1), Type: &str, Label: &optional},
				},
			},
			{
				Name: proto.String("A"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("a_token"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
					{Name: proto.String("next"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.B"), Label: &optional},
				},
			},
			{
				Name: proto.String("B"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("b_token"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
					{Name: proto.String("back"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.A"), Label: &optional},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build two-node cycle descriptor: %v", err)
	}
	md := fd.Messages().ByName("A")
	if md == nil {
		t.Fatal("A descriptor not found")
	}
	return md
}
