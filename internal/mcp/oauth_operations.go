package mcp

import "context"

// Refresh observers are runtime-owned. Callbacks never run under AuthService.mu
// or a credential transaction, so status publication cannot invert lock order.
func (s *AuthService) subscribeRefresh(fn func(Target, bool)) func() {
	s.mu.Lock()
	if s.refreshObservers == nil {
		s.refreshObservers = make(map[uint64]func(Target, bool))
	}
	s.refreshObserverSeq++
	id := s.refreshObserverSeq
	s.refreshObservers[id] = fn
	s.mu.Unlock()
	return func() { s.mu.Lock(); delete(s.refreshObservers, id); s.mu.Unlock() }
}
func (s *AuthService) notifyRefresh(target Target, finished bool) {
	s.mu.Lock()
	callbacks := make([]func(Target, bool), 0, len(s.refreshObservers))
	for _, fn := range s.refreshObservers {
		callbacks = append(callbacks, fn)
	}
	s.mu.Unlock()
	for _, fn := range callbacks {
		fn(target, finished)
	}
}
func (s *AuthService) refreshOperations(target Target) []OperationView {
	if s == nil {
		return []OperationView{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if flight := s.flights[targetKey(target)]; flight != nil {
		return []OperationView{flight.operation}
	}
	return []OperationView{}
}
func (m *Manager) refreshOperationChanged(target Target, finished bool) {
	m.mu.RLock()
	relevant := false
	for _, entry := range m.entries {
		if entry.target.TargetID == target.TargetID && entry.target.ProfileID == target.ProfileID {
			relevant = true
			break
		}
	}
	closed := m.closed
	m.mu.RUnlock()
	if !relevant || closed {
		return
	}
	if finished {
		m.RefreshCredentialSnapshot(context.Background())
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.version++
	m.mu.Unlock()
	m.publish()
}
