#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

tidy_diff="$(mktemp)"
cleanup() {
  rm -f "$tidy_diff"
}
trap cleanup EXIT

if ! go mod tidy -diff >"$tidy_diff"; then
  echo "go.mod/go.sum are not tidy; run: go mod tidy" >&2
  cat "$tidy_diff" >&2
  exit 1
fi

python3 - <<'PY'
import os
import re
import subprocess
import sys
from pathlib import Path

go_mod = Path("go.mod")
lines = go_mod.read_text(encoding="utf-8").splitlines()

module = ""
requirements = []
replace_or_exclude = []
in_require = False
for lineno, line in enumerate(lines, 1):
    stripped = line.strip()
    if stripped.startswith("module "):
        module = stripped.split()[1]
    if stripped.startswith(("replace ", "replace(", "exclude ", "exclude(")):
        replace_or_exclude.append((lineno, stripped))
    if stripped == "require (":
        in_require = True
        continue
    if in_require and stripped == ")":
        in_require = False
        continue
    if stripped.startswith("require "):
        body = stripped[len("require ") :]
    elif in_require:
        body = stripped
    else:
        continue
    body_without_comment = body.split("//", 1)[0].strip()
    if not body_without_comment:
        continue
    parts = body_without_comment.split()
    if len(parts) >= 2:
        requirements.append(
            {
                "module": parts[0],
                "version": parts[1],
                "indirect": "// indirect" in body,
                "line": lineno,
            }
        )

if not module:
    print("dependency audit failed: could not read module path from go.mod", file=sys.stderr)
    sys.exit(1)

errors = []
if replace_or_exclude:
    for lineno, text in replace_or_exclude:
        errors.append(f"go.mod:{lineno}: replace/exclude directives require explicit dependency-audit review: {text}")

allowed_pseudo = {
    "github.com/charmbracelet/ultraviolet": "Charm TUI syntax highlighter API currently pinned before a stable tag.",
    "github.com/charmbracelet/x/exp/slice": "Transitive Charm experimental helper pinned by the current TUI stack.",
    "github.com/xo/terminfo": "Terminal database dependency currently publishes pseudo-version releases.",
}
allowed_forks = {
    "github.com/keakon/bubbles/v2": "Bubbles fork rebased to github.com/keakon/bubbletea/v2 so component tea.Msg/Cmd types match Chord's Bubble Tea fork.",
    "github.com/keakon/bubbletea/v2": "Bubble Tea fork used until WithoutScrollRegionOptimization is available upstream; Chord disables DECSTBM scroll-region optimization to avoid stale terminal rows.",
    "github.com/keakon/ultraviolet": "Ultraviolet fork used for lower-cost cached screen rendering after disabling scroll optimization.",
    "github.com/keakon/x/powernap": "LSP integration fork used until the required protocol/transport changes are available upstream.",
}
# Matches all Go pseudo-version forms by their trailing `-<14-digit timestamp>-<commit hash>`
# segment: the no-base-version form (vX.0.0-<ts>-<hash>) as well as the
# base-version forms (vX.Y.Z-pre.0.<ts>-<hash> and vX.Y.Z-0.<ts>-<hash>).
pseudo_re = re.compile(r"-\d{14}-[0-9a-f]{12,}$")

pseudo = []
forks = []
for req in requirements:
    mod = req["module"]
    version = req["version"]
    if pseudo_re.search(version):
        pseudo.append(req)
        if mod not in allowed_pseudo:
            errors.append(f"go.mod:{req['line']}: pseudo-version dependency {mod} {version} is not allowlisted")
    if version.endswith("-fork") or "/fork/" in mod or mod in allowed_forks:
        forks.append(req)
        if mod not in allowed_forks:
            errors.append(f"go.mod:{req['line']}: fork dependency {mod} {version} is not allowlisted")

graph = subprocess.run(["go", "mod", "graph"], check=True, text=True, stdout=subprocess.PIPE).stdout.splitlines()
root_edges = sum(1 for line in graph if line.startswith(module + " "))
graph_edges = len(graph)
max_root_edges = int(os.environ.get("CHORD_MAX_ROOT_REQUIRE_EDGES", "70"))
max_graph_edges = int(os.environ.get("CHORD_MAX_MOD_GRAPH_EDGES", "650"))
if root_edges > max_root_edges:
    errors.append(f"root dependency edges {root_edges} exceed CHORD_MAX_ROOT_REQUIRE_EDGES={max_root_edges}")
if graph_edges > max_graph_edges:
    errors.append(f"module graph edges {graph_edges} exceed CHORD_MAX_MOD_GRAPH_EDGES={max_graph_edges}")

if errors:
    print("dependency audit failed:", file=sys.stderr)
    for err in errors:
        print(f"- {err}", file=sys.stderr)
    if pseudo:
        print("pseudo-version dependencies found in go.mod:", file=sys.stderr)
        for req in pseudo:
            print(f"- {req['module']} {req['version']}", file=sys.stderr)
    if forks:
        print("fork dependencies found in go.mod:", file=sys.stderr)
        for req in forks:
            print(f"- {req['module']} {req['version']}", file=sys.stderr)
    sys.exit(1)

print(
    "dependency audit passed: "
    f"root_edges={root_edges}/{max_root_edges}, "
    f"graph_edges={graph_edges}/{max_graph_edges}, "
    f"pseudo_versions={len(pseudo)}, forks={len(forks)}"
)
PY
