#!/usr/bin/env bash
set -euo pipefail

unset $(git rev-parse --local-env-vars 2>/dev/null || true)

repo_root=$(cd "$(dirname "$0")/.." && pwd)
check="$repo_root/scripts/check_model_catalog_update.sh"

test_root=$(mktemp -d "${TMPDIR:-/tmp}/chord-model-catalog-check.XXXXXX")
trap 'rm -rf "$test_root"' EXIT

export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_NOSYSTEM=1

chord="$test_root/chord"
git init -q "$chord"
snapshot="$chord/internal/modelcatalog/data/snapshot.yaml"
mkdir -p "$(dirname "$snapshot")"
printf 'revision: v2026-10-17.2\n' >"$snapshot"

new_models() {
  local dir="$1"
  git init -q "$dir"
  git -C "$dir" config user.name test
  git -C "$dir" config user.email test@example.invalid
  for file in catalog.yaml endpoints.yaml models.yaml bindings.yaml; do
    : >"$dir/$file"
  done
  git -C "$dir" add .
  git -C "$dir" commit -qm base
}

run_check() {
  (cd "$chord" && CHORD_MODELS_DIR="$1" bash "$check" 2>&1)
}

models="$test_root/chord-models"
new_models "$models"
git -C "$models" tag v2026-10-17.2
git -C "$models" commit -q --allow-empty -m next
git -C "$models" tag v2026-10-27.1

output="$(run_check "$models")"
[[ "$output" == *"newer catalog tag: v2026-10-27.1"* ]] || {
  printf 'newer tag was not reported:\n%s\n' "$output" >&2
  exit 1
}
[[ "$output" == *"sync_model_catalog.sh v2026-10-27.1"* ]] || {
  printf 'newer tag did not print the sync command:\n%s\n' "$output" >&2
  exit 1
}

linked="$test_root/chord-models-worktree"
git -C "$models" worktree add -q --detach "$linked"
output="$(run_check "$linked")"
[[ "$output" == *"newer catalog tag: v2026-10-27.1"* ]] || {
  printf 'linked worktree was ignored:\n%s\n' "$output" >&2
  exit 1
}

output="$(cd "$chord" && CHORD_MODELS_DIR="$models" CHORD_SKIP_MODEL_CATALOG_CHECK=1 bash "$check" 2>&1)"
[[ -z "$output" ]] || {
  printf 'CHORD_SKIP_MODEL_CATALOG_CHECK did not silence the check:\n%s\n' "$output" >&2
  exit 1
}

printf 'revision: v2026-10-27.1\n' >"$snapshot"
output="$(run_check "$models")"
[[ -z "$output" ]] || {
  printf 'matching snapshot was reported as outdated:\n%s\n' "$output" >&2
  exit 1
}

older="$test_root/older-models"
new_models "$older"
git -C "$older" tag v2026-10-17.2
output="$(run_check "$older")"
[[ -z "$output" ]] || {
  printf 'older local tag was reported as an update:\n%s\n' "$output" >&2
  exit 1
}

shallow="$test_root/shallow-models"
new_models "$shallow"
git -C "$shallow" tag v2026-10-27.1
printf 'revision: v2026-10-17.2\n' >"$snapshot"
output="$(run_check "$shallow")"
[[ "$output" == *"newer catalog tag: v2026-10-27.1"* ]] || {
  printf 'newer tag without the embedded tag was not reported:\n%s\n' "$output" >&2
  exit 1
}

output="$(run_check "$test_root/missing")"
[[ -z "$output" ]] || {
  printf 'missing checkout was not ignored:\n%s\n' "$output" >&2
  exit 1
}

unrelated="$test_root/unrelated"
git init -q "$unrelated"
output="$(run_check "$unrelated")"
[[ -z "$output" ]] || {
  printf 'unrelated checkout was not ignored:\n%s\n' "$output" >&2
  exit 1
}

echo "model catalog check tests passed"
