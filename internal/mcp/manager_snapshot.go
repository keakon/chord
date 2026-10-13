package mcp

import (
	"slices"

	"github.com/keakon/chord/internal/tools"
)

func (m *Manager) snapshotLocked() RuntimeSnapshot {
	result := RuntimeSnapshot{SchemaVersion: RuntimeSnapshotSchemaVersion, BindingID: m.bindingID, Version: m.version, Servers: []ServerView{}}
	for _, name := range m.serverNamesLocked() {
		e := m.entries[name]
		var aliases []string
		for _, other := range m.entries {
			if other.target.TargetID != "" && other.target.TargetID == e.target.TargetID && other.target.ProfileID == e.target.ProfileID {
				aliases = append(aliases, other.target.Ref.ServerName)
			}
		}
		slices.Sort(aliases)
		result.Servers = append(result.Servers, ServerView{Stages: e.report.Stages, Aliases: aliases, Ref: e.target.Ref, DesiredEnabled: m.desiredLocked(name), Auth: e.report.Auth, Operations: m.auth.refreshOperations(e.target), Connection: e.report.Connection, Catalog: e.report.Catalog, ScopeRequirements: e.report.ScopeRequirements, Diagnostic: e.report.Diagnostic})
	}
	return result.Clone()
}
func (m *Manager) Snapshot() RuntimeSnapshot {
	if m == nil {
		return RuntimeSnapshot{SchemaVersion: RuntimeSnapshotSchemaVersion, Servers: []ServerView{}}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotLocked()
}
func (m *Manager) SetOnChange(callback func(RuntimeSnapshot)) {
	m.mu.Lock()
	m.onChange = callback
	m.mu.Unlock()
}
func (m *Manager) publish() {
	m.mu.RLock()
	callback := m.onChange
	var snapshot RuntimeSnapshot
	if callback != nil {
		snapshot = m.snapshotLocked()
	}
	m.mu.RUnlock()
	if callback != nil {
		callback(snapshot)
	}
}

func (m *Manager) ServerEndpoints() []ServerEndpointStatus {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ServerEndpointStatus, 0, len(m.entries))
	for _, name := range m.serverNamesLocked() {
		e := m.entries[name]
		status := ServerEndpointStatus{Name: name, OK: e.report.Connection == ConnectionConnected, Pending: e.pending, Manual: e.target.Config.Manual, Disabled: e.target.Config.Manual && !m.desiredLocked(name), Attempt: e.attempt, MaxAttempts: defaultConnectAttempts}
		if e.report.Diagnostic.ErrorClass != "" && !e.pending {
			status.Error = string(e.report.Diagnostic.ErrorClass)
		}
		result = append(result, status)
	}
	return result
}

type serverToolSurface struct {
	name        string
	identity    HandleIdentity
	definitions []MCPToolDef
}

func (m *Manager) toolSurfaces() []serverToolSurface {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.toolSurfacesLocked()
}

func (m *Manager) toolSurfacesLocked() []serverToolSurface {
	var result []serverToolSurface
	for _, name := range m.serverNamesLocked() {
		e := m.entries[name]
		if e.client == nil || e.retired || e.report.Catalog != CatalogReady {
			continue
		}
		result = append(result, serverToolSurface{name: name, identity: HandleIdentity{ServerName: name, TargetID: e.target.TargetID, ProfileID: e.target.ProfileID, ExpectedIdentity: e.identity}, definitions: cloneToolDefinitions(e.definitions)})
	}
	return result
}

func (m *Manager) toolAvailable(identity HandleIdentity, name, hash string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[identity.ServerName]
	if m.closed || e == nil || e.retired || e.client == nil || e.report.Catalog != CatalogReady || !m.desiredLocked(identity.ServerName) {
		return false
	}
	if e.identity != identity.ExpectedIdentity || e.target.TargetID != identity.TargetID || e.target.ProfileID != identity.ProfileID || e.hashes[name] != hash || hash == "" {
		return false
	}
	return len(e.allowed) == 0 || slices.Contains(e.target.Config.AllowedTools, name)
}

// ToolSnapshot binds the exposed schemas and their status version to one
// publication, so request preparation cannot stamp an old surface as current.
func (m *Manager) ToolSnapshot() ([]tools.Tool, RuntimeSnapshot) {
	if m == nil {
		return nil, m.Snapshot()
	}
	m.mu.RLock()
	surfaces := m.toolSurfacesLocked()
	snapshot := m.snapshotLocked()
	catalog := m.catalog
	m.mu.RUnlock()
	var all []tools.Tool
	seen := map[string]bool{}
	for _, surface := range surfaces {
		for _, tool := range m.wrapSurface(surface, catalog) {
			if !seen[tool.Name()] {
				seen[tool.Name()] = true
				all = append(all, tool)
			}
		}
	}
	return all, snapshot
}
func (m *Manager) wrapSurface(surface serverToolSurface, catalog *Catalog) []tools.Tool {
	hashes := map[string]string{}
	for _, def := range surface.definitions {
		hashes[def.Name] = toolSchemaHash(def)
	}
	return wrapToolDefs(surface.name, surface.definitions, func(server, remote string) *ExecutionHandle {
		return &ExecutionHandle{mgr: m, catalog: catalog, serverName: server, remoteName: remote, identity: surface.identity, schemaHash: hashes[remote]}
	})
}
