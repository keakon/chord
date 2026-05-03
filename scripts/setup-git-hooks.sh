#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
git -C "$repo_root" config core.hooksPath .githooks

echo "Git hooks installed: core.hooksPath=.githooks"
echo "pre-commit will run goimports + gofmt on staged .go files"
echo "(install goimports with: go install golang.org/x/tools/cmd/goimports@latest)"
