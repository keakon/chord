#!/usr/bin/env bash
# Diagnose only files selected by Go for the requested supported platform.
set -euo pipefail

go_command="${CHORD_GO:-go}"
gopls_command="${GOPLS:-gopls}"
target_os=$("$go_command" env GOOS)
target_arch=$("$go_command" env GOARCH)
case "$target_os" in
  darwin|linux|windows) ;;
  *) echo "gopls check: unsupported target $target_os/$target_arch" >&2; exit 1 ;;
esac

scratch=$(mktemp -d "${TMPDIR:-/tmp}/chord-gopls.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
if (( $# == 0 )); then
  set -- ./...
fi

# Do not use -e: a failed package load must not become an incomplete clean check.
"$go_command" list -json "$@" > "$scratch/packages.json"
python3 - "$scratch/packages.json" > "$scratch/files" <<'PY'
import json
import os
import sys

text = open(sys.argv[1], encoding="utf-8").read()
decoder = json.JSONDecoder()
position = 0
files = set()
while position < len(text):
    if text[position].isspace():
        position += 1
        continue
    package, position = decoder.raw_decode(text, position)
    for field in ("GoFiles", "CgoFiles", "TestGoFiles", "XTestGoFiles"):
        files.update(os.path.join(package["Dir"], name) for name in package.get(field, []))
for path in sorted(files):
    sys.stdout.buffer.write(os.fsencode(path) + b"\0")
PY
if [[ ! -s "$scratch/files" ]]; then
  echo "gopls check: no Go files selected for $target_os/$target_arch" >&2
  exit 1
fi

# gopls check can report errors or warnings yet exit zero. Both diagnostic output
# and tool failures fail this gate; do not filter away platform-tagged messages.
if ! xargs -0 "$gopls_command" check < "$scratch/files" > "$scratch/diagnostics" 2>&1; then
  cat "$scratch/diagnostics" >&2
  echo "gopls check failed for $target_os/$target_arch" >&2
  exit 1
fi
if [[ -s "$scratch/diagnostics" ]]; then
  cat "$scratch/diagnostics" >&2
  echo "gopls diagnostics found for $target_os/$target_arch" >&2
  exit 1
fi
echo "gopls check passed for $target_os/$target_arch"
