package main

import (
	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
)

// lspFollowupSource adapts the manager's late diagnostics for the agent.
type lspFollowupSource struct {
	mgr *lsp.Manager
}

// Pending reports the late diagnostics; each item's Ack consumes exactly that
// item.
func (s lspFollowupSource) Pending(paths []string, displayBaseDir string) []agent.LSPFollowupItem {
	mgr := s.mgr
	pending := mgr.PendingLateDiagnostics(paths, displayBaseDir)
	if len(pending) == 0 {
		return nil
	}
	items := make([]agent.LSPFollowupItem, 0, len(pending))
	for _, late := range pending {
		items = append(items, agent.LSPFollowupItem{
			DisplayPath: late.DisplayPath,
			Lines:       late.Lines,
			Reviews:     late.Reviews,
			Ack:         func() { mgr.AckLateDiagnostics([]lsp.LateDiagnostics{late}) },
		})
	}
	return items
}

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
