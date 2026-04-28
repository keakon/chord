package tui

import (
	"bytes"
	"testing"
)

type fakeTerminalImageOut struct {
	bytes.Buffer
}

func (f *fakeTerminalImageOut) Close() error { return nil }
func (f *fakeTerminalImageOut) Fd() uintptr  { return 0 }

func TestTerminalImageOutputDeferredFlush(t *testing.T) {
	out := &fakeTerminalImageOut{}
	wrapped := WrapTerminalImageOutput(out)
	if wrapped == nil {
		t.Fatal("WrapTerminalImageOutput() returned nil")
	}

	seq := deferredCursorSequence(3, 5, "IMG")
	payload := []byte("hello" + encodeDeferredTerminalSequence(seq))
	if _, err := wrapped.Write(payload); err != nil {
		t.Fatalf("first Write() error = %v", err)
	}
	if got := out.String(); got != "hello" {
		t.Fatalf("after deferred write output = %q, want %q", got, "hello")
	}
	if _, err := wrapped.Write([]byte("!")); err != nil {
		t.Fatalf("flush Write() error = %v", err)
	}
	if got := out.String(); got != "hello!"+seq {
		t.Fatalf("after flush output = %q, want %q", got, "hello!"+seq)
	}
}
