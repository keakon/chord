package main

import (
	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
)

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
