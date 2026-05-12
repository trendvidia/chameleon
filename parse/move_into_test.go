// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/chameleon/parse"
	"github.com/trendvidia/chameleon/secret"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// readSecretPlain seals/unseals a Map-stored Secret and returns the
// plaintext for assertion. Caller-controlled cleanup via t.Cleanup.
func readSecretPlain(t *testing.T, s *secret.Secret) string {
	t.Helper()
	require.NotNil(t, s)
	buf, err := s.Open()
	require.NoError(t, err)
	t.Cleanup(buf.Destroy)
	return string(buf.Bytes())
}

// TestMoveInto_HookEntriesSurvive — the hook routes a value into the
// caller's Map during decode; the merged proto message has an empty
// Secret.value. MoveInto walks the message, marks the path visited,
// and the hook entry is preserved.
func TestMoveInto_HookEntriesSurvive(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error {
			secrets.Set(path, secret.FromString(value, "", ""))
			return nil
		},
	}
	msg, err := opts.UnmarshalDescriptor([]byte(`db_password = "p1"`), desc)
	require.NoError(t, err)

	require.NoError(t, parse.MoveInto(msg, secrets))

	assert.Equal(t, 1, secrets.Len())
	assert.Equal(t, "p1", readSecretPlain(t, secrets.Get("db_password")))
}

// TestMoveInto_BlockFormHookRoutedWithMetadata — since protowire-go
// v0.77.0 the block form ALSO fires OnSecretField; the value lands
// in the Map via the hook, and hint/fingerprint stay on the proto
// message until MoveInto folds them back onto the existing Map
// entry via SetMetadata. End state: one Map entry per Secret path
// with all three fields populated, regardless of whether the source
// used scalar shorthand or block form.
func TestMoveInto_BlockFormHookRoutedWithMetadata(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error {
			secrets.Set(path, secret.FromString(value, "", ""))
			return nil
		},
	}
	input := `db_password {
  value = "p1"
  hint = "Postgres primary"
  fingerprint = "sha256:abc"
}`
	msg, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	// v0.77.0 — hook fires once for the block-form value subfield;
	// hint and fingerprint were assigned to the proto message
	// normally and are not yet on the Map's Secret.
	require.Equal(t, 1, secrets.Len(), "hook should fire for block form (protowire-go v0.77)")
	hookEntry := secrets.Get("db_password")
	require.NotNil(t, hookEntry)
	assert.Equal(t, "", hookEntry.Hint(), "hook closure didn't pass hint yet")

	// MoveInto walks the message, sees the hint/fingerprint on the
	// (otherwise empty-value) Secret, and folds them onto the Map's
	// existing hook entry via SetMetadata.
	require.NoError(t, parse.MoveInto(msg, secrets))

	assert.Equal(t, 1, secrets.Len(), "still one entry after enrichment")
	s := secrets.Get("db_password")
	require.NotNil(t, s)
	assert.Equal(t, "p1", readSecretPlain(t, s))
	assert.Equal(t, "Postgres primary", s.Hint(), "metadata folded back by MoveInto")
	assert.Equal(t, "sha256:abc", s.Fingerprint())
}

// TestMoveInto_OrphanCleanup — a hook entry whose path is no longer
// present on the merged message (simulated here by Set + manual Clear)
// is dropped by MoveInto's orphan sweep. This is the path a higher
// layer's null override traverses end-to-end in the resolver.
func TestMoveInto_OrphanCleanup(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	// Pre-populate as the hook would have done for a lower layer.
	secrets.Set("db_password", secret.FromString("p1", "", ""))

	// Decode a message that has NO db_password (simulating a higher
	// layer that nulled it — after merge, dst has the Secret cleared).
	msg, err := pxf.UnmarshalDescriptor([]byte(`base_url = "example.com"`), desc)
	require.NoError(t, err)

	require.NoError(t, parse.MoveInto(msg, secrets))

	assert.Equal(t, 0, secrets.Len(), "orphaned hook entry should be dropped")
	assert.Nil(t, secrets.Get("db_password"))
}

// TestMoveInto_HookEntryOverwrittenByBlockForm — when the merged
// message ends up with a block-form Secret at a path that ALSO had a
// hook entry (e.g. lower layer used scalar shorthand, higher layer
// used block form), MoveInto's harvest of the block-form value
// overwrites the hook entry. Last write wins matches merge semantics.
func TestMoveInto_HookEntryOverwrittenByBlockForm(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	// Lower layer wrote scalar shorthand via hook.
	secrets.Set("db_password", secret.FromString("p1", "", ""))

	// Higher layer used block form; the merged message carries the
	// block-form value on Secret.value.
	input := `db_password {
  value = "p2"
}`
	msg, err := pxf.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	require.NoError(t, parse.MoveInto(msg, secrets))

	assert.Equal(t, 1, secrets.Len())
	assert.Equal(t, "p2", readSecretPlain(t, secrets.Get("db_password")))
}

// TestMoveInto_NilMessage — nil message is a no-op (matches Move's
// nil semantics so callers don't have to special-case empty layers).
func TestMoveInto_NilMessage(t *testing.T) {
	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)
	secrets.Set("db_password", secret.FromString("p1", "", ""))

	require.NoError(t, parse.MoveInto(nil, secrets))

	// Nothing was visited, but nothing should be dropped either —
	// nil-message means "no message to reconcile against," not
	// "every entry is orphaned." This is the cheap-to-guarantee
	// behavior that lets resolver code call MoveInto unconditionally.
	assert.Equal(t, 1, secrets.Len(),
		"nil message must not drop hook entries — caller may pass nil for an optional missing layer")
}

// TestMoveInto_NilMap — must error; the caller has to own a Map.
func TestMoveInto_NilMap(t *testing.T) {
	desc := compileApp(t)
	msg, err := pxf.UnmarshalDescriptor([]byte(`db_password = "x"`), desc)
	require.NoError(t, err)

	err = parse.MoveInto(msg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil")
}

// TestMoveInto_NestedAndMapPaths — combines a nested Secret and a
// map<string,Secret> to verify path-format parity between the hook
// and the walker. After MoveInto:
//
//   - primary.auth_token comes from the hook (scalar shorthand).
//   - tenant_keys["acme"] comes from the hook (map scalar shorthand).
//   - All paths still resolve via secrets.Get with chameleon's
//     pathfmt convention.
func TestMoveInto_NestedAndMapPaths(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error {
			secrets.Set(path, secret.FromString(value, "", ""))
			return nil
		},
	}
	input := `
primary {
  url = "https://api"
  auth_token = "tok"
}
tenant_keys = {
  "acme": "k1"
}
`
	msg, err := opts.UnmarshalDescriptor([]byte(input), desc)
	require.NoError(t, err)

	require.NoError(t, parse.MoveInto(msg, secrets))

	assert.Equal(t, 2, secrets.Len())
	assert.Equal(t, "tok", readSecretPlain(t, secrets.Get("primary.auth_token")))
	assert.Equal(t, "k1", readSecretPlain(t, secrets.Get(`tenant_keys["acme"]`)))
}
