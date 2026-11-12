#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/chord-test-flags.XXXXXX")"
trap 'rm -rf "${test_root}"' EXIT

cat >"${test_root}/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == env ]]; then
  case "${2:-}" in
    GOCACHE) printf '%s/cache\n' "${CHORD_TEST_FLAGS_ROOT}" ;;
    GOHOSTOS) printf 'linux\n' ;;
    GOHOSTARCH) printf 'amd64\n' ;;
    *) exit 1 ;;
  esac
  exit 0
fi
[[ "${1:-}" == test ]] || exit 1
printf '%s\n' "$@" >"${CHORD_TEST_FLAGS_ARGS}"
SH
chmod +x "${test_root}/go"

out="${test_root}/out.txt"
args="${test_root}/args.txt"
expected="${test_root}/expected.txt"
fail() {
  printf 'test_go_testflags: %s\n' "$1" >&2
  cat "${out}" >&2
  printf '%s\n' '--- expected arguments ---' >&2
  cat "${expected}" >&2
  printf '%s\n' '--- actual arguments ---' >&2
  if [[ -f "${args}" ]]; then cat "${args}" >&2; fi
  exit 1
}

run_make() (
  local target="$1" source="$2" flags="$3"
  local command=(make --no-print-directory -C "${repo_root}" -f Makefile
    "GO=${test_root}/go" "PKGS=./internal/example")
  unset GO_TESTFLAGS MAKEFLAGS MFLAGS MAKEOVERRIDES
  export CHORD_TEST_RUNNER_ACTIVE=1
  export CHORD_TEST_FLAGS_ARGS="${args}" CHORD_TEST_FLAGS_ROOT="${test_root}"
  if [[ "${target}" == preflight ]]; then
    local prerequisite
    for prerequisite in fmt-check deps-check vet staticcheck gopls-check \
      modernize-check deadcode-check resource-scripts-check; do
      command+=(-o "${prerequisite}")
    done
  fi
  if [[ "${source}" == environment ]]; then
    export GO_TESTFLAGS="${flags}"
  else
    command+=("GO_TESTFLAGS=${flags}")
  fi
  "${command[@]}" "${target}"
)

check_case() {
  local flags="$1"
  shift
  local source target
  for source in command environment; do
    for target in test test-fast preflight; do
      printf 'test\n' >"${expected}"
      if [[ "${target}" == test ]]; then
        printf '%s\n' '-count=1' >>"${expected}"
      fi
      if [[ "${target}" != preflight && "$#" -gt 0 ]]; then
        printf '%s\n' "$@" >>"${expected}"
      fi
      printf '%s\n' './internal/example' >>"${expected}"
      rm -f "${args}"
      run_make "${target}" "${source}" "${flags}" >"${out}" 2>&1 ||
        fail "${target}/${source} failed for ${flags}"
      cmp -s "${expected}" "${args}" ||
        fail "${target}/${source} changed arguments for ${flags}"
    done
  done
}

check_case "-run '^TestSample$' -count=1 -v" -run '^TestSample$' -count=1 -v
check_case '-run ^TestSample$ -count=1' -run '^TestSample$' -count=1
check_case "-run 'Compaction|Memory' -v" -run 'Compaction|Memory' -v
check_case ""

echo "test_go_testflags: ok"
