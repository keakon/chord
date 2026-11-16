package main

import (
	"fmt"
	"os"
	"time"

	xwindows "github.com/charmbracelet/x/windows"
	"golang.org/x/sys/windows"
)

func waitSetupInput(file *os.File, timeout time.Duration) (bool, error) {
	handle := windows.Handle(file.Fd())
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		result, err := windows.WaitForSingleObject(handle, uint32((remaining+time.Millisecond-1)/time.Millisecond))
		if err != nil {
			return false, fmt.Errorf("wait for terminal input: %w", err)
		}
		if result == uint32(windows.WAIT_TIMEOUT) {
			return false, nil
		}
		var event xwindows.InputRecord
		var count uint32
		if err := xwindows.PeekConsoleInput(handle, &event, 1, &count); err != nil {
			return false, fmt.Errorf("peek terminal input: %w", err)
		}
		if count == 0 {
			continue
		}
		// Console handles also signal for key-up, focus and resize records.
		// These cannot satisfy the following byte read.
		if event.EventType == xwindows.KEY_EVENT && event.KeyEvent().KeyDown && event.KeyEvent().Char != 0 {
			return true, nil
		}
		if err := xwindows.ReadConsoleInput(handle, &event, 1, &count); err != nil {
			return false, fmt.Errorf("discard non-character terminal event: %w", err)
		}
	}
}
