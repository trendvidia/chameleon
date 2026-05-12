// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCmd_Smoke(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, versionCmd(&buf, nil))

	got := buf.String()
	assert.True(t, strings.HasPrefix(got, "chameleon "),
		"first line should start with 'chameleon ', got: %q", got)
	assert.Contains(t, got, "go:", "should report Go version")
	// commit/built lines only present when built with VCS info — not
	// guaranteed under `go test`, so don't assert on them.
}

func TestVersionCmd_Help(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, versionCmd(&buf, []string{"--help"}))
	// Help goes to fs.Output() not the writer arg; the call should
	// just succeed cleanly without erroring.
}

func TestCompletionCmd_Bash(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, completionCmd(&buf, []string{"bash"}))

	got := buf.String()
	assert.Contains(t, got, "_chameleon()", "should emit bash function")
	assert.Contains(t, got, "complete -F _chameleon chameleon",
		"should register the completion function")
	assert.Contains(t, got, "explain verify migrate hash version completion",
		"should list all subcommands")
}

func TestCompletionCmd_Zsh(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, completionCmd(&buf, []string{"zsh"}))

	got := buf.String()
	assert.True(t, strings.HasPrefix(got, "#compdef chameleon"),
		"zsh completion should start with the compdef directive")
	assert.Contains(t, got, "compdef _chameleon chameleon")
}

func TestCompletionCmd_Fish(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, completionCmd(&buf, []string{"fish"}))

	got := buf.String()
	assert.Contains(t, got, "complete -c chameleon",
		"fish completion should use the `complete -c chameleon` form")
	assert.Contains(t, got, "__fish_use_subcommand")
}

func TestCompletionCmd_UnknownShell(t *testing.T) {
	var buf bytes.Buffer
	err := completionCmd(&buf, []string{"powershell"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported shell")
}

func TestCompletionCmd_NoArg(t *testing.T) {
	var buf bytes.Buffer
	err := completionCmd(&buf, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one shell argument required")
}
