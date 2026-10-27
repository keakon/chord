#!/usr/bin/env bash
set -euo pipefail

# Remind maintainers when a local chord-models checkout has a newer published
# catalog tag than the embedded snapshot. This is a best-effort local check:
# a missing or unrelated checkout is ignored, and the result never blocks a
# push.

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$repo_root" ]]; then
  exit 0
fi

snapshot="$repo_root/internal/modelcatalog/data/snapshot.yaml"
[[ -f "$snapshot" ]] || exit 0

embedded="$(sed -n 's/^revision:[[:space:]]*//p' "$snapshot" | sed -n '1p')"
[[ -n "$embedded" ]] || exit 0

if [[ "${CHORD_SKIP_MODEL_CATALOG_CHECK:-}" == "1" ]]; then
  exit 0
fi

models_dir="${CHORD_MODELS_DIR:-$repo_root/../chord-models}"
# .git is a directory in a normal checkout and a file in a linked worktree;
# both are valid sources for the reminder.
[[ -e "$models_dir/.git" ]] || exit 0
models_dir="$(cd "$models_dir" && pwd)" || exit 0

# Only compare against a directory that looks like a chord-models checkout.
for file in catalog.yaml endpoints.yaml models.yaml bindings.yaml; do
  [[ -f "$models_dir/$file" ]] || exit 0
done

latest="$(git -C "$models_dir" for-each-ref --count=1 --sort=-v:refname \
  --format='%(refname:short)' 'refs/tags/v[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9].[0-9]*' 2>/dev/null || true)"
[[ -n "$latest" && "$latest" != "$embedded" ]] || exit 0

version_is_newer() {
  local candidate="$1" current="$2"
  awk -v a="$candidate" -v b="$current" '
    BEGIN {
      sub(/^v/, "", a)
      sub(/^v/, "", b)
      split(a, av, ".")
      split(b, bv, ".")
      if (av[1] > bv[1]) exit 0
      if (av[1] < bv[1]) exit 1
      exit((av[2] + 0) > (bv[2] + 0) ? 0 : 1)
    }'
}

if git -C "$models_dir" rev-parse --verify --quiet "refs/tags/$embedded^{commit}" >/dev/null; then
  if ! git -C "$models_dir" merge-base --is-ancestor "refs/tags/$embedded" "refs/tags/$latest"; then
    exit 0
  fi
elif ! version_is_newer "$latest" "$embedded"; then
  exit 0
fi

cat >&2 <<EOF
model-catalog-check: local chord-models has a newer catalog tag: $latest (embedded: $embedded)
model-catalog-check: review and sync with:
model-catalog-check:   scripts/sync_model_catalog.sh $latest "$models_dir"
model-catalog-check: reminder only; the push is not blocked. Set CHORD_SKIP_MODEL_CATALOG_CHECK=1 to silence this.
EOF
exit 0
