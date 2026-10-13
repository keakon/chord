package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type mcpServerEntry struct {
	Mgr               *mcp.Manager
	Target            mcp.Target
	RuntimeGeneration uint64
	pending           chan struct{}
	prepared          bool
}

func mainMCPServerCacheKey(serverName string) string { return "main\x00" + serverName }
func agentMCPServerCacheKey(agentName, serverName string) string {
	return "agent\x00" + agentName + "\x00" + serverName
}

// SetMCPRuntime supplies owner-independent runtime services. Owner-specific
// permissions are applied to snapshots, never through the focused UI route.
func (a *MainAgent) SetMCPRuntime(targets []mcp.Target, main *mcp.Manager, auth *mcp.AuthService, configHome string) {
	a.stateMu.RLock()
	roles := make(map[string]*config.AgentConfig, len(a.agentConfigs))
	maps.Copy(roles, a.agentConfigs)
	a.stateMu.RUnlock()
	if auth != nil {
		auth.UpdateTargets(mcp.MainOwner, targets)
	}
	a.mcpServerCacheMu.Lock()
	a.mcpRuntimeMain, a.mcpRuntimeAuth, a.mcpRuntimeConfigHome = main, auth, configHome
	a.mcpRuntimeTargets = make([]mcp.Target, len(targets))
	for i, t := range targets {
		a.mcpRuntimeTargets[i] = t.Snapshot()
	}
	if a.mcpServerCache == nil {
		a.mcpServerCache = make(map[string]*mcpServerEntry)
	}
	validPrivate := make(map[string]bool)
	for name, role := range roles {
		if role != nil {
			for server := range role.MCP {
				validPrivate[agentMCPServerCacheKey(name, server)] = true
			}
		}
	}
	var retired []*mcp.Manager
	removedOwners := make(map[string]bool)
	for key := range a.mcpServerCache {
		if strings.HasPrefix(key, "main\x00") {
			delete(a.mcpServerCache, key)
		} else if !validPrivate[key] {
			entry := a.mcpServerCache[key]
			if entry.Mgr != nil {
				retired = append(retired, entry.Mgr)
				removedOwners[entry.Target.Ref.Owner] = true
			}
			delete(a.mcpServerCache, key)
		}
	}
	for _, target := range targets {
		a.mcpServerCache[mainMCPServerCacheKey(target.Ref.ServerName)] = &mcpServerEntry{}
	}
	a.mcpRuntimeGeneration++
	a.mcpServerCacheMu.Unlock()
	for _, mgr := range retired {
		mgr.Close()
	}
	if auth != nil {
		for owner := range removedOwners {
			auth.UpdateTargets(owner, nil)
		}
	}
	for name, role := range roles {
		if role != nil {
			if _, err := a.getOrCreateAgentMCP(name, role.MCP); err != nil {
				log.Warnf("initialize private MCP targets agent=%v error=%v", name, err)
			}
		}
	}
}

func (a *MainAgent) MCPRuntimeTarget(ref mcp.ServerRef) (mcp.Target, *mcp.Manager, bool) {
	a.mcpServerCacheMu.Lock()
	defer a.mcpServerCacheMu.Unlock()
	if ref.Owner == mcp.MainOwner {
		for _, target := range a.mcpRuntimeTargets {
			if target.Ref == ref {
				return target.Snapshot(), a.mcpRuntimeMain, a.mcpRuntimeMain != nil
			}
		}
	}
	for _, entry := range a.mcpServerCache {
		if entry.Mgr != nil && entry.RuntimeGeneration == a.mcpRuntimeGeneration && entry.Target.Ref == ref {
			return entry.Target.Snapshot(), entry.Mgr, true
		}
	}
	return mcp.Target{}, nil, false
}

// getOrCreateAgentMCP creates pending managers without starting network work.
// Multiple instances of one role share the same owner/server entry.
func (a *MainAgent) getOrCreateAgentMCP(agentName string, cfg config.MCPConfig) ([]tools.Tool, error) {
	if err := a.parentCtx.Err(); err != nil {
		return nil, err
	}
	a.mcpServerCacheMu.Lock()
	if a.mcpServerCache == nil {
		a.mcpServerCache = make(map[string]*mcpServerEntry)
	}
	for name, sc := range cfg {
		if _, exists := a.mcpServerCache[mainMCPServerCacheKey(name)]; exists {
			a.mcpServerCacheMu.Unlock()
			return nil, fmt.Errorf("agent %q declares MCP server %q, but that name is already defined by project/global MCP config; remove the agent entry to inherit it or rename the agent server", agentName, name)
		}
		if sc.Manual {
			a.mcpServerCacheMu.Unlock()
			return nil, fmt.Errorf("agent %q MCP server %q sets manual: true, but agent-scoped MCP servers cannot be enabled at runtime; remove manual or configure the server in project/global MCP config", agentName, name)
		}
	}
	a.mcpServerCacheMu.Unlock()
	names := make([]string, 0, len(cfg))
	for name := range cfg {
		names = append(names, name)
	}
	sort.Strings(names)
	var result []tools.Tool
	for _, name := range names {
		sc, err := config.NormalizeMCPAuth(cfg[name])
		if err != nil {
			return nil, err
		}
		key := agentMCPServerCacheKey(agentName, name)
		converted := mcp.ServerConfigsFromConfig(config.MCPConfig{name: sc})[0]
		if converted.URL != "" {
			converted.URL, err = mcp.CanonicalEndpoint(converted.URL)
			if err != nil {
				return nil, err
			}
		}
		a.mcpServerCacheMu.Lock()
		entry := a.mcpServerCache[key]
		generation, auth, home := a.mcpRuntimeGeneration, a.mcpRuntimeAuth, a.mcpRuntimeConfigHome
		if entry != nil && entry.Mgr != nil && entry.RuntimeGeneration == generation && reflect.DeepEqual(entry.Target.Config, converted) {
			a.mcpServerCacheMu.Unlock()
			current, _ := entry.Mgr.ToolSnapshot()
			result = append(result, current...)
			continue
		}
		a.mcpServerCacheMu.Unlock()
		resolved, err := mcp.ResolveTargets(a.parentCtx, mcp.ResolveTargetOptions{Agent: agentName, Agents: map[string]*config.AgentConfig{agentName: {Name: agentName, MCP: config.MCPConfig{name: sc}}}, ContentRoot: a.contentRoot, ConfigHome: home})
		if err != nil {
			return nil, err
		}
		if len(resolved) != 1 {
			return nil, fmt.Errorf("MCP private target could not be resolved")
		}
		target := resolved[0]
		manager := mcp.NewPendingManagerWithClientInfo([]mcp.Target{target}, a.mcpClientInfo, auth)
		next := &mcpServerEntry{Mgr: manager, Target: target, RuntimeGeneration: generation}
		a.mcpServerCacheMu.Lock()
		current := a.mcpServerCache[key]
		if a.mcpRuntimeGeneration != generation || a.mcpServerCache == nil {
			a.mcpServerCacheMu.Unlock()
			manager.Close()
			return nil, context.Canceled
		}
		if current != entry {
			a.mcpServerCacheMu.Unlock()
			manager.Close()
			return a.getOrCreateAgentMCP(agentName, cfg)
		}
		a.mcpServerCache[key] = next
		a.mcpServerCacheMu.Unlock()
		manager.SetOnChange(func(mcp.RuntimeSnapshot) { a.RefreshMCPStatusSnapshot() })
		if entry != nil && entry.Mgr != nil {
			entry.Mgr.Close()
		}
	}
	a.mcpServerCacheMu.Lock()
	auth := a.mcpRuntimeAuth
	var ownerTargets []mcp.Target
	for name := range cfg {
		if entry := a.mcpServerCache[agentMCPServerCacheKey(agentName, name)]; entry != nil && entry.RuntimeGeneration == a.mcpRuntimeGeneration {
			ownerTargets = append(ownerTargets, entry.Target.Snapshot())
		}
	}
	a.mcpServerCacheMu.Unlock()
	if auth != nil {
		auth.UpdateTargets("agent:"+agentName, ownerTargets)
	}
	return result, nil
}

type MCPRequestSnapshot struct {
	Tools   []tools.Tool
	Version string
}

func (a *MainAgent) prepareSubAgentMCP(ctx context.Context, owner string, cfg config.MCPConfig, rules permission.Ruleset) (MCPRequestSnapshot, error) {
	preparationCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := a.prepareMCPRequest(preparationCtx); err != nil && ctx.Err() != nil {
		return MCPRequestSnapshot{}, ctx.Err()
	}
	if _, err := a.getOrCreateAgentMCP(owner, cfg); err != nil {
		return MCPRequestSnapshot{}, err
	}
	a.mcpServerCacheMu.Lock()
	main := a.mcpRuntimeMain
	entries := make([]*mcpServerEntry, 0, len(cfg))
	for name := range cfg {
		prefix := strings.TrimSuffix(mcp.RegisteredMCPToolName(name, ""), "tool")
		var nested []string
		for other := range cfg {
			p := strings.TrimSuffix(mcp.RegisteredMCPToolName(other, ""), "tool")
			if p != prefix && strings.HasPrefix(p, prefix) {
				nested = append(nested, p)
			}
		}
		if rules.DeniesAllWithPrefix(prefix, nested...) {
			continue
		}
		if entry := a.mcpServerCache[agentMCPServerCacheKey(owner, name)]; entry != nil {
			entries = append(entries, entry)
		}
	}
	a.mcpServerCacheMu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Target.Ref.ServerName < entries[j].Target.Ref.ServerName })
	for _, entry := range entries {
		if err := a.preparePrivateMCP(preparationCtx, owner, entry); err != nil {
			if ctx.Err() != nil {
				return MCPRequestSnapshot{}, ctx.Err()
			}
			break
		}
	}
	var snapshot MCPRequestSnapshot
	versions := make([]any, 0, len(entries)+1)
	if main != nil {
		inherited, v := main.ToolSnapshot()
		snapshot.Tools = append(snapshot.Tools, inherited...)
		versions = append(versions, struct {
			Binding string
			Version uint64
		}{v.BindingID, v.Version})
	}
	for _, entry := range entries {
		private, v := entry.Mgr.ToolSnapshot()
		snapshot.Tools = append(snapshot.Tools, private...)
		versions = append(versions, struct {
			Binding string
			Version uint64
		}{v.BindingID, v.Version})
	}
	filtered := snapshot.Tools[:0]
	for _, tool := range snapshot.Tools {
		if !rules.IsDisabled(tool.Name()) {
			filtered = append(filtered, tool)
		}
	}
	snapshot.Tools = filtered
	raw, err := json.Marshal(struct {
		Versions []any
		Rules    permission.Ruleset
	}{versions, rules})
	if err != nil {
		return MCPRequestSnapshot{}, err
	}
	sum := sha256.Sum256(raw)
	snapshot.Version = hex.EncodeToString(sum[:])
	return snapshot, nil
}

func (a *MainAgent) preparePrivateMCP(ctx context.Context, owner string, entry *mcpServerEntry) error {
	a.mcpServerCacheMu.Lock()
	if entry.pending == nil {
		entry.pending = make(chan struct{})
		done := entry.pending
		first := !entry.prepared
		go func() {
			workCtx, cancel := context.WithTimeout(a.parentCtx, 60*time.Second)
			defer cancel()
			if first {
				entry.Mgr.ConnectAll(workCtx, []mcp.Target{entry.Target})
			} else {
				_, _ = entry.Mgr.ReconcileCredentials(workCtx)
			}
			a.mcpServerCacheMu.Lock()
			if a.mcpServerCache[agentMCPServerCacheKey(owner, entry.Target.Ref.ServerName)] == entry {
				entry.prepared = true
			}
			entry.pending = nil
			close(done)
			a.mcpServerCacheMu.Unlock()
		}()
	}
	done := entry.pending
	a.mcpServerCacheMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (a *MainAgent) ownerMCPRuntimeSnapshots() []mcp.RuntimeSnapshot {
	a.mcpServerCacheMu.Lock()
	var managers []*mcp.Manager
	for _, entry := range a.mcpServerCache {
		if entry.Mgr != nil && entry.RuntimeGeneration == a.mcpRuntimeGeneration {
			managers = append(managers, entry.Mgr)
		}
	}
	a.mcpServerCacheMu.Unlock()
	result := make([]mcp.RuntimeSnapshot, 0, len(managers))
	for _, manager := range managers {
		result = append(result, manager.Snapshot())
	}
	return result
}
