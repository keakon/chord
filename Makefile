# Common developer shortcuts.
#
# This Makefile is intentionally minimal: it mirrors the commands used in CI and
# CONTRIBUTING.md without adding extra tooling assumptions.

SHELL := /bin/bash

GO ?= go
PKGS ?= ./...
LOCAL ?= github.com/keakon/chord

GOIMPORTS ?= goimports
STATICCHECK ?= staticcheck
GOPLS ?= gopls
MODERNIZE_VERSION ?= v0.50.0
MODERNIZE ?= $(GO) run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@$(MODERNIZE_VERSION)

.PHONY: ci preflight fmt fmt-check deps-check test test-fast test-cover stress race vet staticcheck gopls-check modernize-check deadcode-check docs-check docs-examples-check resource-scripts-check model-catalog-check bench-tui clean

ci: fmt-check deps-check test-cover stress race vet staticcheck gopls-check modernize-check deadcode-check docs-check docs-examples-check resource-scripts-check

# Same Go gates as .githooks/pre-push; docs checks stay in the hook because
# they depend on the changed paths.
preflight: fmt-check deps-check vet staticcheck modernize-check test-fast resource-scripts-check

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
	$(GO) test -count=1 $(PKGS)

# Opt-in speed-up: run the tests with a memory-backed temp dir when one is
# available, otherwise fall back to the default temp dir. See
# scripts/test_tmpdir.sh and CONTRIBUTING.md.
test-fast:
	@tmp="$$( ./scripts/test_tmpdir.sh )"; \
	 if [[ -n "$$tmp" ]]; then echo "test-fast: TMPDIR=$$tmp"; \
	 else echo "test-fast: no memory-backed temp dir; using the default temp dir"; fi; \
	 TMPDIR="$$tmp" TMP="$$tmp" TEMP="$$tmp" $(GO) test -count=1 $(PKGS)

test-cover:
	CHORD_GO='$(GO)' CHORD_TEST_COUNT=1 ./scripts/check_ci_local.sh $(PKGS)

stress:
	CHORD_GO='$(GO)' ./scripts/check_ci_stress.sh

race:
	./scripts/check_ci_race.sh $(PKGS)

vet:
	$(GO) vet $(PKGS)

staticcheck:
	$(STATICCHECK) -checks 'all,-ST1000' $(PKGS)

gopls-check:
	CHORD_GO='$(GO)' GOPLS='$(GOPLS)' ./scripts/check_gopls.sh $(PKGS)

modernize-check:
	$(MODERNIZE) -test $(PKGS)

deadcode-check:
	./scripts/check_deadcode.sh

docs-check:
	./scripts/check_docs_consistency.sh

docs-examples-check:
	./scripts/check_docs_examples.sh

resource-scripts-check:
	bash -n scripts/test_tmpdir.sh scripts/test_tmpdir_ownership.sh
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
