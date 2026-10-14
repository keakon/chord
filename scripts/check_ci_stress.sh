#!/usr/bin/env bash
set -euo pipefail

# The ordinary suite exercises takeover and concurrent writes with bounded
# repetition. This gate retains the full resource-leak/write workload under the
# race detector and the same output checks as the ordinary race gate.
export CHORD_TEST_STRESS=1
exec "$(dirname "${BASH_SOURCE[0]}")/check_ci_race.sh" \
  -run '^(TestTakeoverRepeatedSaveRestoreReclaimsRuntime|TestToolActivityConcurrentWithMessageWritesStress|TestThinkingTranslationsConcurrentSavesSameSessionStress)$' \
  "$@" ./cmd/chord ./internal/recovery
