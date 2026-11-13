#!/usr/bin/env bash
set -euo pipefail

go_command="${CHORD_GO:-go}"
cache_command=""
case "${1:-}" in
	--cache-status | --cache-trim)
		cache_command="$1"
		shift
		;;
esac
if [[ "${CHORD_TEST_RUNNER_ACTIVE:-}" == 1 && -z "${cache_command}" ]]; then
	if [[ "${1:-}" == --default-temp ]]; then shift; fi
	exec "$@"
fi
go_flags="$("$go_command" env GOFLAGS)"
export GOFLAGS="$go_flags -trimpath"
host_os="$("$go_command" env GOHOSTOS)"
host_arch="$("$go_command" env GOHOSTARCH)"

# Process-group supervision is Unix-only. Keep the existing direct entry point
# on other hosts, including TMP/TEMP for Go on Windows.
if [[ "$host_os" == windows || "$host_os" == plan9 ]]; then
	if [[ -n "${cache_command}" ]]; then
		echo "cache maintenance is supported on Unix hosts only" >&2
		exit 2
	fi
	export CHORD_TEST_RUNNER_ACTIVE=1
	if [[ "${1:-}" == --default-temp ]]; then
		shift
		"$@"
	else
		tmp="$(./scripts/test_tmpdir.sh)"
		TMPDIR="$tmp" TMP="$tmp" TEMP="$tmp" "$@"
	fi
	exit $?
fi

cache="$("$go_command" env GOCACHE)/chord-test-runner/$host_os-$host_arch"
mkdir -p "$cache"
build_dir="$(mktemp -d "$cache/.build.XXXXXX")"
trap 'rm -rf "$build_dir"' EXIT
GOOS="$host_os" GOARCH="$host_arch" "$go_command" build -o "$build_dir/test-runner" ./cmd/test-runner
build_id="$("$go_command" tool buildid "$build_dir/test-runner")"
build_id="${build_id//\//_}"
if [[ -z "$build_id" || "$build_id" == *[^a-zA-Z0-9_-]* ]]; then
	echo "test-runner: invalid executable build ID" >&2
	exit 1
fi
if [[ -n "${cache_command}" ]]; then
	if [[ $# -gt 0 ]]; then
		echo "usage: scripts/run_tests.sh ${cache_command}" >&2
		exit 2
	fi
	run_base="$(./scripts/test_tmpdir.sh --existing)"
	if [[ -z "${run_base}" ]]; then
		run_base="${TMPDIR:-/tmp}"
	fi
	exec "$build_dir/test-runner" --cache-runner "$cache" "$build_id" "$cache_command" "$run_base"
fi
exec "$build_dir/test-runner" --cache-runner "$cache" "$build_id" "$@"
