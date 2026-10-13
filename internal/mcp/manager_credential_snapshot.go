package mcp

import (
	"context"
	"slices"
)

// RefreshCredentialSnapshot projects local credential facts without connecting,
// refreshing tokens, or requesting authorization. All aliases in this manager
// are inspected so a changed shared slot becomes visible together.
func (m *Manager) RefreshCredentialSnapshot(ctx context.Context) {
	if m == nil || m.auth == nil {
		return
	}
	m.mu.RLock()
	targets := make([]Target, 0, len(m.entries))
	for _, entry := range m.entries {
		targets = append(targets, entry.target.Snapshot())
	}
	m.mu.RUnlock()
	changed := false
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		view, err := m.auth.Inspect(ctx, target)
		m.mu.Lock()
		entry := m.entries[serverConfigName(target.Config)]
		if !m.closed && entry != nil && entry.target.ConfigGeneration == target.ConfigGeneration && entry.target.TargetID == target.TargetID && entry.target.ProfileID == target.ProfileID {
			diagnostic := entry.report.Diagnostic
			if err != nil {
				diagnostic = diagnoseAuth(err)
			} else if view.Diagnostic.ErrorClass != "" {
				diagnostic = view.Diagnostic
			}
			if entry.report.Auth != view.Auth || !slices.EqualFunc(entry.report.ScopeRequirements, view.ScopeRequirements, scopeViewEqual) || entry.report.Diagnostic != diagnostic {
				entry.report.Auth = view.Auth
				entry.report.ScopeRequirements = view.ScopeRequirements
				entry.report.Diagnostic = diagnostic
				m.version++
				changed = true
			}
		}
		m.mu.Unlock()
	}
	if changed {
		m.publish()
	}
}
