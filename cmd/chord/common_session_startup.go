package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/recovery"
)

type sessionStartupOptions struct {
	ContinueLatest bool
	ResumeID       string
}

type sessionStartupPlan struct {
	SessionDir       string
	RestoreOnStartup bool
}

func planSessionStartup(sessionsDir string, opts sessionStartupOptions) (sessionStartupPlan, error) {
	if opts.ResumeID != "" {
		sessionDir := filepath.Join(sessionsDir, opts.ResumeID)
		mainPath := filepath.Join(sessionDir, "main.jsonl")
		info, err := os.Stat(mainPath)
		if err != nil || info.Size() == 0 {
			return sessionStartupPlan{}, fmt.Errorf("session %s not found or has no messages", opts.ResumeID)
		}
		return sessionStartupPlan{
			SessionDir:       sessionDir,
			RestoreOnStartup: true,
		}, nil
	}
	if opts.ContinueLatest {
		if sessionDir := recovery.FindMostRecentSession(sessionsDir, ""); sessionDir != "" {
			return sessionStartupPlan{
				SessionDir:       sessionDir,
				RestoreOnStartup: true,
			}, nil
		}
	}
	sessionDir, err := createNewSessionDir(sessionsDir)
	if err != nil {
		return sessionStartupPlan{}, err
	}
	return sessionStartupPlan{SessionDir: sessionDir}, nil
}

func createNewSessionDir(sessionsDir string) (string, error) {
	return recovery.CreateNewSessionDir(sessionsDir)
}

func applyInitialMCPPromptState(ac *AppContext, asyncMCP bool, mcpConfigured bool, syncPromptBlock string) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	if asyncMCP && len(ac.MCPConfigs) > 0 {
		return
	}
	if mcpConfigured {
		// Sync path: register main-agent server names as sentinels now that
		// MainAgent exists, so SubAgents never reconnect them.
		var names []string
		for _, cfg := range ac.MCPConfigs {
			names = append(names, cfg.Name)
		}
		ac.MainAgent.RegisterMainMCPServers(names)
		ac.MainAgent.SetMCPServersPromptBlock(syncPromptBlock)
		return
	}
	ac.MainAgent.SetPendingMCPDiscovery(nil, "")
}
