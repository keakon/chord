package main

import (
	"context"
	"fmt"
	"github.com/keakon/golog/log"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/power"
	"github.com/keakon/chord/internal/tools"
)

// Runtime owns local-mode wiring around a MainAgent. The AppContext still owns
// the agent lifecycle and remains responsible for shutdown via ac.Close().
type Runtime struct {
	Agent    *agent.MainAgent
	powerMgr *power.Manager // nil if prevent_sleep not enabled
}

func createRuntime(ac *AppContext) (*Runtime, error) {
	if ac == nil || ac.MainAgent == nil {
		return nil, fmt.Errorf("runtime requires an initialized main agent")
	}
	if ac.Registry == nil {
		return nil, fmt.Errorf("runtime requires an initialized tool registry")
	}

	ensureRuntimeLSP(ac)
	configureRuntimeStateProviders(ac)

	// Wire power manager if prevent_sleep is enabled.
	var powerMgr *power.Manager
	if ac.Cfg != nil && ac.Cfg.PreventSleep != nil && *ac.Cfg.PreventSleep {
		powerMgr = power.NewManager(power.NewBackend())
		ac.MainAgent.SetActivityObserver(&activityObserverAdapter{mgr: powerMgr})
		log.Debug("prevent_sleep enabled: activity-based sleep prevention active")
	}

	confirmTimeout := time.Duration(ac.Cfg.ConfirmTimeout) * time.Second
	wireMainAgentRuntime(ac.Ctx, ac.MainAgent, ac.Registry, confirmTimeout)
	startRuntimeMCP(ac)
	startRuntimeWarmups(ac)

	return &Runtime{Agent: ac.MainAgent, powerMgr: powerMgr}, nil
}

func wireMainAgentRuntime(ctx context.Context, mainAgent *agent.MainAgent, reg *tools.Registry, confirmTimeout time.Duration) {
	mainAgent.SetConfirmFunc(func(ctx context.Context, toolName, args string, needsApproval, alreadyAllowed []string) (agent.ConfirmResponse, error) {
		resp, err := mainAgent.AwaitConfirm(ctx, toolName, args, confirmTimeout, needsApproval, alreadyAllowed)
		if err != nil {
			return agent.ConfirmResponse{}, err
		}
		return resp, nil
	})

	reg.Register(tools.NewQuestionTool(func(ctx context.Context, questions []tools.QuestionItem) ([]tools.QuestionAnswer, error) {
		return mainAgent.AskQuestions(ctx, questions, confirmTimeout)
	}))

	go mainAgent.Run(ctx)
}

func (rt *Runtime) Close() {
	if rt == nil {
		return
	}
	if rt.powerMgr != nil {
		rt.powerMgr.Close()
	}
	if rt.Agent != nil {
		rt.Agent.ClearPendingInteractions()
	}
}

func (rt *Runtime) WaitIdleOrTimeout(timeout time.Duration) bool {
	if rt == nil || rt.Agent == nil {
		return true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case evt, ok := <-rt.Agent.Events():
			if !ok {
				return true
			}
			if _, ok := evt.(agent.IdleEvent); ok {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

func ensureRuntimeLSP(ac *AppContext) {
	if ac == nil || ac.Cfg == nil || len(ac.Cfg.LSP) == 0 || ac.LSPManager != nil {
		return
	}

	ac.LSPManager = lsp.NewManager(ac.Cfg, ac.ProjectRoot, nil)
	ac.Registry.Register(tools.ReadTool{LSP: ac.LSPManager})
	ac.Registry.Register(tools.WriteTool{LSP: ac.LSPManager})
	ac.Registry.Register(tools.EditTool{LSP: ac.LSPManager})
	ac.Registry.Register(tools.DeleteTool{LSP: ac.LSPManager})
	ac.Registry.Register(tools.LspTool{LSP: ac.LSPManager})
}

func configureRuntimeStateProviders(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil {
		return
	}

	ac.MainAgent.SetLSPStatusFunc(
		func() []agent.LSPServerDisplay { return lspServerDisplayList(ac.LSPManager) },
	)
	ac.MainAgent.SetLSPSessionFuncs(
		func() {
			if ac.LSPManager == nil {
				return
			}
			ac.LSPManager.ResetTouched()
			ac.LSPManager.ResetReviews()
			ac.MainAgent.NotifyEnvStatusUpdated()
		},
		func(msgs []message.Message) {
			if ac.LSPManager == nil {
				return
			}
			ac.LSPManager.RebuildTouchedPaths(agent.RebuildTouchedPathsFromMessages(msgs))
			ac.LSPManager.RebuildReviewSnapshots(lsp.RebuildReviewSnapshotsFromMessages(msgs))
			ac.MainAgent.NotifyEnvStatusUpdated()
		},
	)
	ac.MainAgent.SetMCPStatusFunc(func() []agent.MCPServerDisplay {
		return mcpServerDisplayList(ac.MCPMgr)
	})
}

func startRuntimeMCP(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil || ac.Registry == nil || ac.MCPMgr == nil || len(ac.MCPConfigs) == 0 {
		return
	}
	ac.mcpStartOnce.Do(func() {
		go func() {
			ac.MCPMgr.ConnectAll(ac.Ctx, ac.MCPConfigs)

			var (
				mcpTools []tools.Tool
				block    string
			)
			if len(ac.MCPMgr.Clients()) > 0 {
				var err error
				mcpTools, err = mcp.DiscoverAllTools(ac.Ctx, ac.MCPMgr)
				if err != nil {
					log.Warnf("MCP tool discovery failed error=%v", err)
				}
				block = mcp.ConnectedServersPromptBlock(ac.Ctx, ac.MCPMgr)
			}
			// Register main-agent server names as sentinels so SubAgents
			// never reconnect them.
			var mainServerNames []string
			for _, cfg := range ac.MCPConfigs {
				mainServerNames = append(mainServerNames, cfg.Name)
			}
			ac.MainAgent.RegisterMainMCPServers(mainServerNames)

			ac.MainAgent.SetPendingMCPDiscovery(mcpTools, block)

			ac.MainAgent.NotifyEnvStatusUpdated()
		}()
	})
}

func startRuntimeWarmups(ac *AppContext) {
	if ac == nil || ac.MainAgent == nil {
		return
	}
	go func() {
		if err := ac.MainAgent.PrewarmModelPolicy(); err != nil {
			log.Warnf("main-agent model policy prewarm failed error=%v", err)
		}
	}()
	go func() {
		if ac.MainAgent.ReloadAgentsMD() {
			log.Debug("project AGENTS.md loaded asynchronously")
		}
	}()
}
