#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
gate="$repo_root/scripts/check_gopls.sh"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/chord-gopls-test.XXXXXX")
trap 'rm -rf "$fixture"' EXIT
mkdir "$fixture/project space" "$fixture/bin"
export CHORD_GOPLS_TEST_ARGS="$fixture/args"
export CHORD_GOPLS_TEST_REAL_GO
CHORD_GOPLS_TEST_REAL_GO=$(command -v go)
export GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off GOFLAGS=
export GOPLS="$fixture/bin/gopls" CHORD_GO="$fixture/bin/go"
cat > "$GOPLS" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$CHORD_GOPLS_TEST_ARGS"
case "${CHORD_GOPLS_TEST_MODE:-}" in
  diagnostic) echo 'sample.go:1:1: sample diagnostic' ;;
  stderr) echo 'sample failure' >&2 ;;
  failure) exit 7 ;;
esac
SH
cat > "$CHORD_GO" <<'SH'
#!/usr/bin/env bash
if [[ "$1" == list ]]; then
  case "${CHORD_GOPLS_TEST_MODE:-}" in
    load-failure) echo 'sample package load failure' >&2; exit 9 ;;
    invalid-json) echo 'invalid json'; exit 0 ;;
    empty) echo '{"Dir":"/sample","GoFiles":[]}'; exit 0 ;;
  esac
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
echo 'gopls gate regression tests passed'
