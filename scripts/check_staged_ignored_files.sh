#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/chord-staged-ignore.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT

staged_paths="$tmp_dir/staged-paths"
snapshot="$tmp_dir/snapshot"
template="$tmp_dir/empty-template"
mkdir -p "$snapshot" "$template"

git_outside_repo() (
  unset $(git rev-parse --local-env-vars)
  git "$@"
)

git -C "$repo_root" diff --cached --name-only --diff-filter=ACMR -z > "$staged_paths"
if [[ ! -s "$staged_paths" ]]; then
  exit 0
fi

{
  git -C "$repo_root" ls-files --cached -z -- '.gitignore' ':(glob)**/.gitignore'
  cat "$staged_paths"
} | git -C "$repo_root" checkout-index --force --stdin -z --prefix="$snapshot/"

git_outside_repo -c init.templateDir="$template" init -q "$snapshot"
ignored_paths="$tmp_dir/ignored-paths"
set +e
git_outside_repo -C "$snapshot" -c core.excludesFile=/dev/null check-ignore --no-index --stdin -z < "$staged_paths" > "$ignored_paths"
status=$?
set -e

case "$status" in
  0)
    while IFS= read -r -d '' file; do
      printf 'pre-commit: refusing to commit ignored file: %q\n' "$file" >&2
    done < "$ignored_paths"
    exit 1
    ;;
  1)
    exit 0
    ;;
  *)
    echo "pre-commit: failed to verify staged files against staged ignore rules" >&2
    exit 1
    ;;
esac
