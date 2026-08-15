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
	// SessionDir is captured on the agent event loop when the request is
	// dispatched. The runtime persists the resulting intent into this session
	// even if the agent switches sessions while the control is in flight; the
	// intent belongs to the session that issued /mcp.
	SessionDir string
}

// MCPControlResult carries the post-operation MCP tool set, the prompt block
// describing connected servers, and the manual server names whose enable/disable
// transition actually succeeded (partial-success reporting).
type MCPControlResult struct {
	Tools       []tools.Tool
	PromptBlock string
	Enabled     []string
	Disabled    []string
}

type mcpControlDonePayload struct {
	req    MCPControlRequest
	result MCPControlResult
	err    error
	// readyGen is the readiness generation created when the control was
	// dispatched. A session switch or MCP startup that begins afterwards
	// replaces the barrier; the stale result must then not be staged onto the
	// new session's surface nor release the new barrier.
	readyGen MCPReadyGeneration
}

// SetMCPControlFunc installs the runtime callback used to connect/disconnect MCP servers.
// The callback runs in a background goroutine; results are applied on the agent event loop.
func (a *MainAgent) SetMCPControlFunc(fn func(context.Context, MCPControlRequest) (MCPControlResult, error)) {
	a.mcpControlFn = fn
}

// MCPReadyGeneration is an opaque readiness barrier token for one background
// MCP catalog build. It is created when MCP startup or runtime control begins
// so the next request blocks until the new tool surface is ready.
type MCPReadyGeneration struct {
	id    uint64
	ready chan struct{}
}

// ResetMCPReady starts a new MCP readiness generation.
func (a *MainAgent) ResetMCPReady() MCPReadyGeneration {
	a.mcpReadyMu.Lock()
	defer a.mcpReadyMu.Unlock()
	a.mcpReadyGeneration++
	a.mcpReady = make(chan struct{})
	return MCPReadyGeneration{id: a.mcpReadyGeneration, ready: a.mcpReady}
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
	a.markRuntimeSurfaceDirty()
	gen := a.ResetMCPReady()
	a.NotifyEnvStatusUpdated()

	req.SessionDir = a.SessionDir()
	go func() {
		res, err := a.mcpControlFn(a.parentCtx, req)
		a.sendEvent(Event{Type: EventMCPControlDone, Payload: mcpControlDonePayload{req: req, result: res, err: err, readyGen: gen}})
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

	changed := len(payload.result.Enabled) > 0 || len(payload.result.Disabled) > 0
	applied := a.applyMCPControlSurfaceForGeneration(payload.readyGen, func() {
		if payload.err != nil && !changed {
			return
		}
		// Defer MCP tool registration until ensureSessionBuilt, so any in-flight turn
		// continues with the tool surface it was started with and the next request
		// (including an automatic retry) sees matching tools + system prompt.
		fullInjectionSurface := a.effectiveMCPMountIsFullInjection()
		a.mcpServersPromptMu.Lock()
		a.mcpRuntimePrompt = payload.result.PromptBlock
		// Cache-friendly mounts keep the discoverability prompt stable; the
		// provider-specific declaration is request-only and is replayed at its
		// anchor. Full injection — including a cache-friendly mount that has
		// fallen back — still updates the prompt with the catalog.
		if fullInjectionSurface {
			a.mcpServersPrompt = payload.result.PromptBlock
		}
		a.pendingMCPTools = append([]tools.Tool(nil), payload.result.Tools...)
		a.pendingMCPReplace = true
		a.mcpServersPromptMu.Unlock()

		// Ask the next request to compare the pending runtime MCP surface with the
		// frozen request surface. If toggles return to the same prompt/tools, the
		// existing context surface is reused to preserve provider cache stability.
		a.markRuntimeSurfaceDirty()
	})
	a.mcpTransitionActive.Store(false)
	a.NotifyEnvStatusUpdated()
	if !applied {
		log.Warnf("MCP control finished after a session switch; result not applied to the new session action=%v enabled=%v disabled=%v", payload.req.Action, payload.result.Enabled, payload.result.Disabled)
	}

	scope := ""
	if !applied {
		scope = " for the previous session"
	}
	switch {
	case len(payload.result.Enabled) > 0 && len(payload.result.Disabled) == 0:
		a.emitToTUI(ToastEvent{Message: "MCP enabled" + scope + ": " + strings.Join(payload.result.Enabled, ", "), Level: "info"})
	case len(payload.result.Disabled) > 0 && len(payload.result.Enabled) == 0:
		a.emitToTUI(ToastEvent{Message: "MCP disabled" + scope + ": " + strings.Join(payload.result.Disabled, ", "), Level: "info"})
	case len(payload.result.Enabled) > 0 && len(payload.result.Disabled) > 0:
		a.emitToTUI(ToastEvent{Message: "MCP changed" + scope + " (enabled: " + strings.Join(payload.result.Enabled, ", ") + "; disabled: " + strings.Join(payload.result.Disabled, ", ") + ")", Level: "info"})
	}
	if applied && changed {
		a.emitMCPSurfaceChangeToast(payload.result)
	}
	if msg := summarizeMCPControlError(payload.err); msg != "" {
		a.emitToTUI(ToastEvent{Message: msg, Level: "error"})
	}

	// Resume queued input only when no turn is active. Busy MCP changes are
	// applied to the next request surface without clearing the in-flight turn.
	if a.turn == nil {
		a.setIdleAndDrainPending()
	}
}

// emitMCPSurfaceChangeToast surfaces the cache cost of changing the complete
// top-level MCP tool surface.
func (a *MainAgent) emitMCPSurfaceChangeToast(result MCPControlResult) {
	if !a.mcpControlChangesTopLevelSurface(result) {
		return
	}
	a.emitToTUI(ToastEvent{
		Message: "MCP tool surface changed; the next request may miss the prompt cache",
		Level:   "warn",
	})
}

func (a *MainAgent) mcpControlChangesTopLevelSurface(result MCPControlResult) bool {
	if a.effectiveMCPMountIsFullInjection() {
		return true
	}
	changedServers := append(append([]string(nil), result.Enabled...), result.Disabled...)
	visibility := a.mcpVisibilitySnapshot(changedServers)
	a.mcpMountState.mu.Lock()
	defer a.mcpMountState.mu.Unlock()
	for _, server := range changedServers {
		for _, name := range visibility.visibleToolNames(server) {
			if _, ok := a.mcpMountState.topLevelManual[name]; ok {
				return true
			}
		}
	}
	return false
}

func (a *MainAgent) activateMCPMountFallback() {
	a.mcpServersPromptMu.Lock()
	a.mcpServersPrompt = a.mcpRuntimePrompt
	a.mcpServersPromptMu.Unlock()
	a.markRuntimeSurfaceDirty()
}
