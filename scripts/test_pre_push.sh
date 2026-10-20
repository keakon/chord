#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
hook="$repo_root/.githooks/pre-push"
unset $(git rev-parse --local-env-vars)
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_NOSYSTEM=1

test_root=$(mktemp -d "${TMPDIR:-/tmp}/chord-pre-push-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT
fixture="$test_root/repo"
git init -q "$fixture"
git -C "$fixture" config user.name test
git -C "$fixture" config user.email test@example.invalid
printf base > "$fixture/tracked.txt"
git -C "$fixture" add tracked.txt
git -C "$fixture" commit -qm initial
printf changed > "$fixture/tracked.txt"
git -C "$fixture" commit -qam update
git -C "$fixture" worktree add -qb hook-test "$test_root/worktree"

mkdir "$test_root/bin"
cat > "$test_root/bin/make" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CHORD_HOOK_TEST_MAKE_LOG"
if [[ ! -d "$CHORD_HOOK_TEST_FOREIGN_REPO" ]]; then
  git init -q --bare "$CHORD_HOOK_TEST_FOREIGN_REPO"
fi
[[ $(git --git-dir="$CHORD_HOOK_TEST_FOREIGN_REPO" rev-parse --is-bare-repository) == true ]]
SH
chmod +x "$test_root/bin/make"

for checkout in "$fixture" "$test_root/worktree"; do
  git_dir=$(git -C "$checkout" rev-parse --absolute-git-dir)
  foreign_repo="$test_root/$(basename "$checkout").git"
  make_log="$test_root/$(basename "$checkout").make.log"
  : > "$make_log"
  if ! output=$(cd "$checkout" && \
    GIT_DIR="$git_dir" GIT_WORK_TREE="$checkout" GIT_INDEX_FILE="$git_dir/index" \
    CHORD_HOOK_TEST_FOREIGN_REPO="$foreign_repo" CHORD_HOOK_TEST_MAKE_LOG="$make_log" \
    PATH="$test_root/bin:$PATH" "$hook" </dev/null 2>&1); then
    printf 'pre-push failed in hook environment:\n%s\n' "$output" >&2
    exit 1
  fi
  [[ $(git -C "$checkout" config --get core.bare) == false ]] || {
    echo "pre-push changed repository core.bare" >&2
    exit 1
  }
  [[ -z $(git -C "$checkout" status --porcelain) ]] || {
    echo "pre-push changed the checkout or index" >&2
    exit 1
  }
  grep -q "deps-check" "$make_log" || {
    echo "pre-push hook did not run the dependency audit (deps-check)" >&2
    exit 1
  }
done

# A release-tag fast path is allowed only when the release script provides
# the exact CI-verified SHA and every pushed ref is that lightweight tag.
release_tag="v1.2.3"
git -C "$fixture" tag "$release_tag"
release_sha=$(git -C "$fixture" rev-parse "refs/tags/$release_tag^{commit}")
zero_sha=0000000000000000000000000000000000000000
release_make_log="$test_root/release-tag.make.log"
release_foreign_repo="$test_root/release-tag.git"

run_release_hook() {
  local verified_sha="$1"
  shift
  : > "$release_make_log"
  if [[ -n "$verified_sha" ]]; then
    (cd "$fixture" && printf '%s\n' "$@" | \
      CHORD_RELEASE_CI_VERIFIED_SHA="$verified_sha" \
      CHORD_HOOK_TEST_FOREIGN_REPO="$release_foreign_repo" \
      CHORD_HOOK_TEST_MAKE_LOG="$release_make_log" \
      PATH="$test_root/bin:$PATH" "$hook" origin ssh://example.invalid/repo.git)
  else
    (cd "$fixture" && printf '%s\n' "$@" | \
      CHORD_HOOK_TEST_FOREIGN_REPO="$release_foreign_repo" \
      CHORD_HOOK_TEST_MAKE_LOG="$release_make_log" \
      PATH="$test_root/bin:$PATH" "$hook" origin ssh://example.invalid/repo.git)
  fi
}

output=$(run_release_hook "$release_sha" \
  "refs/tags/$release_tag $release_sha refs/tags/$release_tag $zero_sha")
[[ "$output" == *"skipping duplicate checks for release tag"* ]] || {
  printf 'verified release tag did not skip duplicate checks:\n%s\n' "$output" >&2
  exit 1
}
[[ ! -s "$release_make_log" ]] || {
  echo "verified release tag unexpectedly invoked make" >&2
  exit 1
}

# A mismatched SHA or an ordinary tag push must run the regular checks.
output=$(run_release_hook "1111111111111111111111111111111111111111" \
  "refs/tags/$release_tag $release_sha refs/tags/$release_tag $zero_sha")
[[ "$output" == *"running lightweight checks"* && -s "$release_make_log" ]] || {
  printf 'mismatched release SHA skipped checks:\n%s\n' "$output" >&2
  exit 1
}

output=$(run_release_hook "" \
  "refs/tags/$release_tag $release_sha refs/tags/$release_tag $zero_sha")
[[ "$output" == *"running lightweight checks"* && -s "$release_make_log" ]] || {
  printf 'ordinary tag push skipped checks:\n%s\n' "$output" >&2
  exit 1
}

# Mixed branch/tag pushes must not use the release-tag fast path.
output=$(run_release_hook "$release_sha" \
  "refs/tags/$release_tag $release_sha refs/tags/$release_tag $zero_sha" \
  "refs/heads/main $release_sha refs/heads/main $zero_sha")
[[ "$output" == *"running lightweight checks"* && -s "$release_make_log" ]] || {
  printf 'mixed branch/tag push skipped checks:\n%s\n' "$output" >&2
  exit 1
}

echo "pre-push hook environment and release-tag checks passed"
