#!/usr/bin/env bash
set -euo pipefail

go_command="${CHORD_GO:-go}"
host_os="$("$go_command" env GOHOSTOS)"
host_arch="$("$go_command" env GOHOSTARCH)"

# Process-group supervision is Unix-only. Keep the existing direct entry point
# on other hosts, including TMP/TEMP for Go on Windows.
if [[ "$host_os" == windows || "$host_os" == plan9 ]]; then
	tmp="$(./scripts/test_tmpdir.sh)"
	TMPDIR="$tmp" TMP="$tmp" TEMP="$tmp" "$@"
	exit $?
fi

cache="$("$go_command" env GOCACHE)/chord-test-runner/$host_os-$host_arch"
mkdir -p "$cache"
GOOS="$host_os" GOARCH="$host_arch" "$go_command" build -o "$cache/test-runner" ./cmd/test-runner
exec "$cache/test-runner" "$@"
