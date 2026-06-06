#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

docs=(README.md README_CN.md)
required_links=(docs/index.md docs/index_CN.md docs/headless.md docs/headless_CN.md)
public_docs=(README.md README_CN.md docs website/src/content/docs)
version_docs=(README.md README_CN.md docs/quickstart.md docs/quickstart_CN.md CONTRIBUTING.md)
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

for doc in "${version_docs[@]}"; do
 [[ -f "$doc" ]] || fail "missing Go version doc $doc"
 grep -n 'Go 1\.26\.3' "$doc" >/dev/null || fail "$doc must mention Go 1.26.3"
done
if grep -RInE 'Go 1\.26\+|Go 1\.26 or later|Go 1\.26 或更高版本|需要 Go 1\.26( |$)|Requires Go 1\.26\.$' README*.md docs/quickstart*.md CONTRIBUTING.md >/dev/null; then
 fail "public Go version docs contain stale Go 1.26 wording; use Go 1.26.3+"
fi

ci_coverage=$(grep -E 'MIN_COVERAGE:' .github/workflows/ci.yml | head -n1 | sed -E 's/.*"([0-9.]+)".*/\1/')
[[ -n "$ci_coverage" ]] || fail "could not read MIN_COVERAGE from .github/workflows/ci.yml"
coverage_docs=(CONTRIBUTING.md .github/pull_request_template.md)
for doc in "${coverage_docs[@]}"; do
 [[ -f "$doc" ]] || fail "missing coverage doc $doc"
 grep -n "${ci_coverage}%" "$doc" >/dev/null || fail "$doc coverage threshold must match CI MIN_COVERAGE (${ci_coverage}%)"
 if grep -nE '65\.0%' "$doc" >/dev/null; then
  fail "$doc contains stale coverage threshold 65.0%"
 fi
done

grep -n "staticcheck -checks 'all,-ST1000' ./..." .github/pull_request_template.md >/dev/null || fail ".github/pull_request_template.md staticcheck command must match CI"
if grep -n "staticcheck -checks 'all,-ST\\*' ./..." .github/pull_request_template.md >/dev/null; then
 fail ".github/pull_request_template.md contains stale staticcheck -ST* command"
fi

for path in "${public_docs[@]}"; do
 [[ -e "$path" ]] || continue
 if grep -RIn --exclude-dir=.git --exclude='check_docs_consistency.sh' '\.internal-docs' "$path" >/dev/null; then
  fail "$path must not reference .internal-docs"
 fi
done

for en in docs/*.md; do
 base=$(basename "$en")
 [[ "$base" == *_CN.md ]] && continue
 cn="docs/${base%.md}_CN.md"
 [[ -f "$cn" ]] || fail "missing Chinese companion doc for $en: $cn"
done
for cn in docs/*_CN.md; do
 base=$(basename "$cn")
 en="docs/${base%_CN.md}.md"
 [[ -f "$en" ]] || fail "missing English companion doc for $cn: $en"
done

for en in docs/examples/*.md; do
 base=$(basename "$en")
 [[ "$base" == *_CN.md ]] && continue
 cn="docs/examples/${base%.md}_CN.md"
 [[ -f "$cn" ]] || fail "missing Chinese companion example for $en: $cn"
done
for cn in docs/examples/*_CN.md; do
 base=$(basename "$cn")
 en="docs/examples/${base%_CN.md}.md"
 [[ -f "$en" ]] || fail "missing English companion example for $cn: $en"
done

# Each docs/*.md <-> *_CN.md and docs/examples/*.md <-> *_CN.md pair must carry the
# same number of "## " section headings. This tolerates paragraph compression and
# example substitution but catches whole-section drift between the two languages.
for en in docs/*.md docs/examples/*.md; do
 base=$(basename "$en")
 [[ "$base" == *_CN.md ]] && continue
 cn="${en%.md}_CN.md"
 [[ -f "$cn" ]] || continue
 en_headings=$(grep -c '^## ' "$en" || true)
 cn_headings=$(grep -c '^## ' "$cn" || true)
 if [[ "$en_headings" != "$cn_headings" ]]; then
  fail "section heading count mismatch: $en has $en_headings '## ' headings but $cn has $cn_headings"
 fi
done

for page in docs/examples/examples-*.md; do
 [[ -f "$page" ]] || continue
 if [[ "$page" == *_CN.md ]]; then
  grep -n '## 需要准备的凭据' "$page" >/dev/null || fail "$page must explain credentials to prepare"
  grep -n '## 验证命令' "$page" >/dev/null || fail "$page must include verification commands"
  grep -n '## 常见失败原因' "$page" >/dev/null || fail "$page must list common failure causes"
 else
  grep -n '## Credentials to prepare' "$page" >/dev/null || fail "$page must explain credentials to prepare"
  grep -n '## Verify' "$page" >/dev/null || fail "$page must include verification commands"
  grep -n '## Common failures' "$page" >/dev/null || fail "$page must list common failure causes"
 fi
done

echo 'docs consistency check passed'
