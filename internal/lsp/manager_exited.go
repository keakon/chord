package lsp

import (
	"context"

	"github.com/keakon/golog/log"
)

// pruneExitedClientsForPath removes the clients owning path whose server
// process has died since it started and returns their server names. A dead
// client left registered would take every later didChange (failing it) and
// make each edit wait out the full diagnostics timeout; once removed, the
// next Start for the path relaunches the server like any cold start.
func (m *Manager) pruneExitedClientsForPath(ctx context.Context, path string) []string {
	if m == nil {
		return nil
	}
	path = normalizeWaiterPath(path)
	var (
		dead      []*Client
		names     []string
		survivors map[string][]*Client
	)
	m.clientsMu.Lock()
	m.forEachClientForPathLocked(path, func(key clientKey, c *Client) {
		if c.IsRunning() {
			return
		}
		delete(m.clients, key)
		dead = append(dead, c)
		names = append(names, key.name)
	})
	if len(dead) > 0 {
		// Diagnostics the dead instance published are dropped unless another
		// instance of the same server still serves the file.
		survivors = make(map[string][]*Client, len(names))
		for _, name := range names {
			survivors[name] = nil
		}
		for key, c := range m.clients {
			if _, ok := survivors[key.name]; ok {
				survivors[key.name] = append(survivors[key.name], c)
			}
		}
	}
	m.clientsMu.Unlock()
	if len(dead) == 0 {
		return nil
	}

	for _, c := range dead {
		if err := c.Close(ctx); err != nil {
			log.Debugf("lsp: close exited client name=%v error=%v", c.name, err)
		}
	}
	if cleared := m.dropOrphanedDiagnostics(survivors); m.broadcast != nil {
		for _, c := range cleared {
			m.broadcast(TypeLSPDiagnostics, DiagnosticsPayload{URI: c.uri, ServerID: c.server, Diagnostics: nil})
		}
	}
	m.notifySidebarChanged()
	return names
}
