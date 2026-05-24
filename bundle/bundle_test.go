// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundle_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon"
	"github.com/trendvidia/chameleon/bundle"
	"github.com/trendvidia/chameleon/internal/bundler"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// TestOpen_RoundTripsBuilt is the load-bearing test for the
// seamless-consumer story: build a bundle in memory, open it via
// the public bundle.Open API, then resolve a config from it via
// chameleon.Load. Every step the consumer would touch end-to-end.
//
// Schema has one plain scalar + two pxf.Secret fields; manifest
// allows db_password only, so api_key must come back redacted in
// the resolved config.
func TestOpen_RoundTripsBuilt(t *testing.T) {
	ctx := context.Background()
	schema := buildSchema(t)

	bundleBytes := buildBundle(t, ctx, schema, bundler.Build /* raw zip */)

	b, err := bundle.Open(bytes.NewReader(bundleBytes))
	if err != nil {
		t.Fatalf("bundle.Open: %v", err)
	}
	if b.FS == nil {
		t.Fatal("Bundle.FS must be non-nil")
	}
	if b.Chain == nil {
		t.Fatal("Bundle.Chain must be non-nil")
	}
	if len(b.Audit) == 0 {
		t.Errorf("Bundle.Audit should carry META/audit.txt content; got empty")
	}

	// chameleon.Load against the bundle's chain + FS — the
	// seamless consumer entry point.
	sample := dynamicpb.NewMessage(schema)
	res, err := chameleon.Load[*dynamicpb.Message](sample, b.Chain, b.FS, "prod")
	if err != nil {
		t.Fatalf("chameleon.Load from bundle: %v", err)
	}
	defer res.Destroy()

	cfg := res.Config
	// Non-secret survived.
	if got := cfg.Get(schema.Fields().ByName("base_url")).String(); got != "http://x" {
		t.Errorf("base_url = %q, want %q", got, "http://x")
	}
	// Allowed secret survived (db_password — the resolver hands
	// the secret bytes off to res.Secrets, but Has() on the
	// message reports presence).
	if !cfg.Has(schema.Fields().ByName("db_password")) {
		t.Error("db_password should survive narrowing (in allow list)")
	}
	// Denied secret is gone.
	if cfg.Has(schema.Fields().ByName("api_key")) {
		t.Error("api_key should be denied by the bundle manifest")
	}
}

func TestOpenBase64_RoundTripsBuilt(t *testing.T) {
	ctx := context.Background()
	schema := buildSchema(t)

	bundleBytes := buildBundle(t, ctx, schema, bundler.BuildBase64 /* base64-wrapped */)

	// Sanity: looks like base64 (only chars from the standard alphabet).
	for i, c := range bundleBytes {
		if !isBase64Char(c) {
			t.Fatalf("bundleBytes[%d] = %q, not a base64 character — BuildBase64 produced non-base64 output", i, c)
		}
	}

	b, err := bundle.OpenBase64(bytes.NewReader(bundleBytes))
	if err != nil {
		t.Fatalf("bundle.OpenBase64: %v", err)
	}
	if b.Chain == nil {
		t.Fatal("Bundle.Chain must be non-nil")
	}

	sample := dynamicpb.NewMessage(schema)
	res, err := chameleon.Load[*dynamicpb.Message](sample, b.Chain, b.FS, "prod")
	if err != nil {
		t.Fatalf("chameleon.Load from base64 bundle: %v", err)
	}
	defer res.Destroy()
	if !res.Config.Has(schema.Fields().ByName("db_password")) {
		t.Error("db_password should survive — base64 round-trip preserved narrowing")
	}
}

func TestOpen_RejectsMalformedZip(t *testing.T) {
	_, err := bundle.Open(bytes.NewReader([]byte("not a zip")))
	if err == nil {
		t.Fatal("Open should error on non-zip input")
	}
	if !strings.Contains(err.Error(), "zip") {
		t.Errorf("error should mention zip, got: %v", err)
	}
}

func TestOpenBase64_RejectsMalformedBase64(t *testing.T) {
	_, err := bundle.OpenBase64(bytes.NewReader([]byte("not valid base64!!!@@@")))
	if err == nil {
		t.Fatal("OpenBase64 should error on non-base64 input")
	}
}

func TestOpen_MissingChainEntry(t *testing.T) {
	// Build a zip that's structurally valid but has no chain.pxf.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("some_other.pxf")
	_, _ = w.Write([]byte("hello"))
	_ = zw.Close()

	_, err := bundle.Open(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, bundle.ErrMissingChain) {
		t.Errorf("Open on chain-less zip should return ErrMissingChain, got: %v", err)
	}
}

func TestOpen_AuditOptional(t *testing.T) {
	// Build a zip with chain.pxf but NO audit entry — should
	// succeed (audit is optional) and report nil Audit.
	chainBytes := chainSpecBytes(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(bundler.ChainEntryPath)
	_, _ = w.Write(chainBytes)
	_ = zw.Close()

	b, err := bundle.Open(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Open audit-less bundle: %v", err)
	}
	if b.Audit != nil {
		t.Errorf("Audit should be nil when META/audit.txt is absent, got %d bytes", len(b.Audit))
	}
}

// --- fixtures --------------------------------------------------------

// buildSchema constructs a small pxf.Secret-bearing schema with one
// scalar + two Secret fields, matching the shape exercised by the
// CLI smoke tests. Allows the round-trip test to assert narrowing
// behavior across both kept and dropped secrets.
func buildSchema(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	syntax := "proto3"
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
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
				Name: proto.String("Cfg"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("base_url"), Number: proto.Int32(1), Type: &str, Label: &optional},
					{Name: proto.String("api_key"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
					{Name: proto.String("db_password"), Number: proto.Int32(3), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	cfg := fd.Messages().ByName("Cfg")
	if cfg == nil {
		t.Fatal("Cfg descriptor not found")
	}
	return cfg
}

func chainSpecBytes(t *testing.T) []byte {
	t.Helper()
	// Minimal PXF chain spec — single env, one plaintext layer.
	return []byte(`version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
}

// buildBundle invokes the given bundler entry point (Build or
// BuildBase64) against an in-memory fixture and returns the
// resulting bytes. Used by both the raw-zip and base64 tests so
// they share the same setup.
type buildFn func(context.Context, bundler.BuildOptions) error

func buildBundle(t *testing.T, ctx context.Context, schema protoreflect.MessageDescriptor, fn buildFn) []byte {
	t.Helper()

	// Plaintext layer with both Secret fields populated, so
	// narrowing has both an allowed and a denied target.
	layerPXF := []byte(`base_url = "http://x"
api_key = "live-api-key-do-not-leak"
db_password = "live-db-password"
`)
	layersFS := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: layerPXF},
	}

	chainSpec := &chameleonv1.ChainSpec{
		Version:      "1.0.0",
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
		Layers: []*chameleonv1.LayerEntry{{
			Id: "base", Path: "base.pxf", ForEach: chameleonv1.LayerEntry_ONCE, Required: true,
		}},
	}
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:  "test",
			Allow: []string{"db_password"},
		}},
	}

	var buf bytes.Buffer
	if err := fn(ctx, bundler.BuildOptions{
		Manifest:     manifest,
		Module:       "test",
		Schema:       schema,
		ChainSpec:    chainSpec,
		LayersFS:     layersFS,
		Output:       &buf,
		ManifestName: "test-manifest",
	}); err != nil {
		t.Fatalf("bundler build: %v", err)
	}
	return buf.Bytes()
}

func isBase64Char(c byte) bool {
	if c >= 'A' && c <= 'Z' {
		return true
	}
	if c >= 'a' && c <= 'z' {
		return true
	}
	if c >= '0' && c <= '9' {
		return true
	}
	return c == '+' || c == '/' || c == '='
}

// Ensure the base64 package is referenced even if every test stops
// using it during a refactor — Go's unused-import check otherwise
// breaks the test file silently when a single test is commented out.
var _ = base64.StdEncoding
var _ = io.ReadAll