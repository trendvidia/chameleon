module github.com/trendvidia/chameleon

go 1.26.1

require (
	github.com/awnumar/memguard v0.22.5
	github.com/bufbuild/protocompile v0.14.1
	github.com/stretchr/testify v1.11.1
	github.com/trendvidia/protowire-go v1.5.0
	go.opentelemetry.io/otel v1.43.0
	go.opentelemetry.io/otel/metric v1.43.0
	go.opentelemetry.io/otel/sdk/metric v1.43.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/awnumar/memcall v0.2.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/sdk v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// protowire-go v0.72.0 introduced the generic @<directive> grammar
// chameleon's @header preamble depends on. See
// internal/upstream/protowire_go_patches.go for the cross-repo contract.

// No key management appears above, and none may: every consumer's
// `go list -m all`, dependency review and SBOM inherits this module's
// requirements (trendvidia/chameleon#130). The sops fork moved with the
// backend — see sops/go.mod. TestLibraryModuleRequiresNoKeyManagement
// in linkage_test.go reads `go mod edit -json` and fails if it returns.
