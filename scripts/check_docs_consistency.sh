#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

docs=(README.md README_CN.md)
required_links=(docs/index.md docs/index_CN.md docs/headless.md docs/headless_CN.md)
public_docs=(README.md README_CN.md docs website/src/content/docs)
# The two website index pages are hand-maintained (sync-docs.mjs skips them), so
# they need the same Go version enforcement as the Markdown docs.
version_docs=(README.md README_CN.md docs/quickstart.md docs/quickstart_CN.md CONTRIBUTING.md website/src/content/docs/index.mdx website/src/content/docs/zh/index.mdx)
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

go_mod_version=$(sed -nE 's/^go ([0-9]+(\.[0-9]+)+)$/\1/p' go.mod)
[[ -n "$go_mod_version" ]] || fail "cannot read the go directive from go.mod"

for doc in "${version_docs[@]}"; do
 [[ -f "$doc" ]] || fail "missing Go version doc $doc"
 grep -nF "Go $go_mod_version" "$doc" >/dev/null || fail "$doc must mention Go $go_mod_version"
done
# Every minimum-version phrasing must name the go.mod version. Matching the
# requirement phrasings instead of every "Go x.y" mention keeps a deliberate
# historical reference legal, and deriving the expected version from go.mod means
# this gate needs no edit on the next toolchain bump.
version_requirement='Go [0-9]+(\.[0-9]+)+(\+| or newer| or later| 或更新版本)|需要 Go [0-9]+(\.[0-9]+)+'
if stale=$(grep -REIn "$version_requirement" "${version_docs[@]}" | grep -vF "Go $go_mod_version"); then
 fail "public Go version docs state a minimum other than Go $go_mod_version: $stale"
fi

default_output_cap=$(sed -nE 's/^const DefaultOutputTokenMax = ([0-9]+)$/\1/p' internal/llm/client.go)
[[ "$default_output_cap" =~ ^[0-9]+$ ]] || fail "could not read DefaultOutputTokenMax from internal/llm/client.go"
output_cap_docs=(
 docs/configuration.md
 docs/configuration_CN.md
 docs/examples/codex-oauth-with-lsp.yaml
 docs/examples/examples-codex-workstation.md
 docs/examples/examples-codex-workstation_CN.md
)
for doc in "${output_cap_docs[@]}"; do
 grep -n "max_output_tokens.*${default_output_cap}" "$doc" >/dev/null || fail "$doc must mention max_output_tokens default ${default_output_cap}"
done

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

# Validate in-page anchor links (target.md#fragment) against real headings.
# Slugs follow GitHub's algorithm (lowercase, drop a fixed punctuation set,
# spaces -> hyphens) and are Unicode-aware so CN/accented headings resolve.
if ! perl - <<'PERL'
use strict;
use warnings;
use File::Basename qw(dirname);
use File::Spec;

binmode STDOUT, ':encoding(UTF-8)';
binmode STDERR, ':encoding(UTF-8)';

my @files = ('README.md', 'README_CN.md');
push @files, sort glob('docs/*.md docs/examples/*.md');
@files = grep { -f $_ } @files;

sub slugify {
    my ($s) = @_;
    $s = lc $s;
    # GitHub keeps letters, marks, numbers, connector punctuation (_) and hyphen;
    # everything else (incl. ASCII and CJK fullwidth punctuation/symbols) is dropped.
    $s =~ s/[^\p{L}\p{M}\p{N}\p{Pc}\- ]//g;
    $s =~ s/ /-/g;
    return $s;
}

my %slugs;
for my $f (@files) {
    open my $fh, '<:encoding(UTF-8)', $f or next;
    my %seen;
    while (my $line = <$fh>) {
        next unless $line =~ /^#{1,6}\s+(.*?)\s*#*\s*$/;
        my $text = $1;
        $text =~ s/\[([^\]]*)\]\([^)]*\)/$1/g;
        # Code-span content is literal text and survives GitHub slugging, so
        # underscores stay (connector punctuation); only backticks and asterisks
        # are dropped, matching github-slugger.
        $text =~ s/[`*]//g;
        my $slug = slugify($text);
        if (exists $seen{$slug}) { my $n = $seen{$slug}++; $slug = "$slug-$n"; }
        else { $seen{$slug} = 1; }
        $slugs{$f}{$slug} = 1;
    }
    close $fh;
}

my $issues = 0;
for my $f (@files) {
    open my $fh, '<:encoding(UTF-8)', $f or next;
    my $dir = dirname($f);
    while (my $line = <$fh>) {
        while ($line =~ /\[[^\]]*\]\(([^)\s]+)\)/g) {
            my $target = $1;
            next unless $target =~ /#/;
            my ($path, $frag) = split /#/, $target, 2;
            next if !defined $frag || $frag eq '';
            next if $path =~ m{^https?://} || $path =~ /^mailto:/;
            my $tf;
            if ($path eq '') {
                $tf = $f;
            } else {
                next unless $path =~ /\.md$/;
                $tf = File::Spec->abs2rel(File::Spec->rel2abs($path, $dir));
            }
            next unless exists $slugs{$tf};
            unless ($slugs{$tf}{$frag}) {
                print "BAD ANCHOR $f -> $target\n";
                $issues++;
            }
        }
    }
    close $fh;
}
exit($issues > 0 ? 1 : 0);
PERL
then
    fail "found broken in-page anchor links (see BAD ANCHOR lines above)"
fi

echo 'docs consistency check passed'
