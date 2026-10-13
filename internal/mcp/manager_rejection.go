package mcp

import "context"

// A rejection belongs to the original request's connection and credential
// revision. Reading its persisted effect happens outside the publication lock.
func (m *Manager) observeTransportRejection(ctx context.Context, ticket ConnectionTicket, stamp RequestStamp, failure error) {
	if m.auth == nil {
		return
	}
	target, ok := m.Target(ticket.Ref.ServerName)
	if !ok {
		return
	}
	view, err := m.auth.Inspect(ctx, target)
	if view.Version != stamp.CredentialVersion {
		return
	}
	m.mu.Lock()
	entry := m.entries[ticket.Ref.ServerName]
	if m.closed || entry == nil || entry.generation != ticket.ConnectionGeneration || entry.target.ConfigGeneration != ticket.ConfigGeneration || entry.identity.BindingID != stamp.BindingID || entry.identity.AuthGeneration != stamp.AuthGeneration {
		m.mu.Unlock()
		return
	}
	entry.report.Auth = view.Auth
	entry.report.ScopeRequirements = view.ScopeRequirements
	entry.report.Diagnostic = diagnoseAuth(failure)
	if err != nil {
		entry.report.Diagnostic = diagnoseAuth(err)
	}
	m.version++
	m.mu.Unlock()
	m.publish()
}
