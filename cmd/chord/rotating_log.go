package main

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	defaultRuntimeLogMaxSize             int64 = 100 << 20 // 100 MiB
	defaultRuntimeLogMaxFiles                  = 3
	defaultRuntimeLogCheckEveryBytes     int64 = 256 << 10 // 256 KiB
	defaultRuntimeLogMaintenanceInterval       = time.Second
)

type rotatingLogOptions struct {
	MaxSize             int64
	MaxFiles            int
	CheckEveryBytes     int64
	MaintenanceInterval time.Duration
}

type rotatingLogFile struct {
	path                string
	lockPath            string
	maxSize             int64
	maxFiles            int
	checkEveryBytes     int64
	maintenanceInterval time.Duration

	mu              sync.Mutex
	file            *os.File
	bytesSinceCheck int64
	stderrRedirect  *stderrRedirect
	closed          bool
	stopCh          chan struct{}
	doneCh          chan struct{}
}

func defaultRotatingLogOptions() rotatingLogOptions {
	return rotatingLogOptions{
		MaxSize:             defaultRuntimeLogMaxSize,
		MaxFiles:            defaultRuntimeLogMaxFiles,
		CheckEveryBytes:     defaultRuntimeLogCheckEveryBytes,
		MaintenanceInterval: defaultRuntimeLogMaintenanceInterval,
	}
}

func newRotatingLogFile(path string) (*rotatingLogFile, error) {
	return newRotatingLogFileWithOptions(path, defaultRotatingLogOptions())
}

func newRotatingLogFileWithOptions(path string, opts rotatingLogOptions) (*rotatingLogFile, error) {
	if opts.MaxSize <= 0 {
		opts.MaxSize = defaultRuntimeLogMaxSize
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = defaultRuntimeLogMaxFiles
	}
	if opts.CheckEveryBytes <= 0 {
		opts.CheckEveryBytes = defaultRuntimeLogCheckEveryBytes
	}
	if opts.MaintenanceInterval <= 0 {
		opts.MaintenanceInterval = defaultRuntimeLogMaintenanceInterval
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := openRuntimeLogFile(path)
	if err != nil {
		return nil, err
	}
	w := &rotatingLogFile{
		path:                path,
		lockPath:            path + ".lock",
		maxSize:             opts.MaxSize,
		maxFiles:            opts.MaxFiles,
		checkEveryBytes:     opts.CheckEveryBytes,
		maintenanceInterval: opts.MaintenanceInterval,
		file:                f,
		stopCh:              make(chan struct{}),
		doneCh:              make(chan struct{}),
	}
	go w.maintenanceLoop()
	return w, nil
}

func openRuntimeLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func (w *rotatingLogFile) CurrentFile() *os.File {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file
}

func (w *rotatingLogFile) SetStderrRedirect(r *stderrRedirect) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stderrRedirect = r
}

func (w *rotatingLogFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, os.ErrClosed
	}
	if w.file == nil {
		if err := w.reopenLocked(); err != nil {
			w.mu.Unlock()
			return 0, err
		}
	}
	f := w.file
	n, err := f.Write(p)
	w.bytesSinceCheck += int64(n)
	needMaintain := w.bytesSinceCheck >= w.checkEveryBytes
	if needMaintain {
		w.bytesSinceCheck = 0
	}
	w.mu.Unlock()
	if needMaintain {
		_ = w.maybeMaintain()
	}
	return n, err
}

func (w *rotatingLogFile) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Sync()
}

func (w *rotatingLogFile) Close() error {
	close(w.stopCh)
	<-w.doneCh

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *rotatingLogFile) maintenanceLoop() {
	defer close(w.doneCh)
	ticker := time.NewTicker(w.maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = w.maybeMaintain()
		case <-w.stopCh:
			return
		}
	}
}

func (w *rotatingLogFile) maybeMaintain() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maybeMaintainLocked()
}

func (w *rotatingLogFile) maybeMaintainLocked() error {
	if w.closed {
		return nil
	}
	if w.file == nil {
		return w.reopenLocked()
	}

	pathInfo, err := os.Stat(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			return w.reopenLocked()
		}
		return err
	}
	currentInfo, err := w.file.Stat()
	if err != nil {
		return w.reopenLocked()
	}
	if !os.SameFile(currentInfo, pathInfo) {
		if err := w.reopenLocked(); err != nil {
			return err
		}
		pathInfo, err = os.Stat(w.path)
		if err != nil {
			return err
		}
	}
	if pathInfo.Size() < w.maxSize {
		return nil
	}

	lockFile, err := os.OpenFile(w.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	}()

	pathInfo, err = os.Stat(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			return w.reopenLocked()
		}
		return err
	}
	currentInfo, err = w.file.Stat()
	if err != nil {
		return w.reopenLocked()
	}
	if !os.SameFile(currentInfo, pathInfo) {
		if err := w.reopenLocked(); err != nil {
			return err
		}
		pathInfo, err = os.Stat(w.path)
		if err != nil {
			return err
		}
	}
	if pathInfo.Size() < w.maxSize {
		return nil
	}
	return w.rotateLocked()
}

func (w *rotatingLogFile) reopenLocked() error {
	newFile, err := openRuntimeLogFile(w.path)
	if err != nil {
		return err
	}
	oldFile := w.file
	w.file = newFile
	w.bytesSinceCheck = 0
	if w.stderrRedirect != nil {
		_ = w.stderrRedirect.Rebind(newFile)
	}
	if oldFile != nil {
		_ = oldFile.Close()
	}
	return nil
}

func (w *rotatingLogFile) rotateLocked() error {
	oldFile := w.file

	if w.maxFiles > 1 {
		oldest := w.rotatedPath(w.maxFiles - 1)
		if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
			return err
		}
		for i := w.maxFiles - 1; i >= 2; i-- {
			src := w.rotatedPath(i - 1)
			dst := w.rotatedPath(i)
			if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := os.Rename(w.path, w.rotatedPath(1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	newFile, err := openRuntimeLogFile(w.path)
	if err != nil {
		return err
	}
	w.file = newFile
	w.bytesSinceCheck = 0
	if w.stderrRedirect != nil {
		_ = w.stderrRedirect.Rebind(newFile)
	}
	if oldFile != nil {
		_ = oldFile.Close()
	}
	return nil
}

func (w *rotatingLogFile) rotatedPath(index int) string {
	return w.path + "." + strconv.Itoa(index)
}
