//go:build windows

package lsp

import (
	"errors"
	"io/fs"
	"testing"

	"golang.org/x/sys/windows"
)

// brokenPipeErrno is what a write to a server's stdio pipe returns once the
// server stopped reading.
const brokenPipeErrno = windows.ERROR_NO_DATA

func TestIsTransportFailureMatchesWindowsPipeErrors(t *testing.T) {
	for _, errno := range []windows.Errno{windows.ERROR_NO_DATA, windows.ERROR_BROKEN_PIPE} {
		if !isTransportFailure(&fs.PathError{Op: "write", Path: "|1", Err: errno}) {
			t.Fatalf("%v not classified as a transport failure", errno)
		}
	}
	if isTransportFailure(errors.New("request rejected")) {
		t.Fatal("protocol error classified as a transport failure")
	}
}
