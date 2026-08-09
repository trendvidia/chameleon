// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"filippo.io/age"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sopskeypb "github.com/trendvidia/sops/v4/age/keypb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/internal/bundler"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	chsops "github.com/trendvidia/chameleon/sops"
)

// The bundler's decrypt → narrow → re-encrypt pipeline against a real
// backend. The library module's own copy of this test runs on a fake
// (internal/bundler/build_test.go) because since v0.23.0 that module
// links no key management at all (trendvidia/chameleon#130) — what a
// fake cannot prove is that a bundle this pipeline produced is
// decryptable by the tool that has to read it. That is what lives
// here, in the module that owns the crypto.
//
// Uses age: offline, no KMS round-trip, so it runs in the default test
// path rather than behind -tags=integration.

func TestBuild_RoundTripsThroughSops(t *testing.T) {
	ctx := context.Background()

	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err, "age key generation")
	publicKey := identity.Recipient().String()
	t.Setenv("SOPS_AGE_KEY", identity.String())

	schema := bundleTestSchema(t)
	enc := chsops.DefaultEncrypter()
	dec := chsops.Default()

	// One encrypted layer carrying both secrets; the manifest allows
	// only one of them through, so the narrowing is observable after
	// the round trip.
	layerCiphertext := encryptFixture(t, ctx, enc, schema, publicKey)

	chainSpec := &chameleonv1.ChainSpec{
		Version:      "1",
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
		Layers: []*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", Encrypted: true, ForEach: chameleonv1.LayerEntry_ONCE},
		},
	}
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:       "test",
			Allow:      []string{"db_password"},
			Recipients: &chameleonv1.BundleManifest_RecipientSet{Age: []string{publicKey}},
		}},
	}

	var out bytes.Buffer
	require.NoError(t, bundler.Build(ctx, bundler.BuildOptions{
		Manifest:  manifest,
		Module:    "test",
		Schema:    schema,
		ChainSpec: chainSpec,
		LayersFS:  fstest.MapFS{"base.pxf": &fstest.MapFile{Data: layerCiphertext}},
		Decrypter: dec,
		Encrypter: enc,
		Output:    &out,
	}), "Build")

	// The load-bearing assertion: the bundled layer decrypts with the
	// same credentials the runtime would use.
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	require.NoError(t, err)
	ct := zipEntry(t, zr, "base.pxf")
	require.NotEqual(t, layerCiphertext, ct, "the bundled layer must be re-encrypted, not copied")

	buf, err := dec.Decrypt(ctx, ct, crypt.FormatProtowire)
	require.NoError(t, err, "bundled layer must decrypt under the manifest's recipient")
	defer buf.Destroy()

	msg := dynamicpb.NewMessage(schema)
	_, err = pxf.UnmarshalFull(buf.Bytes(), msg)
	require.NoError(t, err, "decrypted layer must parse as the schema")

	assert.True(t, msg.Has(schema.Fields().ByName("db_password")), "allowed secret survives")
	assert.False(t, msg.Has(schema.Fields().ByName("backup_key")), "denied secret is stripped")
}

func TestBuild_ResolvesChainSpecKeyName(t *testing.T) {
	// BuildOptions.RecipientsForKeyName wired to the real key store.
	// The bundler's own tests exercise the hook with a fake (it may
	// not link a backend); this is the seam where the two halves meet,
	// and a bundle encrypted to the wrong recipient is only observable
	// by decrypting it.
	ctx := context.Background()

	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	t.Setenv("SOPS_AGE_KEY", identity.String())
	writeKeystore(t, map[string]string{"prod-key": identity.String()})

	schema := bundleTestSchema(t)
	enc := chsops.DefaultEncrypter()
	dec := chsops.Default()

	chainSpec := &chameleonv1.ChainSpec{
		Version: "1",
		// No recipients in the manifest at all — every recipient this
		// bundle gets has to come through key_name.
		Environments: []*chameleonv1.Environment{{Name: "prod", KeyName: "prod-key"}},
		Layers: []*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", Encrypted: true, ForEach: chameleonv1.LayerEntry_ONCE},
		},
	}

	var out bytes.Buffer
	require.NoError(t, bundler.Build(ctx, bundler.BuildOptions{
		Manifest:             &chameleonv1.BundleManifest{Modules: []*chameleonv1.BundleManifest_Module{{Name: "test"}}},
		Module:               "test",
		Schema:               schema,
		ChainSpec:            chainSpec,
		LayersFS:             fstest.MapFS{"base.pxf": &fstest.MapFile{Data: encryptFixture(t, ctx, enc, schema, identity.Recipient().String())}},
		Decrypter:            dec,
		Encrypter:            enc,
		RecipientsForKeyName: chsops.RecipientsFromKeyName,
		Output:               &out,
	}), "Build with key_name-derived recipients")

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	require.NoError(t, err)
	buf, err := dec.Decrypt(ctx, zipEntry(t, zr, "base.pxf"), crypt.FormatProtowire)
	require.NoError(t, err, "the key_name identity must be able to decrypt the bundled layer")
	buf.Destroy()
}

func TestBuild_KeyNameWithoutResolverFails(t *testing.T) {
	// The CLI wires RecipientsForKeyName; anything that forgets to
	// must fail loudly rather than quietly bundling a layer nobody can
	// open.
	ctx := context.Background()
	identity, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	t.Setenv("SOPS_AGE_KEY", identity.String())

	schema := bundleTestSchema(t)
	enc := chsops.DefaultEncrypter()

	err = bundler.Build(ctx, bundler.BuildOptions{
		Manifest: &chameleonv1.BundleManifest{Modules: []*chameleonv1.BundleManifest_Module{{Name: "test"}}},
		Module:   "test",
		Schema:   schema,
		ChainSpec: &chameleonv1.ChainSpec{
			Version:      "1",
			Environments: []*chameleonv1.Environment{{Name: "prod", KeyName: "prod-key"}},
			Layers: []*chameleonv1.LayerEntry{
				{Id: "base", Path: "base.pxf", Encrypted: true, ForEach: chameleonv1.LayerEntry_ONCE},
			},
		},
		LayersFS:  fstest.MapFS{"base.pxf": &fstest.MapFile{Data: encryptFixture(t, ctx, enc, schema, identity.Recipient().String())}},
		Decrypter: chsops.Default(),
		Encrypter: enc,
		Output:    &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RecipientsForKeyName")
}

// --- fixtures ---------------------------------------------------------------

// bundleTestSchema builds a two-secret AppConfig by hand. A compact
// cousin of internal/bundler/narrow_test.go's buildTestDescriptor —
// duplicated rather than shared because the two now live in different
// modules and a test helper is not worth an exported surface.
func bundleTestSchema(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	syntax := "proto3"
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("bundle_test.proto"),
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
				Name: proto.String("AppConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("db_password"), Number: proto.Int32(1), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
					{Name: proto.String("backup_key"), Number: proto.Int32(2), Type: &msg, TypeName: proto.String(".pxf.Secret"), Label: &optional},
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	require.NoError(t, err, "build descriptor")
	md := fd.Messages().ByName("AppConfig")
	require.NotNil(t, md, "AppConfig descriptor")
	return md
}

// encryptFixture produces a sops-encrypted layer with both secrets
// populated, so narrowing has something to remove.
func encryptFixture(t *testing.T, ctx context.Context, enc crypt.Encrypter, schema protoreflect.MessageDescriptor, recipient string) []byte {
	t.Helper()
	m := dynamicpb.NewMessage(schema)
	for _, name := range []protoreflect.Name{"db_password", "backup_key"} {
		fd := schema.Fields().ByName(name)
		secret := m.NewField(fd).Message()
		secret.Set(secret.Descriptor().Fields().ByName("value"), protoreflect.ValueOfString("s3cret-"+string(name)))
		m.Set(fd, protoreflect.ValueOfMessage(secret))
	}

	plaintext, err := pxf.MarshalOptions{}.Marshal(m)
	require.NoError(t, err, "marshal fixture")

	ct, err := enc.Encrypt(ctx, plaintext, crypt.FormatProtowire, crypt.Recipients{Age: []string{recipient}})
	require.NoError(t, err, "encrypt fixture")
	return ct
}

// writeKeystore points SOPS_AGE_KEY_FILE at a PXF keystore carrying
// the named identities.
func writeKeystore(t *testing.T, keys map[string]string) {
	t.Helper()
	data, err := pxf.Marshal(&sopskeypb.AgeKeyFile{Keys: keys})
	require.NoError(t, err, "marshal keystore")
	path := filepath.Join(t.TempDir(), "keys.pxf")
	require.NoError(t, os.WriteFile(path, data, 0o600), "write keystore")
	t.Setenv("SOPS_AGE_KEY_FILE", path)
}

func zipEntry(t *testing.T, zr *zip.Reader, name string) []byte {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			require.NoError(t, err)
			defer rc.Close()
			b, err := io.ReadAll(rc)
			require.NoError(t, err)
			return b
		}
	}
	t.Fatalf("zip entry %q not found", name)
	return nil
}
