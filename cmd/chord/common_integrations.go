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
			Root:     row.Root,
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

func mcpServerDisplayList(mgr *mcp.Manager, catalog *mcp.Catalog, runtimeCtrl *runtimeResourceController) []agent.MCPServerDisplay {
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
		// Only a recorded failure makes a row an error. An endpoint the runtime
		// released (idle unload, parked candidate, dropped connection) is
		// recoverable, so it renders as the neutral unloaded state instead of the
		// failed bucket; a row that is actually connecting stays pending unless
		// the runtime already declared the MCP surface idle.
		released := !st.OK && !st.Disabled && !st.Retrying && st.Error == ""
		idle := released && (!st.Pending || mcpIdle)
		out[i] = agent.MCPServerDisplay{
			Name:        st.Name,
			OK:          st.OK,
			Pending:     st.Pending && !idle,
			Idle:        idle,
			Disabled:    st.Disabled,
			Manual:      st.Manual,
			Enabled:     catalog != nil && catalog.DesiredEnabled(st.Name),
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
