//go:build windows

package lsp

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isBrokenPipe matches the Windows errors for writing to a pipe whose reader
// is gone: ERROR_NO_DATA while the pipe is being closed, ERROR_BROKEN_PIPE once
// it is. Neither satisfies errors.Is(err, syscall.EPIPE) on Windows.
func isBrokenPipe(err error) bool {
	return errors.Is(err, windows.ERROR_NO_DATA) || errors.Is(err, windows.ERROR_BROKEN_PIPE)
}
