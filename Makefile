# chameleon developer Makefile.
#
# `make help` lists every target. `make ci` runs every gate
# .github/workflows/ci.yml applies through make, bar one stated omission
# (`test-integration`, which needs live KMS credentials); `ci-check` is
# the guard that keeps that sentence true as the workflow changes.
#
# Requires GNU Make >= 3.82 — enforced below, see the note on
# .SHELLFLAGS. macOS ships 3.81; `brew install make` and run `gmake`.
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

# .SHELLFLAGS arrived in GNU Make 3.82. Under 3.81 — which is what macOS
# still ships as /usr/bin/make — the line above is silently ignored,
# recipes run without -e, and every `for m in $(MODULES)` loop below
# returns only its LAST module's status. A failure in the library or in
# sops is discarded and the target goes green (chameleon#171). That is
# not a formatting nicety: it is a green `make test` over a broken
# module, on the machine where most of this repo is written.
#
# So the requirement is enforced rather than documented. On macOS:
# `brew install make`, then use `gmake` (Homebrew does not displace
# /usr/bin/make).
#
# $(sort) is lexical, which is exactly right across 3.x and 4.x — "3.82"
# sorts before "4.4" — and wrong only if GNU Make ever reaches 10.x.
MAKE_VERSION_MIN := 3.82
ifneq ($(firstword $(sort $(MAKE_VERSION) $(MAKE_VERSION_MIN))),$(MAKE_VERSION_MIN))
$(error This Makefile needs GNU Make >= $(MAKE_VERSION_MIN); you have $(MAKE_VERSION). \
Older make ignores .SHELLFLAGS, so a failure in any but the last module of a \
per-module loop is silently discarded (chameleon#171). On macOS: brew install make, then use gmake)
endif

# `tidy-check` rewrites go.mod/go.sum/go.work.sum, and it is a prerequisite
# of `ci` alongside compile, vet, test and lint — which all read those same
# files. Under `make -j` those run concurrently and the readers see a module
# graph mid-rewrite: "missing go.sum entry" failures that look like real
# regressions and do not reproduce. Nothing here parallelises usefully
# anyway; every per-module target is already one recipe looping over
# $(MODULES), so the whole cost of this line is that -j stops lying.
.NOTPARALLEL:

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

# The files `tidy-check` watches: the pair every module owns, derived from
# MODULES rather than spelled out again (modules-check is what keeps that
# list honest), plus the two workspace files, which belong to the root and
# to no module. Deriving matters here: a hand-written `'**/go.mod'` glob
# both misses the root pair — git's pathspec does not match it at depth
# zero — and sweeps in any go.mod under a testdata fixture that `make
# tidy` never touches.
MODULE_FILES   := go.work go.work.sum \
                  $(patsubst ./%,%,$(foreach m,$(MODULES),$(m)/go.mod $(m)/go.sum))

# --- Test knobs (override on the command line) --------------------------------

TEST_FLAGS     ?= -race -timeout 5m
FUZZ_TIME      ?= 30s
BENCH_PKG      ?= ./...
BENCH_COUNT    ?= 5
BENCHTIME      ?= 500ms
COVER_PROFILE  ?= coverage.out

# Pin to the CI version so local + CI lint behavior cannot diverge.
GOLANGCI_LINT_VERSION ?= v2.13.2

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

# How $(MODULE_FILES) stand relative to HEAD, as one string tidy-check can
# compare before against after: the porcelain lines name files (including
# untracked ones), the diff carries the content, so a rewrite that leaves
# the same set of paths dirty still shows up as a different string.
MODULE_STATE    = git status --porcelain -- $(MODULE_FILES) \
                  && git --no-pager diff HEAD -- $(MODULE_FILES)

# The gate CI applies, as a target rather than a block of workflow YAML —
# the same argument ci.yml already makes for the module walk: one
# definition, or the two drift while `make ci` claims to be what CI runs.
# Note it REWRITES $(MODULE_FILES) in place, because `go mod tidy` is the
# only way to know whether they were already correct; a failure therefore
# leaves the fix sitting in your working tree, unstaged — `git add` it
# yourself.
#
# go.work.sum is in the file set on purpose: it is tracked, and the
# build/vet/test targets run in workspace mode, which rewrites it
# whenever it needs a hash the committed file lacks. Left unchecked, a
# PR lands with it stale and every contributor gets a dirty tree on
# first build.
#
# Three outcomes, told apart on purpose. `git diff` alone cannot tell the
# first two apart — it compares the worktree to HEAD, which says nothing
# about what tidy did or did not do:
#
#   tidy rewrote a file          -> untidy; commit what tidy just fixed.
#   tidy rewrote nothing, but    -> your own in-progress edit, or a
#   a file still differs            go.work.sum an earlier target rewrote.
#   git itself failed            -> nothing was judged; say so.
#
# Reporting the second as "not tidy — the diff above is what 'make tidy'
# just fixed" gets the instruction right by accident and the diagnosis
# wrong: the developer is told their module graph was broken when it was
# not, and sent to read a diff of their own text for a fix that is not in
# it. Same reasoning ci.yml's changelog gate gives for not swallowing a
# broken ref into "no fragment" — one message for several states sends
# some of its readers the wrong way.
#
# `git status --porcelain` and not the diff alone: a go.sum that tidy
# CREATES is untracked, and `git diff` cannot see it.
.PHONY: tidy-check
tidy-check: ## Fail if `make tidy` rewrites any module file, or any differs from HEAD.
	@before=$$($(MODULE_STATE)) || { \
		echo "tidy-check: git could not read the state of $(MODULE_FILES) — check aborted, module graph unjudged."; \
		exit 2; \
	}; \
	$(MAKE) --no-print-directory tidy || exit $$?; \
	after=$$($(MODULE_STATE)) || { \
		echo "tidy-check: git could not read the module files after 'make tidy' — check aborted."; \
		exit 2; \
	}; \
	[ -n "$$after" ] || exit 0; \
	printf '%s\n' "$$after"; \
	if [ "$$before" = "$$after" ]; then \
		echo "'make tidy' changed nothing — the module files above already differed from"; \
		echo "HEAD: an edit of your own, or a go.work.sum an earlier target rewrote."; \
		echo "CI gates the committed files, so commit (or drop) it before pushing."; \
	else \
		echo "Module files are not tidy — the state above is what 'make tidy' just fixed"; \
		echo "in your working tree; 'git add' and commit it."; \
	fi; \
	exit 1

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
changelog-check: ## Fail if any changelog.d/ fragment is misnamed or empty, or the link block is stale.
	@$(GO) run ./hack/changelog -check

changelog-relink: ## Rebuild CHANGELOG.md's link-reference block from its release headings.
	$(GO) run ./hack/changelog -relink

# --- Codegen ------------------------------------------------------------------

.PHONY: generate
generate: ## go generate ./... (regenerates proto bindings via buf).
	$(GO) generate ./...

# --- Tooling ------------------------------------------------------------------

.PHONY: tools
tools: ## Install dev tools: golangci-lint pinned to $(GOLANGCI_LINT_VERSION).
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# --- Meta ---------------------------------------------------------------------

# Every make target .github/workflows/ci.yml runs, bar the one in
# CI_EXEMPT — `ci-check` fails if that stops being true. The order is
# this file's own (cheap and hermetic first, so the slow legs are not
# spent to reach a one-line gofumpt complaint), not the workflow's: make
# aborts at the first failing prerequisite whatever the order, so
# mirroring the step sequence would buy nothing and is not claimed.
#
# What `make ci` still cannot tell you, because none of it runs through
# make: test-integration (CI_EXEMPT below), the workflow's "did this PR
# add a changelog fragment?" step — `changelog-check` validates the
# fragments that exist, it does not require one. Those two, plus CI's
# runners being Linux and macOS while yours is one of them, are the
# whole gap between a green `make ci` and a green PR.
#
# fmt-check was in this list from #149 and was lost in #150's merge of
# main (202ae0e) — both sides had edited the one line, and the
# resolution kept one. That is what #165 is about, and it is why the
# list is a variable with a guard on it rather than a line with a
# comment asking to be kept in step: the comment is what failed.
CI_WORKFLOW := .github/workflows/ci.yml
CI_TARGETS  := modules-check ci-check changelog-check fmt-check compile vet test tidy-check lint

# Targets $(CI_WORKFLOW) runs that `make ci` deliberately does not. Each
# needs a reason here, because this is the list that lets ci-check pass.
#
#   test-integration — needs live KMS credentials. Rolling it in would
#   make `make ci` fail on a clean checkout, which trains people to
#   ignore it: worse than not running it.
CI_EXEMPT   := test-integration

# The guard on the list above, in the same spirit as modules-check, which
# ci.yml calls "the guard on the definition itself". Checked one way only:
# every make target the workflow runs must be a gate `make ci` runs. The
# converse is not an error — `lint` reaches CI through
# golangci-lint-action rather than through make.
.PHONY: ci-check
ci-check: ## Fail if ci.yml runs a make target that `make ci` does not.
	@steps=$$(grep -oE '^[[:space:]]*(run:[[:space:]]+)?make[[:space:]]+[a-z][a-z0-9-]*[[:space:]]*$$' $(CI_WORKFLOW) | awk '{print $$NF}' | sort -u); \
	if [ -z "$$steps" ]; then \
		echo "ci-check: no 'make <target>' steps found in $(CI_WORKFLOW) — the pattern here is broken, not the workflow."; \
		exit 1; \
	fi; \
	missing=""; \
	for t in $$steps; do \
		case " $(CI_TARGETS) $(CI_EXEMPT) " in \
			*" $$t "*) ;; \
			*) missing="$$missing $$t" ;; \
		esac; \
	done; \
	if [ -n "$$missing" ]; then \
		echo "$(CI_WORKFLOW) runs make targets that 'make ci' does not:$$missing"; \
		echo "  make ci : $(CI_TARGETS)"; \
		echo "  exempt  : $(CI_EXEMPT)"; \
		echo "Add each to CI_TARGETS, or to CI_EXEMPT with the reason. This drift is #165."; \
		exit 1; \
	fi

.PHONY: ci
ci: $(CI_TARGETS) ## Everything CI runs through make except test-integration (needs live KMS credentials).

.PHONY: check
check: fmt-check vet test ## Fast pre-commit suite (fmt-check, vet, test).

.PHONY: clean
clean: ## Remove built binaries and coverage artifacts.
	rm -rf $(BIN_DIR) $(COVER_PROFILE)
