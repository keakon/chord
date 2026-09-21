package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// stdoutRedirect owns file descriptor 1 for ACP mode: everything the process
// prints to stdout goes to the log file, while the protocol writes to the
// saved duplicate of the original stdout. Chord's own logging never uses
// stdout, but third-party libraries can, and a stray byte there would corrupt
// the JSON-RPC stream. The fd-level approach mirrors redirectProcessStderr.
type stdoutRedirect struct {
	mu     sync.Mutex
	active bool
	// protocol owns the saved stdout descriptor: fd 1 is restored from it and
	// it is the only owner that closes it, so the guard can never close a
	// descriptor the runtime has reused since.
	protocol *os.File
	// writeFile is where fd 1 points right now. ownsFile records whether this
	// guard opened it: the pre-runtime log file is ours to close, while the
	// file a Rebind adopted belongs to the runtime log writer.
	writeFile *os.File
	ownsFile  bool
}

// redirectProcessStdout redirects fd 1 to logPath and returns a *os.File for
// the original stdout. The caller writes the protocol stream to the returned
// file; the redirect closes that stream when it releases fd 1 on Close.
func redirectProcessStdout(logPath string) (*os.File, *stdoutRedirect, error) {
	if runtime.GOOS == "windows" {
		// dupFD/dup2FD are no-ops on Windows, so fd 1 cannot be moved out of the
		// protocol stream: refuse rather than risk corrupting JSON-RPC.
		return nil, nil, errors.New("stdout redirect is unavailable on Windows")
	}
	if logPath == "" {
		return nil, nil, errors.New("stdout redirect requires a log path")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	dup, err := dupFD(os.Stdout.Fd())
	if err != nil {
		logFile.Close()
		return nil, nil, fmt.Errorf("duplicate stdout: %w", err)
	}
	protocol := os.NewFile(dup, "acp-stdout")
	if protocol == nil {
		_ = closeFD(dup)
		logFile.Close()
		return nil, nil, errors.New("open saved stdout stream")
	}
	if err := dup2FD(logFile.Fd(), os.Stdout.Fd()); err != nil {
		_ = protocol.Close()
		logFile.Close()
		return nil, nil, fmt.Errorf("redirect stdout: %w", err)
	}
	return protocol, &stdoutRedirect{protocol: protocol, active: true, writeFile: logFile, ownsFile: true}, nil
}

// Rebind points fd 1 at the log file the runtime is actually writing to, so
// stdout keeps following log rotation for the rest of the process lifetime.
// The adopted file belongs to the runtime log writer, so the guard stops owning
// it and releases the file it opened itself.
func (r *stdoutRedirect) Rebind(logFile *os.File) error {
	if r == nil || logFile == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return nil
	}
	if err := dup2FD(logFile.Fd(), os.Stdout.Fd()); err != nil {
		return err
	}
	if r.ownsFile && r.writeFile != nil {
		_ = r.writeFile.Close()
	}
	r.writeFile = logFile
	r.ownsFile = false
	return nil
}

// Close restores the original stdout on fd 1 and releases the saved stream.
func (r *stdoutRedirect) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !r.active {
		r.mu.Unlock()
		return nil
	}
	r.active = false
	protocol := r.protocol
	writeFile := r.writeFile
	ownsFile := r.ownsFile
	r.protocol = nil
	r.writeFile = nil
	r.mu.Unlock()

	var errs []error
	// Only close a file this guard opened; one adopted from the runtime log
	// writer is closed by the writer itself, and closing it twice would only
	// produce a bogus failure on the shutdown path.
	if ownsFile && writeFile != nil {
		if err := writeFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	// Restore fd 1 from the saved duplicate before releasing it. The duplicate
	// belongs to the protocol stream, so closing the stream is the only close:
	// a raw close of the descriptor here would race the stream's own close and
	// could hit a descriptor the runtime has reused since.
	if protocol != nil {
		if err := dup2FD(protocol.Fd(), os.Stdout.Fd()); err != nil {
			errs = append(errs, err)
		}
		if err := protocol.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
