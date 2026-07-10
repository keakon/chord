package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

type MCPControlAction string

const (
	MCPControlEnable  MCPControlAction = "enable"
	MCPControlDisable MCPControlAction = "disable"
)

// MCPControlRequest describes a runtime MCP enable/disable operation.
// Servers may contain one or more server names. If Servers is empty, the
// operation applies to all configured servers.
type MCPControlRequest struct {
	Action  MCPControlAction
	Servers []string
}

// MCPControlResult carries the post-operation MCP tool set and the prompt block
// describing connected servers.
type MCPControlResult struct {
	Tools       []tools.Tool
	PromptBlock string
}

type mcpControlDonePayload struct {
	req    MCPControlRequest
	result MCPControlResult
	err    error
}

// SetMCPControlFunc installs the runtime callback used to connect/disconnect MCP servers.
// The callback runs in a background goroutine; results are applied on the agent event loop.
func (a *MainAgent) SetMCPControlFunc(fn func(context.Context, MCPControlRequest) (MCPControlResult, error)) {
	a.mcpControlFn = fn
}

// ResetMCPReady creates a new MCP readiness channel.
// It is used when MCP startup or runtime control begins so the next request
// blocks until the new tool surface is ready.
func (a *MainAgent) ResetMCPReady() {
	a.mcpReadyMu.Lock()
	a.mcpReady = make(chan struct{})
	a.mcpReadyMu.Unlock()
}

// SetMCPServerEnabled requests enabling/disabling an MCP server.
// It is safe to call from any goroutine.
func (a *MainAgent) SetMCPServerEnabled(server string, enabled bool) error {
	server = strings.TrimSpace(server)
	if server == "" {
		return fmt.Errorf("mcp server name required")
	}
	action := MCPControlEnable
	if !enabled {
		action = MCPControlDisable
	}
	a.sendEvent(Event{Type: EventMCPControl, Payload: MCPControlRequest{Action: action, Servers: []string{server}}})
	return nil
}

func (a *MainAgent) handleMCPControlEvent(evt Event) {
	req, ok := evt.Payload.(MCPControlRequest)
	if !ok {
		log.Errorf("handleMCPControlEvent: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	if req.Action == MCPControlEnable {
		if len(req.Servers) == 0 {
			req.Servers = a.visibleManualMCPServerNames()
		}
		if !a.mcpControlTargetsVisible(req.Servers) {
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/mcp enable is unavailable because the active role denies all tools from the selected MCP server")})
			return
		}
	}
	if a.mcpTransitionActive.Load() {
		a.emitToTUI(ToastEvent{Message: "MCP change already in progress", Level: "warn"})
		return
	}
	if a.mcpControlFn == nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/mcp is not available (runtime MCP control not configured)")})
		return
	}

	// Begin transition: block new LLM requests at ensureSessionBuilt until the
	// runtime control result is ready to be applied to the next request surface.
	a.mcpTransitionActive.Store(true)
	a.ResetMCPReady()
	a.NotifyEnvStatusUpdated()

	go func() {
		res, err := a.mcpControlFn(a.parentCtx, req)
		a.sendEvent(Event{Type: EventMCPControlDone, Payload: mcpControlDonePayload{req: req, result: res, err: err}})
	}()
}

func (a *MainAgent) mcpControlTargetsVisible(servers []string) bool {
	if len(servers) == 0 {
		return false
	}
	serverNames := append([]string(nil), servers...)
	if a.mcpServerListFn != nil {
		for _, row := range a.mcpServerListFn() {
			serverNames = append(serverNames, row.Name)
		}
	}
	visibility := a.mcpVisibilitySnapshot(serverNames)
	for _, server := range servers {
		if !visibility.serverVisible(server) {
			return false
		}
	}
	return true
}

func (a *MainAgent) visibleManualMCPServerNames() []string {
	rows := a.MCPServerList()
	servers := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Manual {
			servers = append(servers, row.Name)
		}
	}
	return servers
}

func summarizeMCPControlError(err error) string {
	if err == nil {
		return ""
	}
	var parts []string
	collectMCPControlErrorParts(err, &parts)
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts, "; ")
}

func collectMCPControlErrorParts(err error, parts *[]string) {
	if err == nil {
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			collectMCPControlErrorParts(child, parts)
		}
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	msg := strings.Join(strings.Fields(strings.TrimSpace(err.Error())), " ")
	if msg != "" {
		*parts = append(*parts, msg)
	}
}

func (a *MainAgent) handleMCPControlDoneEvent(evt Event) {
	payload, ok := evt.Payload.(mcpControlDonePayload)
	if !ok {
		log.Errorf("handleMCPControlDoneEvent: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	if payload.err == nil {
		// Defer MCP tool registration until ensureSessionBuilt, so any in-flight turn
		// continues with the tool surface it was started with and the next request
		// (including an automatic retry) sees matching tools + system prompt.
		a.mcpServersPromptMu.Lock()
		a.mcpServersPrompt = payload.result.PromptBlock
		a.pendingMCPTools = append([]tools.Tool(nil), payload.result.Tools...)
		a.pendingMCPReplace = true
		a.mcpServersPromptMu.Unlock()

		// Ask the next request to compare the pending runtime MCP surface with the
		// frozen request surface. If toggles return to the same prompt/tools, the
		// existing context surface is reused to preserve provider cache stability.
		a.markRuntimeSurfaceDirty()
	}
	a.markMCPReady()
	a.mcpTransitionActive.Store(false)
	a.NotifyEnvStatusUpdated()

	if payload.err != nil {
		msg := summarizeMCPControlError(payload.err)
		if msg != "" {
			a.emitToTUI(ToastEvent{Message: msg, Level: "error"})
		}
	}

	// Resume queued input only when no turn is active. Busy MCP changes are
	// applied to the next request surface without clearing the in-flight turn.
	if a.turn == nil {
		a.setIdleAndDrainPending()
	}
}
