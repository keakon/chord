package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// toolCallSignature captures the identity of a tool invocation for comparison.
type toolCallSignature struct {
	name string
	args string // raw JSON string for equality check
}

// RepetitionDetector prevents tool call loops by using two complementary strategies:
//
//  1. Consecutive-repetition guard: rejects a call if the exact same (name, args)
//     has been seen consecutively maxRepeat times in a row.
//
//  2. Sliding-window loop guard: maintains a fixed-size window of recent call
//     signatures (SHA-256 hashes). If the same signature appears more than
//     windowMaxHits times within the window, the call is rejected. This catches
//     A→B→A→B alternating loops that the consecutive guard misses.
type RepetitionDetector struct {
	// Consecutive guard state.
	maxRepeat int
	lastCall  *toolCallSignature
	repeatN   int

	// Sliding-window guard state.
	window        []string // circular buffer of SHA-256 hashes
	windowPos     int      // next write position in window
	windowSize    int      // capacity of the window
	windowMaxHits int      // max allowed occurrences of one hash in the window
}

const (
	defaultWindowSize    = 10
	defaultWindowMaxHits = 5
)

// NewRepetitionDetector creates a detector that rejects the 3rd consecutive
// identical tool call (i.e., allows at most 2 in a row), and also rejects any
// tool call whose SHA-256 signature appears more than 5 times in the last 10
// calls (sliding-window loop detection).
func NewRepetitionDetector() *RepetitionDetector {
	return &RepetitionDetector{
		maxRepeat:     3,
		window:        make([]string, defaultWindowSize),
		windowSize:    defaultWindowSize,
		windowMaxHits: defaultWindowMaxHits,
	}
}

// NewRepetitionDetectorWithMax creates a detector with a custom consecutive
// maximum. The sliding-window parameters use defaults.
func NewRepetitionDetectorWithMax(max int) *RepetitionDetector {
	if max < 1 {
		max = 1
	}
	return &RepetitionDetector{
		maxRepeat:     max,
		window:        make([]string, defaultWindowSize),
		windowSize:    defaultWindowSize,
		windowMaxHits: defaultWindowMaxHits,
	}
}

// NewRepetitionDetectorFull creates a detector with fully custom parameters.
//   - maxRepeat:     max consecutive identical calls allowed before rejection.
//   - windowSize:    number of recent calls tracked in the sliding window.
//   - windowMaxHits: max occurrences of one signature within the window before rejection.
func NewRepetitionDetectorFull(maxRepeat, windowSize, windowMaxHits int) *RepetitionDetector {
	if maxRepeat < 1 {
		maxRepeat = 1
	}
	if windowSize < 1 {
		windowSize = 1
	}
	if windowMaxHits < 1 {
		windowMaxHits = 1
	}
	return &RepetitionDetector{
		maxRepeat:     maxRepeat,
		window:        make([]string, windowSize),
		windowSize:    windowSize,
		windowMaxHits: windowMaxHits,
	}
}

// hashCall returns a hex-encoded SHA-256 hash of the (name, args) pair.
func hashCall(name string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{'\x00'})
	h.Write(args)
	return hex.EncodeToString(h.Sum(nil))
}

// Check returns true if the tool call is allowed to proceed, false if it
// should be rejected due to detected looping.
//
// Two rejection conditions:
//  1. The same (name, args) pair has appeared consecutively maxRepeat times.
//  2. The SHA-256 hash of this call appears windowMaxHits or more times
//     within the sliding window of the last windowSize calls.
func (d *RepetitionDetector) Check(name string, args json.RawMessage) bool {
	sig := toolCallSignature{
		name: name,
		args: string(args),
	}

	// --- Consecutive guard ---
	if d.lastCall != nil && *d.lastCall == sig {
		d.repeatN++
	} else {
		d.lastCall = &sig
		d.repeatN = 1
	}
	if d.repeatN >= d.maxRepeat {
		return false
	}

	// --- Sliding-window guard ---
	h := hashCall(name, args)
	// Write into circular buffer.
	d.window[d.windowPos] = h
	d.windowPos = (d.windowPos + 1) % d.windowSize

	// Count occurrences of h in the window.
	hits := 0
	for _, v := range d.window {
		if v != "" && v == h {
			hits++
		}
	}
	return hits <= d.windowMaxHits
}

// Reset clears all state, as if the detector were newly created.
func (d *RepetitionDetector) Reset() {
	d.lastCall = nil
	d.repeatN = 0
	for i := range d.window {
		d.window[i] = ""
	}
	d.windowPos = 0
}
