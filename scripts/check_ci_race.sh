#!/usr/bin/env bash
set -euo pipefail

# Run the same race-detector test shape as CI, while capturing output and
# enforcing the same no-log-leak contract as the coverage parity script.
# Optional package args limit the run, e.g. scripts/check_ci_race.sh ./cmd/chord.

packages=("$@")
if [[ ${#packages[@]} -eq 0 ]]; then
  packages=(./...)
fi
stdout_file="$(mktemp)"
stderr_file="$(mktemp)"

cleanup() {
  rm -f "${stdout_file}" "${stderr_file}" /tmp/chord-ci-race-log-leaks.txt
}
trap cleanup EXIT

if ! CI="${CI:-true}" GITHUB_ACTIONS="${GITHUB_ACTIONS:-}" \
  go test -race -count=1 -timeout=10m "${packages[@]}" \
  >"${stdout_file}" 2>"${stderr_file}"; then
  echo "go test -race failed" >&2
  if [[ -s "${stderr_file}" ]]; then
    echo "--- go test -race stderr ---" >&2
    cat "${stderr_file}" >&2
  fi
  if [[ -s "${stdout_file}" ]]; then
    echo "--- go test -race stdout ---" >&2
    cat "${stdout_file}" >&2
  fi
  exit 1
fi

if [[ -s "${stderr_file}" ]]; then
  echo "unexpected stderr from successful go test -race:" >&2
  cat "${stderr_file}" >&2
  exit 1
fi

leak_pattern='go: downloading|^\[[DIWE] [0-9]{4}-[0-9]{2}-[0-9]{2} |Created worktree |Entered worktree |Device login URL|user_code:|Complete authorization|Login successful|Credentials written|client_fallback|client_retry|API key permission denied|terminal API error'
if grep -En "${leak_pattern}" "${stdout_file}" "${stderr_file}" >/tmp/chord-ci-race-log-leaks.txt; then
  echo "unexpected CI race test output detected:" >&2
  cat /tmp/chord-ci-race-log-leaks.txt >&2
  exit 1
fi

cat "${stdout_file}"
