// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package crypt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/trendvidia/chameleon/crypt"
)

// Recipients moved here from chameleon/sops in v0.23.0; this case set
// came with it.

func TestRecipients_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		r    crypt.Recipients
		want bool
	}{
		{"all unset", crypt.Recipients{}, true},
		{"empty slices", crypt.Recipients{KMS: []string{}, Age: []string{}, PGP: []string{}}, true},
		{"only kms", crypt.Recipients{KMS: []string{"arn:..."}}, false},
		{"only age", crypt.Recipients{Age: []string{"age1..."}}, false},
		{"only pgp", crypt.Recipients{PGP: []string{"DEAD..."}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.r.IsEmpty())
		})
	}
}
