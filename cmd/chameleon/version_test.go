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
	offered := bashSubcommands(t, got)
	assert.Subset(t, offered,
		[]string{"explain", "verify", "check-schema", "migrate", "hash", "lint", "version", "completion"},
		"the top-level subcommand list dropped an entry")
	// Every subcommand the list offers must also have a case in the
	// flag switch, or completing after its name silently produces
	// nothing — the gap that would otherwise let a half-wired
	// subcommand ship. Asserted over the list rather than for one name,
	// so the next subcommand added is covered without editing this.
	// version and help take no flags of their own.
	for _, sub := range offered {
		switch sub {
		case "version", "help":
			continue
		}
		assert.Contains(t, got, "\n        "+sub+")\n",
			"subcommand %q is offered by the completion list but has no flag case", sub)
	}
}

// bashSubcommands returns the subcommands the generated bash script
// offers at the top level, read out of its `local subcmds="…"` line so
// the assertions above are about what the script says rather than a
// copy of it kept in step by hand.
func bashSubcommands(t *testing.T, script string) []string {
	t.Helper()
	const marker = `local subcmds="`
	i := strings.Index(script, marker)
	require.GreaterOrEqual(t, i, 0, "bash completion should declare a subcmds list")
	rest := script[i+len(marker):]
	j := strings.Index(rest, `"`)
	require.GreaterOrEqual(t, j, 0, "subcmds list is unterminated")
	return strings.Fields(rest[:j])
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
