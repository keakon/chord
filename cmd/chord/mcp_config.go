package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/permission"
)

// loadMCPCommandConfig resolves only configuration, visibility and repository
// identity. It never creates an agent, LLM client, or MCP subprocess. The
// returned directory is the state dir that roots the MCP credential store.
func loadMCPCommandConfig(ctx context.Context, agentName, serverName string) ([]mcp.Target, string, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("resolve working directory: %w", err)
	}
	root = resolveContentRoot(ctx, root)
	cfg, err := config.LoadConfig()
	if errors.Is(err, os.ErrNotExist) {
		cfg = config.DefaultConfig()
	} else if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	if _, merged, err := config.MergeProjectConfig(cfg, config.ProjectConfigPath(root)); err != nil {
		return nil, "", fmt.Errorf("load project config: %w", err)
	} else if merged != nil {
		cfg = merged
	}
	paths, err := config.ResolvePathLocator(cfg, config.PathOptions{})
	if err != nil {
		return nil, "", err
	}
	agents, err := config.ResolveAgentConfigs(filepath.Join(root, ".chord", "agents"), filepath.Join(paths.ConfigHome, "agents"))
	if err != nil {
		return nil, "", err
	}
	role := agentName
	if role == "" {
		role = "builder"
	}
	agentConfig := agents[role]
	if agentConfig == nil {
		return nil, "", fmt.Errorf("unknown agent %q", role)
	}
	targets, err := mcp.ResolveTargets(ctx, mcp.ResolveTargetOptions{Main: cfg.MCP, Agents: agents, Agent: agentName, Ruleset: permission.ParsePermission(&agentConfig.Permission), ContentRoot: root})
	if err != nil {
		return nil, "", err
	}
	if serverName != "" {
		for _, target := range targets {
			if target.Ref.ServerName == serverName {
				return []mcp.Target{target}, paths.StateDir, nil
			}
		}
		return nil, "", fmt.Errorf("MCP server %q is not configured or is not visible to agent %q", serverName, role)
	}
	return targets, paths.StateDir, nil
}
