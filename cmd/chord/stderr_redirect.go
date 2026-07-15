package main

import (
	"os"
	"sync"
)

func writeStartupStderrNotice(logPath string, err error) {
	if err == nil || logPath == "" {
		return
	}
	_ = appendFile(logPath, []byte("startup stderr redirect unavailable: "+err.Error()+"\n"), 0o600)
}

func appendFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

type stderrRedirect struct {
	mu        sync.Mutex
	dup       uintptr
	active    bool
	writeFile *os.File
}

func redirectProcessStderr(logFile *os.File) (*stderrRedirect, error) {
	if logFile == nil {
		return nil, nil
	}
	dup, err := dupFD(os.Stderr.Fd())
	if err != nil {
		return nil, err
	}
	if err := dup2FD(logFile.Fd(), os.Stderr.Fd()); err != nil {
		_ = closeFD(dup)
		return nil, err
	}
	r := &stderrRedirect{
		dup:       dup,
		active:    true,
		writeFile: logFile,
	}
	return r, nil
}

func (r *stderrRedirect) Rebind(logFile *os.File) error {
	if r == nil || logFile == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return nil
	}
	if err := dup2FD(logFile.Fd(), os.Stderr.Fd()); err != nil {
		return err
	}
	r.writeFile = logFile
	return nil
}

func (r *stderrRedirect) Restore() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !r.active {
		r.mu.Unlock()
		return nil
	}
	r.active = false
	dup := r.dup
	r.mu.Unlock()

	var restoreErr error
	if err := dup2FD(dup, os.Stderr.Fd()); err != nil {
		restoreErr = err
	}
	if err := closeFD(dup); err != nil && restoreErr == nil {
		restoreErr = err
	}
	return restoreErr
}
