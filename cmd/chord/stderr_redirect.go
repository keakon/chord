package main

import (
	"bufio"
	"os"
	"strings"
	"sync"

	"github.com/keakon/golog"
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
	logger    *golog.Logger
}

func redirectProcessStderr(logFile *os.File, logger *golog.Logger) (*stderrRedirect, error) {
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
		logger:    logger,
	}
	go r.consume()
	return r, nil
}

func (r *stderrRedirect) consume() {
	defer close(r.doneCh)
	reader := bufio.NewReader(r.pipeRead)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			r.logLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (r *stderrRedirect) logLine(line string) {
	trimmed := strings.TrimRight(line, "\r\n")
	if trimmed == "" {
		return
	}
	r.mu.Lock()
	logger := r.logger
	r.mu.Unlock()
	if logger == nil {
		return
	}
	logger.Warnf("stderr stderr_text=%v", trimmed)
}

func (r *stderrRedirect) SetLogger(logger *golog.Logger) {
	if r == nil || logger == nil {
		return
	}
	r.mu.Lock()
	r.logger = logger
	r.mu.Unlock()
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
