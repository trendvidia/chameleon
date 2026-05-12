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

func TestAddField_NestedPathRejectedInV1(t *testing.T) {
	doc := parseDoc(t, "x = 1\n")
	tf := &addField{path: "db.password", value: &pxf.StringVal{Value: "x"}, raw: `"x"`}

	_, err := tf.apply(doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested")
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
			tr, err := parseTransform("add-field", "feature", c.strVal, c.intVal, c.boolVal)
			require.NoError(t, err)
			assert.Contains(t, tr.describe(), c.expected)
		})
	}
}

func TestParseTransform_Errors(t *testing.T) {
	cases := []struct {
		name           string
		kind, path, sv string
		want           string
	}{
		{"missing_path", "add-field", "", "x", "--path"},
		{"missing_kind", "", "f", "x", "--transform"},
		{"unknown_kind", "set-default", "f", "x", "unknown transform"},
		{"no_value_flag", "add-field", "f", "", "one of"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseTransform(c.kind, c.path, c.sv, "", "")
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), c.want),
				"err %q should contain %q", err.Error(), c.want)
		})
	}
}

func TestParseTransform_MultipleValuesError(t *testing.T) {
	_, err := parseTransform("add-field", "f", "x", "1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}
