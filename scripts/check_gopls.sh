#!/usr/bin/env bash
# Diagnose only files selected by Go for the requested supported platform.
#
# The file set is partitioned into whole packages balanced by file count and
# diagnosed by several gopls processes in parallel: a single gopls check run
# is mostly single-threaded for this workload. Packages are kept whole and
# every file is still diagnosed by exactly one process with full type
# information, so the reported diagnostics match a single-process run.
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
python3 - "$scratch/packages.json" "$scratch/groups" <<'PY'
import json
import os
import sys

text = open(sys.argv[1], encoding="utf-8").read()
decoder = json.JSONDecoder()
position = 0
packages = []
while position < len(text):
    if text[position].isspace():
        position += 1
        continue
    package, position = decoder.raw_decode(text, position)
    files = []
    for field in ("GoFiles", "CgoFiles", "TestGoFiles", "XTestGoFiles"):
        files.extend(os.path.join(package["Dir"], name) for name in package.get(field, []))
    if files:
        packages.append(files)
os.makedirs(sys.argv[2])
jobs = min(8, os.cpu_count() or 1)
# Greedy packing: the heaviest package goes to the currently lightest group.
groups = [[] for _ in range(jobs)]
sizes = [0] * jobs
for files in sorted(packages, key=len, reverse=True):
    index = sizes.index(min(sizes))
    groups[index].extend(files)
    sizes[index] += len(files)
for index, files in enumerate(groups):
    with open(os.path.join(sys.argv[2], str(index)), "wb") as out:
        for path in files:
            out.write(os.fsencode(path) + b"\0")
PY
if [[ ! -s "$scratch/groups/0" ]]; then
  echo "gopls check: no Go files selected for $target_os/$target_arch" >&2
  exit 1
fi

# gopls check can report errors or warnings yet exit zero. Both diagnostic output
# and tool failures fail this gate; do not filter away platform-tagged messages.
# Workers write to separate files first so concurrent output cannot interleave.
status=0
outputs=()
pids=()
index=0
for group in "$scratch/groups"/*; do
  [[ -s "$group" ]] || continue
  out="$scratch/diag.$index"
  outputs+=("$out")
  xargs -0 "$gopls_command" check < "$group" > "$out" 2>&1 &
  pids+=("$!")
  index=$((index + 1))
done
for pid in "${pids[@]}"; do
  wait "$pid" || status=1
done
cat "${outputs[@]}" > "$scratch/diagnostics"
if [[ -s "$scratch/diagnostics" ]]; then
  cat "$scratch/diagnostics" >&2
fi
if (( status != 0 )); then
  echo "gopls check failed for $target_os/$target_arch" >&2
  exit 1
fi
if [[ -s "$scratch/diagnostics" ]]; then
  echo "gopls diagnostics found for $target_os/$target_arch" >&2
  exit 1
fi
echo "gopls check passed for $target_os/$target_arch"
