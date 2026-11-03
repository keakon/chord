//go:build unix

package main

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/keakon/chord/internal/externalsupervisor"
)

// supervise keeps output witnesses alive after the runner is killed. The
// lifecycle supervisor proves group teardown; these pipes also detect children
// that escaped the group but still hold inherited stdout or stderr.
func supervise(args []string) int {
	if len(args) != 2 || !filepath.IsAbs(args[0]) || !externalsupervisor.ValidRunID(args[1]) {
		return 1
	}
	if _, err := syscall.Setsid(); err != nil {
		return 1
	}
	dir, id := args[0], args[1]
	done := make(chan error, 2)
	for _, fd := range []int{externalsupervisor.ChildStdoutFD, externalsupervisor.ChildStderrFD} {
		reader, err := witnessOutput(fd, done)
		if err != nil {
			return 1
		}
		defer reader.Close()
	}
	code := externalsupervisor.Run(nil)
	timer := time.NewTimer(cleanupGrace)
	defer timer.Stop()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				return 1
			}
		case <-timer.C:
			return 1
		}
	}
	receipt, err := externalsupervisor.LoadReceipt(dir, id)
	if err != nil || !receipt.Cleanup.Proven {
		return 1
	}
	// Publish last: a recovery process may remove the directory immediately
	// after this rename. No further operation may create files in it.
	tmp := filepath.Join(dir, readyFile+".tmp")
	if err := os.WriteFile(tmp, []byte(id), 0o600); err != nil {
		return 1
	}
	if err := os.Rename(tmp, filepath.Join(dir, readyFile)); err != nil {
		return 1
	}
	return code
}

func witnessOutput(fd int, done chan<- error) (*os.File, error) {
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(duplicate)
	dest := os.NewFile(uintptr(duplicate), "output")
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = dest.Close()
		return nil, err
	}
	if err := unix.Dup2(int(writer.Fd()), fd); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = dest.Close()
		return nil, err
	}
	_ = writer.Close()
	go func() {
		_, err := io.Copy(dest, reader)
		if err != nil {
			// The runner may have been killed. Still wait for every writer to
			// close rather than treating its broken output pipe as child EOF.
			_, err = io.Copy(io.Discard, reader)
		}
		_ = dest.Close()
		done <- err
	}()
	return reader, nil
}
