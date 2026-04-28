package lsp

import (
	"path/filepath"
	"sort"
	"strings"
)

func (m *Manager) normalizeTrackedPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && m != nil && m.projectRoot != "" {
		path = filepath.Join(m.projectRoot, path)
	}
	return normalizeWaiterPath(path)
}

// MarkTouched records a file as part of the current session-scoped diagnostics set.
func (m *Manager) MarkTouched(path string) {
	if m == nil {
		return
	}
	path = m.normalizeTrackedPath(path)
	if path == "" {
		return
	}
	m.touchedMu.Lock()
	if m.touchedPaths == nil {
		m.touchedPaths = make(map[string]struct{})
	}
	_, existed := m.touchedPaths[path]
	m.touchedPaths[path] = struct{}{}
	m.touchedMu.Unlock()
	if !existed {
		m.notifySidebarChanged()
	}
}

// UnmarkTouched removes a file from the current session-scoped diagnostics set.
func (m *Manager) UnmarkTouched(path string) {
	if m == nil {
		return
	}
	path = m.normalizeTrackedPath(path)
	if path == "" {
		return
	}
	m.touchedMu.Lock()
	_, existed := m.touchedPaths[path]
	delete(m.touchedPaths, path)
	m.touchedMu.Unlock()
	if existed {
		m.notifySidebarChanged()
	}
}

// ResetTouched clears the current session-scoped diagnostics set.
func (m *Manager) ResetTouched() {
	if m == nil {
		return
	}
	m.touchedMu.Lock()
	changed := len(m.touchedPaths) > 0
	m.touchedPaths = make(map[string]struct{})
	m.touchedMu.Unlock()
	if changed {
		m.notifySidebarChanged()
	}
}

// TouchedPaths returns the current touched-file set as a sorted copy.
func (m *Manager) TouchedPaths() []string {
	if m == nil {
		return nil
	}
	m.touchedMu.RLock()
	defer m.touchedMu.RUnlock()
	if len(m.touchedPaths) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.touchedPaths))
	for path := range m.touchedPaths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) touchedSnapshot() map[string]struct{} {
	if m == nil {
		return nil
	}
	m.touchedMu.RLock()
	defer m.touchedMu.RUnlock()
	if len(m.touchedPaths) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(m.touchedPaths))
	for path := range m.touchedPaths {
		out[path] = struct{}{}
	}
	return out
}

// RebuildTouchedPaths replaces the touched-file set using relative or absolute
// file paths from persisted session history.
func (m *Manager) RebuildTouchedPaths(paths []string) {
	if m == nil {
		return
	}
	tracked := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		normalized := m.normalizeTrackedPath(path)
		if normalized == "" {
			continue
		}
		tracked[normalized] = struct{}{}
	}
	m.touchedMu.Lock()
	m.touchedPaths = tracked
	m.touchedMu.Unlock()
	m.notifySidebarChanged()
}
