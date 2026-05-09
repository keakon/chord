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

.PHONY: ci fmt fmt-check test test-cover race vet staticcheck gopls-check docs-check bench-tui clean

ci: fmt-check test-cover race vet staticcheck gopls-check docs-check

fmt:
	$(GOIMPORTS) -w -local $(LOCAL) .

fmt-check:
	@out="$$( $(GOIMPORTS) -l -local $(LOCAL) . )"; \
	if [[ -n "$$out" ]]; then \
		echo "goimports formatting needed:"; \
		echo "$$out"; \
		echo "Run: make fmt"; \
		exit 1; \
	fi

test:
	$(GO) test -count=1 $(PKGS)

test-cover:
	$(GO) test -count=1 -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out

race:
	$(GO) test -race -count=1 -timeout=10m $(PKGS)

vet:
	$(GO) vet $(PKGS)

staticcheck:
	$(STATICCHECK) -checks 'all,-ST1000' $(PKGS)

gopls-check:
	git ls-files -z '*.go' | xargs -0 $(GOPLS) check

docs-check:
	./scripts/check_docs_consistency.sh

bench-tui:
	./scripts/bench_tui_regression.sh

clean:
	rm -f coverage.out *.out *.test
