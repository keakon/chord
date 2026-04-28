//go:build windows

package main

func dupFD(fd uintptr) (uintptr, error) {
	return fd, nil
}

func dup2FD(from, to uintptr) error {
	return nil
}

func closeFD(fd uintptr) error {
	return nil
}
