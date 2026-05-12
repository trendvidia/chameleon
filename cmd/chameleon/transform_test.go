// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

func parseDoc(t *testing.T, src string) *pxf.Document {
	t.Helper()
	doc, err := pxf.Parse([]byte(src))
	require.NoError(t, err)
	return doc
}

func TestAddField_AppendsToplevel(t *testing.T) {
	doc := parseDoc(t, "base_url = \"x\"\n")
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: true}, raw: "true"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	require.True(t, changed)

	out := string(pxf.FormatDocument(doc))
	assert.Contains(t, out, "base_url")
	assert.Contains(t, out, "feature = true")
}

func TestAddField_IdempotentWhenPresent(t *testing.T) {
	doc := parseDoc(t, "feature = true\n")
	tf := &addField{path: "feature", value: &pxf.BoolVal{Value: false}, raw: "false"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	assert.False(t, changed, "field already present → idempotent skip")

	out := string(pxf.FormatDocument(doc))
	// The original `true` is preserved — apply did NOT overwrite.
	assert.Contains(t, out, "feature = true")
	assert.NotContains(t, out, "feature = false")
}

func TestAddField_NestedPath_AppendsIntoExistingBlock(t *testing.T) {
	doc := parseDoc(t, "db {\n  host = \"x\"\n}\n")
	tf := &addField{path: "db.password", value: &pxf.StringVal{Value: "secret"}, raw: `"secret"`}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	require.True(t, changed)

	out := string(pxf.FormatDocument(doc))
	assert.Contains(t, out, "db {")
	assert.Contains(t, out, `password = "secret"`)
}

func TestAddField_NestedPath_MissingBlockErrors(t *testing.T) {
	doc := parseDoc(t, "other = 1\n")
	tf := &addField{path: "db.password", value: &pxf.StringVal{Value: "x"}, raw: `"x"`}

	_, err := tf.apply(doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "intermediate block")
}

func TestDeleteField_Toplevel(t *testing.T) {
	doc := parseDoc(t, "feature = true\nkeep = 1\n")
	tf := &deleteField{path: "feature"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	require.True(t, changed)

	out := string(pxf.FormatDocument(doc))
	assert.NotContains(t, out, "feature")
	assert.Contains(t, out, "keep = 1")
}

func TestDeleteField_IdempotentWhenAbsent(t *testing.T) {
	doc := parseDoc(t, "keep = 1\n")
	tf := &deleteField{path: "feature"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestDeleteField_MissingIntermediateBlockIsSkip(t *testing.T) {
	doc := parseDoc(t, "keep = 1\n")
	tf := &deleteField{path: "db.password"}

	changed, err := tf.apply(doc)
	require.NoError(t, err, "missing intermediate block should be idempotent skip, not error")
	assert.False(t, changed)
}

func TestDeleteField_NestedPath(t *testing.T) {
	doc := parseDoc(t, "db {\n  host = \"x\"\n  password = \"old\"\n}\n")
	tf := &deleteField{path: "db.password"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	require.True(t, changed)

	out := string(pxf.FormatDocument(doc))
	assert.Contains(t, out, `host = "x"`)
	assert.NotContains(t, out, "password")
}

func TestRenameField_Toplevel(t *testing.T) {
	doc := parseDoc(t, "feature = true\n")
	tf := &renameField{from: "feature", to: "feature_flag"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	require.True(t, changed)

	out := string(pxf.FormatDocument(doc))
	assert.Contains(t, out, "feature_flag = true")
	assert.NotContains(t, out, "feature = ")
}

func TestRenameField_NestedSameParent(t *testing.T) {
	doc := parseDoc(t, "db {\n  user = \"alice\"\n}\n")
	tf := &renameField{from: "db.user", to: "db.username"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	require.True(t, changed)

	out := string(pxf.FormatDocument(doc))
	assert.Contains(t, out, `username = "alice"`)
	assert.NotContains(t, out, `user = "alice"`)
}

func TestRenameField_IdempotentWhenSourceAbsent(t *testing.T) {
	doc := parseDoc(t, "username = \"alice\"\n")
	tf := &renameField{from: "user", to: "username"}

	changed, err := tf.apply(doc)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestRenameField_CollisionRefuses(t *testing.T) {
	doc := parseDoc(t, "user = \"alice\"\nusername = \"bob\"\n")
	tf := &renameField{from: "user", to: "username"}

	_, err := tf.apply(doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestRenameField_CrossContainerRejected(t *testing.T) {
	doc := parseDoc(t, "db {\n  user = \"alice\"\n}\nauth {\n}\n")
	tf := &renameField{from: "db.user", to: "auth.username"}

	_, err := tf.apply(doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-container")
}

func TestRenameField_DepthMismatchRejected(t *testing.T) {
	doc := parseDoc(t, "user = \"alice\"\n")
	tf := &renameField{from: "user", to: "db.user"}

	_, err := tf.apply(doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same depth")
}

func TestParseTransform_AddFieldHappy(t *testing.T) {
	cases := []struct {
		name     string
		strVal   string
		intVal   string
		boolVal  string
		expected string // substring of describe()
	}{
		{"string", `"hello"`, "", "", "= "},
		{"int", "", "42", "", "= 42"},
		{"bool_true", "", "", "true", "= true"},
		{"bool_false", "", "", "false", "= false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr, err := parseTransform("add-field", "feature", "", "", c.strVal, c.intVal, c.boolVal)
			require.NoError(t, err)
			assert.Contains(t, tr.describe(), c.expected)
		})
	}
}

func TestParseTransform_DeleteAndRenameHappy(t *testing.T) {
	d, err := parseTransform("delete-field", "feature", "", "", "", "", "")
	require.NoError(t, err)
	assert.Contains(t, d.describe(), "delete-field feature")

	r, err := parseTransform("rename-field", "", "old", "new", "", "", "")
	require.NoError(t, err)
	assert.Contains(t, r.describe(), "old -> new")
}

func TestParseTransform_Errors(t *testing.T) {
	cases := []struct {
		name                   string
		kind, path, from, to   string
		sv, iv, bv             string
		want                   string
	}{
		{"missing_kind", "", "f", "", "", "x", "", "", "--transform"},
		{"unknown_kind", "set-default", "f", "", "", "x", "", "", "unknown transform"},
		{"add_missing_path", "add-field", "", "", "", "x", "", "", "--path"},
		{"add_no_value_flag", "add-field", "f", "", "", "", "", "", "one of"},
		{"delete_missing_path", "delete-field", "", "", "", "", "", "", "--path"},
		{"delete_with_value", "delete-field", "f", "", "", "x", "", "", "not allowed"},
		{"rename_missing_from", "rename-field", "", "", "y", "", "", "", "--from and --to"},
		{"rename_missing_to", "rename-field", "", "x", "", "", "", "", "--from and --to"},
		{"rename_with_path", "rename-field", "p", "x", "y", "", "", "", "not --path"},
		{"rename_with_value", "rename-field", "", "x", "y", "v", "", "", "not allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseTransform(c.kind, c.path, c.from, c.to, c.sv, c.iv, c.bv)
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), c.want),
				"err %q should contain %q", err.Error(), c.want)
		})
	}
}

func TestParseTransform_MultipleValuesError(t *testing.T) {
	_, err := parseTransform("add-field", "f", "", "", "x", "1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}
