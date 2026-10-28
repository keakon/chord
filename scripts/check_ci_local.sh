#!/usr/bin/env bash
set -euo pipefail

# Run the same coverage test shape as CI, but also enforce that successful test
# output stays free of interactive prompts, runtime logs, and cold-cache download
# noise. Local runs intentionally keep the Go test cache enabled by default so
# iterative verification stays fast. For strict CI-equivalent verification, run
# CHORD_TEST_COUNT=1 scripts/check_ci_local.sh or set GITHUB_ACTIONS=true.
# Optional package args limit the run, e.g. scripts/check_ci_local.sh ./cmd/chord;
# a focused run skips the whole-program deadcode gate that full runs add locally.
# CI skips it too: the workflow gates every baseline in its own job.

min_coverage="${MIN_COVERAGE:-80.0}"
go_command="${CHORD_GO:-go}"
coverage_file="${COVERAGE_FILE:-coverage.out}"
test_count="${CHORD_TEST_COUNT:-}"
if [[ -z "${test_count}" && ("${GITHUB_ACTIONS:-}" == "true" || "${CHORD_CI_STRICT:-}" == "1") ]]; then
  test_count="1"
fi
packages=("$@")
run_deadcode_gate=1
if [[ ${#packages[@]} -eq 0 ]]; then
  packages=(./...)
else
  # A focused package set cannot evaluate whole-program reachability.
  run_deadcode_gate=0
fi
# The workflow gates all three baselines in its own job. Repeating the host
# platform pass here adds no coverage and turns one finding into two failed
# jobs, so CI relies on that job alone.
if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
  run_deadcode_gate=0
fi
stdout_file="$(mktemp)"
stderr_file="$(mktemp)"

cleanup() {
  rm -f "${stdout_file}" "${stderr_file}" /tmp/chord-ci-log-leaks.txt
}
trap cleanup EXIT

rm -f "${coverage_file}"

# Match the race gate: full lifecycle and recovery suites can exceed two minutes.
test_args=(-timeout=10m -coverprofile="${coverage_file}")
if [[ -n "${test_count}" ]]; then
  test_args=(-count="${test_count}" "${test_args[@]}")
fi

if ! CI="${CI:-true}" GITHUB_ACTIONS="${GITHUB_ACTIONS:-}" \
  GIT_AUTHOR_NAME="${GIT_AUTHOR_NAME:-Chord Test}" \
  GIT_AUTHOR_EMAIL="${GIT_AUTHOR_EMAIL:-chord-test@example.invalid}" \
  GIT_COMMITTER_NAME="${GIT_COMMITTER_NAME:-Chord Test}" \
  GIT_COMMITTER_EMAIL="${GIT_COMMITTER_EMAIL:-chord-test@example.invalid}" \
  "${go_command}" test "${test_args[@]}" "${packages[@]}" \
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
leak_pattern='go: downloading|^\[[DIWE] [0-9]{4}-[0-9]{2}-[0-9]{2} |Created worktree |Entered worktree |Device login URL|user_code:|Complete authorization|Login successful|Credentials written|client_fallback|client_retry|API key permission denied|terminal API error'
if grep -En "${leak_pattern}" "${stdout_file}" "${stderr_file}" >/tmp/chord-ci-log-leaks.txt; then
  echo "unexpected CI test output detected:" >&2
  cat /tmp/chord-ci-log-leaks.txt >&2
  exit 1
fi

# A successful run prints only the outcome and the coverage table below, which
# already lists every package with statements; failing runs above still print
# both streams in full.
echo "go test passed"

# Aggregate the profile by package: one line per package plus the total, both
# weighted by statement count. This matches how `go test -cover` reports a
# package, and it avoids the per-function table (`go tool cover -func`) that
# would add thousands of lines to the job log.
coverage_report="$(awk '
  NR == 1 { next }
  {
    file = $1
    sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", file)
    pkg = file
    sub(/[\/\\][^\/\\]+$/, "", pkg)
    stmts[pkg] += $2
    all += $2
    if ($3 + 0 > 0) {
      covered[pkg] += $2
      allCovered += $2
    }
  }
  END {
    if (all > 0) printf "total %.1f\n", 100 * allCovered / all
    for (pkg in stmts) printf "%.1f %s\n", 100 * covered[pkg] / stmts[pkg], pkg
  }' "${coverage_file}")"

total_coverage="$(printf '%s\n' "${coverage_report}" | awk '$1 == "total" { print $2 }')"
if [[ -z "${total_coverage}" ]]; then
  echo "failed to parse total coverage" >&2
  exit 1
fi

package_coverage="$(printf '%s\n' "${coverage_report}" | grep -v '^total ' | sort -n | awk '{ printf "%6.1f%%  %s\n", $1, $2 }')"

if awk -v total="${total_coverage}" -v min="${min_coverage}" 'BEGIN { exit !(total + 0 >= min + 0) }'; then
  echo "per-package coverage (lowest first):"
  printf '%s\n' "${package_coverage}"
  printf 'coverage check passed: total %s%% >= required %s%%\n' "${total_coverage}" "${min_coverage}"
else
  echo "per-package coverage (lowest first):" >&2
  printf '%s\n' "${package_coverage}" >&2
  echo "coverage check failed: total ${total_coverage}% < required ${min_coverage}%" >&2
  exit 1
fi

if [[ "${run_deadcode_gate}" -eq 1 ]]; then
  "$(dirname "${BASH_SOURCE[0]}")/check_deadcode.sh"
fi
