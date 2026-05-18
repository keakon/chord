#!/usr/bin/env bash
set -euo pipefail

# Run the same coverage test shape as CI, but also enforce that successful test
# output stays free of interactive prompts, runtime logs, and cold-cache download
# noise. Local runs intentionally keep the Go test cache enabled by default so
# iterative verification stays fast. For strict CI-equivalent verification, run
# CHORD_TEST_COUNT=1 scripts/check_ci_local.sh or set GITHUB_ACTIONS=true.
# Optional package args limit the run, e.g. scripts/check_ci_local.sh ./cmd/chord.

min_coverage="${MIN_COVERAGE:-70.0}"
coverage_file="${COVERAGE_FILE:-coverage.out}"
test_count="${CHORD_TEST_COUNT:-}"
if [[ -z "${test_count}" && ("${GITHUB_ACTIONS:-}" == "true" || "${CHORD_CI_STRICT:-}" == "1") ]]; then
  test_count="1"
fi
packages=("$@")
if [[ ${#packages[@]} -eq 0 ]]; then
  packages=(./...)
fi
stdout_file="$(mktemp)"
stderr_file="$(mktemp)"

cleanup() {
  rm -f "${stdout_file}" "${stderr_file}" /tmp/chord-ci-log-leaks.txt
}
trap cleanup EXIT

rm -f "${coverage_file}"

test_args=(-timeout=2m -coverprofile="${coverage_file}")
if [[ -n "${test_count}" ]]; then
  test_args=(-count="${test_count}" "${test_args[@]}")
fi

if ! CI="${CI:-true}" GITHUB_ACTIONS="${GITHUB_ACTIONS:-}" \
  go test "${test_args[@]}" "${packages[@]}" \
  >"${stdout_file}" 2>"${stderr_file}"; then
  echo "go test failed" >&2
  if [[ -s "${stderr_file}" ]]; then
    echo "--- go test stderr ---" >&2
    cat "${stderr_file}" >&2
  fi
  if [[ -s "${stdout_file}" ]]; then
    echo "--- go test stdout ---" >&2
    cat "${stdout_file}" >&2
  fi
  exit 1
fi

if [[ -s "${stderr_file}" ]]; then
  echo "unexpected stderr from successful go test:" >&2
  cat "${stderr_file}" >&2
  exit 1
fi

# Keep this list focused on output classes that should never appear in a clean
# successful CI test run. It intentionally includes generic golog line prefixes:
# package tests should capture expected logs explicitly instead of letting runtime
# logs leak to the job log.
leak_pattern='go: downloading|^\[[DIWE] [0-9]{4}-[0-9]{2}-[0-9]{2} |Device login URL|user_code:|Complete authorization|Login successful|Credentials written|client_fallback|client_retry|API key permission denied|terminal API error'
if grep -En "${leak_pattern}" "${stdout_file}" "${stderr_file}" >/tmp/chord-ci-log-leaks.txt; then
  echo "unexpected CI test output detected:" >&2
  cat /tmp/chord-ci-log-leaks.txt >&2
  exit 1
fi

cat "${stdout_file}"

cover_output="$(go tool cover -func="${coverage_file}")"
printf '%s\n' "${cover_output}"

total_coverage="$(printf '%s\n' "${cover_output}" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')"
if [[ -z "${total_coverage}" ]]; then
  echo "failed to parse total coverage" >&2
  exit 1
fi

awk -v total="${total_coverage}" -v min="${min_coverage}" 'BEGIN {
  if (total + 0 < min + 0) {
    printf("coverage check failed: total %.1f%% < required %.1f%%\n", total + 0, min + 0)
    exit 1
  }
  printf("coverage check passed: total %.1f%% >= required %.1f%%\n", total + 0, min + 0)
}'
