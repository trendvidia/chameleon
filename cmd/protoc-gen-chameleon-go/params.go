// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import "fmt"

// state holds plugin-wide configuration set via --chameleon-go_opt.
// protogen handles `paths=` itself; we only need to capture the
// chameleon-specific options.
type state struct {
	secretPkg    string
	withResolved bool
}

var pluginState = state{secretPkg: defaultSecretPkg}

// parseParams handles --chameleon-go_opt key=value pairs that protogen
// doesn't recognize itself.
func parseParams(name, value string) error {
	switch name {
	case "secret_pkg":
		if value == "" {
			return fmt.Errorf("chameleon-go: secret_pkg cannot be empty")
		}
		pluginState.secretPkg = value
		return nil
	case "with_resolved":
		// Opt-in: emit a `<Message>Resolved` wrapper alongside the
		// standalone accessor functions. Wrapper methods are the
		// recommended consumer API — they centralize the
		// secret.Map argument and make the chameleon-lint
		// `cfg.X.Value` footgun harder to reach (consumers go through
		// the wrapper instead of poking at the raw proto type).
		switch value {
		case "true", "1", "":
			pluginState.withResolved = true
		case "false", "0":
			pluginState.withResolved = false
		default:
			return fmt.Errorf("chameleon-go: with_resolved must be true or false; got %q", value)
		}
		return nil
	case "paths", "module", "M", "default_api_level":
		// Recognized by protogen; let it handle them.
		return nil
	}
	// Be permissive on unknown keys — future protogen options shouldn't
	// break us, and chameleon-specific keys not yet implemented are
	// silent no-ops rather than build breakers.
	return nil
}
