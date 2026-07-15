package llm

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/keakon/golog/log"
)

const authStateMonitorPollInterval = time.Minute

type authStateMonitor struct {
	path     string
	onChange func()
	stopCh   chan struct{}
	doneCh   chan struct{}
	once     sync.Once
}

func newAuthStateMonitor(path string, onChange func()) *authStateMonitor {
	path = strings.TrimSpace(path)
	if path == "" || onChange == nil {
		return nil
	}
	return &authStateMonitor{
		path:     path,
		onChange: onChange,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (m *authStateMonitor) start() {
	if m == nil {
		return
	}
	go m.run()
}

func (m *authStateMonitor) stop() {
	if m == nil {
		return
	}
	m.once.Do(func() { close(m.stopCh) })
}

func (m *authStateMonitor) wait() {
	if m != nil {
		<-m.doneCh
	}
}

func (m *authStateMonitor) run() {
	defer close(m.doneCh)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Debugf("auth state fs watcher unavailable path=%v error=%v", m.path, err)
		m.runPolling()
		return
	}
	defer watcher.Close()

	dir := filepath.Dir(m.path)
	if err := watcher.Add(dir); err != nil {
		log.Debugf("auth state fs watch unavailable path=%v dir=%v error=%v", m.path, dir, err)
		m.runPolling()
		return
	}

	ticker := time.NewTicker(authStateMonitorPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				m.runPolling()
				return
			}
			if filepath.Clean(event.Name) == filepath.Clean(m.path) && event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				m.onChange()
			}
		case err, ok := <-watcher.Errors:
			if ok {
				log.Debugf("auth state fs watch error path=%v error=%v", m.path, err)
			}
		case <-ticker.C:
			m.onChange()
		}
	}
}

func (m *authStateMonitor) runPolling() {
	ticker := time.NewTicker(authStateMonitorPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.onChange()
		}
	}
}
