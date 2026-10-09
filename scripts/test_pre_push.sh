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
git init -q --bare "$CHORD_HOOK_TEST_FOREIGN_REPO"
[[ $(git --git-dir="$CHORD_HOOK_TEST_FOREIGN_REPO" rev-parse --is-bare-repository) == true ]]
SH
chmod +x "$test_root/bin/make"

for checkout in "$fixture" "$test_root/worktree"; do
  git_dir=$(git -C "$checkout" rev-parse --absolute-git-dir)
  foreign_repo="$test_root/$(basename "$checkout").git"
  if ! output=$(cd "$checkout" && \
    GIT_DIR="$git_dir" GIT_WORK_TREE="$checkout" GIT_INDEX_FILE="$git_dir/index" \
    CHORD_HOOK_TEST_FOREIGN_REPO="$foreign_repo" PATH="$test_root/bin:$PATH" \
    "$hook" 2>&1); then
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
done

echo "pre-push hook environment checks passed"
