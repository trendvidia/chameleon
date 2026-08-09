// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package resolver_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/crypt"
	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/source"
)

// Registry-backed decrypter selection: what a consumer gets when they
// blank-import a backend instead of calling WithDecrypter
// (trendvidia/chameleon#130). This test binary deliberately does NOT
// import chameleon/sops — nothing here would compile differently if it
// did, but the empty-registry cases below would stop being reachable.
//
// The registry is process-global; every test registers under its own
// name with cleanup, and none call t.Parallel.

func registerBackend(t *testing.T, name string, b crypt.Backend) {
	t.Helper()
	crypt.Register(name, b)
	t.Cleanup(func() { crypt.Unregister(name) })
}

// ciphertextFS is the "disk" behind the one-encrypted-layer chain the
// cases below resolve. The bytes are opaque — only a decrypter sees
// them — so a sentinel is enough.
func ciphertextFS() fstest.MapFS {
	return fstest.MapFS{"secret.pxf": &fstest.MapFile{Data: []byte("ciphertext-sentinel")}}
}

func TestResolve_UsesSoleRegisteredBackend(t *testing.T) {
	// No WithDecrypter anywhere: the resolver must find the one
	// registered backend on its own. This is the blank-import path.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`)
	fs := ciphertextFS()

	fake := &recordingDecrypter{plaintextTo: []byte(`base_url = "from-registered-backend"`)}
	registerBackend(t, "fake", crypt.Backend{Decrypter: fake})

	r, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err, "a registered backend must satisfy an encrypted layer with no wiring")
	defer res.Destroy()

	assert.Equal(t, int32(1), atomic.LoadInt32(&fake.calls))
	assert.Equal(t, "from-registered-backend",
		res.Config.Get(appDesc.Fields().ByName("base_url")).String())
}

func TestResolve_ExplicitDecrypterBeatsRegistry(t *testing.T) {
	// WithDecrypter is the caller being specific; a backend that
	// happens to be linked in (possibly by a transitive dependency)
	// must not displace it.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`)
	fs := ciphertextFS()

	registered := &recordingDecrypter{plaintextTo: []byte(`base_url = "registry"`)}
	explicit := &recordingDecrypter{plaintextTo: []byte(`base_url = "explicit"`)}
	registerBackend(t, "fake", crypt.Backend{Decrypter: registered})

	r, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithDecrypter(explicit),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	assert.Equal(t, int32(1), atomic.LoadInt32(&explicit.calls), "explicit decrypter runs")
	assert.Zero(t, atomic.LoadInt32(&registered.calls), "registered backend must not be consulted")
	assert.Equal(t, "explicit", res.Config.Get(appDesc.Fields().ByName("base_url")).String())
}

func TestResolve_NoBackendSaysHowToLinkOne(t *testing.T) {
	// The empty-registry diagnostic is the one a consumer hits after
	// adding their first encrypted layer. It has to name the fix.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`)
	fs := ciphertextFS()
	require.Empty(t, crypt.Registered(), "this test binary must link no backend")

	r, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err, "New must not require a decrypter — the chain might never reach one")

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.ErrorIs(t, err, resolver.ErrNoDecrypter, "the sentinel consumers match on")
	assert.ErrorIs(t, err, crypt.ErrNoBackend, "and the registry cause underneath it")
	assert.Contains(t, err.Error(), `github.com/trendvidia/chameleon/sops`,
		"the message must name the import that fixes it")
	assert.Contains(t, err.Error(), `layer="secret"`, "and which layer wanted it")
}

func TestResolve_AmbiguousBackendsAskForAChoice(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`)
	fs := ciphertextFS()

	registerBackend(t, "alpha", crypt.Backend{Decrypter: &recordingDecrypter{}})
	registerBackend(t, "beta", crypt.Backend{Decrypter: &recordingDecrypter{}})

	r, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.ErrorIs(t, err, resolver.ErrNoDecrypter)
	assert.ErrorIs(t, err, crypt.ErrAmbiguousBackend)
	assert.Contains(t, err.Error(), "alpha, beta", "name the candidates")
	assert.Contains(t, err.Error(), "WithDecrypterNamed", "and the option that picks one")
}

func TestResolve_PlaintextChainNeedsNoBackend(t *testing.T) {
	// The load-bearing case for #130: nothing registered, nothing
	// wired, and a plaintext chain resolves fine. If this ever needs a
	// decrypter, the coupling is back.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "plaintext"`)}}
	require.Empty(t, crypt.Registered())

	r, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fs)),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()
	assert.Equal(t, "plaintext", res.Config.Get(appDesc.Fields().ByName("base_url")).String())
}

func TestWithDecrypterNamed_SelectsThatBackend(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`)
	fs := ciphertextFS()

	alpha := &recordingDecrypter{plaintextTo: []byte(`base_url = "alpha"`)}
	beta := &recordingDecrypter{plaintextTo: []byte(`base_url = "beta"`)}
	registerBackend(t, "alpha", crypt.Backend{Decrypter: alpha})
	registerBackend(t, "beta", crypt.Backend{Decrypter: beta})

	r, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithDecrypterNamed("beta"),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	assert.Equal(t, int32(1), atomic.LoadInt32(&beta.calls))
	assert.Zero(t, atomic.LoadInt32(&alpha.calls))
	assert.Equal(t, "beta", res.Config.Get(appDesc.Fields().ByName("base_url")).String())
}

func TestWithDecrypterNamed_UnknownFailsAtNew(t *testing.T) {
	// A missing import is a build-time mistake; surfacing it at New
	// rather than at the first encrypted layer means a plaintext dev
	// environment can't hide it until prod.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	registerBackend(t, "present", crypt.Backend{Decrypter: &recordingDecrypter{}})

	_, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fstest.MapFS{})),
		resolver.WithDecrypterNamed("absent"),
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, crypt.ErrUnknownBackend)
	assert.Contains(t, err.Error(), "present", "list what the build does have")
}

func TestWithDecrypterNamed_EncryptOnlyFailsAtNew(t *testing.T) {
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	registerBackend(t, "writeonly", crypt.Backend{Encrypter: stubEncrypter{}})

	_, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fstest.MapFS{})),
		resolver.WithDecrypterNamed("writeonly"),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypt-only")
}

func TestWithDecrypterNamed_ConflictsWithWithDecrypter(t *testing.T) {
	// Two ways of saying which decrypter, disagreeing silently, is
	// exactly the wiring bug that is hardest to see in a diff.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE }]
`)
	registerBackend(t, "named", crypt.Backend{Decrypter: &recordingDecrypter{}})

	_, err := resolver.New[*dynamicpb.Message](dynamicpb.NewMessage(appDesc), spec,
		resolver.WithSource("default", source.FS(fstest.MapFS{})),
		resolver.WithDecrypter(&recordingDecrypter{}),
		resolver.WithDecrypterNamed("named"),
	)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "mutually exclusive"),
		"got %q", err.Error())
}

// stubEncrypter is a crypt.Encrypter that is never called — it exists
// so a Backend can be registered without a Decrypter.
type stubEncrypter struct{}

func (stubEncrypter) Encrypt(_ context.Context, plaintext []byte, _ crypt.Format, _ crypt.Recipients) ([]byte, error) {
	return plaintext, nil
}
