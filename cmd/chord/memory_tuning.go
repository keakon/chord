package main

import (
	"os"
	"runtime/debug"
	"strings"
)

// defaultGCPercent is the GC target Chord applies when GOGC is not set in the
// environment.
//
// Go's default (100) lets a long-lived TUI session's heap grow to roughly twice
// its live set. Chord's live set is small relative to what it allocates while
// loading and rendering a session — per-block render state, per-message
// bookkeeping, config normalization — so that headroom, not the live data,
// accounts for most of the process footprint.
//
// Measured back to back on the same config and the same 202-message session,
// sampling phys_footprint (the figure Activity Monitor reports): 51.5 MB at the
// Go default versus 45.6 MB at 50, and 36.6 MB versus 34.4 MB for an
// empty-session start. Reclaiming instead of retargeting does not substitute
// for this — a forced collection plus scavenge recovers about 3 MB and creeps
// back within the minute.
//
// The tradeoff is more frequent but cheaper collections; GC work is
// proportional to the live heap, which here is single-digit megabytes.
const defaultGCPercent = 50

// applyDefaultGCPercent lowers the GC target for the process. It runs for every
// invocation, including the short-lived subcommands where the setting is
// irrelevant but harmless.
//
// Setting GOGC explicitly wins: debug.SetGCPercent overrides the environment
// variable, so an operator-supplied GOGC must short-circuit this. GOMEMLIMIT is
// left untouched — a soft memory limit stays the ceiling when one is configured.
func applyDefaultGCPercent() {
	if strings.TrimSpace(os.Getenv("GOGC")) != "" {
		return
	}
	debug.SetGCPercent(defaultGCPercent)
}
