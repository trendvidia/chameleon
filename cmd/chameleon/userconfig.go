// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// userConfigPath is where the loader looks for the user-level
// chameleon config. Centralized at $HOME/.chameleon so scripts can
// invoke chameleon from any directory and reference apps by name.
// Overridable for tests via the env var CHAMELEON_USER_CONFIG
// (not documented as a public knob — purely a hook so smoke tests
// don't depend on the developer's real $HOME).
func userConfigPath() string {
	if override := os.Getenv("CHAMELEON_USER_CONFIG"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to a path that won't exist; loadUserConfig
		// will treat the missing file as "no defaults" rather
		// than blowing up.
		return ".chameleon"
	}
	return filepath.Join(home, ".chameleon")
}

// loadUserConfig reads $HOME/.chameleon as a chameleon.v1.UserConfig
// PXF document. A missing file returns (nil, nil) — the absence of
// a config is not an error; flags simply have no defaults. Parse
// errors and I/O errors other than "not exist" are returned
// verbatim so the user sees them at startup rather than as the
// downstream "required flag missing" error.
func loadUserConfig() (*chameleonv1.UserConfig, error) {
	path := userConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &chameleonv1.UserConfig{}
	if err := pxf.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// selectApp picks the AppConfig from cfg given the user's --app
// flag value. Resolution rules:
//
//  1. explicit appName non-empty → cfg.Apps[appName] (error if absent).
//  2. cfg.DefaultApp non-empty → cfg.Apps[default_app] (error if absent).
//  3. cfg.Apps has exactly one entry → that entry (single-app
//     shortcut; saves config noise for the one-app case).
//  4. Otherwise → nil, with an error listing available apps so
//     the user can pick one.
//
// cfg == nil yields (nil, nil): no .chameleon file, no defaults,
// no error — flags must come from the CLI.
func selectApp(cfg *chameleonv1.UserConfig, appName string) (*chameleonv1.AppConfig, error) {
	if cfg == nil {
		if appName != "" {
			return nil, fmt.Errorf("--app=%q but no %s file found", appName, userConfigPath())
		}
		return nil, nil
	}
	switch {
	case appName != "":
		app, ok := cfg.Apps[appName]
		if !ok {
			return nil, fmt.Errorf("--app=%q not found in %s; available: %s",
				appName, userConfigPath(), listAvailableApps(cfg))
		}
		return app, nil
	case cfg.GetDefaultApp() != "":
		app, ok := cfg.Apps[cfg.GetDefaultApp()]
		if !ok {
			return nil, fmt.Errorf("default_app=%q in %s but not present in apps; available: %s",
				cfg.GetDefaultApp(), userConfigPath(), listAvailableApps(cfg))
		}
		return app, nil
	case len(cfg.Apps) == 1:
		// Single-app shortcut: only one entry, no need for --app or
		// default_app to disambiguate.
		for _, app := range cfg.Apps {
			return app, nil
		}
		return nil, nil // unreachable; defensive.
	case len(cfg.Apps) == 0:
		// File present but no apps defined yet — treat like no file.
		return nil, nil
	default:
		return nil, fmt.Errorf("no app selected: pass --app=<name> or set default_app in %s; available: %s",
			userConfigPath(), listAvailableApps(cfg))
	}
}

// listAvailableApps returns the sorted, comma-joined list of app
// names for inclusion in error messages. Sorted for deterministic
// test output and a stable UX.
func listAvailableApps(cfg *chameleonv1.UserConfig) string {
	names := make([]string, 0, len(cfg.Apps))
	for name := range cfg.Apps {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	return out
}

// resolveAppPath joins a path-typed AppConfig field against app.Root
// when relative. Used to expand schema / chain / layers / include
// values into absolute paths suitable for the rest of the CLI
// pipeline. Empty input passes through unchanged so callers can
// distinguish "AppConfig didn't set this field" from "AppConfig
// said this absolute path."
func resolveAppPath(app *chameleonv1.AppConfig, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(app.GetRoot(), p)
}

// applyAppDefaults merges app's values into the supplied flag
// pointers when the flag is empty. Explicit CLI flags ALWAYS win
// — this function never overwrites a target the user set. Path-typed
// fields (schema, chain, layers, include) are resolved against
// app.Root via resolveAppPath. A nil app is a no-op so callers can
// pass the result of selectApp unconditionally.
//
// fs is the command's parsed FlagSet, used only to tell "the user
// typed this" from "this is the flag's Go default" for flags whose
// default is not the empty string. Pass nil to treat every flag as
// untyped (unit tests that exercise the merge rules directly).
//
// Pointer arguments rather than a return value keep the call sites
// in each Cmd readable: the flag variables are already declared at
// the top of the Cmd; this fills in any that are still empty.
func applyAppDefaults(fs *flag.FlagSet, app *chameleonv1.AppConfig, schema, message, chainPath, layersDir, env, decrypter *string, includePaths *[]string) {
	if app == nil {
		return
	}
	if *schema == "" {
		*schema = resolveAppPath(app, app.GetSchema())
	}
	if *message == "" {
		*message = app.GetMessage()
	}
	if *chainPath == "" {
		*chainPath = resolveAppPath(app, app.GetChain())
	}
	// --layers' Go default is "." (not ""), so a missing CLI flag
	// still looks set. Treat "." as "fall back to config" so the
	// app's layers field actually takes effect.
	if *layersDir == "" || *layersDir == "." {
		if app.GetLayers() != "" {
			*layersDir = resolveAppPath(app, app.GetLayers())
		}
	}
	if *env == "" {
		*env = app.GetEnv()
	}
	// --decrypter's Go default is "none" (not ""), so an unset flag
	// still looks set — but unlike --layers above, "is it set?"
	// cannot be answered from the value: `--decrypter=none` is a
	// deliberate opt-out (it wires resolver.WithoutDecrypter), and
	// reading it as "unset" would let a config file turn decryption
	// back on for an operator who explicitly declined it, with
	// `reveal` then printing plaintext. So ask the FlagSet instead.
	if !flagSet(fs, "decrypter") && (*decrypter == "" || *decrypter == "none") {
		if app.GetDecrypter() != "" {
			*decrypter = app.GetDecrypter()
		}
	}
	if len(*includePaths) == 0 && len(app.GetInclude()) > 0 {
		resolved := make([]string, 0, len(app.GetInclude()))
		for _, p := range app.GetInclude() {
			resolved = append(resolved, resolveAppPath(app, p))
		}
		*includePaths = resolved
	}
}

// flagSet reports whether the user actually typed -name on the command
// line, as opposed to the flag still holding its Go default. fs.Visit
// walks only the flags Parse saw, which is the one thing the flag
// package can tell us that the value cannot.
//
// A nil fs reports false: callers that merge defaults without a
// command line (unit tests) get the value-only rules.
func flagSet(fs *flag.FlagSet, name string) bool {
	if fs == nil {
		return false
	}
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
