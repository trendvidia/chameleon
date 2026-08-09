// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package sops

import "github.com/trendvidia/chameleon/crypt"

// The contracts below moved to chameleon/crypt in v0.23.0 so that
// naming a type does not link an implementation: a consumer that
// referred to sops.Decrypter or sops.FormatProtowire pulled the whole
// AWS / Azure / GCP / Vault key-management tree into its binary just
// to spell a type (trendvidia/chameleon#130).
//
// These aliases keep that code compiling. They are the same types —
// `var d sops.Decrypter = myDecrypter{}` and `var d crypt.Decrypter =
// myDecrypter{}` are interchangeable, and either can be handed to
// resolver.WithDecrypter. Switching an import from this package to
// chameleon/crypt is what actually sheds the weight.

// Decrypter is an alias for [crypt.Decrypter].
//
// Deprecated: use crypt.Decrypter. Importing this package for the
// interface alone links the sops key-management tree.
type Decrypter = crypt.Decrypter

// Encrypter is an alias for [crypt.Encrypter].
//
// Deprecated: use crypt.Encrypter.
type Encrypter = crypt.Encrypter

// Recipients is an alias for [crypt.Recipients].
//
// Deprecated: use crypt.Recipients.
type Recipients = crypt.Recipients

// Format is an alias for [crypt.Format].
//
// Deprecated: use crypt.Format.
type Format = crypt.Format

// The format constants moved with the type.
//
// Deprecated: use the crypt.Format constants.
const (
	FormatAuto      = crypt.FormatAuto
	FormatBinary    = crypt.FormatBinary
	FormatDotenv    = crypt.FormatDotenv
	FormatINI       = crypt.FormatINI
	FormatJSON      = crypt.FormatJSON
	FormatYAML      = crypt.FormatYAML
	FormatProtowire = crypt.FormatProtowire
)

// FormatForPath infers a Format from a filename extension.
//
// Deprecated: use [crypt.FormatForPath].
func FormatForPath(path string) crypt.Format { return crypt.FormatForPath(path) }
