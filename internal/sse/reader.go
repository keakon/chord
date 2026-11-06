// Package sse decodes fields and event boundaries without interpreting payloads.
package sse

import (
	"bufio"
	"bytes"
	"io"
)

// Event contains one frame. Data remains valid until the next call to Next.
type Event struct {
	Type      string
	Data      []byte
	DataBytes int64
	DataLines int64
}

// Reader assembles SSE events, leaving protocol-specific completion to callers.
type Reader struct {
	scanner  *bufio.Scanner
	maxBytes int
	terminal func(Event) bool
	first    bool
	data     []byte
	err      error
}

// NewReader bounds both physical lines and assembled data with maxBytes.
// Zero leaves size unbounded. terminal may recognize a protocol's explicit
// end marker before a blank line arrives; nil always waits for a full frame.
func NewReader(r io.Reader, maxBytes int, terminal func(Event) bool) *Reader {
	scanner := bufio.NewScanner(r)
	limit := maxBytes
	if limit == 0 {
		limit = int(^uint(0) >> 1)
	}
	scanner.Buffer(make([]byte, 0, min(4096, limit)), limit)
	skipLF := false
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		start := 0
		if skipLF && len(data) > 0 && data[0] == '\n' {
			start = 1
		}
		if i := bytes.IndexAny(data[start:], "\r\n"); i >= 0 {
			i += start
			skipLF = data[i] == '\r'
			return i + 1, data[start:i], nil
		}
		if atEOF {
			if len(data) > start {
				return len(data), data[start:], nil
			}
			return len(data), nil, nil
		}
		return 0, nil, nil
	})
	return &Reader{scanner: scanner, maxBytes: maxBytes, terminal: terminal, first: true}
}

// Next returns a complete event, or pending data together with EOF/read error.
// A non-nil error never certifies an event boundary. Further calls return the
// same error and no data, so callers may process an EOF tail without losing the
// underlying read failure.
func (r *Reader) Next() (Event, error) {
	if r.err != nil {
		return Event{}, r.err
	}
	r.data = r.data[:0]
	var event Event
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if r.first {
			line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
			r.first = false
		}
		if len(line) == 0 {
			if event.DataLines > 0 {
				event.Data = r.data
				return event, nil
			}
			event.Type = ""
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			event.Type = string(value)
		case "data":
			size := len(r.data) + len(value)
			if event.DataLines > 0 {
				size++
			}
			if r.maxBytes > 0 && size > r.maxBytes {
				r.err = bufio.ErrTooLong
				return Event{}, r.err
			}
			if event.DataLines > 0 {
				r.data = append(r.data, '\n')
			}
			r.data = append(r.data, value...)
			// Empty data fields still distinguish a frame from no event.
			if r.data == nil {
				r.data = []byte{}
			}
			event.DataLines++
			event.DataBytes += int64(len(line) + 1)
			event.Data = r.data
			if r.terminal != nil && r.terminal(event) {
				return event, nil
			}
		}
	}
	r.err = r.scanner.Err()
	if r.err == nil {
		r.err = io.EOF
	}
	event.Data = r.data
	if event.DataLines == 0 {
		event.Data = nil
	}
	return event, r.err
}
