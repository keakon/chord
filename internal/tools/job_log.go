package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/privatefs"
)

// maxJobLogBytes caps a single job log file. It is a var so tests can shrink it
// instead of writing megabytes. It matches the in-memory output cap: past this
// size the disk file only needs to hold the most recent tail for diagnosis.
// Active plus rotated file bound each job to 2x this, so a registry holding
// maxJobs jobs can reach roughly 1 GB on disk.
var maxJobLogBytes int64 = maxOutputBytes

// jobLogRetention is how long a job log file is kept before the next job start
// prunes it. Logs are diagnostic, not durable artifacts.
const jobLogRetention = 7 * 24 * time.Hour

// rotatingJobLog is the on-disk sink for a job's stdout/stderr. It rotates to
// <path>.1 once the active file would exceed maxJobLogBytes, so a long-running
// job cannot grow its log without bound.
type rotatingJobLog struct {
	// mu guards the whole file state. stdout and stderr are tee'd through two
	// distinct MultiWriter instances, so os/exec runs two copier goroutines that
	// write concurrently; without this lock the size read-modify-write and the
	// rotation/close sequence race.
	mu         sync.Mutex
	sessionDir string
	path       string
	file       *os.File
	size       int64
	closed     bool
}

func openRotatingJobLog(sessionDir, path string) (*rotatingJobLog, error) {
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return nil, err
	}
	return &rotatingJobLog{sessionDir: sessionDir, path: path, file: f}, nil
}

func (l *rotatingJobLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Report the caller's full input as written in every path: io.MultiWriter
	// turns a short write into ErrShortWrite, which would drop the same bytes
	// from the in-memory window and kill the child mid-run.
	total := len(p)
	if l.closed {
		// Close happens after the process is reaped, so a write here is a
		// straggler from the teardown race. The diagnostic copy is expendable
		// but the tee also feeds the in-memory window, so swallowing keeps the
		// model's output intact instead of failing the copy.
		return total, nil
	}
	if l.file == nil {
		// A previous rotation could not reopen the log. The diagnostic copy is
		// expendable, but the tee also feeds the in-memory window, so report the
		// bytes as written instead of failing the tee and stalling the job.
		return total, nil
	}
	if int64(len(p)) > maxJobLogBytes {
		// A single write larger than the cap (a large pipe chunk) would bypass
		// the rotation check below and leave the file over the cap; keep only
		// its newest bytes, matching the in-memory window's tail policy.
		p = p[len(p)-int(maxJobLogBytes):]
	}
	if l.size > 0 && l.size+int64(len(p)) > maxJobLogBytes {
		l.rotate()
		if l.file == nil {
			return total, nil
		}
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	if err != nil {
		// The on-disk copy is diagnostic only: log the failure and keep going.
		log.Debugf("job log write %s: %v", l.path, err)
	}
	return total, nil
}

// rotate moves the active log aside and starts a fresh one. When the rename or
// reopen fails it falls back to appending to the existing file so the job keeps
// making progress instead of losing its output.
func (l *rotatingJobLog) rotate() {
	_ = l.file.Close()
	backup := l.path + ".1"
	_ = os.Remove(backup)
	if err := os.Rename(l.path, backup); err != nil {
		log.Debugf("job log rotate %s: %v", l.path, err)
		l.reopen(os.O_APPEND)
		return
	}
	l.reopen(os.O_CREATE | os.O_WRONLY | os.O_TRUNC)
}

func (l *rotatingJobLog) reopen(flag int) {
	f, err := privatefs.OpenFile(l.sessionDir, l.path, flag)
	if err != nil {
		log.Debugf("job log reopen %s: %v", l.path, err)
		l.file = nil
		return
	}
	l.file = f
	l.size = 0
	if flag&os.O_APPEND != 0 {
		if info, err := f.Stat(); err == nil {
			l.size = info.Size()
		}
	}
}

func (l *rotatingJobLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.file == nil {
		l.closed = true
		return nil
	}
	l.closed = true
	return l.file.Close()
}

// pruneJobLogs deletes job logs whose last write is older than jobLogRetention.
// It is best-effort: a missing directory or an individual removal failure is
// logged and otherwise ignored so starting a job never fails on cleanup.
func pruneJobLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Debugf("prune job logs: read %s: %v", dir, err)
		}
		return
	}
	cutoff := time.Now().Add(-jobLogRetention)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log.1") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			log.Debugf("prune job log %s: %v", name, err)
		}
	}
}
