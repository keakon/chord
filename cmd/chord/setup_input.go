package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	uv "github.com/keakon/ultraviolet"
)

// readSetupEscape waits for one complete CSI/SS3 sequence. The deadline
// bounds the entire sequence, and non-sequence bytes remain for the next key.
func readSetupEscape(reader *bufio.Reader, wait func(time.Duration) (bool, error)) (string, bool, error) {
	deadline := time.Now().Add(uv.DefaultEscTimeout)
	sequence := []byte{0x1b}
	read := func() (byte, error) {
		if time.Now().After(deadline) {
			return 0, io.EOF
		}
		if reader.Buffered() == 0 && wait != nil {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, io.EOF
			}
			ready, err := wait(remaining)
			if err != nil {
				return 0, err
			}
			if !ready {
				return 0, io.EOF
			}
		}
		return reader.ReadByte()
	}
	introducer, err := read()
	if err == io.EOF {
		return string(sequence), true, nil
	}
	if err != nil {
		return string(sequence), false, err
	}
	if introducer != '[' && introducer != 'O' {
		_ = reader.UnreadByte()
		return string(sequence), true, nil
	}
	sequence = append(sequence, introducer)
	for {
		if len(sequence) >= 64 {
			return "", false, fmt.Errorf("terminal escape sequence exceeds input limit")
		}
		b, err := read()
		if err == io.EOF {
			return string(sequence), false, fmt.Errorf("read terminal escape sequence: %w", io.ErrUnexpectedEOF)
		}
		if err != nil {
			return string(sequence), false, err
		}
		if b >= 0x40 && b <= 0x7e {
			return string(append(sequence, b)), false, nil
		}
		if b < 0x20 || b > 0x3f {
			_ = reader.UnreadByte()
			return string(sequence), false, nil
		}
		sequence = append(sequence, b)
	}
}

// querySetupPasteMode preserves early user input while querying the terminal.
// A terminal that cannot report bracketed paste support uses line mode instead
// of guessing where a fragmented unmarked paste ends. unmarkedInput only
// describes input before the report, not bytes buffered after it.
func querySetupPasteMode(reader *bufio.Reader, out io.Writer, wait func(time.Duration) (bool, error)) (mode ansi.ModeSetting, unmarkedInput bool, err error) {
	if _, err := io.WriteString(out, ansi.RequestMode(ansi.ModeBracketedPaste)); err != nil {
		return ansi.ModeNotRecognized, false, fmt.Errorf("query terminal paste mode: %w", err)
	}
	var pending []byte
	defer func() {
		if len(pending) != 0 {
			old := *reader
			buffered, _ := old.Peek(old.Buffered())
			pending = append(pending, buffered...)
			_, _ = old.Discard(len(buffered))
			*reader = *bufio.NewReaderSize(io.MultiReader(bytes.NewReader(pending), &old), max(old.Size(), len(pending)))
			// Readiness checks must see replayed bytes before polling the fd.
			_, _ = reader.Peek(len(pending))
		}
	}()
	inPaste := false
	deadline := time.Now().Add(uv.DefaultEscTimeout)
	boundedWait := func(timeout time.Duration) (bool, error) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		if wait == nil {
			return true, nil
		}
		return wait(min(timeout, remaining))
	}
	for time.Now().Before(deadline) {
		if reader.Buffered() == 0 {
			ready, err := boundedWait(time.Until(deadline))
			if err != nil {
				return ansi.ModeNotRecognized, unmarkedInput, err
			}
			if !ready {
				break
			}
		}
		b, err := reader.ReadByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ansi.ModeNotRecognized, unmarkedInput, fmt.Errorf("read terminal paste mode: %w", err)
		}
		if b == 0x03 {
			return ansi.ModeNotRecognized, unmarkedInput, errSingleKeyInterrupt
		}
		if b != 0x1b {
			pending = append(pending, b)
			unmarkedInput = unmarkedInput || !inPaste
			continue
		}
		sequence, _, err := readSetupEscape(reader, boundedWait)
		if err != nil {
			pending = append(pending, sequence...)
			return ansi.ModeNotRecognized, unmarkedInput, err
		}
		for mode := ansi.ModeNotRecognized; mode <= ansi.ModePermanentlyReset; mode++ {
			if sequence == ansi.ReportMode(ansi.ModeBracketedPaste, mode) {
				// Input that arrived before paste mode was enabled has no
				// reliable boundary; leave it for the line-based reader.
				if mode == ansi.ModeReset && (len(pending) != 0 || reader.Buffered() != 0) {
					return ansi.ModeNotRecognized, unmarkedInput, nil
				}
				return mode, unmarkedInput, nil
			}
		}
		switch sequence {
		case ansi.BracketedPasteStart:
			inPaste = true
		case ansi.BracketedPasteEnd:
			unmarkedInput = unmarkedInput || !inPaste
			inPaste = false
		default:
			unmarkedInput = unmarkedInput || !inPaste
		}
		pending = append(pending, sequence...)
	}
	return ansi.ModeNotRecognized, unmarkedInput, nil
}

const setupPasteTimeout = 5 * time.Second

func readSetupChoicePaste(reader *bufio.Reader, wait func(time.Duration) (bool, error)) (byte, error) {
	deadline := time.Now().Add(setupPasteTimeout)
	end := ansi.BracketedPasteEnd
	matched := 0
	var candidate byte
	selected := false
	accept := func(b byte) {
		if !selected && !strings.ContainsRune(" \t\r\n", rune(b)) {
			candidate, selected = b, true
		}
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("read bracketed paste: %w", os.ErrDeadlineExceeded)
		}
		if reader.Buffered() == 0 && wait != nil {
			ready, err := wait(remaining)
			if err != nil {
				return 0, fmt.Errorf("read bracketed paste: %w", err)
			}
			if !ready {
				return 0, fmt.Errorf("read bracketed paste: %w", os.ErrDeadlineExceeded)
			}
		}
		b, err := reader.ReadByte()
		if err == io.EOF {
			return 0, fmt.Errorf("read bracketed paste: %w", io.ErrUnexpectedEOF)
		}
		if err != nil {
			return 0, fmt.Errorf("read bracketed paste: %w", err)
		}
		switch b {
		case 0x03:
			return 0, errSingleKeyInterrupt
		case 0x04:
			return 0, io.EOF
		}
		if b == end[matched] {
			matched++
			if matched == len(end) {
				return candidate, nil
			}
			continue
		}
		for i := 0; i < matched; i++ {
			accept(end[i])
		}
		matched = 0
		if b == end[0] {
			matched = 1
		} else {
			accept(b)
		}
	}
}

func normalizeSetupChoiceKey(b byte, allowed map[byte]struct{}) byte {
	if b >= 'A' && b <= 'Z' {
		if _, ok := allowed[b+'a'-'A']; ok {
			return b + 'a' - 'A'
		}
	}
	return b
}
