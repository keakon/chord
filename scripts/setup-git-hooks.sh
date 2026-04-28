#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
git -C "$repo_root" config core.hooksPath .githooks

echo "Git hooks installed: core.hooksPath=.githooks"
echo "pre-commit will run gofmt on staged .go files"
