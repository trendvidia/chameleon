// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import "github.com/trendvidia/chameleon/crypt"

// BackendName is the name this package registers itself under. Pass it
// to resolver.WithDecrypterNamed (or [crypt.Get]) in a build that links
// more than one backend.
const BackendName = "sops"

// init registers the in-process sops implementation with the
// [crypt] registry, so a program can select it with a blank import
// and never mention it again:
//
//	import _ "github.com/trendvidia/chameleon/sops"
//
// This is the database/sql driver model, and it exists for the same
// reason: the resolver must not import this package (doing so linked
// every cloud KMS SDK into plaintext-only consumers —
// trendvidia/chameleon#130), but a consumer needs some way to say
// "and use this one" that costs a single line.
//
// Registration is unconditional — importing the package at all,
// including for [Default] or [DefaultEncrypter], registers the
// backend. That is deliberate: a build that has paid for the sops
// tree should not also have to wire it up.
func init() {
	crypt.Register(BackendName, crypt.Backend{
		Decrypter: Default(),
		Encrypter: DefaultEncrypter(),
	})
}
