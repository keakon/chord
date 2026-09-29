//go:build unix

package lsp

import (
	"errors"
	"syscall"
)

func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
