//go:build unix

package runtimecache

import (
	"errors"
	"syscall"
)

func isWouldBlockLock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
