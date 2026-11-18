// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain

import (
	"fmt"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"google.golang.org/protobuf/proto"
)

// EffectiveKeyName returns the Environment.key_name that applies to
// envName, inheriting up the ancestry tree. A child env's non-empty
// key_name overrides its parent's; a child's empty key_name inherits
// the nearest non-empty ancestor value. Returns "" with a nil error
// when no env in the chain leaf→root sets key_name (callers fall back
// to the sops library's DefaultRecipientFromKeyFile, or to an
// out-of-config recipient list).
//
// Errors when spec is not a chameleon.v1.ChainSpec, envName is not
// declared in the spec, or the ancestry contains a cycle / dangling
// extends.
func EffectiveKeyName(spec proto.Message, envName string) (string, error) {
	cs, err := toChainSpec(spec)
	if err != nil {
		return "", err
	}
	parents, _, err := readEnvironments(cs)
	if err != nil {
		return "", err
	}
	if envName == "" {
		envName = cs.GetDefaultEnvironment()
		if envName == "" {
			return "", fmt.Errorf("chain: no env supplied and spec has no default_environment")
		}
	}
	ancestry, err := ancestryChain(parents, envName)
	if err != nil {
		return "", err
	}
	keys := effectiveKeyNames(cs, ancestry)
	return keys[len(keys)-1], nil
}

// effectiveKeyNames returns the effective key_name of every env in
// ancestry (root first, leaf last, as ancestryChain returns it), by the
// rule EffectiveKeyName documents: an env's own non-empty key_name, or
// else its parent's effective one. An env no ancestor gives a key_name
// gets "".
func effectiveKeyNames(cs *chameleonv1.ChainSpec, ancestry []string) []string {
	own := make(map[string]string, len(cs.GetEnvironments()))
	for _, e := range cs.GetEnvironments() {
		own[e.GetName()] = e.GetKeyName()
	}
	keys := make([]string, len(ancestry))
	inherited := ""
	for i, name := range ancestry {
		if k := own[name]; k != "" {
			inherited = k
		}
		keys[i] = inherited
	}
	return keys
}
