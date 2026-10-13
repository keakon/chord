package mcp

import (
	"context"
	"errors"
	"slices"
)

type credentialObservation struct {
	Version          CredentialVersion
	Auth             AuthState
	Class            ErrorClass
	BlockID          string
	ConfigGeneration uint64
}

// ReconcileCredentials runs before request preparation, including an already
// built surface. Failure stamps suppress repeated connection attempts until a
// credential fact changes or the user explicitly reconnects the server.
func (m *Manager) ReconcileCredentials(ctx context.Context) (bool, error) {
	if m == nil || m.auth == nil {
		return false, nil
	}
	m.mu.Lock()
	if m.reconcileGate == nil {
		m.reconcileGate = make(chan struct{}, 1)
	}
	gate := m.reconcileGate
	m.mu.Unlock()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	m.mu.RLock()
	targets := make([]Target, 0, len(m.entries))
	for _, entry := range m.entries {
		targets = append(targets, entry.target.Snapshot())
	}
	m.mu.RUnlock()
	slices.SortFunc(targets, func(a, b Target) int {
		if a.Ref.ServerName < b.Ref.ServerName {
			return -1
		}
		if a.Ref.ServerName > b.Ref.ServerName {
			return 1
		}
		return 0
	})
	changed := false
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		if !managedAuth(target) {
			continue
		}
		view, inspectErr := m.auth.Inspect(ctx, target)
		stamp := credentialObservation{Version: view.Version, Auth: view.Auth, Class: view.Diagnostic.ErrorClass, ConfigGeneration: target.ConfigGeneration}
		if view.Blocked != nil {
			stamp.BlockID = view.Blocked.BlockID
		}
		name := serverConfigName(target.Config)
		m.mu.Lock()
		entry := m.entries[name]
		if entry == nil || entry.target.ConfigGeneration != target.ConfigGeneration || m.closed {
			m.mu.Unlock()
			continue
		}
		reportChanged := entry.report.Auth != view.Auth || !slices.EqualFunc(entry.report.ScopeRequirements, view.ScopeRequirements, scopeViewEqual)
		entry.report.Auth = view.Auth
		entry.report.ScopeRequirements = view.ScopeRequirements
		if inspectErr != nil {
			entry.report.Diagnostic = diagnoseAuth(inspectErr)
		}
		if reportChanged {
			m.version++
		}
		enabled := m.desiredLocked(name)
		connected := entry.client != nil
		identity := entry.identity
		if m.reconcileAttempts == nil {
			m.reconcileAttempts = map[string]credentialObservation{}
		}
		previous, attempted := m.reconcileAttempts[name]
		identityChanged := identity.BindingID != view.Version.BindingID || identity.AuthGeneration != view.Version.AuthGeneration
		ready := inspectErr == nil && (view.Auth == AuthReady || view.Auth == AuthRefreshRequired)
		shouldConnect := enabled && ready && (!connected || identityChanged) && (!attempted || previous != stamp)
		if shouldConnect {
			m.reconcileAttempts[name] = stamp
		}
		m.mu.Unlock()
		if reportChanged {
			m.publish()
		}
		credentialInvalid := inspectErr != nil || view.Auth == AuthReauthRequired || view.Auth == AuthError || (view.Auth == AuthMissing && identity.BindingID != "")
		if connected && (identityChanged || credentialInvalid) {
			m.RetireConnection(name, RetireLogout)
			changed = true
			connected = false
		}
		if shouldConnect {
			// ConnectOne owns publication tickets; unrelated service completions cannot
			// replace this server's result or expose a partial tools/list directory.
			err := m.ConnectOne(ctx, target)
			if errors.Is(err, context.Canceled) {
				return changed, err
			}
			changed = true
		} else if connected && enabled && view.Auth == AuthRefreshRequired {
			_, err := m.auth.Acquire(ctx, target, identity)
			if errors.Is(err, context.Canceled) {
				return changed, err
			}
			if err != nil {
				m.mu.Lock()
				if e := m.entries[name]; e != nil && e.identity == identity {
					e.report.Diagnostic = diagnoseAuth(err)
					m.version++
				}
				m.mu.Unlock()
				m.publish()
			}
		}
	}
	return changed, nil
}

func scopeViewEqual(a, b ScopeRequirementView) bool {
	return a.BindingID == b.BindingID && a.TokenRevision == b.TokenRevision && a.RemoteTool == b.RemoteTool && a.Stage == b.Stage && a.ErrorClass == b.ErrorClass && slices.Equal(a.Scopes, b.Scopes)
}
