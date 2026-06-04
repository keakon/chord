package main

import (
	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
)

func lspServerDisplayList(mgr *lsp.Manager, runtimeCtrl *runtimeResourceController) []agent.LSPServerDisplay {
	if mgr == nil {
		return nil
	}
	rows := mgr.SidebarEntries()
	if len(rows) == 0 {
		return nil
	}
	lspIdle, _ := runtimeIdleStatus(runtimeCtrl)
	out := make([]agent.LSPServerDisplay, len(rows))
	for i, row := range rows {
		idle := lspIdle && runtimeCtrl != nil && runtimeCtrl.WasLSPLoaded(row.Name) && !row.OK && row.Pending && row.Error == ""
		out[i] = agent.LSPServerDisplay{
			Name:     row.Name,
			OK:       row.OK,
			Pending:  row.Pending && !idle,
			Idle:     idle,
			Err:      row.Error,
			Errors:   row.Errors,
			Warnings: row.Warnings,
		}
	}
	return out
}

func mcpServerDisplayList(mgr *mcp.Manager, runtimeCtrl *runtimeResourceController) []agent.MCPServerDisplay {
	if mgr == nil {
		return nil
	}
	status := mgr.ServerEndpoints()
	if len(status) == 0 {
		return nil
	}
	_, mcpIdle := runtimeIdleStatus(runtimeCtrl)
	out := make([]agent.MCPServerDisplay, len(status))
	for i, st := range status {
		idle := mcpIdle && !st.OK && !st.Disabled && !st.Retrying && st.Error == ""
		out[i] = agent.MCPServerDisplay{
			Name:        st.Name,
			OK:          st.OK,
			Pending:     st.Pending && !idle,
			Idle:        idle,
			Disabled:    st.Disabled,
			Manual:      st.Manual,
			Retrying:    st.Retrying,
			Attempt:     st.Attempt,
			MaxAttempts: st.MaxAttempts,
			Err:         st.Error,
		}
	}
	return out
}

func runtimeIdleStatus(runtimeCtrl *runtimeResourceController) (lspIdle, mcpIdle bool) {
	if runtimeCtrl == nil {
		return false, false
	}
	return runtimeCtrl.IdleStatus()
}
