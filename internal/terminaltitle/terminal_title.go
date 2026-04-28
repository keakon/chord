// Package terminaltitle provides helpers for setting the terminal tab/window
// title via OSC escape sequences. It handles sanitization (stripping control
// characters, collapsing whitespace) so that callers can pass untrusted text
// such as user messages safely.
package terminaltitle

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
)

// Practical upper bound on title length, measured in runes.
// Most terminals silently truncate titles beyond a few hundred characters.
// 30 leaves headroom for the spinner and framing while keeping titles
// readable in tab bars.
const MaxTitleRunes = 30

// Braille-pattern dot-spinner frames for the terminal title animation.
// These render smoothly in most modern terminals.
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// DefaultTitle is the title shown when no user message has been submitted.
const DefaultTitle = "chord"

// ---------------------------------------------------------------------------
// Low-level OSC write
// ---------------------------------------------------------------------------

// SetWindowTitle writes an OSC 0 (set title + icon name) sequence to w.
// The title is sanitized before emission. If sanitization removes all visible
// content, the function returns without writing anything.
func SetWindowTitle(w io.Writer, title string) error {
	clean := sanitizeTitle(title)
	if clean == "" {
		return nil
	}
	return writeWindowTitle(w, clean)
}

// SetWindowTitleWithPrefix writes an OSC 0 title with an optional sanitized
// prefix. The prefix is sanitized without collapsing or trimming spaces so the
// caller can keep a fixed-width placeholder (for example, toggling between
// "❓" and " ") without causing the visible title text to shift.
func SetWindowTitleWithPrefix(w io.Writer, title string, prefix string) error {
	cleanTitle := sanitizeTitle(title)
	if cleanTitle == "" {
		return nil
	}
	cleanPrefix := sanitizeTitlePrefix(prefix)
	if cleanPrefix == "" {
		return writeWindowTitle(w, cleanTitle)
	}
	return writeWindowTitle(w, cleanPrefix+" "+cleanTitle)
}

// SetWindowTitleWithSpinner writes an OSC 0 title with an optional spinner
// prefix. When spinner is empty/nil, only the title is emitted.
func SetWindowTitleWithSpinner(w io.Writer, title string, spinner string) error {
	return SetWindowTitleWithPrefix(w, title, spinner)
}

func writeWindowTitle(w io.Writer, clean string) error {
	// OSC 0: set both icon name and window title; terminated by ST (ESC \).
	_, err := fmt.Fprintf(w, "\x1b]0;%s\x1b\\", clean)
	return err
}

// ---------------------------------------------------------------------------
// Sanitization
// ---------------------------------------------------------------------------

// sanitizeTitle normalizes untrusted title text into a single bounded line.
// It removes control characters, strips invisible formatting characters,
// collapses whitespace runs to single spaces, and truncates after
// MaxTitleRunes emitted runes.
func sanitizeTitle(title string) string {
	var b strings.Builder
	runesWritten := 0
	pendingSpace := false

	for _, ch := range title {
		if unicode.IsSpace(ch) {
			if b.Len() > 0 {
				pendingSpace = true
			}
			continue
		}

		if isDisallowedTitleChar(ch) {
			continue
		}

		if pendingSpace {
			remaining := MaxTitleRunes - runesWritten
			if remaining > 1 {
				b.WriteByte(' ')
				runesWritten++
				pendingSpace = false
			}
		}

		if runesWritten >= MaxTitleRunes {
			break
		}

		b.WriteRune(ch)
		runesWritten++
	}

	return b.String()
}

// sanitizeTitlePrefix normalizes an optional title prefix while preserving
// caller-provided spacing. Unlike sanitizeTitle it does not trim or collapse
// whitespace, so a single-space placeholder can still occupy visible width.
func sanitizeTitlePrefix(prefix string) string {
	var b strings.Builder
	for _, ch := range prefix {
		if ch == '\n' || ch == '\r' || ch == '\t' {
			continue
		}
		if isDisallowedTitleChar(ch) {
			continue
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// isDisallowedTitleChar reports whether ch should be dropped from output.
// This includes control characters and a curated set of invisible/bidi
// formatting codepoints.
func isDisallowedTitleChar(ch rune) bool {
	if ch < 0x20 {
		// ASCII control characters (except we handle whitespace separately).
		return true
	}
	if ch == 0x7F {
		return true // DEL
	}
	// Strip Trojan-Source bidi controls and common non-rendering characters.
	switch ch {
	case
		'\u00AD', // soft hyphen
		'\u034F', // combining grapheme joiner
		'\u061C', // Arabic letter mark
		'\u180E', // Mongolian vowel separator
		'\u200B', // zero-width space
		'\u200C', // zero-width non-joiner
		'\u200D', // zero-width joiner
		'\u200E', // left-to-right mark
		'\u200F', // right-to-left mark
		'\u202A', // left-to-right embedding
		'\u202B', // right-to-left embedding
		'\u202C', // pop directional formatting
		'\u202D', // left-to-right override
		'\u202E', // right-to-left override
		'\u2060', // word joiner
		'\uFEFF', // zero-width no-break space
		'\uFFF9', // interlinear annotation anchor
		'\uFFFA', // interlinear annotation separator
		'\uFFFB': // interlinear annotation terminator
		return true
	}
	// C1 controls and other ranges.
	if ch >= 0x80 && ch <= 0x9F {
		return true
	}
	if ch >= 0x2000 && ch <= 0x206F {
		// General Punctuation (includes bidi controls, formatting chars).
		// We already checked specific ones above; drop the rest of the
		// zero-width / invisible subset.
		switch ch {
		case '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005',
			'\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u2028',
			'\u2029', '\u202F', '\u205F', '\u2061', '\u2062', '\u2063',
			'\u2064', '\u206A', '\u206B', '\u206C', '\u206D', '\u206E',
			'\u206F':
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Spinner helpers
// ---------------------------------------------------------------------------

var spinnerMu sync.Mutex
var spinnerFrameIndex int

// NextSpinnerFrame returns the next Braille spinner frame (cycles through 10).
// It is safe for concurrent use.
func NextSpinnerFrame() string {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	frame := SpinnerFrames[spinnerFrameIndex%len(SpinnerFrames)]
	spinnerFrameIndex++
	return frame
}

// ResetSpinner resets the spinner frame index to 0.
func ResetSpinner() {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	spinnerFrameIndex = 0
}

// ---------------------------------------------------------------------------
// Stdout convenience
// ---------------------------------------------------------------------------

// IsTerminal returns true if stdout is a terminal (supports OSC sequences).
func IsTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
