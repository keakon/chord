package tools

import "time"

// Production kill and poll timings (killGracePeriod 3s, jobGroupPollInterval
// 1s) are calibrated for real interactive runs. Tests that don't assert on the
// durations would otherwise spend seconds of grace and poll waits per case, so
// the package-level test profile keeps those waits small. jobOutputDrainGrace
// keeps its production value: it only elapses when a writer escaped the job's
// process group, and shrinking it would let a collector delayed under -race
// load lose the tail of an ordinary job's output.
func init() {
	killGracePeriod = 100 * time.Millisecond
	jobGroupPollInterval = 20 * time.Millisecond
}
