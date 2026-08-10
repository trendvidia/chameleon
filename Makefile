# chameleon developer Makefile.
#
# `make help` lists every target. CI mirrors `test`, `vet`, `lint` and
# `fmt-check`; `make ci` reproduces the GitHub Actions workflow locally.
#
# Three modules live in this repo (see docs/RELEASING.md): the library
# at the root, the sops backend, and the CLI. Per-module targets loop
# over $(MODULES) because `./...` stops at a module boundary — a target
# that forgets the loop silently tests a third of the repo. `cover`,
# `bench` and `fuzz` deliberately do not loop: they are the library
# module's tools (the other two have no benchmarks or fuzz targets),
# and each prints what it did and did not cover.

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

# Every module in the repo, as directories relative to the root. The
# go.work at the root wires them together for local development.
MODULES        := . sops cmd/chameleon

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

.PHONY: compile
compile: ## Type-check + compile every package in every module (no binaries).
	@for m in $(MODULES); do echo "==> compile $$m"; (cd $$m && $(GO) build ./...); done

.PHONY: build
build: $(CHAMELEON_BIN) $(PROTOC_GEN_BIN) ## Build chameleon + protoc-gen-chameleon-go into ./bin/.

$(CHAMELEON_BIN): $(shell find cmd/chameleon -name '*.go' -not -name '*_test.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	cd cmd/chameleon && $(GO) build $(GOFLAGS) -o $(CURDIR)/$@ .

$(PROTOC_GEN_BIN): $(shell find cmd/protoc-gen-chameleon-go -name '*.go' -not -name '*_test.go' 2>/dev/null)
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -o $@ ./cmd/protoc-gen-chameleon-go

.PHONY: install
install: ## go install both binaries into $$GOBIN / $$GOPATH/bin.
	cd cmd/chameleon && $(GO) install $(GOFLAGS) .
	$(GO) install $(GOFLAGS) ./cmd/protoc-gen-chameleon-go

# --- Test ---------------------------------------------------------------------

.PHONY: test
test: ## Run unit tests with the race detector, every module (mirrors CI).
	@for m in $(MODULES); do echo "==> test $$m"; (cd $$m && $(GO) test $(TEST_FLAGS) ./...); done

.PHONY: test-short
test-short: ## Quick smoke run: skip -race, fail fast.
	@for m in $(MODULES); do echo "==> test-short $$m"; (cd $$m && $(GO) test -short ./...); done

.PHONY: test-integration
test-integration: ## Run sops integration tests (real KMS round-trips).
	cd sops && $(GO) test -tags=integration -timeout 5m ./...
	cd cmd/chameleon && $(GO) test -tags=integration -timeout 10m ./...

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
	@echo "note: library module only. For another module, run the tools in it directly —"
	@echo "      cd sops && go test $(TEST_FLAGS) -coverprofile=cover.out -covermode=atomic ./... && go tool cover -func=cover.out"

.PHONY: cover-html
cover-html: cover ## Open the coverage profile in a browser.
	$(GO) tool cover -html=$(COVER_PROFILE)

# --- Static analysis ----------------------------------------------------------

.PHONY: vet
vet: ## go vet ./... in every module.
	@for m in $(MODULES); do echo "==> vet $$m"; (cd $$m && $(GO) vet ./...); done

# Shared by lint, fmt and fmt-check so the install hint has one home. It
# checks presence only: the version the three targets actually get is
# whatever `make tools` last installed, which is why GOLANGCI_LINT_VERSION
# is pinned and CI installs the same string.
.PHONY: require-golangci-lint
require-golangci-lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not on PATH — run 'make tools' to install $(GOLANGCI_LINT_VERSION)."; \
		exit 1; \
	}

.PHONY: lint
lint: require-golangci-lint ## golangci-lint run (pinned to $(GOLANGCI_LINT_VERSION)).
	@for m in $(MODULES); do echo "==> lint $$m"; (cd $$m && golangci-lint run); done

.PHONY: self-lint
self-lint: $(CHAMELEON_BIN) ## Run `chameleon lint` against this repo (eats own dogfood).
	$(CHAMELEON_BIN) lint ./...

# --- Formatting ---------------------------------------------------------------
#
# gofumpt, applied through the pinned golangci-lint rather than a
# standalone binary. One tool, one version, one answer: a separately
# installed gofumpt drifts ahead of the one golangci-lint bundles (v0.10.0
# splits `f(a, b,` onto its own line where the bundled build does not), and
# a formatter whose check and fix disagree is worse than no formatter.
# `make tools` installs the pinned version; lint, fmt and fmt-check all
# share the require-golangci-lint guard.
#
# Scope follows golangci-lint's own file discovery. Generated code is
# excluded by .golangci.yml (reformatting it would be reverted by the next
# `make generate`), and cmd/chameleon/testdata is skipped because
# golangci-lint ignores testdata directories the way the go tool does.
# That skip is what keeps `make fmt` off the analyzer fixtures — note it is
# the only thing that does, since nothing in .golangci.yml names them.
# lint_test.go itself would survive a reformat: it pins expectations with
# same-line `// want` markers, not absolute line numbers.
#
# The MODULES loop here is belt-and-braces rather than load-bearing:
# unlike `go build ./...`, `golangci-lint fmt` walks the filesystem and
# already descends into sops/ and cmd/chameleon/ from the root. Three
# short runs are cheap insurance against the gate silently narrowing to
# one module if that ever changes.

.PHONY: fmt
fmt: require-golangci-lint ## Format every module with gofumpt (via the pinned golangci-lint).
	@for m in $(MODULES); do echo "==> fmt $$m"; (cd $$m && golangci-lint fmt); done

# Checks every module before failing. Stopping at the first one sends the
# contributor round the fix-and-rerun loop once per module for a drift
# that a single run could have listed in full.
.PHONY: fmt-check
fmt-check: require-golangci-lint ## Fail if anything is not gofumpt-clean.
	@failed=""; \
	for m in $(MODULES); do \
		echo "==> fmt-check $$m"; \
		(cd $$m && golangci-lint fmt --diff) || failed="$$failed $$m"; \
	done; \
	if [ -n "$$failed" ]; then \
		echo "Not gofumpt-clean (run 'make fmt'):$$failed"; exit 1; \
	fi

# --- Module hygiene -----------------------------------------------------------

.PHONY: modules-check
modules-check: ## Fail if MODULES and go.work disagree.
	@work=$$($(GO) work edit -json | grep -o '"DiskPath": *"[^"]*"' | sed 's/.*: *"//; s/"$$//; s|^\./||' | sort | tr '\n' ' '); \
	mk=$$(printf '%s\n' $(MODULES) | sed 's|^\./||' | sort | tr '\n' ' '); \
	if [ "$$work" != "$$mk" ]; then \
		echo "MODULES and go.work disagree — every per-module target would silently skip one."; \
		echo "  Makefile MODULES : $$mk"; \
		echo "  go.work use      : $$work"; \
		exit 1; \
	fi

.PHONY: tidy
tidy: ## go mod tidy in every module.
	@for m in $(MODULES); do echo "==> tidy $$m"; (cd $$m && $(GO) mod tidy); done

.PHONY: verify
verify: ## Verify dependency checksums against the module cache.
	@for m in $(MODULES); do echo "==> verify $$m"; (cd $$m && $(GO) mod verify); done

# --- Changelog ----------------------------------------------------------------
#
# Entries live one-per-file in changelog.d/ until release; see its README.
# VERSION is required by the two assembling targets and is the release
# being cut, without a leading "v" — the same string the tagging steps of
# docs/RELEASING.md put a "v" in front of. The tool rejects a leading "v"
# rather than writing one into a heading that is about to be tagged.

.PHONY: changelog
changelog: ## Fold changelog.d/ fragments into CHANGELOG.md (VERSION=x.y.z).
	@[ -n "$(VERSION)" ] || { echo "VERSION is required, e.g. make changelog VERSION=0.23.0"; exit 1; }
	$(GO) run ./hack/changelog -version $(VERSION)

.PHONY: changelog-preview
changelog-preview: ## Print the section VERSION would produce; change nothing.
	@[ -n "$(VERSION)" ] || { echo "VERSION is required, e.g. make changelog-preview VERSION=0.23.0"; exit 1; }
	@$(GO) run ./hack/changelog -version $(VERSION) -preview

.PHONY: changelog-check
changelog-check: ## Fail if any changelog.d/ fragment is misnamed or empty.
	@$(GO) run ./hack/changelog -check

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
ci: modules-check changelog-check vet test lint ## Reproduce the CI matrix locally (modules-check + changelog-check + vet + test -race + lint).

.PHONY: check
check: fmt-check vet test ## Fast pre-commit suite (fmt-check, vet, test).

.PHONY: clean
clean: ## Remove built binaries and coverage artifacts.
	rm -rf $(BIN_DIR) $(COVER_PROFILE)
