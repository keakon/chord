//go:build windows

package runtimecache

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isWouldBlockLock(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
