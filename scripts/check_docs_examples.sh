#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

go test ./internal/config -run '^TestDocs(ExampleConfigsLoad|TeamExampleRoles)$' -count=1
