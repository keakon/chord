//go:build !windows

package tools

import "syscall"

func removeFileOrSymlink(path string) error {
	return syscall.Unlink(path)
}
