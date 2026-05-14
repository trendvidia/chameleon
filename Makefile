# chameleon developer Makefile.
#
# `make help` lists every target. CI mirrors `test`, `vet`, and `lint`;
# `make ci` reproduces the GitHub Actions workflow locally.

SHELL          := /usr/bin/env bash
.SHELLFLAGS    := -eu -o pipefail -c
.DEFAULT_GOAL  := help

# --- Paths --------------------------------------------------------------------

BIN_DIR        := bin
CHAMELEON_BIN  := $(BIN_DIR)/chameleon
PROTOC_GEN_BIN := $(BIN_DIR)/protoc-gen-chameleon-go

# All Go module commands inherit GOFLAGS; override on the command line.
GO             ?= go
GOFLAGS        ?=

# --- Test knobs (override on the command line) --------------------------------

TEST_FLAGS     ?= -race -timeout 5m
FUZZ_TIME      ?= 30s
BENCH_PKG      ?= ./...
BENCH_COUNT    ?= 5
BENCHTIME      ?= 500ms
COVER_PROFILE  ?= coverage.out

# Pin to the CI version so local + CI lint behavior cannot diverge.
GOLANGCI_LINT_VERSION ?= v2.12.2

# --- Help (default) -----------------------------------------------------------

.PHONY: help
help: ## Show this help.
	@printf "Targets:\n"
	@grep -hE '^[a-zA-Z0-9_-]+:.*##' $(MAKEFILE_LIST) \
	  | sort \
	  | awk 'BEGIN{FS=":.*##[[:space:]]?"} {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# --- Build / install ----------------------------------------------------------

.PHONY: build
build: $(CHAMELEON_BIN) $(PROTOC_GEN_BIN) ## Build chameleon + protoc-gen-chameleon-go into ./bin/.

$(CHAMELEON_BIN): $(shell find cmd/chameleon -name '*.go' -not -name '*_test.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $@ ./cmd/chameleon

$(PROTOC_GEN_BIN): $(shell find cmd/protoc-gen-chameleon-go -name '*.go' -not -name '*_test.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $@ ./cmd/protoc-gen-chameleon-go

.PHONY: install
install: ## go install both binaries into $$GOBIN / $$GOPATH/bin.
	$(GO) install $(GOFLAGS) ./cmd/chameleon
	$(GO) install $(GOFLAGS) ./cmd/protoc-gen-chameleon-go

# --- Test ---------------------------------------------------------------------

.PHONY: test
test: ## Run unit tests with the race detector (mirrors CI).
	$(GO) test $(TEST_FLAGS) ./...

.PHONY: test-short
test-short: ## Quick smoke run: skip -race, fail fast.
	$(GO) test -short ./...

.PHONY: test-integration
test-integration: ## Run sops integration tests (real KMS round-trips).
	$(GO) test -tags=integration -timeout 5m ./sops/...

.PHONY: fuzz
fuzz: ## Run all fuzz targets for FUZZ_TIME (default 30s).
	$(GO) test -run='^$$' -fuzz=FuzzApply  -fuzztime=$(FUZZ_TIME) ./interpolate/
	$(GO) test -run='^$$' -fuzz=FuzzExpand -fuzztime=$(FUZZ_TIME) ./chain/
	$(GO) test -run='^$$' -fuzz=FuzzParse  -fuzztime=$(FUZZ_TIME) ./internal/header/

.PHONY: bench
bench: ## Run benchmarks (BENCH_PKG, BENCH_COUNT, BENCHTIME overridable).
	$(GO) test -bench=. -benchmem -count=$(BENCH_COUNT) -run='^$$' -benchtime=$(BENCHTIME) $(BENCH_PKG)

.PHONY: cover
cover: ## Write a coverage profile to $(COVER_PROFILE) and print summary.
	$(GO) test $(TEST_FLAGS) -coverprofile=$(COVER_PROFILE) -covermode=atomic ./...
	$(GO) tool cover -func=$(COVER_PROFILE) | tail -n 20

.PHONY: cover-html
cover-html: cover ## Open the coverage profile in a browser.
	$(GO) tool cover -html=$(COVER_PROFILE)

# --- Static analysis ----------------------------------------------------------

.PHONY: vet
vet: ## go vet ./...
	$(GO) vet ./...

.PHONY: lint
lint: ## golangci-lint run (pinned to $(GOLANGCI_LINT_VERSION)).
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not on PATH — run 'make tools' to install $(GOLANGCI_LINT_VERSION)."; \
		exit 1; \
	}
	golangci-lint run

.PHONY: self-lint
self-lint: $(CHAMELEON_BIN) ## Run `chameleon lint` against this repo (eats own dogfood).
	$(CHAMELEON_BIN) lint ./...

# --- Formatting ---------------------------------------------------------------

.PHONY: fmt
fmt: ## gofmt -w on all tracked Go files.
	gofmt -w -s $(shell find . -name '*.go' -not -path './examples/buf/gen/*' -not -path './vendor/*')

.PHONY: fmt-check
fmt-check: ## Fail if anything is not gofmt-clean.
	@unformatted=$$(gofmt -l $(shell find . -name '*.go' -not -path './examples/buf/gen/*' -not -path './vendor/*')); \
	if [ -n "$$unformatted" ]; then \
		echo "Unformatted files (run 'make fmt'):"; echo "$$unformatted"; exit 1; \
	fi

# --- Module hygiene -----------------------------------------------------------

.PHONY: tidy
tidy: ## go mod tidy.
	$(GO) mod tidy

.PHONY: verify
verify: ## Verify dependency checksums against the module cache.
	$(GO) mod verify

# --- Codegen ------------------------------------------------------------------

.PHONY: generate
generate: ## go generate ./... (regenerates proto bindings via buf).
	$(GO) generate ./...

# --- Tooling ------------------------------------------------------------------

.PHONY: tools
tools: ## Install dev tools: golangci-lint pinned to $(GOLANGCI_LINT_VERSION).
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# --- Meta ---------------------------------------------------------------------

.PHONY: ci
ci: vet test lint ## Reproduce the CI matrix locally (vet + test -race + lint).

.PHONY: check
check: fmt-check vet test ## Fast pre-commit suite (fmt, vet, test).

.PHONY: clean
clean: ## Remove built binaries and coverage artifacts.
	rm -rf $(BIN_DIR) $(COVER_PROFILE)
