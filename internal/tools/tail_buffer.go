package tools

import (
	"fmt"
	"strings"
)

// TailBuffer keeps the most recent maxBytes written while counting every byte
// ever written. A reader that falls behind therefore still sees the newest
// output — where failures usually land — instead of a stale head, and the
// absolute cursors let it report how much it missed.
//
// It is not safe for concurrent use. The job registry wraps it in tailWriter,
// which adds the lock and the write signal; the local shell paths (the TUI's
// `!` command and the headless local shell) use it directly, because os/exec
// serializes writes when Stdout and Stderr are the same writer.
type TailBuffer struct {
	window   []byte
	start    int   // index of the oldest retained byte within window
	base     int64 // absolute offset of window[start] in the stream
	total    int64 // absolute count of bytes accepted
	maxBytes int64
}

// NewTailBuffer returns a buffer that retains the most recent maxBytes.
func NewTailBuffer(maxBytes int64) *TailBuffer {
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &TailBuffer{maxBytes: maxBytes}
}

func (c *TailBuffer) Write(p []byte) (int, error) {
	if int64(len(p)) >= c.maxBytes {
		// This single write fills the whole window: keep only its last bytes.
		c.window = append(c.window[:0], p[len(p)-int(c.maxBytes):]...)
		c.start = 0
		c.base = c.total + int64(len(p)) - c.maxBytes
	} else {
		c.window = append(c.window, p...)
		if extra := int64(len(c.window)-c.start) - c.maxBytes; extra > 0 {
			// Drop the oldest bytes by moving the window start on instead of
			// memmoving the whole window on every write. The abandoned prefix is
			// reclaimed once it reaches half the window, which bounds the live
			// length to 1.5x the window while keeping the slide O(1) amortized
			// rather than O(window) per write. That bound is on len, not cap:
			// append growth can leave the backing array at a larger historical
			// peak, so a caller's steady-state memory may exceed 1.5x.
			c.start += int(extra)
			c.base += extra
			if c.start >= int(c.maxBytes)/2 {
				kept := copy(c.window, c.window[c.start:])
				c.window = c.window[:kept]
				c.start = 0
			}
		}
	}
	c.total += int64(len(p))
	return len(p), nil
}

// hasDataAfter reports whether output beyond the absolute cursor is retained.
func (c *TailBuffer) hasDataAfter(cursor int64) bool { return c.total > cursor }

// readFrom returns the output after the absolute cursor, the cursor to pass to
// the next read, and how many bytes the reader missed because they had already
// been dropped from the window.
func (c *TailBuffer) readFrom(cursor int64) (string, int64, int64) {
	var dropped int64
	if cursor < c.base {
		dropped = c.base - cursor
		cursor = c.base
	}
	if cursor > c.total {
		cursor = c.total
	}
	return string(c.window[c.start+int(cursor-c.base):]), c.total, dropped
}

// tail returns up to the last maxLen bytes of retained output, how many earlier
// bytes were dropped from the window, and whether the excerpt was cut.
func (c *TailBuffer) tail(maxLen int) (string, int64, bool) {
	retained := c.window[c.start:]
	truncated := maxLen > 0 && len(retained) > maxLen
	if truncated {
		retained = retained[len(retained)-maxLen:]
	}
	return string(retained), c.base, truncated
}

// raw returns the retained output with no truncation notice attached. Logic
// that classifies output — runtime-failure classification, build-failure
// sniffing — must read this rather than String: a model-facing notice is
// decoration and must never feed output-sniffing.
func (c *TailBuffer) raw() string { return string(c.window[c.start:]) }

// String returns the retained output, prefixed with a truncation notice when
// earlier bytes were dropped.
func (c *TailBuffer) String() string {
	if c.base == 0 {
		return string(c.window[c.start:])
	}
	var builder strings.Builder
	// The fixed text plus two decimal counters stays below this reserve for
	// practical output sizes; reserving it avoids a second growth copy while
	// still letting the builder handle unusually large counters safely.
	builder.Grow(len(c.window) - c.start + 64)
	fmt.Fprintf(&builder, "...(output truncated: showing the most recent %d of %d bytes)\n", len(c.window)-c.start, c.total)
	builder.Write(c.window[c.start:])
	return builder.String()
}
