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
	parents, order, err := readEnvironments(cs)
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
	byName := make(map[string]*chameleonv1.Environment, len(order))
	for _, e := range cs.GetEnvironments() {
		byName[e.GetName()] = e
	}
	for i := len(ancestry) - 1; i >= 0; i-- {
		if e := byName[ancestry[i]]; e != nil && e.GetKeyName() != "" {
			return e.GetKeyName(), nil
		}
	}
	return "", nil
}
