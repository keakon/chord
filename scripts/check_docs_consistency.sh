#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

docs=(README.md README_CN.md)
required_links=(docs/index.md docs/index_CN.md docs/headless.md docs/headless_CN.md)
forbidden_patterns=(
 '\bserve\b'
 'HTTP/SSE'
 '(^|/)docs/architecture/'
 '(^|/)docs/troubleshooting/'
 '(^|/)docs/pitfalls/'
 '(^|/)docs/guides/'
 '(^|/)docs/plans/'
)

fail() {
 echo "docs consistency check failed: $*" >&2
 exit 1
}

for doc in "${docs[@]}"; do
 [[ -f "$doc" ]] || fail "missing doc $doc"
 for pattern in "${forbidden_patterns[@]}"; do
 if grep -nE "$pattern" "$doc" >/dev/null; then
 fail "$doc contains forbidden pattern: $pattern"
 fi
 done
 if ! grep -n 'chord headless' "$doc" >/dev/null; then
 fail "$doc must mention chord headless"
 fi
 while IFS= read -r link; do
 [[ -n "$link" ]] || continue
 target=${link%%#*}
 [[ -n "$target" ]] || continue
 if [[ "$target" != /* && "$target" != http://* && "$target" != https://* && "$target" != mailto:* ]]; then
 [[ -e "$target" ]] || fail "$doc links to missing local path: $target"
 fi
 done < <(grep -oE '\[[^]]+\]\(([^)]+)\)' "$doc" | sed -E 's/.*\(([^)]+)\)/\1/')
done

for link in "${required_links[@]}"; do
 if [[ "$link" == *_CN.md ]]; then
  grep -n "$link" README_CN.md >/dev/null || fail "README_CN.md must link to $link"
 else
  grep -n "$link" README.md >/dev/null || fail "README.md must link to $link"
 fi
done

echo 'docs consistency check passed'
