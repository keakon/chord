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
# Each baseline groups its symbols under comment lines that carry the
# classification and the reason. The check also fails while any symbol is still
# listed under the trailing marker line
#   # --- unclassified: needs classification and reason ---
# so every accepted entry ends up in a classified group.
#
# Target platform: GOOS/GOARCH from the environment, otherwise the host platform.
# deadcode has no platform flag — it inherits the target platform from the process
# environment through go/packages — so the analyzer must be built for the host
# while GOOS/GOARCH select the platform under analysis.
#
# Baselines live in scripts/deadcode-baseline/<goos>-<goarch>.txt. CI checks every
# baseline — linux/amd64, windows/amd64, and darwin/arm64 — by pointing GOOS/GOARCH
# at each target from one host; a local run checks the host platform unless the
# environment selects another. Update the current platform's baseline with:
#   CHORD_DEADCODE_UPDATE=1 scripts/check_deadcode.sh
# The update edits the file in place: comments, blank lines, and the order of
# existing entries stay as they are, symbols that are no longer reported are
# dropped, and newly reported symbols are appended under the unclassified marker
# (created when absent). Move each of them into a classified group with a reason
# before committing. A group comment whose symbols were all removed is reported
# as a warning and left for manual cleanup.
#
# Test hooks: CHORD_DEADCODE_BASELINE=<path> replaces the baseline path, and
# CHORD_DEADCODE_FINDINGS=<path> reads deadcode -json output from that file
# instead of building and running the analyzer.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! command -v python3 >/dev/null 2>&1; then
  echo "check_deadcode: python3 is required to compare findings with the baseline" >&2
  exit 1
fi

go_command="${CHORD_GO:-go}"
host_os="$("${go_command}" env GOHOSTOS)"
host_arch="$("${go_command}" env GOHOSTARCH)"
target_os="${GOOS:-$host_os}"
target_arch="${GOARCH:-$host_arch}"
baseline="${CHORD_DEADCODE_BASELINE:-scripts/deadcode-baseline/${target_os}-${target_arch}.txt}"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

if [[ -n "${CHORD_DEADCODE_FINDINGS:-}" ]]; then
  findings="${CHORD_DEADCODE_FINDINGS}"
else
  bin="${tmp_dir}/deadcode"
  findings="${tmp_dir}/findings.json"
  # Build the analyzer for the host platform so it stays runnable when GOOS/GOARCH
  # point at another platform. The tool version is pinned by the go.mod tool
  # directive, which keeps CI and local runs on the same analysis.
  GOOS="${host_os}" GOARCH="${host_arch}" "${go_command}" build -o "${bin}" golang.org/x/tools/cmd/deadcode
  GOOS="${target_os}" GOARCH="${target_arch}" "${bin}" -json -test=false -generated=false ./... >"${findings}"
fi

python3 - "${findings}" "${baseline}" "${target_os}/${target_arch}" "${CHORD_DEADCODE_UPDATE:-}" <<'PY'
import json
import sys
from pathlib import Path

findings_path, baseline_path, config, update = sys.argv[1:5]

UNCLASSIFIED = "# --- unclassified: needs classification and reason ---"
UPDATE_CMD = "CHORD_DEADCODE_UPDATE=1 scripts/check_deadcode.sh"

# Identity is the package path plus the symbol name; positions only serve as
# diagnostics because they drift with every unrelated edit.
current = {}
for pkg in json.loads(Path(findings_path).read_text(encoding="utf-8")):
    for fn in pkg["Funcs"]:
        pos = fn.get("Position") or {}
        where = "{}:{}:{}".format(pos.get("File", "?"), pos.get("Line", "?"), pos.get("Col", "?"))
        current["{}.{}".format(pkg["Path"], fn["Name"])] = where


def kind(line):
    text = line.strip()
    if not text:
        return "blank"
    if text == UNCLASSIFIED:
        return "marker"
    if text.startswith("#"):
        return "comment"
    return "symbol"


def unclassified_symbols(lines):
    """Symbols between the marker and the next comment line or end of file."""
    found = []
    inside = False
    for line in lines:
        k = kind(line)
        if k == "marker":
            inside = True
        elif k == "comment":
            inside = False
        elif k == "symbol" and inside:
            found.append(line.strip())
    return found


baseline_file = Path(baseline_path)
if not baseline_file.exists() and not update:
    print("deadcode gate failed for {}: no baseline at {}; create it with {} "
          "and classify the entries".format(config, baseline_file, UPDATE_CMD), file=sys.stderr)
    sys.exit(1)

original = ""
if baseline_file.exists():
    original = baseline_file.read_text(encoding="utf-8")
lines = original.splitlines(keepends=True)
baseline_set = {line.strip() for line in lines if kind(line) == "symbol"}

added = sorted(k for k in current if k not in baseline_set)
removed = sorted(k for k in baseline_set if k not in current)

if update:
    if not baseline_file.exists():
        lines = [text + "\n" for text in (
            "# Unreachable symbols reported by deadcode for {}.".format(config),
            "# Identity: <import path>.<symbol>. Positions are diagnostics and never stored here.",
            "# Regenerate: {}".format(UPDATE_CMD),
            "# Additions need their own commit with a classification and a reason per entry.",
        )]

    # Drop symbols that are no longer reported and remember which group comment
    # (the comment line right before a run of symbols) lost all of its entries.
    kept = []
    emptied = []
    group = None
    group_had = group_kept = 0

    def close_group():
        if group is not None and group_had and not group_kept:
            emptied.append(group)

    for line in lines:
        k = kind(line)
        if k in ("comment", "marker"):
            close_group()
            group = line.strip() if k == "comment" else None
            group_had = group_kept = 0
        elif k == "symbol":
            group_had += 1
            if line.strip() not in current:
                continue
            group_kept += 1
        kept.append(line)
    close_group()

    if added:
        if kept and not kept[-1].endswith("\n"):
            kept[-1] += "\n"
        marker_at = next((i for i, line in enumerate(kept) if kind(line) == "marker"), None)
        if marker_at is None:
            kept.append(UNCLASSIFIED + "\n")
            insert_at = len(kept)
        else:
            # Keep the entries already waiting under the marker and append after them.
            insert_at = marker_at + 1
            while insert_at < len(kept) and kind(kept[insert_at]) in ("symbol", "blank"):
                insert_at += 1
            while insert_at > marker_at + 1 and kind(kept[insert_at - 1]) == "blank":
                insert_at -= 1
        kept[insert_at:insert_at] = [key + "\n" for key in added]

    updated = "".join(kept)
    changed = updated != original or not baseline_file.exists()
    if changed:
        baseline_file.parent.mkdir(parents=True, exist_ok=True)
        baseline_file.write_text(updated, encoding="utf-8")
    print("check_deadcode: {} {} with {} findings (+{} / -{})".format(
        "updated" if changed else "kept", baseline_file, len(current), len(added), len(removed)))
    for key in added:
        print("  + {}  {}".format(current[key], key))
    for key in removed:
        print("  - {}".format(key))
    for comment in emptied:
        print("warning: group comment left without symbols; delete or rewrite it: {}".format(comment))
    if added:
        print("New entries are listed under '{}'. Move each of them into a classified group "
              "with a reason before committing; the check fails until then.".format(UNCLASSIFIED))
    sys.exit(0)

failed = False
if added:
    failed = True
    print("deadcode gate failed for {}: {} finding(s) missing from {}:".format(
        config, len(added), baseline_file), file=sys.stderr)
    for key in added:
        print("  {}  {}".format(current[key], key), file=sys.stderr)
    print("Remove the symbol, or add it to the baseline in a dedicated commit: run {} "
          "and move it into a classified group with a reason.".format(UPDATE_CMD), file=sys.stderr)

pending = unclassified_symbols(lines)
if pending:
    failed = True
    print("deadcode gate failed for {}: {} baseline entr{} in {} not classified yet:".format(
        config, len(pending), "y" if len(pending) == 1 else "ies", baseline_file), file=sys.stderr)
    for key in pending:
        print("  {}".format(key), file=sys.stderr)
    print("Move each of them from under '{}' into a classified group whose comment "
          "gives the classification and a reason.".format(UNCLASSIFIED), file=sys.stderr)

if failed:
    sys.exit(1)

print("deadcode gate passed for {}: {} finding(s) match {}".format(config, len(current), baseline_file))
if removed:
    text = "entry" if len(removed) == 1 else "entries"
    print("note: {} baseline {} no longer reported; shrink it with {}".format(
        len(removed), text, UPDATE_CMD))
PY
