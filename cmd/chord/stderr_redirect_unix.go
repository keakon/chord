//go:build unix

package main

import "golang.org/x/sys/unix"

func dupFD(fd uintptr) (uintptr, error) {
	newFD, err := unix.Dup(int(fd))
	return uintptr(newFD), err
}

func dup2FD(from, to uintptr) error {
	return unix.Dup2(int(from), int(to))
}

func closeFD(fd uintptr) error {
	return unix.Close(int(fd))
}
