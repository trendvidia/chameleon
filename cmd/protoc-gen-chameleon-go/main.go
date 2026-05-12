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
//	secret_pkg=IMPORT_PATH   override the runtime secret package
//	                         (default: github.com/trendvidia/chameleon/secret).
//	with_resolved=true|false emit a `<Root>Resolved` wrapper per root
//	                         message with at least one Secret, alongside
//	                         the function-form accessors. Wrapper methods
//	                         encapsulate the secret.Map argument and steer
//	                         callers away from the `cfg.X.Value` footgun;
//	                         chameleon-lint stays the load-bearing
//	                         compile-time check until Phase 2b ships the
//	                         field-replacement variant.
//	                         (default: false; opt-in.)
//
// Compile-time prevention of `cfg.X.Value`. The `with_resolved` mode
// emits a wrapper with typed accessor methods for every field —
// scalar / repeated / map primitives, enums, bytes, non-Secret
// messages, plus *secret.Secret accessors and chained-access helpers
// for Secret-bearing paths. Consumers using the wrapper end-to-end
// never see *pxfv1.Secret.Value: the typed Secret methods return
// *secret.Secret, and the non-Secret field getters return either a
// scalar Go type or a *MsgType that contains no Secret (since
// wrapper-bearing message containers are routed through the
// chained-access helpers).
//
// Underlying() — the legacy escape hatch that returns the raw proto —
// is marked `// Deprecated:` on every wrapper since chameleon#6
// Phase 2b PR4. Static analyzers (staticcheck SA1019,
// golangci-lint's deprecation linters) flag consumer code that
// still calls it. Underlying() is kept emitted (not hard-removed)
// for callers passing the wrapper's payload to proto.Marshal,
// proto.Equal, or other reflection-driven APIs until a follow-up
// adds proto.Message delegation on the wrapper itself; once that
// lands, Underlying() can be hard-removed.
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
