package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/mcp"
)

func TestHeadlessMCPStatusPushAndCache(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{headlessMCPStatusChanged: true}}
	snapshot := mcp.RuntimeSnapshot{SchemaVersion: 1, BindingID: "view", Version: 2, Servers: []mcp.ServerView{{Ref: mcp.ServerRef{Owner: "main", ServerName: "sample"}, Auth: mcp.AuthMissing, ScopeRequirements: []mcp.ScopeRequirementView{{Scopes: []string{"files:read"}}}}}}
	events := filterHeadlessEvent(agent.MCPStatusChangedEvent{Snapshot: snapshot}, state)
	if len(events) != 1 || events[0].Type != headlessMCPStatusChanged || events[0].Seq == 0 {
		t.Fatalf("push = %#v", events)
	}
	snapshot.Servers[0].ScopeRequirements[0].Scopes[0] = "changed"
	if state.mcp.Servers[0].ScopeRequirements[0].Scopes[0] != "files:read" {
		t.Fatal("cache aliases input")
	}
	if events := filterHeadlessEvent(agent.MCPStatusChangedEvent{Snapshot: mcp.RuntimeSnapshot{SchemaVersion: 1, BindingID: "view", Version: 1}}, state); len(events) != 0 || state.mcp.Version != 2 {
		t.Fatal("old same-view snapshot applied")
	}
	if events := filterHeadlessEvent(agent.MCPAuthLocalProgressEvent{AuthorizationURL: "https://auth.example.invalid/?code=private"}, state); len(events) != 0 {
		t.Fatal("authorization URL escaped to headless")
	}
}
func TestHeadlessMCPStatusUpdatesWithoutSubscription(t *testing.T) {
	state := &headlessState{subscriptions: map[string]bool{}}
	events := filterHeadlessEvent(agent.MCPStatusChangedEvent{Snapshot: mcp.RuntimeSnapshot{SchemaVersion: 1, BindingID: "view", Version: 1}}, state)
	if len(events) != 0 || state.mcp.Version != 1 || state.seq == 0 {
		t.Fatal("unsubscribed change failed to advance cache and seq")
	}
}
func TestHeadlessMCPAuthCommandNeverReachesModel(t *testing.T) {
	for _, action := range []string{"login", "logout", "reauth", "reconnect", "cancel"} {
		backend := &mockBackend{}
		state := &headlessState{}
		out := newTestOut(t)
		handleHeadlessCommand(headlessCommand{Type: "send", RequestID: "request", Content: "/mcp " + action + " sample"}, backend, state, out.writer())
		envelopes := out.drain()
		if len(backend.sentMessages) != 0 {
			t.Fatal("remote auth command reached the model")
		}
		raw, _ := json.Marshal(envelopes)
		if !strings.Contains(string(raw), "host_login_required") {
			t.Fatalf("missing host guidance: %s", raw)
		}
	}
	if isHeadlessMCPCommand("/mcp enable sample") || isHeadlessMCPCommand("/mcp disable sample") {
		t.Fatal("enable or disable intercepted")
	}
}

func TestHeadlessMCPRejectsPreviousRoleSnapshot(t *testing.T) {
	backend := &mockBackend{currentRole: "explorer"}
	state := &headlessState{role: "explorer", subscriptions: map[string]bool{headlessMCPStatusChanged: true}}
	event := agent.MCPStatusChangedEvent{Role: "builder", Snapshot: mcp.RuntimeSnapshot{SchemaVersion: 1, BindingID: "previous-view", Version: 9, Servers: []mcp.ServerView{{Ref: mcp.ServerRef{Owner: "agent:builder", ServerName: "private"}}}}}
	if events := filterHeadlessEvent(event, state, backend); len(events) != 0 || len(state.mcp.Servers) != 0 {
		t.Fatal("previous role view was published")
	}
	event.Role = "explorer"
	event.Snapshot.BindingID = "current-view"
	if events := filterHeadlessEvent(event, state, backend); len(events) != 1 {
		t.Fatal("current role view was rejected")
	}
}
