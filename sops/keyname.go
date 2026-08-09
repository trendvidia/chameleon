// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import (
	"fmt"

	sopsage "github.com/trendvidia/sops/v4/age"

	"github.com/trendvidia/chameleon/crypt"
)

// RecipientsFromKeyName resolves a named age key from the sops
// keystore (SOPS_AGE_KEY_FILE, PXF multi-key format) into a
// [crypt.Recipients] value carrying that age recipient.
//
// Passing an empty name returns an empty crypt.Recipients and a nil error
// — the caller is expected to fall back to its default recipient
// source (e.g. DefaultRecipientFromKeyFile, externally configured
// RecipientsByEnv, etc.). All keystore implementation details
// (file format, env-var contract, plugin-identity restrictions)
// stay inside the sops library; chameleon only ever sees the name
// going in and the recipient string coming out.
func RecipientsFromKeyName(name string) (crypt.Recipients, error) {
	if name == "" {
		return crypt.Recipients{}, nil
	}
	recipient, err := sopsage.RecipientFromKeyFileByName(name)
	if err != nil {
		return crypt.Recipients{}, fmt.Errorf("sops: resolve age key %q: %w", name, err)
	}
	return crypt.Recipients{Age: []string{recipient}}, nil
}
