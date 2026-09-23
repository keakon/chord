#!/usr/bin/env bash
set -euo pipefail

# Ratchet gate for symbols that are unreachable from the production entry points.
#
# `deadcode` reports every function it cannot reach from `cmd/chord` after
# excluding test packages and generated files. This script compares that set with
# the checked-in baseline for the target platform: a finding that is missing from
# the baseline fails the gate, a baseline entry that is no longer reported is an
# improvement and only gets a note. Cleanup commits shrink the baseline; adding
# entries is a deliberate review step that deserves its own commit with a
# classification and a reason per entry (see scripts/deadcode-baseline/).
#
# Target platform: GOOS/GOARCH from the environment, otherwise the host platform.
# deadcode has no platform flag — it inherits the target platform from the process
# environment through go/packages — so the analyzer must be built for the host
# while GOOS/GOARCH select the platform under analysis.
#
# Baselines live in scripts/deadcode-baseline/<goos>-<goarch>.txt. CI checks every
# baseline — linux/amd64, windows/amd64, and darwin/arm64 — by pointing GOOS/GOARCH
# at each target from one host; a local run checks the host platform unless the
# environment selects another. Regenerate the current platform's baseline after
# removing symbols with:
#   CHORD_DEADCODE_UPDATE=1 scripts/check_deadcode.sh

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! command -v python3 >/dev/null 2>&1; then
  echo "check_deadcode: python3 is required to compare findings with the baseline" >&2
  exit 1
fi

host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
target_os="${GOOS:-$host_os}"
target_arch="${GOARCH:-$host_arch}"
baseline="scripts/deadcode-baseline/${target_os}-${target_arch}.txt"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

bin="${tmp_dir}/deadcode"
findings="${tmp_dir}/findings.json"

# Build the analyzer for the host platform so it stays runnable when GOOS/GOARCH
# point at another platform. The tool version is pinned by the go.mod tool
# directive, which keeps CI and local runs on the same analysis.
GOOS="${host_os}" GOARCH="${host_arch}" go build -o "${bin}" golang.org/x/tools/cmd/deadcode
GOOS="${target_os}" GOARCH="${target_arch}" "${bin}" -json -test=false -generated=false ./... >"${findings}"

python3 - "${findings}" "${baseline}" "${target_os}/${target_arch}" "${CHORD_DEADCODE_UPDATE:-}" <<'PY'
import json
import sys
from pathlib import Path

findings_path, baseline_path, config, update = sys.argv[1:5]

# Identity is the package path plus the symbol name; positions only serve as
# diagnostics because they drift with every unrelated edit.
current = {}
for pkg in json.loads(Path(findings_path).read_text(encoding="utf-8")):
    for fn in pkg["Funcs"]:
        pos = fn.get("Position") or {}
        where = "{}:{}:{}".format(pos.get("File", "?"), pos.get("Line", "?"), pos.get("Col", "?"))
        current["{}.{}".format(pkg["Path"], fn["Name"])] = where

baseline_file = Path(baseline_path)
if not baseline_file.exists() and not update:
    print("deadcode gate failed for {}: no baseline at {}; create it with "
          "CHORD_DEADCODE_UPDATE=1 scripts/check_deadcode.sh and review the entries".format(
              config, baseline_file), file=sys.stderr)
    sys.exit(1)

baseline = []
if baseline_file.exists():
    for line in baseline_file.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            baseline.append(line)
baseline_set = set(baseline)

if update:
    added = sorted(k for k in current if k not in baseline_set)
    removed = sorted(k for k in baseline_set if k not in current)
    baseline_file.parent.mkdir(parents=True, exist_ok=True)
    body = ["# Unreachable symbols reported by deadcode for {}.".format(config),
            "# Identity: <import path>.<symbol>. Positions are diagnostics and never stored here.",
            "# Regenerate: CHORD_DEADCODE_UPDATE=1 scripts/check_deadcode.sh",
            "# Additions need their own commit with a classification and a reason per entry."]
    body.extend(sorted(current))
    baseline_file.write_text("\n".join(body) + "\n", encoding="utf-8")
    print("check_deadcode: wrote {} findings to {} (+{} / -{})".format(
        len(current), baseline_file, len(added), len(removed)))
    for key in added:
        print("  + {}  {}".format(current[key], key))
    for key in removed:
        print("  - {}".format(key))
    sys.exit(0)

new = sorted(k for k in current if k not in baseline_set)
stale = sorted(k for k in baseline_set if k not in current)

if new:
    print("deadcode gate failed for {}: {} finding(s) missing from {}:".format(
        config, len(new), baseline_file), file=sys.stderr)
    for key in new:
        print("  {}  {}".format(current[key], key), file=sys.stderr)
    print("Remove the symbol, or add it to the baseline in a dedicated commit with a "
          "classification and a reason.", file=sys.stderr)
    sys.exit(1)

print("deadcode gate passed for {}: {} finding(s) match {}".format(config, len(current), baseline_file))
if stale:
    text = "entry" if len(stale) == 1 else "entries"
    print("note: {} baseline {} no longer reported; shrink it with "
          "CHORD_DEADCODE_UPDATE=1 scripts/check_deadcode.sh".format(len(stale), text))
PY
