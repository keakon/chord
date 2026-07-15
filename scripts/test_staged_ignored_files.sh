#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
checker="$repo_root/scripts/check_staged_ignored_files.sh"
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_NOSYSTEM=1

new_repo() {
  local repo
  repo=$(mktemp -d "${TMPDIR:-/tmp}/chord-staged-ignore-test.XXXXXX")
  git init -q "$repo"
  git -C "$repo" config user.name test
  git -C "$repo" config user.email test@example.com
  printf '%s\n' "$repo"
}

expect_accept() {
  local repo=$1
  if ! output=$(cd "$repo" && "$checker" 2>&1); then
    printf 'expected staged ignore check to pass, got:\n%s\n' "$output" >&2
    exit 1
  fi
}

expect_reject() {
  local repo=$1
  if output=$(cd "$repo" && "$checker" 2>&1); then
    echo "expected staged ignore check to reject ignored path" >&2
    exit 1
  fi
  [[ "$output" == *"refusing to commit ignored file"* ]] || {
    printf 'unexpected rejection output:\n%s\n' "$output" >&2
    exit 1
  }
}

expect_accept_from_hook_env() {
  local repo=$1
  local git_dir
  git_dir=$(git -C "$repo" rev-parse --absolute-git-dir)
  if ! output=$(cd "$repo" && GIT_DIR="$git_dir" GIT_WORK_TREE="$repo" "$checker" 2>&1); then
    printf 'expected staged ignore check in hook environment to pass, got:\n%s\n' "$output" >&2
    exit 1
  fi
  [[ $(git -C "$repo" config --get core.bare) == false ]] || {
    echo "staged ignore check changed repository core.bare" >&2
    exit 1
  }
}

repos=()
cleanup() {
  if ((${#repos[@]} > 0)); then
    rm -rf "${repos[@]}"
  fi
}
trap cleanup EXIT

repo=$(new_repo)
repos+=("$repo")
: > "$repo/.gitignore"
printf base > "$repo/legit.txt"
git -C "$repo" add .gitignore legit.txt
git -C "$repo" commit -qm init
printf 'legit.txt\n' > "$repo/global-ignore"
git -C "$repo" config core.excludesFile "$repo/global-ignore"
printf change >> "$repo/legit.txt"
git -C "$repo" add legit.txt
expect_accept "$repo"
expect_accept_from_hook_env "$repo"

repo=$(new_repo)
repos+=("$repo")
: > "$repo/.gitignore"
git -C "$repo" add .gitignore
git -C "$repo" commit -qm init
printf 'secret.txt\n' > "$repo/.gitignore"
git -C "$repo" add .gitignore
: > "$repo/.gitignore"
printf secret > "$repo/secret.txt"
git -C "$repo" add -f secret.txt
expect_reject "$repo"

repo=$(new_repo)
repos+=("$repo")
printf 'ignored/\n*.secret\n' > "$repo/.gitignore"
printf base > "$repo/tracked.txt"
git -C "$repo" add .gitignore tracked.txt
git -C "$repo" commit -qm init
mkdir "$repo/ignored"
git -C "$repo" mv tracked.txt ignored/tracked.txt
expect_reject "$repo"

git -C "$repo" reset -q --hard HEAD
for name in $'line\nbreak.secret' '-dash.secret' 'space name.secret'; do
  printf x > "$repo/$name"
  git -C "$repo" add -f -- "$name"
done
expect_reject "$repo"

echo "staged ignored-file checks passed"
