#!/usr/bin/env bash
set -euo pipefail

go_command="${CHORD_GO:-go}"
if [[ "${CHORD_TEST_RUNNER_ACTIVE:-}" == 1 ]]; then
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
# os.Executable is used to launch the supervisor. A different worktree must
# never replace that path with a different build between those two launches.
build_id="$("$go_command" tool buildid "$build_dir/test-runner")"
build_id="${build_id//\//_}"
if [[ -z "$build_id" || "$build_id" == *[^a-zA-Z0-9_-]* ]]; then
	echo "test-runner: invalid executable build ID" >&2
	exit 1
fi
mkdir -p "$cache/$build_id"
if [[ ! -x "$cache/$build_id/test-runner" ]]; then
	mv "$build_dir/test-runner" "$cache/$build_id/test-runner"
fi
rm -rf "$build_dir"
trap - EXIT
exec "$cache/$build_id/test-runner" "$@"
