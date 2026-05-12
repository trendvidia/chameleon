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

// The MoveOptions.LayerResults clearing-semantic tests. Each test
// simulates a multi-layer decode by constructing pxf.Result values
// at known states + a merged proto message. MoveIntoWithOptions
// should preserve the existing hook entry when the most-recent
// layer set .value (hook-routed), and clear it when the most-recent
// layer wrote a block-form Secret WITHOUT value (WKT atomic-merge).

// TestMoveAtomicClear_HookRoutedKept — single layer wrote `pw = "x"`.
// The hook routed value; merged dst.pw has empty value field.
// MoveIntoWithOptions should KEEP the existing hook entry — value
// was set in the only layer's Result.
func TestMoveAtomicClear_HookRoutedKept(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	// Hook routed N's value into the Map.
	secrets.Set("db_password", secret.FromString("n-pw", "", ""))

	// Decode a single layer that mirrors N's scalar shorthand. The
	// hook would have run during decode; here we just produce the
	// Result + msg shape so the walker can interpret it.
	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error {
			// Re-route into the Map (idempotent for this test).
			secrets.Set(path, secret.FromString(value, "", ""))
			return nil
		},
	}
	msg, result, err := opts.UnmarshalFullDescriptor([]byte(`db_password = "n-pw"`), desc)
	require.NoError(t, err)

	err = parse.MoveIntoWithOptions(msg, secrets, parse.MoveOptions{
		LayerResults: []*pxf.Result{result},
	})
	require.NoError(t, err)

	got := secrets.Get("db_password")
	require.NotNil(t, got, "hook entry must survive — only layer set .value via hook")
	buf, err := got.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "n-pw", string(buf.Bytes()))
}

// TestMoveAtomicClear_BlockFormWithoutValueClears — N: `pw = "x"`,
// M: `pw { hint = "h" }`. M's atomic-merge wholesale replaces N's
// Secret with an empty-value Secret. The new clearing semantic
// says secrets["pw"] should be cleared — the prior hook entry no
// longer reflects what the merged config carries.
func TestMoveAtomicClear_BlockFormWithoutValueClears(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	// Pretend N's hook fired and routed value.
	secrets.Set("db_password", secret.FromString("n-pw", "", ""))

	// N's layer state (scalar shorthand, value set).
	optsN := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	_, resN, err := optsN.UnmarshalFullDescriptor([]byte(`db_password = "n-pw"`), desc)
	require.NoError(t, err)

	// M's layer state (block form, hint only — no value).
	optsM := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	msgM, resM, err := optsM.UnmarshalFullDescriptor([]byte(`db_password { hint = "Postgres primary" }`), desc)
	require.NoError(t, err)

	err = parse.MoveIntoWithOptions(msgM, secrets, parse.MoveOptions{
		LayerResults: []*pxf.Result{resN, resM},
	})
	require.NoError(t, err)

	assert.Nil(t, secrets.Get("db_password"),
		"WKT atomic-merge clear: M's block-form-without-value replaces N entirely, dropping N's value")
}

// TestMoveAtomicClear_BlockFormWithValueKept — N: `pw = "x"`, M:
// `pw { value = "y" hint = "h" }`. M's block form supplied value via
// the hook. Merged dst.pw has value="" (hook took it). The clearing
// semantic should NOT fire — M's most-recent Result marks
// .value present.
func TestMoveAtomicClear_BlockFormWithValueKept(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)

	// Simulate the hook firing for N then M (M's value wins).
	secrets.Set("db_password", secret.FromString("m-pw", "", ""))

	optsN := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	_, resN, err := optsN.UnmarshalFullDescriptor([]byte(`db_password = "n-pw"`), desc)
	require.NoError(t, err)

	optsM := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	msgM, resM, err := optsM.UnmarshalFullDescriptor(
		[]byte(`db_password { value = "m-pw" hint = "Postgres primary" }`), desc)
	require.NoError(t, err)

	err = parse.MoveIntoWithOptions(msgM, secrets, parse.MoveOptions{
		LayerResults: []*pxf.Result{resN, resM},
	})
	require.NoError(t, err)

	got := secrets.Get("db_password")
	require.NotNil(t, got, "M set .value via hook — entry must survive")
	buf, err := got.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "m-pw", string(buf.Bytes()))
	assert.Equal(t, "Postgres primary", got.Hint(),
		"M's hint should fold onto the entry")
}

// TestMoveAtomicClear_LegacyMoveIntoPreservesEntries — MoveInto
// (zero-options shim) MUST preserve the pre-v0.11 behavior:
// empty-value Secrets on dst keep their hook entry regardless of
// whether the upstream layer wrote .value. Caller has to opt into
// the atomic-merge clearing via MoveIntoWithOptions.
func TestMoveAtomicClear_LegacyMoveIntoPreservesEntries(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)
	secrets.Set("db_password", secret.FromString("n-pw", "", ""))

	opts := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	msg, _, err := opts.UnmarshalFullDescriptor(
		[]byte(`db_password { hint = "Postgres primary" }`), desc)
	require.NoError(t, err)

	// Use MoveInto (no LayerResults). Pre-v0.11 behavior.
	err = parse.MoveInto(msg, secrets)
	require.NoError(t, err)

	got := secrets.Get("db_password")
	require.NotNil(t, got, "MoveInto (no opts) must preserve the entry — pre-v0.11 behavior")
	assert.Equal(t, "Postgres primary", got.Hint(),
		"MoveInto still folds metadata onto the existing entry")
}

// TestMoveAtomicClear_EmptyBlockClears — N: `pw = "x"`, M: `pw { }`.
// M's empty block writes a Secret with NO subfields. Atomic-merge
// clear should fire.
func TestMoveAtomicClear_EmptyBlockClears(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)
	secrets.Set("db_password", secret.FromString("n-pw", "", ""))

	optsN := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	_, resN, err := optsN.UnmarshalFullDescriptor([]byte(`db_password = "n-pw"`), desc)
	require.NoError(t, err)

	optsM := pxf.UnmarshalOptions{
		OnSecretField: func(path, value string) error { return nil },
	}
	msgM, resM, err := optsM.UnmarshalFullDescriptor([]byte(`db_password { }`), desc)
	require.NoError(t, err)

	err = parse.MoveIntoWithOptions(msgM, secrets, parse.MoveOptions{
		LayerResults: []*pxf.Result{resN, resM},
	})
	require.NoError(t, err)

	assert.Nil(t, secrets.Get("db_password"),
		"empty `pw { }` is a wholesale replace with an empty-Secret; atomic-merge clear")
}

// TestMoveAtomicClear_HookEntryAcrossTwoScalarShorthands — N + M
// both use scalar shorthand. Both Results mark .value present.
// The most-recent (M) layer set value → entry stays.
func TestMoveAtomicClear_HookEntryAcrossTwoScalarShorthands(t *testing.T) {
	desc := compileApp(t)

	secrets := secret.NewMap()
	t.Cleanup(secrets.Destroy)
	secrets.Set("db_password", secret.FromString("m-pw", "", ""))

	optsN := pxf.UnmarshalOptions{OnSecretField: func(string, string) error { return nil }}
	_, resN, err := optsN.UnmarshalFullDescriptor([]byte(`db_password = "n-pw"`), desc)
	require.NoError(t, err)

	optsM := pxf.UnmarshalOptions{OnSecretField: func(string, string) error { return nil }}
	msgM, resM, err := optsM.UnmarshalFullDescriptor([]byte(`db_password = "m-pw"`), desc)
	require.NoError(t, err)

	err = parse.MoveIntoWithOptions(msgM, secrets, parse.MoveOptions{
		LayerResults: []*pxf.Result{resN, resM},
	})
	require.NoError(t, err)

	got := secrets.Get("db_password")
	require.NotNil(t, got)
	buf, err := got.Open()
	require.NoError(t, err)
	defer buf.Destroy()
	assert.Equal(t, "m-pw", string(buf.Bytes()))
}
