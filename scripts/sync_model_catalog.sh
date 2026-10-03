#!/usr/bin/env bash
set -euo pipefail

# Sync the embedded model catalog snapshot from the chord-models repository.
#
# Usage:
#   scripts/sync_model_catalog.sh <tag-or-revision> [path-to-chord-models]
#
# The script pins the given revision, copies the four YAML source files into
# internal/modelcatalog/data, records the pin in data/snapshot.yaml, and
# regenerates the committed catalog.json. The golden test keeps the artifact
# honest afterwards: `go test ./internal/modelcatalog` must pass. chord-models
# owns the data; this repository owns the schema and the generated artifact.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

revision="${1:-}"
if [[ -z "$revision" ]]; then
  echo "usage: scripts/sync_model_catalog.sh <tag-or-revision> [path-to-chord-models]" >&2
  exit 1
fi

repo="${2:-$repo_root/../chord-models}"
if [[ ! -d "$repo/.git" ]]; then
  echo "sync_model_catalog: no chord-models checkout at $repo; pass the path as the second argument" >&2
  exit 1
fi

default_repository="https://github.com/keakon/chord-models"

if ! git -C "$repo" rev-parse --verify --quiet "${revision}^{commit}" >/dev/null; then
  echo "sync_model_catalog: revision $revision does not exist in $repo" >&2
  exit 1
fi

data_dir="internal/modelcatalog/data"
for file in catalog.yaml endpoints.yaml models.yaml bindings.yaml; do
  if ! git -C "$repo" show "${revision}:${file}" >"${data_dir}/${file}"; then
    echo "sync_model_catalog: $file is missing from $revision" >&2
    exit 1
  fi
done

if repository="$(git -C "$repo" remote get-url origin 2>/dev/null)" && [[ -n "$repository" ]]; then
  :
else
  repository="$default_repository"
fi
if [[ "$repository" != https://* ]]; then
  # Local remotes (paths, ssh) are not a publication URL; record the canonical one.
  repository="$default_repository"
fi

cat >"${data_dir}/snapshot.yaml" <<EOF
# Records which chord-models revision this snapshot was synced from, written
# by scripts/sync_model_catalog.sh. The generator copies it into catalog.json
# and the golden test keeps both honest. Not part of the upstream repository.
repository: ${repository}
revision: ${revision}
EOF

go run ./cmd/modelcatalog-gen

changed="$(git status --porcelain -- "${data_dir}" internal/modelcatalog/catalog.json)"
if [[ -z "$changed" ]]; then
  echo "sync_model_catalog: snapshot ${revision} matches the committed artifact (no changes)"
else
  echo "sync_model_catalog: snapshot ${revision} synced; review and commit:"
  echo "$changed"
fi
