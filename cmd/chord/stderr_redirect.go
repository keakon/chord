package main

import (
	"bufio"
	"log/slog"
	"os"
	"strings"
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
	pipeRead  *os.File
	pipeWrite *os.File
	doneCh    chan struct{}
}

func redirectProcessStderr(logFile *os.File, logger *slog.Logger) (*stderrRedirect, error) {
	if logFile == nil || logger == nil {
		return nil, nil
	}
	dup, err := dupFD(os.Stderr.Fd())
	if err != nil {
		return nil, err
	}
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		_ = closeFD(dup)
		return nil, err
	}
	if err := dup2FD(pipeWrite.Fd(), os.Stderr.Fd()); err != nil {
		_ = pipeRead.Close()
		_ = pipeWrite.Close()
		_ = closeFD(dup)
		return nil, err
	}
	r := &stderrRedirect{
		dup:       dup,
		active:    true,
		writeFile: logFile,
		pipeRead:  pipeRead,
		pipeWrite: pipeWrite,
		doneCh:    make(chan struct{}),
	}
	go r.consume(logger)
	return r, nil
}

func (r *stderrRedirect) consume(logger *slog.Logger) {
	defer close(r.doneCh)
	reader := bufio.NewReader(r.pipeRead)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			r.logLine(logger, line)
		}
		if err != nil {
			return
		}
	}
}

func (r *stderrRedirect) logLine(logger *slog.Logger, line string) {
	if logger == nil {
		return
	}
	trimmed := strings.TrimRight(line, "\r\n")
	if trimmed == "" {
		return
	}
	logger.Warn("stderr", "stderr_text", trimmed)
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
	pipeRead := r.pipeRead
	pipeWrite := r.pipeWrite
	doneCh := r.doneCh
	r.pipeRead = nil
	r.pipeWrite = nil
	r.mu.Unlock()

	var restoreErr error
	if err := dup2FD(dup, os.Stderr.Fd()); err != nil {
		restoreErr = err
	}
	if pipeWrite != nil {
		if err := pipeWrite.Close(); err != nil && restoreErr == nil {
			restoreErr = err
		}
	}
	if doneCh != nil {
		<-doneCh
	}
	if pipeRead != nil {
		if err := pipeRead.Close(); err != nil && restoreErr == nil {
			restoreErr = err
		}
	}
	if err := closeFD(dup); err != nil && restoreErr == nil {
		restoreErr = err
	}
	return restoreErr
}
