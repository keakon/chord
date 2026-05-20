//go:build !windows

package tools

import "syscall"

func makeNamedPipeForTest(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
