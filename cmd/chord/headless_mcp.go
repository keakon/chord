package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/agent"

	"github.com/keakon/chord/internal/mcp"
)

const headlessMCPStatusChanged = "mcp_status_changed"

type headlessMCPBackend interface{ MCPRuntimeSnapshot() mcp.RuntimeSnapshot }

func headlessMCPSnapshot(backend headlessBackend) mcp.RuntimeSnapshot {
	if provider, ok := backend.(headlessMCPBackend); ok {
		return provider.MCPRuntimeSnapshot().Clone()
	}
	return mcp.RuntimeSnapshot{SchemaVersion: mcp.RuntimeSnapshotSchemaVersion, Servers: []mcp.ServerView{}}
}

// The caller holds state.mu. The cached projection and its envelope seq advance
// together; status never samples the backend after it has copied the seq.
func applyHeadlessMCPSnapshotLocked(state *headlessState, snapshot mcp.RuntimeSnapshot) bool {
	if snapshot.SchemaVersion != mcp.RuntimeSnapshotSchemaVersion {
		return false
	}
	if state.mcp.BindingID == snapshot.BindingID && snapshot.Version <= state.mcp.Version {
		return false
	}
	state.mcp = snapshot.Clone()
	return true
}

func isHeadlessMCPCommand(content string) bool {
	fields := strings.Fields(content)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/mcp") {
		return false
	}
	if len(fields) == 1 {
		return true
	}
	switch strings.ToLower(fields[1]) {
	case "status", "login", "logout", "reauth", "reconnect", "cancel":
		return true
	}
	return false
}

func handleHeadlessMCPCommand(cmd headlessCommand, state *headlessState, out *stdoutWriter) {
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	if cmd.Type == "send" {
		fields := strings.Fields(cmd.Content)
		if len(fields) > 1 {
			action = strings.ToLower(fields[1])
		}
	}
	if action == "" || action == "status" {
		state.mu.Lock()
		snapshot := state.mcp.Clone()
		seq := state.stampHeadlessSeq(false)
		state.mu.Unlock()
		if cmd.Type == "send" && cmd.RequestID != "" {
			out.emit(headlessEnvelope{Type: "input_result", Payload: agent.InputResultEvent{RequestID: cmd.RequestID, Status: agent.InputHandled}})
		}
		out.emit(headlessEnvelope{Type: "mcp_status_response", Seq: seq, Payload: snapshot})
		return
	}
	message := "Manage MCP authentication on the host: chord mcp login <server> or chord mcp logout <server>. Reconnect in the local TUI; use status to inspect MCP state."
	if cmd.Type == "send" {
		emitHeadlessInputRejected(cmd.RequestID, message, state, out)
	}
	out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"error_class": string(mcp.ErrorHostLoginRequired), "suggested_action": mcp.ActionLoginOnHost, "message": message}})
}

func disableHeadlessMCPAuthorization(mainAgent *agent.MainAgent) {
	if mainAgent == nil {
		return
	}
	mainAgent.SetMCPAuthControlFunc(func(context.Context, agent.MCPAuthRequest, func(mcp.LoginProgress)) error {
		return fmt.Errorf("host_login_required: run chord mcp login on the host; reconnect in the local TUI")
	})
}

func clearHeadlessMCPStateLocked(state *headlessState) {
	state.mcp = mcp.RuntimeSnapshot{SchemaVersion: mcp.RuntimeSnapshotSchemaVersion, Servers: []mcp.ServerView{}}
	state.mcpRole = ""
	state.mcpSession = ""
}
