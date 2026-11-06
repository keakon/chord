# Common developer shortcuts.
#
# This Makefile is intentionally minimal: it mirrors the commands used in CI and
# CONTRIBUTING.md without adding extra tooling assumptions.

SHELL := /bin/bash

GO ?= go
PKGS ?= ./...
LOCAL ?= github.com/keakon/chord
RUN_GO = CHORD_GO='$(GO)' bash scripts/run_tests.sh --default-temp

GOIMPORTS ?= goimports
STATICCHECK ?= staticcheck
GOPLS_VERSION ?= v0.24.0
# gopls runs on the host even when GOOS/GOARCH select another analysis target,
# and the versioned directory keeps an older pin from being reused.
GOPLS_DIR := $(shell $(GO) env GOCACHE)/chord-gopls/$(GOPLS_VERSION)-$(shell $(GO) env GOHOSTOS)-$(shell $(GO) env GOHOSTARCH)
GOPLS ?= $(GOPLS_DIR)/gopls
MODERNIZE_VERSION ?= v0.50.0
MODERNIZE ?= $(GO) run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@$(MODERNIZE_VERSION)

.PHONY: ci preflight build fmt fmt-check deps-check test test-fast test-cover stress race race-fast vet staticcheck gopls-install gopls-check modernize-check deadcode-check docs-check docs-examples-check resource-scripts-check model-catalog-check bench-tui clean

ci: fmt-check deps-check test-cover stress race vet staticcheck gopls-check modernize-check deadcode-check docs-check docs-examples-check resource-scripts-check

# Same Go gates as .githooks/pre-push; docs checks stay in the hook because
# they depend on the changed paths. deadcode-check covers the host target only;
# the other baselines stay in the CI deadcode job.
preflight: fmt-check deps-check vet staticcheck gopls-check modernize-check deadcode-check test-fast resource-scripts-check

# Release-style build: pure Go (no cgo), path-trimmed, and stripped.
build:
	CGO_ENABLED=0 $(RUN_GO) $(GO) build -trimpath -ldflags "-s -w" -o chord ./cmd/chord/

# fmt / fmt-check cover every Go file Git tracks or would add. Ignored private
# trees (for example .chord/) must never block the gate or get rewritten by it.
fmt:
	./scripts/goimports_files.sh $(GOIMPORTS) -w -local $(LOCAL)

fmt-check:
	@out="$$( ./scripts/goimports_files.sh $(GOIMPORTS) -l -local $(LOCAL) )" || exit $$?; \
	if [[ -n "$$out" ]]; then \
		echo "goimports formatting needed:"; \
		echo "$$out"; \
		echo "Run: make fmt"; \
		exit 1; \
	fi

deps-check:
	./scripts/check_deps.sh

test:
	@CHORD_GO='$(GO)' bash scripts/run_tests.sh --default-temp $(GO) test -count=1 $(PKGS)

# Opt-in speed-up: run the tests with a memory-backed temp dir when one is
# available, otherwise fall back to the default temp dir. See
# scripts/test_tmpdir.sh and CONTRIBUTING.md. Each Unix run owns a temporary
# directory; shared mounts are never swept or ejected.
test-fast:
	@CHORD_GO='$(GO)' bash scripts/run_tests.sh $(GO) test -count=1 $(PKGS)

test-cover:
	CHORD_GO='$(GO)' CHORD_TEST_COUNT=1 ./scripts/check_ci_local.sh $(PKGS)

stress:
	CHORD_GO='$(GO)' ./scripts/check_ci_stress.sh

race:
	CHORD_GO='$(GO)' ./scripts/check_ci_race.sh $(PKGS)

# Local race gate: same script and checks as CI, with a memory-backed temp dir
# when one is available. Use `make race` to reproduce the default-temp path.
race-fast:
	@CHORD_GO='$(GO)' bash scripts/run_tests.sh ./scripts/check_ci_race.sh $(PKGS)

vet:
	$(RUN_GO) $(GO) vet $(PKGS)

staticcheck:
	$(RUN_GO) $(STATICCHECK) -checks 'all,-ST1000' $(PKGS)

# Keep cache paths in shell quotes so Make does not split paths with spaces.
gopls-install:
	@if [[ ! -x '$(GOPLS_DIR)/gopls' ]]; then \
		mkdir -p '$(GOPLS_DIR)' && \
		GOOS=$(shell $(GO) env GOHOSTOS) GOARCH=$(shell $(GO) env GOHOSTARCH) GOBIN='$(GOPLS_DIR)' $(RUN_GO) $(GO) install golang.org/x/tools/gopls@$(GOPLS_VERSION); \
	fi

ifeq ($(GOPLS),$(GOPLS_DIR)/gopls)
GOPLS_DEP := gopls-install
endif

gopls-check: $(GOPLS_DEP)
	GOPLS='$(GOPLS)' $(RUN_GO) ./scripts/check_gopls.sh $(PKGS)

modernize-check:
	$(RUN_GO) $(MODERNIZE) -test $(PKGS)

deadcode-check:
	$(RUN_GO) ./scripts/check_deadcode.sh

docs-check:
	./scripts/check_docs_consistency.sh

docs-examples-check:
	./scripts/check_docs_examples.sh

resource-scripts-check:
	bash -n scripts/test_tmpdir.sh scripts/test_tmpdir_ownership.sh scripts/run_tests.sh
	bash -n scripts/check_model_catalog_update.sh
	bash -n scripts/test_model_catalog_check.sh
	bash scripts/test_tmpdir_ownership.sh
	bash scripts/test_model_catalog_check.sh

model-catalog-check:
	./scripts/check_model_catalog_update.sh

bench-tui:
	./scripts/bench_tui_regression.sh

clean:
	rm -f chord chord.exe coverage.* *.out *.test
