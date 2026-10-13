package agent

import (
	"crypto/rand"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/permission"
)

// MCPStatusChangedEvent contains only the safe runtime view. Local authorization
// URLs use a different event type and are never part of this projection.
type MCPStatusChangedEvent struct {
	Snapshot  mcp.RuntimeSnapshot
	Role      string
	SessionID string
}

func (MCPStatusChangedEvent) agentEvent() {}

type mcpStatusState struct {
	publishMu     sync.Mutex
	mu            sync.Mutex
	provider      func() mcp.RuntimeSnapshot
	session, role string
	views         map[string]mcp.RuntimeSnapshot
}

// SetMCPStatusSnapshotFunc installs the main Manager's non-blocking snapshot
// provider. Private managers are collected independently by owner.
func (a *MainAgent) SetMCPStatusSnapshotFunc(provider func() mcp.RuntimeSnapshot) {
	a.mcpStatus.mu.Lock()
	a.mcpStatus.provider = provider
	a.mcpStatus.mu.Unlock()
	a.RefreshMCPStatusSnapshot()
}

func (a *MainAgent) MCPRuntimeSnapshot() mcp.RuntimeSnapshot {
	a.mcpStatus.mu.Lock()
	defer a.mcpStatus.mu.Unlock()
	if view, ok := a.mcpStatus.views[a.mcpStatus.role]; ok {
		return view.Clone()
	}
	return mcp.RuntimeSnapshot{SchemaVersion: mcp.RuntimeSnapshotSchemaVersion, Servers: []mcp.ServerView{}}
}
func (a *MainAgent) MCPRuntimeSnapshotForAgent(name string) (mcp.RuntimeSnapshot, error) {
	a.mcpStatus.mu.Lock()
	defer a.mcpStatus.mu.Unlock()
	if view, ok := a.mcpStatus.views[name]; ok {
		return view.Clone(), nil
	}
	return mcp.RuntimeSnapshot{}, fmt.Errorf("unknown MCP agent %q", name)
}

// RefreshMCPStatusSnapshot serializes sampling and publication so a slower
// observer cannot overwrite a newer projection. No network or disk IO occurs.
func (a *MainAgent) RefreshMCPStatusSnapshot() {
	a.mcpStatus.publishMu.Lock()
	defer a.mcpStatus.publishMu.Unlock()
	a.mcpStatus.mu.Lock()
	a.stateMu.RLock()
	role := "builder"
	if a.activeConfig != nil {
		role = a.activeConfig.Name
	}
	session := a.sessionDir
	configs := maps.Clone(a.agentConfigs)
	activeRules := slices.Clone(a.ruleset)
	a.stateMu.RUnlock()
	changedBinding := a.mcpStatus.role != role || a.mcpStatus.session != session
	if changedBinding || a.mcpStatus.views == nil {
		a.mcpStatus.views = make(map[string]mcp.RuntimeSnapshot)
	}
	a.mcpStatus.role = role
	a.mcpStatus.session = session
	var all []mcp.ServerView
	if a.mcpStatus.provider != nil {
		all = append(all, a.mcpStatus.provider().Servers...)
	}
	for _, snapshot := range a.ownerMCPRuntimeSnapshots() {
		all = append(all, snapshot.Servers...)
	}
	a.mcpAuthMu.Lock()
	for i := range all {
		if operation := a.mcpAuthOperations[all[i].Ref]; operation != nil {
			kind := mcp.OperationLogin
			if operation.request.Action == MCPAuthReconnect || operation.request.Action == MCPAuthLogout {
				kind = mcp.OperationProbe
			}
			all[i].Operations = append(slices.Clone(all[i].Operations), mcp.OperationView{OperationID: fmt.Sprintf("auth-%d", operation.id), Kind: kind, State: operation.state, StartedAt: operation.started})
		}
	}
	a.mcpAuthMu.Unlock()

	if configs == nil {
		configs = make(map[string]*config.AgentConfig)
	}
	if configs[role] == nil {
		configs[role] = &config.AgentConfig{Name: role}
	}
	changed := false
	for name, cfg := range configs {
		if cfg == nil {
			continue
		}
		rules := permission.ParsePermission(&cfg.Permission)
		if name == role {
			rules = activeRules
		}
		filtered := (mcp.RuntimeSnapshot{Servers: filterMCPStatusServers(all, name, rules)}).Clone().Servers
		previous := a.mcpStatus.views[name]
		if previous.BindingID != "" && reflect.DeepEqual(previous.Servers, filtered) {
			continue
		}
		if previous.BindingID == "" || !sameMCPStatusRefs(previous.Servers, filtered) {
			previous = mcp.RuntimeSnapshot{SchemaVersion: mcp.RuntimeSnapshotSchemaVersion, BindingID: rand.Text()}
		}
		previous.Version++
		previous.Servers = filtered
		a.mcpStatus.views[name] = previous.Clone()
		if name == role {
			changed = true
		}
	}
	view := a.mcpStatus.views[role].Clone()
	// Readers must remain independent of output backpressure. The publication
	// lock preserves event order after releasing the cache lock.
	a.mcpStatus.mu.Unlock()
	if changed && a.outputCh != nil {
		a.emitReliableToTUI(MCPStatusChangedEvent{Snapshot: view, Role: role, SessionID: filepath.Base(session)}, "MCP status output is delayed")
	}
}

func filterMCPStatusServers(all []mcp.ServerView, role string, rules permission.Ruleset) []mcp.ServerView {
	owned := make([]mcp.ServerView, 0, len(all))
	for _, server := range all {
		if server.Ref.Owner == mcp.MainOwner || server.Ref.Owner == "agent:"+role {
			owned = append(owned, server)
		}
	}
	visible := make([]mcp.ServerView, 0, len(owned))
	seen := make(map[mcp.ServerRef]bool)
	for _, server := range owned {
		prefix := strings.TrimSuffix(mcp.RegisteredMCPToolName(server.Ref.ServerName, ""), "tool")
		var nested []string
		for _, other := range owned {
			p := strings.TrimSuffix(mcp.RegisteredMCPToolName(other.Ref.ServerName, ""), "tool")
			if p != prefix && strings.HasPrefix(p, prefix) {
				nested = append(nested, p)
			}
		}
		if rules.DeniesAllWithPrefix(prefix, nested...) || seen[server.Ref] {
			continue
		}
		visible = append(visible, server)
		seen[server.Ref] = true
	}

	for i := range visible {
		var aliases []string
		for _, name := range visible[i].Aliases {
			ref := visible[i].Ref
			ref.ServerName = name
			if seen[ref] {
				aliases = append(aliases, name)
			}
		}
		visible[i].Aliases = aliases
	}
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].Ref.Owner != visible[j].Ref.Owner {
			return visible[i].Ref.Owner < visible[j].Ref.Owner
		}
		return visible[i].Ref.ServerName < visible[j].Ref.ServerName
	})
	return visible
}

func sameMCPStatusRefs(a, b []mcp.ServerView) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Ref != b[i].Ref {
			return false
		}
	}
	return true
}
