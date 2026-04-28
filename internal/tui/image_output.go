package tui

import (
	"bytes"
	"encoding/base64"
	"sync"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

const deferredImageSequencePrefix = "\x00CHORD_IMAGE:"

func encodeDeferredTerminalSequence(seq string) string {
	if seq == "" {
		return ""
	}
	return deferredImageSequencePrefix + base64.StdEncoding.EncodeToString([]byte(seq)) + "\x00"
}

func deferredCursorSequence(row, col int, seq string) string {
	if seq == "" {
		return ""
	}
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	const saveCursor = "\x1b7"
	const restoreCursor = "\x1b8"
	// Save/restore cursor so inline image protocols can draw at an arbitrary
	// location without disturbing Bubble Tea's cursor bookkeeping.
	return saveCursor + xansi.CursorPosition(col, row) + seq + restoreCursor
}

// WrapTerminalImageOutput wraps a terminal output file so image protocol escape
// sequences can be deferred until after Bubble Tea finishes rendering a frame.
func WrapTerminalImageOutput(out term.File) term.File {
	if out == nil {
		return nil
	}
	return &terminalImageOutput{out: out}
}

type terminalImageOutput struct {
	out term.File
	mu  sync.Mutex

	pending [][]byte
}

func (w *terminalImageOutput) Read(p []byte) (int, error) {
	return w.out.Read(p)
}

func (w *terminalImageOutput) Write(p []byte) (int, error) {
	direct, extracted := w.extractDeferredSequences(p)
	if len(direct) > 0 {
		if _, err := w.out.Write(direct); err != nil {
			return 0, err
		}
	}
	if !extracted {
		if err := w.flushPending(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *terminalImageOutput) Close() error {
	return nil
}

func (w *terminalImageOutput) Fd() uintptr {
	return w.out.Fd()
}

func (w *terminalImageOutput) extractDeferredSequences(p []byte) ([]byte, bool) {
	prefix := []byte(deferredImageSequencePrefix)
	if !bytes.Contains(p, prefix) {
		return p, false
	}
	var direct bytes.Buffer
	rest := p
	extracted := false
	for len(rest) > 0 {
		idx := bytes.Index(rest, prefix)
		if idx < 0 {
			direct.Write(rest)
			break
		}
		direct.Write(rest[:idx])
		rest = rest[idx+len(prefix):]
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			direct.Write(prefix)
			direct.Write(rest)
			break
		}
		payload, err := base64.StdEncoding.DecodeString(string(rest[:end]))
		if err != nil {
			direct.Write(prefix)
			direct.Write(rest[:end+1])
		} else {
			w.mu.Lock()
			w.pending = append(w.pending, payload)
			w.mu.Unlock()
			extracted = true
		}
		rest = rest[end+1:]
	}
	return direct.Bytes(), extracted
}

func (w *terminalImageOutput) flushPending() error {
	w.mu.Lock()
	pending := w.pending
	w.pending = nil
	w.mu.Unlock()
	for _, seq := range pending {
		if len(seq) == 0 {
			continue
		}
		if _, err := w.out.Write(seq); err != nil {
			return err
		}
	}
	return nil
}
