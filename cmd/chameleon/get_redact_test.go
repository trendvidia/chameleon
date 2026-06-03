// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Direct coverage for redactList and redactMap. The CLI smoke test
// in cli_smoke_test.go exercises the scalar-Secret path through
// getCmd; this file targets the list and map paths — which previously
// stood at 0% coverage despite being the same kind of payload a
// production schema commonly produces (repeated tokens, per-tenant
// keys).
//
// Tests construct dynamicpb messages from synthesized FileDescriptors
// so each scenario can isolate one redaction branch (scalar /
// list-of-Secret / map-of-Secret / recurse into non-secret message).

const redactPkg = "pxf"

// secretDP is the pxf.Secret descriptor proto used by every fixture
// here. Kept package-level because each fixture imports it via
// TypeName = ".pxf.Secret".
func secretDP() *descriptorpb.DescriptorProto {
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	return &descriptorpb.DescriptorProto{
		Name: proto.String("Secret"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("value"), Number: proto.Int32(1), Type: &str, Label: &optional},
			{Name: proto.String("hint"), Number: proto.Int32(2), Type: &str, Label: &optional},
		},
	}
}

// buildDesc compiles a single-file descriptor proto with the named
// top-level message. Helper to keep each test concise.
func buildDesc(t *testing.T, root *descriptorpb.DescriptorProto, others ...*descriptorpb.DescriptorProto) protoreflect.MessageDescriptor {
	t.Helper()
	syntax := "proto3"
	msgs := append([]*descriptorpb.DescriptorProto{secretDP()}, others...)
	msgs = append(msgs, root)
	fdp := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("test.proto"),
		Package:     proto.String(redactPkg),
		Syntax:      &syntax,
		MessageType: msgs,
	}
	fd, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err)
	md := fd.Messages().ByName(protoreflect.Name(root.GetName()))
	require.NotNil(t, md, "root descriptor %s not found", root.GetName())
	return md
}

func mapEntryDescRedact(name string, valType descriptorpb.FieldDescriptorProto_Type, valTypeName string) *descriptorpb.DescriptorProto {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	valField := &descriptorpb.FieldDescriptorProto{
		Name: proto.String("value"), Number: proto.Int32(2), Type: &valType, Label: &optional,
	}
	if valTypeName != "" {
		valField.TypeName = proto.String(valTypeName)
	}
	return &descriptorpb.DescriptorProto{
		Name: proto.String(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("key"), Number: proto.Int32(1), Type: &str, Label: &optional},
			valField,
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
}

func setSecretValue(msg protoreflect.Message, value string) {
	msg.Set(msg.Descriptor().Fields().ByName("value"), protoreflect.ValueOfString(value))
}

func getSecretValue(msg protoreflect.Message) string {
	return msg.Get(msg.Descriptor().Fields().ByName("value")).String()
}

func TestRedactList_SecretsReplacedWithSentinel(t *testing.T) {
	// Schema: message Container { repeated Secret tokens = 1; }
	// Three populated Secrets get all their `value` fields rewritten
	// to the redaction sentinel. This is the load-bearing path:
	// production schemas often surface secrets as repeated lists
	// (rotating keys, backup tokens).
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	root := &descriptorpb.DescriptorProto{
		Name: proto.String("Container"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("tokens"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &repeated},
		},
	}
	md := buildDesc(t, root)

	container := dynamicpb.NewMessage(md)
	list := container.Mutable(md.Fields().ByName("tokens")).List()
	for _, v := range []string{"plain-1", "plain-2", "plain-3"} {
		sec := list.AppendMutable().Message()
		setSecretValue(sec, v)
	}

	require.NoError(t, redactSecrets(container))

	gotList := container.Get(md.Fields().ByName("tokens")).List()
	require.Equal(t, 3, gotList.Len())
	for i := range 3 {
		got := getSecretValue(gotList.Get(i).Message())
		if got != redactedSentinel {
			t.Errorf("tokens[%d] = %q, want %q (redaction sentinel)", i, got, redactedSentinel)
		}
	}
}

func TestRedactList_ScalarKindIsNoOp(t *testing.T) {
	// repeated string — no Secret messages possible. redactList
	// early-returns; the list is untouched.
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	root := &descriptorpb.DescriptorProto{
		Name: proto.String("Container"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("tags"), Number: proto.Int32(1), Type: &str, Label: &repeated},
		},
	}
	md := buildDesc(t, root)
	container := dynamicpb.NewMessage(md)
	list := container.Mutable(md.Fields().ByName("tags")).List()
	list.Append(protoreflect.ValueOfString("public"))
	list.Append(protoreflect.ValueOfString("safe"))

	require.NoError(t, redactSecrets(container))

	got := container.Get(md.Fields().ByName("tags")).List()
	require.Equal(t, 2, got.Len())
	assert.Equal(t, "public", got.Get(0).String())
	assert.Equal(t, "safe", got.Get(1).String())
}

func TestRedactList_NonSecretMessageRecurses(t *testing.T) {
	// repeated Endpoint where Endpoint contains a nested Secret.
	// redactList must recurse into the message and the inner
	// secret must surface as redacted. The url scalar stays.
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	endpoint := &descriptorpb.DescriptorProto{
		Name: proto.String("Endpoint"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("url"), Number: proto.Int32(1), Type: &str, Label: &optional},
			{Name: proto.String("auth_token"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
		},
	}
	root := &descriptorpb.DescriptorProto{
		Name: proto.String("Cluster"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("replicas"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Endpoint"), Label: &repeated},
		},
	}
	md := buildDesc(t, root, endpoint)

	cluster := dynamicpb.NewMessage(md)
	list := cluster.Mutable(md.Fields().ByName("replicas")).List()
	for i, name := range []string{"a", "b"} {
		ep := list.AppendMutable().Message()
		ep.Set(ep.Descriptor().Fields().ByName("url"), protoreflect.ValueOfString("https://"+name))
		tok := ep.Mutable(ep.Descriptor().Fields().ByName("auth_token")).Message()
		setSecretValue(tok, "raw-token-"+name+"-"+string(rune('0'+i)))
	}

	require.NoError(t, redactSecrets(cluster))

	gotList := cluster.Get(md.Fields().ByName("replicas")).List()
	require.Equal(t, 2, gotList.Len())
	for i := range 2 {
		ep := gotList.Get(i).Message()
		// url survives.
		url := ep.Get(ep.Descriptor().Fields().ByName("url")).String()
		assert.True(t, strings.HasPrefix(url, "https://"),
			"replicas[%d].url should be untouched, got %q", i, url)
		// inner secret redacted.
		tok := ep.Get(ep.Descriptor().Fields().ByName("auth_token")).Message()
		assert.Equal(t, redactedSentinel, getSecretValue(tok),
			"replicas[%d].auth_token.value should be redacted", i)
	}
}

func TestRedactMap_SecretsReplacedWithSentinel(t *testing.T) {
	// Schema: message Container { map<string, Secret> tenants = 1; }
	// Each value's `value` field is rewritten to the sentinel.
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	root := &descriptorpb.DescriptorProto{
		Name: proto.String("Container"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("tenants"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Container.TenantsEntry"), Label: &repeated},
		},
		NestedType: []*descriptorpb.DescriptorProto{
			mapEntryDescRedact("TenantsEntry", msg, ".pxf.Secret"),
		},
	}
	md := buildDesc(t, root)

	container := dynamicpb.NewMessage(md)
	mapField := md.Fields().ByName("tenants")
	mp := container.Mutable(mapField).Map()
	for _, k := range []string{"acme", "globex"} {
		val := mp.NewValue()
		setSecretValue(val.Message(), "leaked-"+k)
		mp.Set(protoreflect.ValueOfString(k).MapKey(), val)
	}

	require.NoError(t, redactSecrets(container))

	gotMap := container.Get(mapField).Map()
	require.Equal(t, 2, gotMap.Len())
	gotMap.Range(func(mk protoreflect.MapKey, v protoreflect.Value) bool {
		got := getSecretValue(v.Message())
		if got != redactedSentinel {
			t.Errorf("tenants[%q].value = %q, want %q", mk.String(), got, redactedSentinel)
		}
		return true
	})
}

func TestRedactMap_ScalarValueIsNoOp(t *testing.T) {
	// map<string, string> — values aren't message-typed, redactMap
	// early-returns. The map content stays as authored.
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	root := &descriptorpb.DescriptorProto{
		Name: proto.String("Container"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("labels"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Container.LabelsEntry"), Label: &repeated},
		},
		NestedType: []*descriptorpb.DescriptorProto{
			mapEntryDescRedact("LabelsEntry", str, ""),
		},
	}
	md := buildDesc(t, root)

	container := dynamicpb.NewMessage(md)
	mp := container.Mutable(md.Fields().ByName("labels")).Map()
	mp.Set(protoreflect.ValueOfString("env").MapKey(), protoreflect.ValueOfString("prod"))
	mp.Set(protoreflect.ValueOfString("region").MapKey(), protoreflect.ValueOfString("us-east-1"))

	require.NoError(t, redactSecrets(container))

	got := container.Get(md.Fields().ByName("labels")).Map()
	assert.Equal(t, 2, got.Len())
	envVal := got.Get(protoreflect.ValueOfString("env").MapKey())
	assert.Equal(t, "prod", envVal.String(), "scalar map value must be untouched")
}

func TestRedactMap_NonSecretMessageRecurses(t *testing.T) {
	// map<string, Endpoint> where Endpoint contains an inner Secret.
	// redactMap must recurse into the message; the inner secret's
	// value gets rewritten.
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	endpoint := &descriptorpb.DescriptorProto{
		Name: proto.String("Endpoint"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("url"), Number: proto.Int32(1), Type: &str, Label: &optional},
			{Name: proto.String("auth_token"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
		},
	}
	root := &descriptorpb.DescriptorProto{
		Name: proto.String("Cluster"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{Name: proto.String("sites"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Cluster.SitesEntry"), Label: &repeated},
		},
		NestedType: []*descriptorpb.DescriptorProto{
			mapEntryDescRedact("SitesEntry", msg, ".pxf.Endpoint"),
		},
	}
	md := buildDesc(t, root, endpoint)

	cluster := dynamicpb.NewMessage(md)
	mp := cluster.Mutable(md.Fields().ByName("sites")).Map()
	for _, k := range []string{"east", "west"} {
		val := mp.NewValue()
		ep := val.Message()
		ep.Set(ep.Descriptor().Fields().ByName("url"), protoreflect.ValueOfString("https://"+k))
		tok := ep.Mutable(ep.Descriptor().Fields().ByName("auth_token")).Message()
		setSecretValue(tok, "raw-token-"+k)
		mp.Set(protoreflect.ValueOfString(k).MapKey(), val)
	}

	require.NoError(t, redactSecrets(cluster))

	got := cluster.Get(md.Fields().ByName("sites")).Map()
	require.Equal(t, 2, got.Len())
	got.Range(func(mk protoreflect.MapKey, v protoreflect.Value) bool {
		ep := v.Message()
		url := ep.Get(ep.Descriptor().Fields().ByName("url")).String()
		assert.True(t, strings.HasPrefix(url, "https://"),
			"sites[%q].url should be untouched, got %q", mk.String(), url)
		tok := ep.Get(ep.Descriptor().Fields().ByName("auth_token")).Message()
		assert.Equal(t, redactedSentinel, getSecretValue(tok),
			"sites[%q].auth_token.value should be redacted", mk.String())
		return true
	})
}

func TestRedactSecrets_NilMessageNoPanic(t *testing.T) {
	// redactSecrets is documented to be a no-op on nil.
	if err := redactSecrets(nil); err != nil {
		t.Errorf("redactSecrets(nil) = %v, want nil (documented no-op)", err)
	}
}

// parseCtx direct coverage. The existing explain_test.go drives
// parseCtx indirectly through the explain CLI flow, but only exercised
// the empty-string and one comma-pair path. The malformed cases,
// trim-whitespace behavior, and trailing-comma handling were silent.

func TestParseCtx_EmptyReturnsNil(t *testing.T) {
	got, err := parseCtx("")
	if err != nil {
		t.Fatalf("err = %v, want nil for empty input", err)
	}
	if got != nil {
		t.Errorf("parseCtx(\"\") = %v, want nil map", got)
	}
}

func TestParseCtx_SinglePair(t *testing.T) {
	got, err := parseCtx("region=us")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"region": "us"}, got)
}

func TestParseCtx_MultiplePairs(t *testing.T) {
	got, err := parseCtx("region=us,tier=prod,zone=east-1")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"region": "us",
		"tier":   "prod",
		"zone":   "east-1",
	}, got)
}

func TestParseCtx_TrimsWhitespaceAroundKeysAndValues(t *testing.T) {
	got, err := parseCtx(" region = us , tier=prod ")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"region": "us",
		"tier":   "prod",
	}, got)
}

func TestParseCtx_SkipsEmptyEntriesFromTrailingOrDoubleCommas(t *testing.T) {
	// Trailing comma or `,,` produces empty kv tokens after Split;
	// parseCtx must skip those rather than erroring. Lenient parser
	// — operators editing on the CLI often have stray commas.
	got, err := parseCtx("region=us,,tier=prod,")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"region": "us",
		"tier":   "prod",
	}, got)
}

func TestParseCtx_MalformedPairErrors(t *testing.T) {
	_, err := parseCtx("region")
	if err == nil {
		t.Fatal("expected error for pair without =")
	}
	assert.Contains(t, err.Error(), "malformed",
		"error should identify the malformed-pair case")
	assert.Contains(t, err.Error(), "region",
		"error should echo the offending pair so the operator sees the typo")
}

func TestParseCtx_LeadingEqualsRejected(t *testing.T) {
	// `=value` has eq == 0, which triggers the eq <= 0 guard.
	// Empty key is a structural error — silently accepting it
	// would produce a map[""]string entry that no LayerEntry can
	// reference.
	_, err := parseCtx("=oops")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestParseCtx_EqualsInValueIsKept(t *testing.T) {
	// strings.IndexByte returns the FIRST '=' so "k=v=w" parses as
	// k → "v=w". Pin that semantics — flipping to LastIndexByte
	// would silently change behavior for values containing '='.
	got, err := parseCtx("token=base64==")
	require.NoError(t, err)
	assert.Equal(t, "base64==", got["token"],
		"value should retain '=' after the first separator")
}
