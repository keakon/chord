#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
gate="$repo_root/scripts/check_gopls.sh"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/chord-gopls-test.XXXXXX")
trap 'rm -rf "$fixture"' EXIT
mkdir "$fixture/project space" "$fixture/bin"
export CHORD_GOPLS_TEST_ARGS="$fixture/args"
export CHORD_GOPLS_TEST_RUNS="$fixture/runs"
export CHORD_GOPLS_TEST_INSTALLS="$fixture/installs"
export CHORD_GOPLS_TEST_CACHE="$fixture/cache space"
export CHORD_GOPLS_TEST_REAL_GO
CHORD_GOPLS_TEST_REAL_GO=$(command -v go)
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off GOFLAGS=
export GOPLS="$fixture/bin/gopls" CHORD_GO="$fixture/bin/go"
export CHORD_GOPLS_TEST_GOPLS="$GOPLS"
cat > "$GOPLS" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$GOOS/$GOARCH|$0" >> "$CHORD_GOPLS_TEST_RUNS"
printf '%s\n' "$@" > "$CHORD_GOPLS_TEST_ARGS"
case "${CHORD_GOPLS_TEST_MODE:-}" in
  diagnostic) echo 'sample.go:1:1: sample diagnostic' ;;
  stderr) echo 'sample failure' >&2 ;;
  failure) exit 7 ;;
esac
SH
cat > "$CHORD_GO" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == env && "$2" == GOCACHE ]]; then
  printf '%s\n' "$CHORD_GOPLS_TEST_CACHE"
  exit 0
fi
if [[ "$1" == install ]]; then
  printf '%s\n' "$GOOS/$GOARCH|$GOBIN|$*" >> "$CHORD_GOPLS_TEST_INSTALLS"
  if [[ "${CHORD_GOPLS_TEST_MODE:-}" == install-failure ]]; then
    echo 'sample install failure' >&2
    exit 11
  fi
  mkdir -p "$GOBIN"
  cp "$CHORD_GOPLS_TEST_GOPLS" "$GOBIN/gopls"
  exit 0
fi
if [[ "$1" == list ]]; then
  case "${CHORD_GOPLS_TEST_MODE:-}" in
    load-failure) echo 'sample package load failure' >&2; exit 9 ;;
    invalid-json) echo 'invalid json'; exit 0 ;;
    empty) echo '{"Dir":"/sample","GoFiles":[]}'; exit 0 ;;
  esac
  if [[ -n "${CHORD_GOPLS_TEST_MAKE_PROJECT:-}" ]]; then
    exec "$CHORD_GOPLS_TEST_REAL_GO" -C "$CHORD_GOPLS_TEST_MAKE_PROJECT" "$@"
  fi
fi
exec "$CHORD_GOPLS_TEST_REAL_GO" "$@"
SH
chmod +x "$GOPLS" "$CHORD_GO"
cd "$fixture/project space"
printf 'module example.invalid/sample\n\ngo 1.24\n' > go.mod
printf 'package sample\n' > sample.go
printf 'package sample\n' > sample_linux.go
printf 'package sample\n' > sample_windows.go
printf 'package sample\n' > sample_js.go
printf 'package sample\n' > sample_test.go
printf 'package sample_test\n' > external_test.go
printf '//go:build ignored\n\npackage sample\n' > excluded.go

"$gate" > "$fixture/output" 2>&1
for file in sample.go sample_linux.go sample_test.go external_test.go; do
  grep -Fqx "$PWD/$file" "$CHORD_GOPLS_TEST_ARGS"
done
for file in sample_windows.go sample_js.go excluded.go; do
  if grep -Fqx "$PWD/$file" "$CHORD_GOPLS_TEST_ARGS"; then
    echo "unexpected file: $file" >&2
    exit 1
  fi
done

GOOS=windows "$gate" > "$fixture/output" 2>&1
grep -Fqx "$PWD/sample_windows.go" "$CHORD_GOPLS_TEST_ARGS"
if grep -Fqx "$PWD/sample_linux.go" "$CHORD_GOPLS_TEST_ARGS"; then
  echo 'Windows check selected Linux source' >&2
  exit 1
fi

for mode in diagnostic stderr failure load-failure invalid-json empty; do
  rm -f "$CHORD_GOPLS_TEST_ARGS"
  if CHORD_GOPLS_TEST_MODE="$mode" "$gate" > "$fixture/output" 2>&1; then
    echo "gate unexpectedly accepted $mode" >&2
    exit 1
  fi
  case "$mode" in
    load-failure|invalid-json|empty) [[ ! -e "$CHORD_GOPLS_TEST_ARGS" ]] ;;
  esac
done
rm -f "$CHORD_GOPLS_TEST_ARGS"
if GOOS=js GOARCH=wasm "$gate" > "$fixture/output" 2>&1; then
  echo 'gate accepted unsupported target' >&2
  exit 1
fi
[[ ! -e "$CHORD_GOPLS_TEST_ARGS" ]]

run_make_gate() {
  local version="$1"
  shift
  if make --no-print-directory -C "$repo_root" gopls-check GO="$CHORD_GO" GOPLS_VERSION="$version" "$@" > "$fixture/output" 2>&1; then
    return 0
  fi
  cat "$fixture/output" >&2
  return 1
}

export CHORD_GOPLS_TEST_MAKE_PROJECT="$PWD"
host_os=$("$CHORD_GOPLS_TEST_REAL_GO" env GOHOSTOS)
host_arch=$("$CHORD_GOPLS_TEST_REAL_GO" env GOHOSTARCH)
cache_dir="$CHORD_GOPLS_TEST_CACHE/chord-gopls/v0.0.1-$host_os-$host_arch"
unset GOPLS
: > "$CHORD_GOPLS_TEST_INSTALLS"
: > "$CHORD_GOPLS_TEST_RUNS"
GOOS=windows GOARCH=amd64 run_make_gate v0.0.1
if [[ $(wc -l < "$CHORD_GOPLS_TEST_INSTALLS") -ne 1 ]]; then
  echo 'initial Make check should install gopls exactly once' >&2
  cat "$CHORD_GOPLS_TEST_INSTALLS" >&2
  exit 1
fi
[[ -x "$cache_dir/gopls" ]]
grep -Fqx "$host_os/$host_arch|$cache_dir|install golang.org/x/tools/gopls@v0.0.1" "$CHORD_GOPLS_TEST_INSTALLS"
grep -Fqx "windows/amd64|$cache_dir/gopls" "$CHORD_GOPLS_TEST_RUNS"
grep -Fqx "$PWD/sample_windows.go" "$CHORD_GOPLS_TEST_ARGS"

GOOS=darwin GOARCH=arm64 run_make_gate v0.0.1
[[ $(wc -l < "$CHORD_GOPLS_TEST_INSTALLS") -eq 1 ]]
grep -Fqx "darwin/arm64|$cache_dir/gopls" "$CHORD_GOPLS_TEST_RUNS"

run_make_gate v0.0.2
[[ $(wc -l < "$CHORD_GOPLS_TEST_INSTALLS") -eq 2 ]]
grep -Fqx "$host_os/$host_arch|$CHORD_GOPLS_TEST_CACHE/chord-gopls/v0.0.2-$host_os-$host_arch|install golang.org/x/tools/gopls@v0.0.2" "$CHORD_GOPLS_TEST_INSTALLS"

: > "$CHORD_GOPLS_TEST_INSTALLS"
: > "$CHORD_GOPLS_TEST_RUNS"
run_make_gate v0.0.3 "GOPLS=$CHORD_GOPLS_TEST_GOPLS"
GOPLS="$CHORD_GOPLS_TEST_GOPLS" run_make_gate v0.0.3
[[ ! -s "$CHORD_GOPLS_TEST_INSTALLS" ]]
[[ $(wc -l < "$CHORD_GOPLS_TEST_RUNS") -eq 2 ]]
grep -Fqx "linux/amd64|$CHORD_GOPLS_TEST_GOPLS" "$CHORD_GOPLS_TEST_RUNS"

rm -f "$CHORD_GOPLS_TEST_ARGS"
: > "$CHORD_GOPLS_TEST_RUNS"
if CHORD_GOPLS_TEST_MODE=install-failure run_make_gate v0.0.3; then
  echo 'Make gate unexpectedly accepted an installation failure' >&2
  exit 1
fi
grep -Fq 'sample install failure' "$fixture/output"
[[ $(wc -l < "$CHORD_GOPLS_TEST_INSTALLS") -eq 1 ]]
[[ ! -s "$CHORD_GOPLS_TEST_RUNS" && ! -e "$CHORD_GOPLS_TEST_ARGS" ]]
echo 'gopls gate regression tests passed'
