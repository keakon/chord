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
func (s lspFollowupSource) Pending(paths []string, displayBaseDir, agentID string) []agent.LSPFollowupItem {
	mgr := s.mgr
	pending := mgr.PendingLateDiagnostics(paths, displayBaseDir, agentID)
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
	return aggregateLSPRows(rows, runtimeCtrl)
}

// aggregateLSPRows merges the manager's per-root instances into one row per
// server. The panel reports which servers are active and how many problems they
// found, not how many checkouts this session touched: counts add up across
// instances, and one instance that cannot start marks the whole row failed
// because the row has no room to say which checkout broke.
func aggregateLSPRows(rows []lsp.SidebarServerEntry, runtimeCtrl *runtimeResourceController) []agent.LSPServerDisplay {
	lspIdle, _ := runtimeIdleStatus(runtimeCtrl)
	out := make([]agent.LSPServerDisplay, 0, len(rows))
	index := make(map[string]int, len(rows))
	for _, row := range rows {
		i, seen := index[row.Name]
		if !seen {
			index[row.Name] = len(out)
			out = append(out, agent.LSPServerDisplay{Name: row.Name})
			i = len(out) - 1
		}
		merged := &out[i]
		merged.OK = merged.OK || row.OK
		merged.Pending = merged.Pending || row.Pending
		merged.Errors += row.Errors
		merged.Warnings += row.Warnings
		if row.Error != "" && merged.Err == "" {
			merged.Err = row.Error
		}
	}
	for i := range out {
		row := &out[i]
		if row.Err != "" {
			row.OK = false
			row.Pending = false
			row.Idle = false
			continue
		}
		idle := lspIdle && runtimeCtrl != nil && runtimeCtrl.WasLSPLoaded(row.Name) && !row.OK && row.Pending
		row.Idle = idle
		row.Pending = row.Pending && !idle
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
