package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStdoutWriterCloseRejectsEmitAfterClose(t *testing.T) {
	var buf bytes.Buffer
	out := newStdoutWriter(context.Background(), &buf)
	go out.run()
	if !out.emit(headlessEnvelope{Type: "ready"}) {
		t.Fatal("initial emit returned false")
	}
	if !out.closeUntil(nil) {
		t.Fatal("closeUntil returned false")
	}
	if out.emit(headlessEnvelope{Type: "late"}) {
		t.Fatal("emit after close returned true")
	}
	envs := decodeHeadlessJSONLines(t, buf.Bytes())
	if len(envs) != 1 || envs[0].Type != "ready" {
		t.Fatalf("envs = %+v, want one ready envelope", envs)
	}
}

type headlessErrorWriter struct{}

func (headlessErrorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStdoutWriterReportsWriteError(t *testing.T) {
	out := newStdoutWriter(context.Background(), headlessErrorWriter{})
	go out.run()
	if !out.emit(headlessEnvelope{Type: "ready"}) {
		t.Fatal("emit returned false before writer processed the message")
	}
	select {
	case err := <-out.errors():
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("writer error = %v, want closed pipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not report the write error")
	}
	if !out.closeUntil(time.After(time.Second)) {
		t.Fatal("closeUntil timed out after writer error")
	}
}

type headlessBlockedWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w headlessBlockedWriter) Write(data []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(data), nil
}

func TestStdoutWriterCloseDeadlineBoundsBlockedWrite(t *testing.T) {
	w := headlessBlockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	out := newStdoutWriter(context.Background(), w)
	go out.run()
	t.Cleanup(func() {
		close(w.release)
		<-out.done
	})
	if !out.emit(headlessEnvelope{Type: "ready"}) {
		t.Fatal("initial emit returned false")
	}
	<-w.started
	if out.closeUntil(time.After(20 * time.Millisecond)) {
		t.Fatal("close succeeded while the underlying write was blocked")
	}
	if out.emit(headlessEnvelope{Type: "late"}) {
		t.Fatal("writer accepted output after close timed out")
	}
}

func TestStdoutWriterReportsJSONEncodingFailure(t *testing.T) {
	var buf bytes.Buffer
	out := newStdoutWriter(context.Background(), &buf)
	go out.run()
	if !out.emit(headlessEnvelope{Type: "invalid", Payload: make(chan struct{})}) {
		t.Fatal("initial emit returned false")
	}
	select {
	case err := <-out.errors():
		if _, ok := errors.AsType[*json.UnsupportedTypeError](err); !ok {
			t.Fatalf("writer error = %v, want unsupported JSON type", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not report JSON encoding failure")
	}
	if !out.closeUntil(time.After(time.Second)) {
		t.Fatal("writer failed to stop after encoding failure")
	}
}

func TestStdoutWriterRejectsOversizedEnvelope(t *testing.T) {
	var buf bytes.Buffer
	out := newStdoutWriter(context.Background(), &buf)
	go out.run()
	if !out.emit(headlessEnvelope{Type: "large", Payload: strings.Repeat("x", headlessMaxEnvelopeBytes)}) {
		t.Fatal("emit returned false before writer processed the message")
	}
	select {
	case err := <-out.errors():
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("writer error = %v, want encoded-size error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not report oversized envelope")
	}
}
