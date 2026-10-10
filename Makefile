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

.PHONY: ci fmt fmt-check deps-check test test-cover race vet staticcheck gopls-check modernize-check deadcode-check docs-check docs-examples-check bench-tui clean

ci: fmt-check deps-check test-cover race vet staticcheck gopls-check modernize-check deadcode-check docs-check docs-examples-check

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

test-cover:
	CHORD_GO='$(GO)' CHORD_TEST_COUNT=1 ./scripts/check_ci_local.sh $(PKGS)

race:
	./scripts/check_ci_race.sh $(PKGS)

vet:
	$(GO) vet $(PKGS)

staticcheck:
	$(STATICCHECK) -checks 'all,-ST1000' $(PKGS)

gopls-check:
	git ls-files -z '*.go' | xargs -0 $(GOPLS) check

modernize-check:
	$(MODERNIZE) -test $(PKGS)

deadcode-check:
	./scripts/check_deadcode.sh

docs-check:
	./scripts/check_docs_consistency.sh

docs-examples-check:
	./scripts/check_docs_examples.sh

bench-tui:
	./scripts/bench_tui_regression.sh

clean:
	rm -f chord chord.exe coverage.* *.out *.test
