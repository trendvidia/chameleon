// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package resolver_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/awnumar/memguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/chameleon/resolver"
	"github.com/trendvidia/chameleon/sops"
	"github.com/trendvidia/chameleon/source"
)

// WithDecrypter direct coverage. Until now this functional option
// was only exercised indirectly (and the no-decrypter error path was
// the only assertion). A regression that dropped the option on the
// floor — e.g. by forgetting to assign to c.dec — would silently
// fall back to the default decrypter (or none) at resolve time.

// recordingDecrypter is a fake sops.Decrypter that records its call
// arguments and returns a fixed plaintext. Lets us assert WithDecrypter
// installed THIS specific decrypter (rather than the package default)
// in the resolver's config.
type recordingDecrypter struct {
	calls       int32
	gotCipher   []byte
	gotFormat   sops.Format
	plaintextTo []byte // returned in the memguard buffer
	err         error
}

func (r *recordingDecrypter) Decrypt(ctx context.Context, ciphertext []byte, format sops.Format) (*memguard.LockedBuffer, error) {
	atomic.AddInt32(&r.calls, 1)
	r.gotCipher = append(r.gotCipher[:0], ciphertext...)
	r.gotFormat = format
	if r.err != nil {
		return nil, r.err
	}
	return memguard.NewBufferFromBytes(r.plaintextTo), nil
}

func TestWithDecrypter_InstallsAndIsInvokedOnEncryptedLayer(t *testing.T) {
	// One encrypted layer in the chain spec → Resolve must call
	// the decrypter we installed via WithDecrypter, NOT sops.Default().
	// The fake's call counter is the canary; the plaintext it
	// returns also ends up in the resolved config, providing a
	// second independent check.
	chainDesc, appDesc := compileAll(t)

	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`)

	// Ciphertext bytes are opaque to the resolver — only the fake
	// decrypter sees them. Use a sentinel string so the test can
	// assert the fake received exactly what was on "disk".
	ciphertext := []byte("ciphertext-sentinel")
	fs := fstest.MapFS{
		"secret.pxf": &fstest.MapFile{Data: ciphertext},
	}

	// The fake returns a tiny valid PXF body. Resolver feeds this
	// through the PXF decoder, so it must parse.
	fake := &recordingDecrypter{
		plaintextTo: []byte(`base_url = "from-fake-decrypter"`),
	}

	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithDecrypter(fake),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err, "resolve must succeed when WithDecrypter is installed")
	defer res.Destroy()

	assert.Equal(t, int32(1), atomic.LoadInt32(&fake.calls),
		"WithDecrypter's decrypter must be called exactly once for one encrypted layer")
	assert.Equal(t, string(ciphertext), string(fake.gotCipher),
		"fake decrypter must receive the layer's raw bytes")
	// Format is FormatProtowire (the PXF spelling sops calls "pxf").
	assert.Equal(t, sops.FormatProtowire, fake.gotFormat,
		"resolver must pass FormatProtowire to the decrypter")

	// The resolved plaintext propagates into the message.
	got := res.Config.Get(appDesc.Fields().ByName("base_url")).String()
	assert.Equal(t, "from-fake-decrypter", got,
		"value must come from the fake's plaintext — proves WithDecrypter is in the path")
}

func TestWithDecrypter_DecryptErrorPropagates(t *testing.T) {
	// When the installed decrypter errors, Resolve must surface
	// that error rather than panicking or silently emitting an
	// empty layer. The wrapping is whatever the resolver chooses
	// — assert the underlying sentinel reaches the caller.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "secret" path = "secret.pxf" for_each = ONCE encrypted = true required = true }]
`)

	sentinel := errors.New("decrypter said no")
	fake := &recordingDecrypter{err: sentinel}

	fs := fstest.MapFS{
		"secret.pxf": &fstest.MapFile{Data: []byte("anything")},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithDecrypter(fake),
	)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel,
		"resolver must propagate the decrypter's error via errors.Is")
}

// Trace.Has direct coverage. Test on a nil Trace (the documented
// safe-receiver contract) and on a real Trace recorded during a
// resolve.

func TestTraceHas_NilReceiverReturnsFalse(t *testing.T) {
	// Has is documented to be nil-safe — callers don't have to
	// guard. A regression to a nil-pointer panic would surface here.
	var tr *resolver.Trace
	if got := tr.Has("any-path"); got {
		t.Errorf("(*Trace)(nil).Has = true, want false")
	}
}

func TestTraceHas_AfterResolve_TrueForKnownPathsFalseForUnknown(t *testing.T) {
	// End-to-end: enable WithTrace, resolve a layer that sets one
	// field, then assert Has(true) for the field that was set and
	// Has(false) for a path that doesn't appear in the schema's
	// resolved set. The companion Get tests live in resolver_test.go;
	// this file pins Has alongside.
	chainDesc, appDesc := compileAll(t)
	spec := parseSpec(t, chainDesc, `
version = "1.0.0"
environments = [{ name = "prod" }]
layers = [{ id = "base" path = "base.pxf" for_each = ONCE required = true }]
`)
	fs := fstest.MapFS{
		"base.pxf": &fstest.MapFile{Data: []byte(`base_url = "x"`)},
	}
	sample := dynamicpb.NewMessage(appDesc)
	r, err := resolver.New[*dynamicpb.Message](sample, spec,
		resolver.WithSource("default", source.FS(fs)),
		resolver.WithTrace(),
	)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), resolver.Request{Environment: "prod"})
	require.NoError(t, err)
	defer res.Destroy()

	require.NotNil(t, res.Trace, "Trace must be non-nil when WithTrace was passed")
	assert.True(t, res.Trace.Has("base_url"),
		"Has must report true for a path the resolver populated")
	assert.False(t, res.Trace.Has("definitely-not-a-real-field"),
		"Has must report false for a path the resolver never saw")
}
