package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/mcp"
)

type MCPAuthAction string

const (
	MCPAuthLogin       MCPAuthAction = "login"
	MCPAuthReauthorize MCPAuthAction = "reauth"
	MCPAuthLogout      MCPAuthAction = "logout"
	MCPAuthReconnect   MCPAuthAction = "reconnect"
	MCPAuthCancel      MCPAuthAction = "cancel"
)
const (
	EventMCPAuthControl  = "mcp_auth_control"
	EventMCPAuthProgress = "mcp_auth_progress"
	EventMCPAuthDone     = "mcp_auth_done"
)

type MCPAuthRequest struct {
	AgentDefinition string
	Action          MCPAuthAction
	Ref             mcp.ServerRef
	SessionDir      string
}
type mcpAuthOperation struct {
	id      uint64
	request MCPAuthRequest
	cancel  context.CancelFunc
	state   mcp.OperationState
	started time.Time
	url     string
}
type mcpAuthProgress struct {
	ref      mcp.ServerRef
	id       uint64
	progress mcp.LoginProgress
}
type mcpAuthDone struct {
	ref mcp.ServerRef
	id  uint64
	err error
}

// MCPAuthLocalProgressEvent is ephemeral local UI data. Bridges must never
// forward it to headless subscribers, transcripts, or generic event dumps.
type MCPAuthLocalProgressEvent struct {
	Ref              mcp.ServerRef
	AuthorizationURL string
}
type MCPDetailEvent struct {
	Ref             mcp.ServerRef
	AgentDefinition string
}

type MCPAuthStateProvider interface {
	MCPRuntimeSnapshot() mcp.RuntimeSnapshot
	MCPRuntimeSnapshotForAgent(string) (mcp.RuntimeSnapshot, error)
	RequestMCPAuth(MCPAuthRequest)
	MCPAuthorizationURL(mcp.ServerRef) string
}

func (a *MainAgent) SetMCPAuthControlFunc(fn func(context.Context, MCPAuthRequest, func(mcp.LoginProgress)) error) {
	a.mcpAuthMu.Lock()
	defer a.mcpAuthMu.Unlock()
	a.mcpAuthControlFn = fn
}
func (a *MainAgent) RequestMCPAuth(req MCPAuthRequest) {
	a.sendEvent(Event{Type: EventMCPAuthControl, Payload: req})
}
func (a *MainAgent) MCPAuthorizationURL(ref mcp.ServerRef) string {
	a.mcpAuthMu.Lock()
	defer a.mcpAuthMu.Unlock()
	if op := a.mcpAuthOperations[ref]; op != nil {
		return op.url
	}
	return ""
}
func (a *MainAgent) cancelMCPAuthorizations() {
	a.mcpAuthMu.Lock()
	defer a.mcpAuthMu.Unlock()
	a.cancelMCPAuthorizationsLocked()
}

// stopMCPAuthorizations closes admission before shutdown waits for workers.
// Session switches only cancel current operations and keep admission open.
func (a *MainAgent) stopMCPAuthorizations() {
	a.mcpAuthMu.Lock()
	defer a.mcpAuthMu.Unlock()
	a.mcpAuthClosed = true
	a.cancelMCPAuthorizationsLocked()
}

func (a *MainAgent) cancelMCPAuthorizationsLocked() {
	for ref, op := range a.mcpAuthOperations {
		op.cancel()
		delete(a.mcpAuthOperations, ref)
	}
}
func (a *MainAgent) handleMCPAuthControlEvent(event Event) {
	req, ok := event.Payload.(MCPAuthRequest)
	if !ok {
		return
	}
	visible := false
	snapshot := a.MCPRuntimeSnapshot()
	if req.AgentDefinition != "" {
		var err error
		snapshot, err = a.MCPRuntimeSnapshotForAgent(req.AgentDefinition)
		if err != nil {
			return
		}
	}
	for _, server := range snapshot.Servers {
		if server.Ref == req.Ref {
			visible = true
			break
		}
	}
	if !visible {
		a.emitToTUI(ToastEvent{Level: "error", Message: "MCP server is not available for the active role"})
		return
	}
	req.SessionDir = a.SessionDir()
	a.mcpAuthMu.Lock()
	if a.mcpAuthOperations == nil {
		a.mcpAuthOperations = map[mcp.ServerRef]*mcpAuthOperation{}
	}
	if req.Action == MCPAuthCancel {
		if op := a.mcpAuthOperations[req.Ref]; op != nil {
			op.cancel()
			delete(a.mcpAuthOperations, req.Ref)
		}
		a.mcpAuthMu.Unlock()
		a.RefreshMCPStatusSnapshot()
		return
	}
	if old := a.mcpAuthOperations[req.Ref]; old != nil {
		if req.Action == MCPAuthLogout {
			old.cancel()
		} else {
			a.mcpAuthMu.Unlock()
			a.emitToTUI(ToastEvent{Level: "warn", Message: "An MCP authentication operation is already running"})
			return
		}
	}
	fn := a.mcpAuthControlFn
	if fn == nil {
		a.mcpAuthMu.Unlock()
		a.emitToTUI(ToastEvent{Level: "warn", Message: "Sign in with chord mcp login on the host"})
		return
	}
	if a.mcpAuthClosed {
		a.mcpAuthMu.Unlock()
		return
	}
	select {
	case <-a.parentCtx.Done():
		a.mcpAuthMu.Unlock()
		return
	case <-a.stoppingCh:
		a.mcpAuthMu.Unlock()
		return
	default:
	}
	a.mcpAuthOperationSeq++
	id := a.mcpAuthOperationSeq
	ctx, cancel := context.WithCancel(a.parentCtx)
	a.mcpAuthOperations[req.Ref] = &mcpAuthOperation{id: id, request: req, cancel: cancel, started: time.Now().UTC(), state: mcp.OperationDiscovering}
	a.mcpAuthWg.Add(1)
	a.mcpAuthMu.Unlock()
	go func() {
		defer a.mcpAuthWg.Done()
		defer cancel()
		err := fn(ctx, req, func(progress mcp.LoginProgress) {
			a.sendEventUntil(Event{Type: EventMCPAuthProgress, Payload: mcpAuthProgress{req.Ref, id, progress}}, ctx.Done())
		})
		a.sendEventUntil(Event{Type: EventMCPAuthDone, Payload: mcpAuthDone{req.Ref, id, err}}, ctx.Done())
	}()
	a.RefreshMCPStatusSnapshot()
}
func (a *MainAgent) handleMCPAuthProgressEvent(event Event) {
	p, ok := event.Payload.(mcpAuthProgress)
	if !ok {
		return
	}
	a.mcpAuthMu.Lock()
	op := a.mcpAuthOperations[p.ref]
	if op == nil || op.id != p.id {
		a.mcpAuthMu.Unlock()
		return
	}
	op.state = p.progress.State
	op.url = p.progress.AuthorizationURL
	a.mcpAuthMu.Unlock()
	if p.progress.AuthorizationURL != "" {
		a.emitToTUI(MCPAuthLocalProgressEvent{Ref: p.ref, AuthorizationURL: p.progress.AuthorizationURL})
	}
	a.RefreshMCPStatusSnapshot()
}
func (a *MainAgent) handleMCPAuthDoneEvent(event Event) {
	done, ok := event.Payload.(mcpAuthDone)
	if !ok {
		return
	}
	a.mcpAuthMu.Lock()
	op := a.mcpAuthOperations[done.ref]
	if op == nil || op.id != done.id {
		a.mcpAuthMu.Unlock()
		return
	}
	delete(a.mcpAuthOperations, done.ref)
	a.mcpAuthMu.Unlock()
	if done.err != nil {
		a.emitToTUI(ToastEvent{Level: "warn", Message: "MCP authentication operation did not complete: " + done.err.Error()})
	} else {
		a.emitToTUI(ToastEvent{Level: "info", Message: "MCP authentication operation completed"})
	}
	a.RefreshMCPStatusSnapshot()
}

func (a *MainAgent) handleMCPAuthCommand(fields []string) error {
	role := ""
	var positional []string
	for i := 1; i < len(fields); i++ {
		if fields[i] == "--agent" {
			i++
			if i == len(fields) {
				return fmt.Errorf("--agent requires a role name")
			}
			role = fields[i]
		} else {
			positional = append(positional, fields[i])
		}
	}
	snapshot := a.MCPRuntimeSnapshot()
	if role != "" {
		var err error
		snapshot, err = a.MCPRuntimeSnapshotForAgent(role)
		if err != nil {
			return err
		}
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: /mcp %s <server> [--agent <definition>]", fields[0])
	}
	for _, server := range snapshot.Servers {
		if server.Ref.ServerName == positional[0] {
			if fields[0] == "status" {
				a.emitToTUI(MCPDetailEvent{Ref: server.Ref, AgentDefinition: role})
			} else {
				a.handleMCPAuthControlEvent(Event{Payload: MCPAuthRequest{Action: MCPAuthAction(fields[0]), Ref: server.Ref, AgentDefinition: role}})
			}
			return nil
		}
	}
	return fmt.Errorf("MCP server %q is unavailable", strings.TrimSpace(positional[0]))
}

func (MCPAuthLocalProgressEvent) agentEvent() {}
func (MCPDetailEvent) agentEvent()            {}
