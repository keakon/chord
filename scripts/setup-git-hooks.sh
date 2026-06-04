#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
git -C "$repo_root" config core.hooksPath .githooks

echo "Git hooks installed: core.hooksPath=.githooks"
echo "pre-commit will run goimports + gofmt on staged .go files"
echo "pre-push will run fmt-check, vet, staticcheck, and test"
echo "(install goimports with: go install golang.org/x/tools/cmd/goimports@latest)"
