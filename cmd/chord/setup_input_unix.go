//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func waitSetupInput(file *os.File, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	fds := []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		n, err := unix.Poll(fds, int((remaining+time.Millisecond-1)/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("wait for terminal input: %w", err)
		}
		return n > 0, nil
	}
}
