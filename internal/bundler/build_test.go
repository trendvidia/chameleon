// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"

	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/internal/upstream/upstreamtest"
	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
)

// TestBuild_EndToEnd is the bundler's full-pipeline smoke test: a
// multi-env chain spec + encrypted layers → Build → assert the
// produced zip contains the expected entries, that each bundled layer
// decrypts cleanly to a narrowed version of the original, and that
// each was encrypted to the recipients the manifest named.
//
// Runs against fakeCrypto rather than a real backend: since v0.23.0
// the library module links no key management (trendvidia/chameleon#130),
// and the pipeline only ever sees the crypt interfaces. The equivalent
// against real sops + age lives in the sops module
// (sops/bundle_test.go) — change one, look at the other.
func TestBuild_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// The recipient is opaque to the bundler — it reaches the
	// Encrypter verbatim, which is what step 7 below asserts.
	const publicKey = "age1testrecipient000000000000000000000000000000000000000000000"

	schema := buildTestDescriptor(t)
	// Two instances: `fixtures` seeds the layer FS, `backend` is what
	// Build itself uses, so backend's call log is exactly the
	// pipeline's re-encrypts and nothing else.
	fixtures := &fakeCrypto{}
	backend := &fakeCrypto{}

	// 2. Build encrypted-layer fixtures. Each layer has every secret
	// populated so narrowing has something to remove.
	baseCiphertext := encryptPopulated(t, ctx, fixtures, schema, publicKey)
	prodCiphertext := encryptPopulated(t, ctx, fixtures, schema, publicKey)
	sandboxCiphertext := encryptPopulated(t, ctx, fixtures, schema, publicKey)

	layersFS := fstest.MapFS{
		"base.pxf":        &fstest.MapFile{Data: baseCiphertext},
		"env/prod.pxf":    &fstest.MapFile{Data: prodCiphertext},
		"env/sandbox.pxf": &fstest.MapFile{Data: sandboxCiphertext},
	}

	// 3. Chain spec — two envs, one shared base layer (ONCE), one
	// per-env layer (PER_ENV expanding ${env}).
	chainSpec := &chameleonv1.ChainSpec{
		Version: "1",
		Environments: []*chameleonv1.Environment{
			{Name: "prod"},
			{Name: "sandbox"},
		},
		Layers: []*chameleonv1.LayerEntry{
			{Id: "base", Path: "base.pxf", Encrypted: true, ForEach: chameleonv1.LayerEntry_ONCE},
			{Id: "env", Path: "env/${env}.pxf", Encrypted: true, ForEach: chameleonv1.LayerEntry_PER_ENV},
		},
	}

	// 4. Manifest — module "test" allows db_password + primary.auth_token,
	// implicitly DENY_ALL for everything else (non-empty allow).
	// Single recipient set (no recipients_by_env) so the union path
	// is exercised on the shared base layer.
	manifest := &chameleonv1.BundleManifest{
		Modules: []*chameleonv1.BundleManifest_Module{{
			Name:  "test",
			Allow: []string{"db_password", "primary.auth_token"},
			Recipients: &chameleonv1.BundleManifest_RecipientSet{
				Age: []string{publicKey},
			},
		}},
	}

	// 5. Build.
	var out bytes.Buffer
	if err := Build(ctx, BuildOptions{
		Manifest:     manifest,
		Module:       "test",
		Schema:       schema,
		ChainSpec:    chainSpec,
		LayersFS:     layersFS,
		Decrypter:    backend,
		Encrypter:    backend,
		Output:       &out,
		ManifestName: "test-manifest",
		Now:          func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// 6. Read the zip + assert expected entries are all present.
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	got := zipNames(zr)
	want := []string{"base.pxf", "env/prod.pxf", "env/sandbox.pxf", "chain.pxf", "META/audit.txt"}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("bundle missing %q (entries: %v)", w, sortedKeys(got))
		}
	}

	// 6b. Both halves of the pipeline were handed the format derived
	// from the layer's own path (every fixture here is `.pxf` →
	// FormatProtowire). Sampled before step 7, which drives the fake
	// directly and would otherwise show up in the same log. A
	// hardcoded format survives every other assertion in this test:
	// the fake ignores it and the round trip still closes.
	encFormats, decFormats := backend.formatsSeen()
	if len(decFormats) != 3 || len(encFormats) != 3 {
		t.Errorf("format log = %d decrypts / %d encrypts, want 3 each", len(decFormats), len(encFormats))
	}
	for i, f := range append(append([]crypt.Format(nil), decFormats...), encFormats...) {
		if f != crypt.FormatProtowire {
			t.Errorf("call %d used format %v, want %v (derived from the .pxf layer path)", i, f, crypt.FormatProtowire)
		}
	}

	// 7. Every encrypted layer in the bundle must decrypt and
	// unmarshal as the schema with denied secrets stripped.
	for _, name := range []string{"base.pxf", "env/prod.pxf", "env/sandbox.pxf"} {
		t.Run("decrypt+narrow/"+name, func(t *testing.T) {
			ct := readZipEntry(t, zr, name)
			buf, err := backend.Decrypt(ctx, ct, crypt.FormatProtowire)
			if err != nil {
				t.Fatalf("decrypt %s: %v", name, err)
			}
			defer buf.Destroy()

			msg := dynamicpb.NewMessage(schema)
			if _, err := pxf.UnmarshalFull(buf.Bytes(), msg); err != nil {
				t.Fatalf("parse decrypted %s: %v", name, err)
			}

			// Allowed: db_password and primary.auth_token survive.
			if !msg.Has(schema.Fields().ByName("db_password")) {
				t.Errorf("%s: db_password should survive (allow)", name)
			}
			primaryFd := schema.Fields().ByName("primary")
			if !msg.Has(primaryFd) {
				t.Errorf("%s: primary should survive (parent of allowed secret)", name)
			} else {
				primary := msg.Get(primaryFd).Message()
				if !primary.Has(primary.Descriptor().Fields().ByName("auth_token")) {
					t.Errorf("%s: primary.auth_token should survive (allow)", name)
				}
			}
			// Denied: backup_keys gone. (tenant_keys map narrowing
			// is exercised in narrow_test.go directly; see
			// encryptPopulated for why it's cleared in this
			// PXF-round-tripping fixture.)
			if msg.Has(schema.Fields().ByName("backup_keys")) {
				t.Errorf("%s: backup_keys should be denied", name)
			}
		})
	}

	// 7b. Each of the three encrypted layers was handed to the
	// Encrypter with the manifest's recipient set. A regression that
	// dropped unionRecipients on the floor would still produce a
	// well-formed bundle — one nobody in production could decrypt.
	seen := backend.recipientsSeen()
	if len(seen) != 3 {
		t.Errorf("Encrypt called %d times, want 3 (one per encrypted layer)", len(seen))
	}
	for i, r := range seen {
		if len(r.Age) != 1 || r.Age[0] != publicKey {
			t.Errorf("layer %d encrypted to Age=%v, want [%s]", i, r.Age, publicKey)
		}
	}

	// 8. chain.pxf in the bundle round-trips to a ChainSpec with
	// the same Version + Environments + Layers as the input. This
	// is what the runtime reads to re-resolve.
	chainBytes := readZipEntry(t, zr, "chain.pxf")
	gotChain := &chameleonv1.ChainSpec{}
	if err := pxf.Unmarshal(chainBytes, gotChain); err != nil {
		t.Fatalf("unmarshal bundle chain.pxf: %v", err)
	}
	if gotChain.GetVersion() != "1" {
		t.Errorf("bundle chain.pxf version = %q, want %q", gotChain.GetVersion(), "1")
	}
	if len(gotChain.GetEnvironments()) != 2 {
		t.Errorf("bundle chain.pxf has %d envs, want 2", len(gotChain.GetEnvironments()))
	}
	if len(gotChain.GetLayers()) != 2 {
		t.Errorf("bundle chain.pxf has %d layers, want 2", len(gotChain.GetLayers()))
	}

	// 9. Audit entry has the expected fields.
	auditBytes := readZipEntry(t, zr, "META/audit.txt")
	audit := string(auditBytes)
	for _, want := range []string{"module: test", "manifest_name: test-manifest", "manifest_sha256:", "built_at: 2023-11-14T22:13:20Z"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit missing %q (full: %s)", want, audit)
		}
	}
}

func TestBuild_RejectsCtxTemplate(t *testing.T) {
	chainSpec := &chameleonv1.ChainSpec{
		Version:      "1",
		Environments: []*chameleonv1.Environment{{Name: "prod"}},
		ContextKeys:  []*chameleonv1.ContextKey{{Name: "region"}},
		Layers: []*chameleonv1.LayerEntry{{
			Id: "ctx-layer", Path: "regions/${ctx.region}.pxf", Encrypted: false,
		}},
	}
	err := Build(context.Background(), BuildOptions{
		Manifest:  &chameleonv1.BundleManifest{},
		Module:    "anything",
		Schema:    buildTestDescriptor(t),
		ChainSpec: chainSpec,
		LayersFS:  fstest.MapFS{},
		Output:    &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("Build should reject chain specs with ${ctx.*} (no build-time context)")
	}
	if !strings.Contains(err.Error(), "ctx") {
		t.Errorf("error should mention ctx, got: %v", err)
	}
}

func TestBuild_RejectsZipUnsafePaths(t *testing.T) {
	// The zip entry name is the layer's resolved path verbatim. Go's
	// own reader refuses to open an entry that is not an fs.ValidPath,
	// so a bundle carrying one is unreadable by chameleon anyway — but
	// `unzip` and most CI unpack steps happily walk out of the
	// extraction root, so planLayers has to refuse to write it.
	// A leading "../" is the obvious shape; an embedded one is the
	// shape that used to slip past.
	for _, p := range []string{"../escape.pxf", "/abs.pxf", "layers/../../etc/cron.d/x.pxf", "..", "."} {
		t.Run(p, func(t *testing.T) {
			err := Build(context.Background(), BuildOptions{
				Manifest: &chameleonv1.BundleManifest{},
				Module:   "anything",
				Schema:   buildTestDescriptor(t),
				ChainSpec: &chameleonv1.ChainSpec{
					Version:      "1",
					Environments: []*chameleonv1.Environment{{Name: "prod"}},
					Layers: []*chameleonv1.LayerEntry{{
						Id: "escape", Path: p, ForEach: chameleonv1.LayerEntry_ONCE,
					}},
				},
				LayersFS: fstest.MapFS{},
				Output:   &bytes.Buffer{},
			})
			if err == nil {
				t.Fatalf("Build should reject the zip-unsafe layer path %q", p)
			}
			if !strings.Contains(err.Error(), "zip-unsafe") {
				t.Errorf("error should say zip-unsafe, got: %v", err)
			}
		})
	}
}

func TestBuild_ValidateRejectsMissingRequired(t *testing.T) {
	cases := []struct {
		name string
		opts BuildOptions
		want string
	}{
		{"nil manifest", BuildOptions{Module: "x", Schema: buildTestDescriptor(t), ChainSpec: &chameleonv1.ChainSpec{}, LayersFS: fstest.MapFS{}, Output: &bytes.Buffer{}}, "manifest is required"},
		{"empty module", BuildOptions{Manifest: &chameleonv1.BundleManifest{}, Schema: buildTestDescriptor(t), ChainSpec: &chameleonv1.ChainSpec{}, LayersFS: fstest.MapFS{}, Output: &bytes.Buffer{}}, "module is required"},
		{"nil schema", BuildOptions{Manifest: &chameleonv1.BundleManifest{}, Module: "x", ChainSpec: &chameleonv1.ChainSpec{}, LayersFS: fstest.MapFS{}, Output: &bytes.Buffer{}}, "schema is required"},
		{"nil chain spec", BuildOptions{Manifest: &chameleonv1.BundleManifest{}, Module: "x", Schema: buildTestDescriptor(t), LayersFS: fstest.MapFS{}, Output: &bytes.Buffer{}}, "chain spec is required"},
		{"nil layers FS", BuildOptions{Manifest: &chameleonv1.BundleManifest{}, Module: "x", Schema: buildTestDescriptor(t), ChainSpec: &chameleonv1.ChainSpec{}, Output: &bytes.Buffer{}}, "layers FS is required"},
		{"nil output", BuildOptions{Manifest: &chameleonv1.BundleManifest{}, Module: "x", Schema: buildTestDescriptor(t), ChainSpec: &chameleonv1.ChainSpec{}, LayersFS: fstest.MapFS{}}, "output is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Build(context.Background(), tc.opts)
			if err == nil {
				t.Fatalf("Build with %s should error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// encryptPopulated marshals a populated test message to PXF and
// runs it through enc for the given recipient. Used to seed the
// layer FS for the end-to-end test.
//
// tenant_keys is cleared before marshal — protowire-go v1.0.0
// emits map fields with `:` separator but the unmarshaler expects
// `=`, so a marshal→unmarshal round-trip of a populated map field
// fails to parse. Narrow's map-field coverage stays in
// narrow_test.go (which constructs messages directly without the
// PXF round-trip). Once the protowire-go asymmetry is fixed,
// restore the map field here.
func encryptPopulated(t *testing.T, ctx context.Context, enc crypt.Encrypter, schema protoreflect.MessageDescriptor, publicKey string) []byte {
	t.Helper()
	msg := populated(t, schema)
	msg.Clear(schema.Fields().ByName("tenant_keys"))
	plaintext, err := pxf.MarshalOptions{}.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal populated: %v", err)
	}
	ct, err := enc.Encrypt(ctx, plaintext, crypt.FormatProtowire, crypt.Recipients{Age: []string{publicKey}})
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	return ct
}

// zipNames returns the entries of zr keyed by name for membership
// checks. Slightly clearer than walking zr.File at every call site.
func zipNames(zr *zip.Reader) map[string]*zip.File {
	out := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		out[f.Name] = f
	}
	return out
}

func sortedKeys(m map[string]*zip.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func readZipEntry(t *testing.T, zr *zip.Reader, name string) []byte {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", name, err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			return b
		}
	}
	t.Fatalf("zip entry %q not found", name)
	return nil
}

// silence unused-imports complaint when proto/proto-related helpers
// aren't all referenced (some tests use them through dynamicpb /
// chameleonv1 reflection paths only).
var _ = proto.Marshal

// A bundle binds a schema without going through resolver.New, so it
// refuses (pxf.required) with (pxf.default) itself, before reading a
// layer. The empty layers FS is what shows that: nothing here could
// fail a parse.
func TestBuild_RefusesRequiredWithDefault(t *testing.T) {
	build := func(raw ...[]byte) error {
		fd := upstreamtest.File(t, "a.proto", []*descriptorpb.DescriptorProto{upstreamtest.Message("M", raw...)})
		return BuildOptions{
			Manifest: &chameleonv1.BundleManifest{}, Module: "x", Schema: fd.Messages().Get(0),
			ChainSpec: &chameleonv1.ChainSpec{}, LayersFS: fstest.MapFS{}, Output: &bytes.Buffer{},
		}.validate()
	}
	err := build(upstreamtest.Required(true), upstreamtest.Default("x"))
	if err == nil || !strings.Contains(err.Error(), "(pxf.required) and (pxf.default) are exclusive on one field") {
		t.Errorf("want the pair refused, got: %v", err)
	}
	if err := build(upstreamtest.Required(false), upstreamtest.Default("x")); err != nil {
		t.Errorf("(pxf.required) = false beside a default must stay accepted, got: %v", err)
	}
}
