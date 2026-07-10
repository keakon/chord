package agent

import (
	"sort"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) SetLSPStatusFunc(serverList func() []LSPServerDisplay) {
	a.lspServerListFn = serverList
}

func (a *MainAgent) SetLSPSessionFuncs(reset func(), load func([]message.Message)) {
	a.lspSessionResetFn = reset
	a.lspSessionLoadFn = load
}

func (a *MainAgent) SetMCPStatusFunc(serverList func() []MCPServerDisplay) {
	a.mcpServerListFn = serverList
}

func (a *MainAgent) SetMCPKnownToolNamesFunc(toolNames func(string) []string) {
	a.mcpKnownToolNamesFn = toolNames
}

func (a *MainAgent) NotifyEnvStatusUpdated() { a.emitToTUI(EnvStatusUpdateEvent{}) }

func (a *MainAgent) LSPServerList() []LSPServerDisplay {
	if a.lspServerListFn == nil {
		return nil
	}
	if ruleset := a.effectiveRuleset(); len(ruleset) > 0 && ruleset.IsDisabled(tools.NameLsp) {
		return nil
	}
	return a.lspServerListFn()
}

func (a *MainAgent) MCPServerList() []MCPServerDisplay {
	if a.mcpServerListFn == nil {
		return nil
	}
	rows := a.mcpServerListFn()
	serverNames := make([]string, 0, len(rows))
	for _, row := range rows {
		serverNames = append(serverNames, row.Name)
	}
	visibility := a.mcpVisibilitySnapshot(serverNames)
	visible := make([]MCPServerDisplay, 0, len(rows))
	for _, row := range rows {
		if visibility.serverVisible(row.Name) {
			visible = append(visible, row)
		}
	}
	return visible
}

type mcpVisibilitySnapshot struct {
	ruleset        permission.Ruleset
	knownTools     map[string][]string
	knownToolsFunc func(string) []string
	nestedPrefixes map[string][]string
}

type mcpServerNamedTool interface {
	MCPServerName() string
}

func (a *MainAgent) mcpVisibilitySnapshot(serverNames []string) mcpVisibilitySnapshot {
	snapshot := mcpVisibilitySnapshot{
		ruleset:        a.effectiveRuleset(),
		knownTools:     make(map[string][]string),
		knownToolsFunc: a.mcpKnownToolNamesFn,
	}
	if len(serverNames) == 0 {
		return snapshot
	}
	type serverPrefix struct {
		key    string
		prefix string
	}
	prefixes := make([]serverPrefix, 0, len(serverNames))
	for _, serverName := range serverNames {
		key := strings.TrimSpace(serverName)
		if key == "" {
			continue
		}
		prefixes = append(prefixes, serverPrefix{
			key:    key,
			prefix: strings.TrimSuffix(mcp.RegisteredMCPToolName(key, ""), "tool"),
		})
		for _, remoteName := range configuredMCPAllowedTools(a.globalConfig, a.projectConfig, key) {
			snapshot.knownTools[key] = append(snapshot.knownTools[key], mcp.RegisteredMCPToolName(key, remoteName))
		}
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if len(prefixes[i].prefix) != len(prefixes[j].prefix) {
			return len(prefixes[i].prefix) > len(prefixes[j].prefix)
		}
		return prefixes[i].key < prefixes[j].key
	})
	unique := prefixes[:0]
	for _, server := range prefixes {
		if len(unique) > 0 && unique[len(unique)-1].key == server.key {
			continue
		}
		unique = append(unique, server)
	}
	prefixes = unique
	for i, server := range prefixes {
		for _, nested := range prefixes[:i] {
			if len(nested.prefix) > len(server.prefix) && strings.HasPrefix(nested.prefix, server.prefix) {
				if snapshot.nestedPrefixes == nil {
					snapshot.nestedPrefixes = make(map[string][]string)
				}
				snapshot.nestedPrefixes[server.key] = append(snapshot.nestedPrefixes[server.key], nested.prefix)
			}
		}
	}
	if a.tools == nil {
		return snapshot
	}
	registryTools := a.tools.ToolsSnapshot()
	serverForTool := func(tool tools.Tool) string {
		if named, ok := tool.(mcpServerNamedTool); ok {
			return strings.TrimSpace(named.MCPServerName())
		}
		name := tools.NormalizeName(tool.Name())
		for _, server := range prefixes {
			if strings.HasPrefix(name, server.prefix) {
				return server.key
			}
		}
		return ""
	}
	counts := make(map[string]int, len(prefixes))
	for _, tool := range registryTools {
		serverKey := serverForTool(tool)
		if serverKey != "" {
			counts[serverKey]++
		}
	}
	for server, count := range counts {
		existing := snapshot.knownTools[server]
		known := make([]string, len(existing), len(existing)+count)
		copy(known, existing)
		snapshot.knownTools[server] = known
	}
	for _, tool := range registryTools {
		if serverKey := serverForTool(tool); serverKey != "" {
			snapshot.knownTools[serverKey] = append(snapshot.knownTools[serverKey], tools.NormalizeName(tool.Name()))
		}
	}
	return snapshot
}

func configuredMCPAllowedTools(globalCfg, projectCfg *config.Config, serverName string) []string {
	if projectCfg != nil {
		if serverCfg, ok := projectCfg.MCP[serverName]; ok {
			return serverCfg.AllowedTools
		}
	}
	if globalCfg != nil {
		return globalCfg.MCP[serverName].AllowedTools
	}
	return nil
}

func (s mcpVisibilitySnapshot) visibleToolNames(serverName string) []string {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return nil
	}
	names := s.knownTools[serverName]
	if len(names) == 0 && s.knownToolsFunc != nil {
		names = s.knownToolsFunc(serverName)
	}
	if len(names) == 0 {
		return nil
	}
	visible := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = tools.NormalizeName(name)
		if name == "" || s.ruleset.IsDisabled(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		visible = append(visible, name)
	}
	return visible
}

func (s mcpVisibilitySnapshot) serverVisible(serverName string) bool {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return false
	}
	if names := s.knownTools[serverName]; len(names) > 0 {
		for _, name := range names {
			if !s.ruleset.IsDisabled(name) {
				return true
			}
		}
		return false
	}
	if s.knownToolsFunc != nil {
		if names := s.knownToolsFunc(serverName); len(names) > 0 {
			for _, name := range names {
				if !s.ruleset.IsDisabled(name) {
					return true
				}
			}
			return false
		}
	}
	prefix := strings.TrimSuffix(mcp.RegisteredMCPToolName(serverName, ""), "tool")
	return !s.ruleset.DeniesAllWithPrefix(prefix, s.nestedPrefixes[serverName]...)
}
