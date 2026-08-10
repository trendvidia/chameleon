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

// buildAppConfigDescriptor synthesizes the same schema shape the
// bundler narrow tests use:
//
//	message Secret { string value = 1; ... }       // FullName: pxf.Secret
//	message Endpoint {
//	  string url = 1;
//	  Secret auth_token = 2;
//	}
//	message AppConfig {
//	  Secret db_password = 1;                       // scalar Secret
//	  Endpoint primary = 2;                         // non-secret nested message
//	  repeated Secret backup_keys = 3;              // repeated Secret
//	  map<string, Secret> tenant_keys = 4;          // map<K, Secret>
//	  string service_name = 5;                      // non-secret scalar
//	  repeated string regions = 6;                  // non-secret repeated scalar
//	  map<string, string> tags = 7;                 // non-secret map scalar
//	}
func buildAppConfigDescriptor(t *testing.T) protoreflect.MessageDescriptor {
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
					{Name: proto.String("service_name"), Number: proto.Int32(5), Type: &str, Label: &optional},
					{Name: proto.String("regions"), Number: proto.Int32(6), Type: &str, Label: &repeated},
					{Name: proto.String("tags"), Number: proto.Int32(7), Type: &msg, TypeName: proto.String(".pxf.AppConfig.TagsEntry"), Label: &repeated},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					mapEntryDesc("TenantKeysEntry", str, msg, ".pxf.Secret"),
					mapEntryDesc("TagsEntry", str, str, ""),
				},
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

func mapEntryDesc(name string, keyType, valType descriptorpb.FieldDescriptorProto_Type, valTypeName string) *descriptorpb.DescriptorProto {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	valField := &descriptorpb.FieldDescriptorProto{
		Name: proto.String("value"), Number: proto.Int32(2), Type: &valType, Label: &optional,
	}
	if valTypeName != "" {
		valField.TypeName = proto.String(valTypeName)
	}
	return &descriptorpb.DescriptorProto{
		Name: proto.String(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("key"), Number: proto.Int32(1), Type: &keyType, Label: &optional},
			valField,
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
}

func TestFindNonSecretFieldsInMessage_EnumeratesNonSecretPaths(t *testing.T) {
	md := buildAppConfigDescriptor(t)
	sites := FindNonSecretFieldsInMessage(md)

	paths := make([]string, 0, len(sites))
	for _, s := range sites {
		paths = append(paths, s.SchemaPath())
	}
	sort.Strings(paths)

	want := []string{
		"primary",      // non-secret nested message
		"primary.url",  // scalar inside it
		"regions",      // repeated scalar
		"service_name", // scalar leaf
		"tags",         // map<K, scalar>
	}
	sort.Strings(want)

	if len(paths) != len(want) {
		t.Errorf("got %d sites, want %d\n got:  %q\n want: %q", len(paths), len(want), paths, want)
		return
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("site %d: got %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestFindNonSecretFieldsInMessage_SecretsExcluded(t *testing.T) {
	md := buildAppConfigDescriptor(t)
	sites := FindNonSecretFieldsInMessage(md)

	for _, s := range sites {
		p := s.SchemaPath()
		// db_password, backup_keys, tenant_keys, primary.auth_token are secrets;
		// none of them should appear.
		for _, bad := range []string{"db_password", "backup_keys", "tenant_keys", "primary.auth_token"} {
			if p == bad {
				t.Errorf("secret path %q must not appear in non-secret catalog", p)
			}
		}
	}
}

func TestFindNonSecretFieldsInMessage_NestedNonSecretMessageEmittedAndRecursed(t *testing.T) {
	// Confirms that `primary` (the Endpoint nested-message field) is
	// both emitted as a site AND recursed into so `primary.url` shows
	// up too. The pxf.Secret child `primary.auth_token` is excluded.
	md := buildAppConfigDescriptor(t)
	sites := FindNonSecretFieldsInMessage(md)

	have := map[string]bool{}
	for _, s := range sites {
		have[s.SchemaPath()] = true
	}
	if !have["primary"] {
		t.Errorf("expected non-secret message field `primary` to appear")
	}
	if !have["primary.url"] {
		t.Errorf("expected nested scalar `primary.url` to appear (walker must recurse)")
	}
	if have["primary.auth_token"] {
		t.Errorf("primary.auth_token is a Secret — must not appear")
	}
}
