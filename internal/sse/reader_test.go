package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReaderFrames(t *testing.T) {
	for _, sep := range []string{"\n", "\r\n", "\r"} {
		for _, fragmented := range []bool{false, true} {
			mode := "buffered"
			if fragmented {
				mode = "fragmented"
			}
			t.Run(strings.NewReplacer("\r", "CR", "\n", "LF").Replace(sep)+"/"+mode, func(t *testing.T) {
				stream := "\xef\xbb\xbf" + strings.Join([]string{
					": keepalive", "event: discarded", "",
					"data: first", "data:  second", "event: sample", "id: 1", "retry: 10", "",
					"event: overwritten", "event", "data", "",
					"data:\xef\xbb\xbfthird", "", "",
				}, sep)
				var source io.Reader = strings.NewReader(stream)
				if fragmented {
					source = iotest.OneByteReader(source)
				}
				reader := NewReader(source, 128, nil)
				for i, want := range []struct {
					kind, data string
					lines      int64
				}{{"sample", "first\n second", 2}, {"", "", 1}, {"", "\xef\xbb\xbfthird", 1}} {
					event, err := reader.Next()
					if err != nil || event.Data == nil || event.Type != want.kind || string(event.Data) != want.data || event.DataLines != want.lines || event.DataBytes <= 0 {
						t.Fatalf("event %d = %+v, err=%v, want %+v", i, event, err, want)
					}
				}
				event, err := reader.Next()
				if err != io.EOF || event.Data != nil {
					t.Fatalf("end = %+v, %v", event, err)
				}
			})
		}
	}
}

func TestReaderIncompleteFrame(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fail       error
	}{
		{"empty", "", io.EOF},
		{"comment", ": ping\n", io.EOF},
		{"pending data", "data: sample\n", io.EOF},
		{"unterminated line", "data: sample", io.EOF},
		{"read failure", "data: sample\n", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var source io.Reader = strings.NewReader(tc.body)
			if tc.fail != io.EOF {
				source = io.MultiReader(source, errorReader{tc.fail})
			}
			reader := NewReader(source, 128, nil)
			event, err := reader.Next()
			if !errors.Is(err, tc.fail) {
				t.Fatalf("error = %v, want %v", err, tc.fail)
			}
			want := ""
			if strings.HasPrefix(tc.body, "data:") {
				want = "sample"
			}
			if string(event.Data) != want {
				t.Fatalf("tail = %q, want %q", event.Data, want)
			}
			event, err = reader.Next()
			if !errors.Is(err, tc.fail) || event.Data != nil {
				t.Fatalf("repeated end = %+v, %v", event, err)
			}
		})
	}
}

func TestReaderSizeLimits(t *testing.T) {
	for _, tc := range []struct {
		name, stream string
		max          int
		wantErr      bool
	}{
		{"line too long", "data: " + strings.Repeat("x", 128) + "\n\n", 64, true},
		{"event too long", strings.Repeat("data: 12345678\n", 8) + "\n", 64, true},
		{"bounded", "data: value\n\n", 64, false},
		{"unbounded", "data: " + strings.Repeat("x", 8192) + "\n\n", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := NewReader(strings.NewReader(tc.stream), tc.max, nil)
			_, err := reader.Next()
			if tc.wantErr {
				if !errors.Is(err, bufio.ErrTooLong) {
					t.Fatalf("error = %v, want size limit", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReaderExplicitTerminal(t *testing.T) {
	source := io.MultiReader(strings.NewReader("data: done\n"), errorReader{io.ErrUnexpectedEOF})
	reader := NewReader(source, 128, func(event Event) bool { return string(event.Data) == "done" })
	event, err := reader.Next()
	if err != nil || string(event.Data) != "done" {
		t.Fatalf("terminal event=%+v, err=%v", event, err)
	}
	_, err = reader.Next()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("next error=%v, want read failure", err)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
