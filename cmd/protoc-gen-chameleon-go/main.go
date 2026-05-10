// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// protoc-gen-chameleon-go is a protoc plugin that emits a sidecar Go
// file per .proto containing typed-path accessors for every
// pxf.Secret-typed field. Consumers get compile-time-checked access to
// the secret.Map returned by the chameleon resolver:
//
//	res, _ := r.Resolve(ctx, req)
//	defer res.Destroy()
//	pw := mypkg.AppConfigSecret_DbPassword(res.Secrets)
//	buf, _ := pw.Open(); defer buf.Destroy()
//
// Run alongside protoc-gen-go:
//
//	protoc \
//	    --go_out=. --go_opt=paths=source_relative \
//	    --chameleon-go_out=. --chameleon-go_opt=paths=source_relative \
//	    proto/myapp/v1/config.proto
//
// This produces config.pb.go (protoc-gen-go) + config.chameleon.go
// (this plugin).
//
// What's emitted, per Secret-typed field:
//
//   - Top-level scalar: a function taking secret.Map, returning *secret.Secret.
//   - Nested in user-defined message: same; path is dotted.
//   - Repeated: function takes (secret.Map, index int).
//   - Map<*, Secret>: function takes (secret.Map, key K) where K matches
//     the proto map key type.
//
// Function naming follows `<RootMessage>Secret_<Path>` where Path uses
// underscores between segments. Example: a Secret at
// `app.AppConfig.endpoints[i].auth_token` produces
// `AppConfigSecret_Endpoints_AuthToken(s secret.Map, i int) *secret.Secret`.
//
// Plugin parameters (--chameleon-go_opt=KEY=VALUE,...):
//
//	paths=source_relative   pass-through to protogen, mirrors protoc-gen-go's option.
//	secret_pkg=IMPORT_PATH  override the runtime secret package
//	                        (default: github.com/trendvidia/chameleon/secret).
//
// Limitation. The proto-generated type's Secret field still has a public
// Value string — `cfg.DbPassword.Value` reads the plaintext directly,
// bypassing memguard. That's a property of protoc-gen-go's emit shape;
// fully closing it requires either forking protoc-gen-go or wrapping
// its output. The sidecar emits the *correct* path constants so user
// code goes through secret.Map (which holds memguard Enclaves), but a
// determined caller can still read the raw field. Discipline + lint
// rule territory; the tooling firewall is on the roadmap.
package main

import (
	"google.golang.org/protobuf/compiler/protogen"
)

const defaultSecretPkg = "github.com/trendvidia/chameleon/secret"

func main() {
	var flags protogen.Options
	flags.ParamFunc = parseParams
	flags.Run(generate)
}
