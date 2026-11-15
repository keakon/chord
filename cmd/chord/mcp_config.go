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
// connect selects the stricter trust contract: offline inspection may read
// the project layer for display, while a connect applies it only under an
// authorization or an explicit CLI override.
func loadMCPCommandConfig(ctx context.Context, agentName, serverName string, connect bool) ([]mcp.Target, string, error) {
	if err := projectTrustFlagOverridesError(); err != nil {
		return nil, "", err
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("resolve working directory: %w", err)
	}
	root = resolveContentRoot(ctx, root)
	skipProject := flagSkipProjectConfig
	var cfg *config.Config
	if connect {
		globalPath, err := config.ConfigPath()
		if err != nil {
			return nil, "", wrapConfigLoadError("resolve config path", err)
		}
		globalData, err := os.ReadFile(globalPath)
		if errors.Is(err, os.ErrNotExist) {
			globalData = nil
		} else if err != nil {
			return nil, "", wrapConfigLoadError("read global config", err)
		}
		resolved, _, err := resolveProjectConfigForUse(ctx, root, "headless", globalPath, globalData)
		if err != nil {
			return nil, "", err
		}
		cfg = resolved.Config
	} else {
		cfg, err = config.LoadConfig()
		if errors.Is(err, os.ErrNotExist) {
			cfg = config.DefaultConfig()
		} else if err != nil {
			return nil, "", fmt.Errorf("load config: %w", err)
		}
		if !skipProject {
			if _, merged, err := config.MergeProjectConfig(cfg, config.ProjectConfigPath(root)); err != nil {
				return nil, "", fmt.Errorf("load project config: %w", err)
			} else if merged != nil {
				cfg = merged
			}
		}
	}
	paths, err := config.ResolvePathLocator(cfg, config.PathOptions{})
	if err != nil {
		return nil, "", err
	}
	projectAgentsDir := ""
	if !skipProject {
		projectAgentsDir = filepath.Join(root, ".chord", "agents")
	}
	agents, err := config.ResolveAgentConfigs(projectAgentsDir, filepath.Join(paths.ConfigHome, "agents"))
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
