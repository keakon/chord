#!/usr/bin/env bash
# Run goimports on existing tracked and non-ignored untracked Go files.
set -euo pipefail

if (( $# == 0 )); then
  echo "Usage: $0 <goimports command> [arguments...]" >&2
  exit 2
fi

paths=$(mktemp "${TMPDIR:-/tmp}/chord-goimports.XXXXXX")
trap 'rm -f "$paths"' EXIT

# Finish enumeration before invoking the formatter so Git errors fail closed.
git ls-files -z --cached --others --exclude-standard -- '*.go' > "$paths"
files=()
while IFS= read -r -d '' path; do
  if [[ -f "$path" ]]; then
    files+=("./$path")
  fi
done < "$paths"

# An empty selection must not make goimports read from stdin.
if (( ${#files[@]} > 0 )); then
  printf '%s\0' "${files[@]}" | xargs -0 "$@"
fi
