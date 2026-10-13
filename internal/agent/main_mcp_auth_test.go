package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
)

func authControlTestAgent(t *testing.T) (*MainAgent, mcp.ServerRef, *mcp.Manager) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	ref := mcp.ServerRef{WorkspaceID: "sample-workspace", Owner: mcp.MainOwner, ServerName: "sample"}
	target := mcp.Target{Ref: ref, ConfigGeneration: 1, Config: mcp.ServerConfig{Name: ref.ServerName, URL: "https://example.invalid/mcp", Auth: config.MCPAuthNone, Manual: true}}
	manager := mcp.NewPendingManagerWithClientInfo([]mcp.Target{target}, mcp.ClientInfo{Name: "chord-test", Version: "test"}, nil)
	t.Cleanup(manager.Close)
	a.SetMCPRuntime([]mcp.Target{target}, manager, nil, t.TempDir())
	a.SetMCPStatusSnapshotFunc(manager.Snapshot)
	drainAgentEvents(a.Events())
	return a, ref, manager
}

func TestMCPAuthWaitingDoesNotChangeReadinessOrManualIntent(t *testing.T) {
	a, ref, manager := authControlTestAgent(t)
	ready := a.ResetMCPReady()
	a.markMCPReady()
	started, stopped := make(chan struct{}), make(chan struct{})
	a.SetMCPAuthControlFunc(func(ctx context.Context, request MCPAuthRequest, progress func(mcp.LoginProgress)) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	a.handleMCPAuthControlEvent(Event{Payload: MCPAuthRequest{Action: MCPAuthLogin, Ref: ref}})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("authentication callback did not start")
	}
	outputsDrained := make(chan struct{})
	go func() {
		a.outputWg.Wait()
		close(outputsDrained)
	}()
	select {
	case <-outputsDrained:
	case <-time.After(time.Second):
		t.Fatal("interactive authorization blocks session output drain")
	}
	if current := a.CurrentMCPReadyGeneration(); current.id != ready.id {
		t.Fatal("interactive authorization reset global readiness")
	}
	select {
	case <-ready.ready:
	default:
		t.Fatal("interactive authorization blocks readiness")
	}
	for _, server := range manager.Snapshot().Servers {
		if server.DesiredEnabled {
			t.Fatal("login enabled a manual server")
		}
	}
	a.cancelMCPAuthorizations()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("authorization cancellation did not stop callback")
	}
}

func TestMCPAuthSessionCancellationDropsLateProgressURL(t *testing.T) {
	a, ref, _ := authControlTestAgent(t)
	started, stopped := make(chan struct{}, 2), make(chan struct{}, 2)
	a.SetMCPAuthControlFunc(func(ctx context.Context, request MCPAuthRequest, progress func(mcp.LoginProgress)) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return ctx.Err()
	})
	a.handleMCPAuthControlEvent(Event{Payload: MCPAuthRequest{Action: MCPAuthLogin, Ref: ref}})
	<-started
	a.mcpAuthMu.Lock()
	oldID := a.mcpAuthOperations[ref].id
	a.mcpAuthMu.Unlock()
	a.cancelMCPAuthorizations()
	<-stopped
	// A new operation for the same visible server must not accept an old
	// session's delayed callback event, even after its cancellation completed.
	a.handleMCPAuthControlEvent(Event{Payload: MCPAuthRequest{Action: MCPAuthLogin, Ref: ref}})
	<-started
	drainAgentEvents(a.Events())
	link := "https://example.invalid/authorize?state=sample-state&code_challenge=sample-challenge"
	a.handleMCPAuthProgressEvent(Event{Payload: mcpAuthProgress{ref: ref, id: oldID, progress: mcp.LoginProgress{State: mcp.OperationAwaitingCallback, AuthorizationURL: link}}})
	a.handleMCPAuthDoneEvent(Event{Payload: mcpAuthDone{ref: ref, id: oldID, err: context.Canceled}})
	if a.MCPAuthorizationURL(ref) != "" {
		t.Fatal("old authorization URL entered the current session")
	}
	for _, event := range drainAgentEvents(a.Events()) {
		if _, ok := event.(MCPAuthLocalProgressEvent); ok {
			t.Fatal("late authorization URL was emitted")
		}
	}
	encoded, err := json.Marshal(a.MCPRuntimeSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sample-state") || strings.Contains(string(encoded), "code_challenge") {
		t.Fatal("runtime status leaked authorization material")
	}
	a.mcpAuthMu.Lock()
	active := a.mcpAuthOperations[ref]
	a.mcpAuthMu.Unlock()
	if active == nil || active.id == oldID {
		t.Fatal("old done event removed current authorization")
	}
	a.cancelMCPAuthorizations()
	<-stopped
}

func TestMCPAuthExplicitCancelClearsOperationAndRejectsLateEvents(t *testing.T) {
	a, ref, _ := authControlTestAgent(t)
	started, stopped := make(chan struct{}, 2), make(chan struct{}, 2)
	a.SetMCPAuthControlFunc(func(ctx context.Context, request MCPAuthRequest, progress func(mcp.LoginProgress)) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return ctx.Err()
	})
	a.handleMCPAuthControlEvent(Event{Payload: MCPAuthRequest{Action: MCPAuthLogin, Ref: ref}})
	<-started
	a.mcpAuthMu.Lock()
	oldID := a.mcpAuthOperations[ref].id
	a.mcpAuthMu.Unlock()
	link := "https://example.invalid/authorize?state=sample-state"
	late := Event{Payload: mcpAuthProgress{ref: ref, id: oldID, progress: mcp.LoginProgress{State: mcp.OperationAwaitingCallback, AuthorizationURL: link}}}
	a.handleMCPAuthProgressEvent(late)
	if a.MCPAuthorizationURL(ref) != link {
		t.Fatal("authorization URL not published before cancellation")
	}
	a.handleMCPAuthControlEvent(Event{Payload: MCPAuthRequest{Action: MCPAuthCancel, Ref: ref}})
	<-stopped
	if a.MCPAuthorizationURL(ref) != "" {
		t.Fatal("cancelled authorization URL remained visible")
	}
	for _, server := range a.MCPRuntimeSnapshot().Servers {
		if len(server.Operations) != 0 {
			t.Fatal("cancelled authorization remained in status snapshot")
		}
	}
	drainAgentEvents(a.Events())
	a.handleMCPAuthProgressEvent(late)
	a.handleMCPAuthDoneEvent(Event{Payload: mcpAuthDone{ref: ref, id: oldID, err: context.Canceled}})
	if events := drainAgentEvents(a.Events()); len(events) != 0 {
		t.Fatal("cancelled operation emitted late UI events")
	}
	// Cancellation immediately releases the operation slot; delayed completion
	// from that operation cannot remove a replacement for the same server.
	a.handleMCPAuthControlEvent(Event{Payload: MCPAuthRequest{Action: MCPAuthLogin, Ref: ref}})
	<-started
	a.handleMCPAuthProgressEvent(late)
	a.handleMCPAuthDoneEvent(Event{Payload: mcpAuthDone{ref: ref, id: oldID}})
	a.mcpAuthMu.Lock()
	current := a.mcpAuthOperations[ref]
	a.mcpAuthMu.Unlock()
	if current == nil || current.id == oldID || a.MCPAuthorizationURL(ref) != "" {
		t.Fatal("late events changed the replacement operation")
	}
	a.cancelMCPAuthorizations()
	<-stopped
	a.mcpAuthWg.Wait()
}
