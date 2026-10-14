package lsp

import (
	"sort"
)

// SidebarEntries keeps connection and review state separate for each root.
// It only reads in-memory snapshots and never discovers roots or starts clients.
func (m *Manager) SidebarEntries() []SidebarServerEntry {
	if m == nil || m.cfg == nil || len(m.cfg.LSP) == 0 {
		return nil
	}
	rows := make(map[clientKey]SidebarServerEntry)
	m.clientsMu.RLock()
	for key := range m.clients {
		rows[key] = SidebarServerEntry{Name: key.name, OK: true}
	}
	for key := range m.starting {
		if _, ok := rows[key]; !ok {
			rows[key] = SidebarServerEntry{Name: key.name, Pending: true}
		}
	}
	m.clientsMu.RUnlock()
	m.startFailMu.Lock()
	for key, msg := range m.startFail {
		if _, ok := rows[key]; !ok {
			if len(msg) > 120 {
				msg = truncateBytesAtRune(msg, 117, "...")
			}
			rows[key] = SidebarServerEntry{Name: key.name, Error: msg}
		}
	}
	m.startFailMu.Unlock()
	counts := make(map[string]int)
	for key := range rows {
		counts[key.name]++
	}
	for name, cfg := range m.cfg.LSP {
		if !cfg.Disabled && counts[name] == 0 {
			rows[clientKey{name: name}] = SidebarServerEntry{Name: name, Pending: true}
		}
	}
	touched := m.touchedSnapshot()
	m.diagMu.RLock()
	for name, paths := range m.reviewByServer {
		for path, review := range paths {
			if _, ok := touched[path]; !ok {
				continue
			}
			var owner clientKey
			for key, row := range rows {
				if key.name == name && row.OK && pathUnderDir(path, key.root) && len(key.root) > len(owner.root) {
					owner = key
				}
			}
			if owner.root != "" {
				row := rows[owner]
				row.Errors += review.errors
				row.Warnings += review.warnings
				rows[owner] = row
			}
		}
	}
	m.diagMu.RUnlock()
	var out []SidebarServerEntry
	for key, row := range rows {
		cfg, ok := m.cfg.LSP[key.name]
		if !ok || cfg.Disabled {
			continue
		}
		if key.root != "" && (counts[key.name] > 1 || key.root != m.projectRootPath()) {
			row.Root = key.root
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Root < out[j].Root
	})
	return out
}
