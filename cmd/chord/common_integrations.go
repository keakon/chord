package main

import (
	"log/slog"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
)

func applyProjectConfigOverrides(ac *AppContext, pc *config.Config) {
	if ac == nil || ac.Cfg == nil || pc == nil {
		return
	}
	ac.ProjectCfg = pc
	slog.Info("loaded project config")
	if len(pc.LSP) > 0 {
		if ac.Cfg.LSP == nil {
			ac.Cfg.LSP = make(config.LSPConfig)
		}
		for name, srv := range pc.LSP {
			ac.Cfg.LSP[name] = srv
		}
	}
	if pc.DesktopNotification != nil {
		v := *pc.DesktopNotification
		ac.Cfg.DesktopNotification = &v
	}
	if pc.PreventSleep != nil {
		v := *pc.PreventSleep
		ac.Cfg.PreventSleep = &v
	}
	if pc.WebFetch.UserAgent != nil {
		v := *pc.WebFetch.UserAgent
		ac.Cfg.WebFetch.UserAgent = &v
	}
}

func lspServerDisplayList(mgr *lsp.Manager) []agent.LSPServerDisplay {
	if mgr == nil {
		return nil
	}
	rows := mgr.SidebarEntries()
	if len(rows) == 0 {
		return nil
	}
	out := make([]agent.LSPServerDisplay, len(rows))
	for i, row := range rows {
		out[i] = agent.LSPServerDisplay{
			Name:     row.Name,
			OK:       row.OK,
			Pending:  row.Pending,
			Err:      row.Error,
			Errors:   row.Errors,
			Warnings: row.Warnings,
		}
	}
	return out
}

func mcpServerDisplayList(mgr *mcp.Manager) []agent.MCPServerDisplay {
	if mgr == nil {
		return nil
	}
	status := mgr.ServerEndpoints()
	if len(status) == 0 {
		return nil
	}
	out := make([]agent.MCPServerDisplay, len(status))
	for i, st := range status {
		out[i] = agent.MCPServerDisplay{
			Name:        st.Name,
			OK:          st.OK,
			Pending:     st.Pending,
			Retrying:    st.Retrying,
			Attempt:     st.Attempt,
			MaxAttempts: st.MaxAttempts,
			Err:         st.Error,
		}
	}
	return out
}
