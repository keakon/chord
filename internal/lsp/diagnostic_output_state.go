package lsp

import (
	"os"
	"path/filepath"
	"time"
)

// updateDiagnosticOutputStateLocked records one server's latest diagnostics,
// removes resolved identities from the session suppression set, and updates
// freshness. m.diagMu must be held.
func (m *Manager) updateDiagnosticOutputStateLocked(key clientKey, path string, diags []Diagnostic) {
	if m.publishedDiagByServer == nil {
		m.publishedDiagByServer = make(map[clientKey]map[string]map[diagnosticIdentity]struct{})
	}
	if m.diagState == nil {
		m.diagState = make(map[string]diagnosticPathState)
	}
	if m.reportedByPath == nil {
		m.reportedByPath = make(map[string]map[diagnosticIdentity]struct{})
	}
	byPath := m.publishedDiagByServer[key]
	if byPath == nil {
		byPath = make(map[string]map[diagnosticIdentity]struct{})
		m.publishedDiagByServer[key] = byPath
	}
	if len(diags) == 0 {
		delete(byPath, path)
		if len(byPath) == 0 {
			delete(m.publishedDiagByServer, key)
		}
	} else {
		current := make(map[diagnosticIdentity]struct{}, len(diags))
		for _, d := range diags {
			current[diagnosticIdentityKey(d)] = struct{}{}
		}
		byPath[path] = current
	}

	state, tracked := m.diagState[path]
	info, statErr := os.Stat(path)
	published := m.publishedDiagnosticIdentitiesLocked(path)
	if len(published) == 0 {
		delete(m.reportedByPath, path)
		if tracked && (state.stale || statErr != nil || !state.mtime.Equal(info.ModTime())) {
			// A clean publish does not prove freshness after an external
			// edit. Keep the path stale until Chord explicitly synchronizes
			// the current file contents.
			state.stale = true
			m.diagState[path] = state
		} else {
			delete(m.diagState, path)
		}
		return
	}
	if statErr != nil {
		state.stale = true
		m.diagState[path] = state
		return
	}
	if tracked && !state.mtime.Equal(info.ModTime()) {
		// A process outside Chord changed the file. Do not advance the
		// recorded mtime on an arbitrary publish: a queued old publish must
		// not make its stale snapshot appear fresh again. The suppression set
		// is deliberately kept — a problem the model was already told about
		// does not become news because the file was touched; only its
		// disappearance from the published set makes it reportable again.
		state.stale = true
		m.diagState[path] = state
		return
	}
	if reported := m.reportedByPath[path]; len(reported) > 0 {
		for identity := range reported {
			if _, exists := published[identity]; !exists {
				delete(reported, identity)
			}
		}
		if len(reported) == 0 {
			delete(m.reportedByPath, path)
		}
	}

	if !tracked {
		m.diagState[path] = diagnosticPathState{mtime: info.ModTime()}
		return
	}
	m.diagState[path] = state
}

// publishedDiagnosticIdentitiesLocked returns the union of all servers'
// current diagnostics for path. m.diagMu must be held.
func (m *Manager) publishedDiagnosticIdentitiesLocked(path string) map[diagnosticIdentity]struct{} {
	var out map[diagnosticIdentity]struct{}
	for _, byPath := range m.publishedDiagByServer {
		for identity := range byPath[path] {
			if out == nil {
				out = make(map[diagnosticIdentity]struct{})
			}
			out[identity] = struct{}{}
		}
	}
	return out
}

type diagnosticSyncToken struct {
	path       string
	mtime      time.Time
	generation uint64
}

// beginDiagnosticsSync marks path stale until AwaitFreshWaiter confirms a
// publish for this write. The generation prevents an older concurrent write
// from confirming a newer one.
func (m *Manager) beginDiagnosticsSync(path string) diagnosticSyncToken {
	if m == nil {
		return diagnosticSyncToken{}
	}
	path = normalizeWaiterPath(path)
	info, err := os.Stat(path)
	m.diagMu.Lock()
	state := m.diagState[path]
	state.generation++
	state.stale = true
	if err != nil {
		state.mtime = time.Time{}
	} else {
		state.mtime = info.ModTime()
	}
	m.diagState[path] = state
	m.diagMu.Unlock()
	return diagnosticSyncToken{path: path, mtime: state.mtime, generation: state.generation}
}

// confirmDiagnosticsSync makes a path eligible for future other-file output
// only when this write's fresh publish arrived and the file has not changed on
// disk since synchronization began.
func (m *Manager) confirmDiagnosticsSync(token diagnosticSyncToken) {
	if m == nil || token.path == "" {
		return
	}
	info, err := os.Stat(token.path)
	m.diagMu.Lock()
	state, ok := m.diagState[token.path]
	if !ok || state.generation != token.generation {
		m.diagMu.Unlock()
		return
	}
	if err == nil && token.mtime.Equal(info.ModTime()) {
		state.stale = false
		state.mtime = token.mtime
	} else {
		state.stale = true
	}
	m.diagState[token.path] = state
	m.diagMu.Unlock()
}

// ResetReportedDiagnostics starts a new session-scoped suppression window.
// Process-level freshness and published diagnostic state remain intact.
func (m *Manager) ResetReportedDiagnostics() {
	if m == nil {
		return
	}
	m.diagMu.Lock()
	m.reportedByPath = make(map[string]map[diagnosticIdentity]struct{})
	m.diagMu.Unlock()
}

// RestoreReportedDiagnostics replaces the suppression window with diagnostics
// that a restored transcript already showed. Resume must not re-announce them:
// the model has seen those lines in its own history, so only a later update to
// the file makes them worth reporting again.
func (m *Manager) RestoreReportedDiagnostics(reported map[string][]Diagnostic) {
	if m == nil {
		return
	}
	m.diagMu.Lock()
	defer m.diagMu.Unlock()
	m.reportedByPath = make(map[string]map[diagnosticIdentity]struct{}, len(reported))
	for path, diags := range reported {
		if len(diags) == 0 {
			continue
		}
		m.recordReportedLocked(normalizeWaiterPath(path), diags)
	}
}

// reserveOtherFileDiagnostics atomically selects and records diagnostics that
// have not yet been shown in this session. Holding diagMu across freshness
// checks and reservation prevents concurrent tool calls from selecting the
// same diagnostic.
func (m *Manager) reserveOtherFileDiagnostics(candidates []string, byPath map[string][]Diagnostic, primaryDirs map[string]struct{}, remaining int) (others []otherFileDiagnostics, omitted int) {
	if remaining <= 0 {
		return nil, 0
	}
	m.diagMu.Lock()
	defer m.diagMu.Unlock()
	for _, path := range candidates {
		if remaining <= 0 || len(others) >= ToolOutputMaxOtherErrorFiles {
			break
		}
		if _, sameDir := primaryDirs[filepath.Dir(path)]; !sameDir {
			continue
		}
		if !m.diagnosticsFreshLocked(path) {
			continue
		}
		reported := m.reportedByPath[path]
		published := m.publishedDiagnosticIdentitiesLocked(path)
		_, hasPublishState := m.diagState[path]
		available := make([]Diagnostic, 0, len(byPath[path]))
		for _, d := range deduplicateDiagnostics(byPath[path]) {
			identity := diagnosticIdentityKey(d)
			if hasPublishState {
				if _, stillPublished := published[identity]; !stillPublished {
					continue
				}
			}
			if _, exists := reported[identity]; !exists {
				available = append(available, d)
			}
		}
		selected := remainBudgetLimited(available, remaining)
		if len(selected) == 0 {
			continue
		}
		m.recordReportedLocked(path, selected)
		others = append(others, otherFileDiagnostics{path: path, diags: selected})
		remaining -= len(selected)
		omitted += len(available) - len(selected)
	}
	return others, omitted
}

// diagnosticsFreshLocked verifies that the file still has the mtime captured
// for its published diagnostics. m.diagMu must be held.
func (m *Manager) diagnosticsFreshLocked(path string) bool {
	state, tracked := m.diagState[path]
	if !tracked {
		// Some tests and direct backends seed client diagnostics without going
		// through publishDiagnostics; they have no freshness metadata.
		return true
	}
	info, err := os.Stat(path)
	if err != nil || state.stale || !state.mtime.Equal(info.ModTime()) {
		state.stale = true
		m.diagState[path] = state
		return false
	}
	return true
}

// recordReportedLocked records diagnostics that were actually rendered.
// m.diagMu must be held.
func (m *Manager) recordReportedLocked(path string, diags []Diagnostic) {
	if m.reportedByPath == nil {
		m.reportedByPath = make(map[string]map[diagnosticIdentity]struct{})
	}
	reported := m.reportedByPath[path]
	if reported == nil {
		reported = make(map[diagnosticIdentity]struct{}, len(diags))
		m.reportedByPath[path] = reported
	}
	for _, d := range diags {
		reported[diagnosticIdentityKey(d)] = struct{}{}
	}
}
