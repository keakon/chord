#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
check="${repo_root}/scripts/check_ci_race.sh"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/chord-race-check.XXXXXX")"
trap 'rm -rf "${test_root}"' EXIT

cat >"${test_root}/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >"${CHORD_RACE_TEST_ARGS}"
printf 'package context before\n'
case "${CHORD_RACE_TEST_MODE}" in
  failure)
    printf 'test failure details\n'
    printf 'WARNING: DATA RACE\nrace details\n' >&2
    ;;
  stderr)
    printf 'diagnostic details\n' >&2
    ;;
  leak)
    printf 'client_retry sample leak\n'
    ;;
esac
printf 'package context after\n'
if [[ "${CHORD_RACE_TEST_MODE}" == failure ]]; then
  exit 7
fi
SH
chmod +x "${test_root}/go"

out="${test_root}/out.txt"
args="${test_root}/args.txt"
fail() {
  printf 'test_check_ci_race: %s\n' "$1" >&2
  cat "${out}" >&2
  exit 1
}

for mode in success failure stderr leak; do
  status=0
  CHORD_TEST_RUNNER_ACTIVE=1 CHORD_GO="${test_root}/go" \
    CHORD_RACE_TEST_MODE="${mode}" CHORD_RACE_TEST_ARGS="${args}" \
    bash "${check}" ./internal/example >"${out}" 2>&1 || status=$?
  if [[ "${mode}" == success ]]; then
    [[ "${status}" == 0 ]] || fail "clean test output failed the gate"
    [[ "$(cat "${out}")" == $'package context before\npackage context after' ]] ||
      fail "clean test output changed"
  else
    [[ "${status}" != 0 ]] || fail "${mode} passed the gate"
    grep -qxF 'package context before' "${out}" || fail "${mode} lost preceding stdout"
    grep -qxF 'package context after' "${out}" || fail "${mode} lost trailing stdout"
    case "${mode}" in
      failure)
        grep -qxF 'test failure details' "${out}" || fail "test failure lost stdout details"
        grep -qxF 'WARNING: DATA RACE' "${out}" || fail "test failure lost the race report"
        grep -qxF 'race details' "${out}" || fail "test failure lost stderr details"
        ;;
      stderr)
        grep -qxF 'diagnostic details' "${out}" || fail "stderr failure lost diagnostics"
        ;;
      leak)
        grep -qxF 'client_retry sample leak' "${out}" || fail "leak failure lost the original line"
        ;;
    esac
  fi
  [[ "$(cat "${args}")" == $'test\n-race\n-count=1\n-timeout=10m\n./internal/example' ]] ||
    fail "${mode} changed the race command or package arguments"
done

# The package default must keep the full repository gate.
CHORD_TEST_RUNNER_ACTIVE=1 CHORD_GO="${test_root}/go" \
  CHORD_RACE_TEST_MODE=success CHORD_RACE_TEST_ARGS="${args}" \
  bash "${check}" >"${out}" 2>&1 || fail "default package run failed"
[[ "$(cat "${args}")" == $'test\n-race\n-count=1\n-timeout=10m\n./...' ]] ||
  fail "default package run did not test the full repository"

echo "test_check_ci_race: ok"
