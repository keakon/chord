package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/mcp"
)

func TestHeadlessSendCommandReturnsBareMCPStatus(t *testing.T) {
	to := newTestOut()
	backend := &mockBackend{}
	state := &headlessState{mcp: mcp.RuntimeSnapshot{SchemaVersion: mcp.RuntimeSnapshotSchemaVersion, BindingID: "view", Version: 1, Servers: []mcp.ServerView{}}}
	handleHeadlessCommand(headlessCommand{Type: "send", Content: "/mcp"}, backend, state, to.writer())
	envs := to.drain()
	env := findHeadlessEnvelopeValue(envs, "mcp_status_response")
	if env == nil || findHeadlessEnvelopeValue(envs, "error") != nil {
		t.Fatalf("expected status response: %v", envs)
	}
	payload, _ := env.Payload.(map[string]any)
	if payload["binding_id"] != "view" {
		t.Fatalf("wrong MCP view: %#v", payload)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.sentMessages) != 0 {
		t.Fatalf("status reached the model: %v", backend.sentMessages)
	}
}

func TestHeadlessUnsupportedRemoteCommand(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"/models", false},
		{"/models fast", false},
		{"/models status", false},
		{"/models --agent reviewer strong", false},
		{"/new", false},
		{"/resume abc", false},
		{"/export", true},
		{"hello world", false},
		{"/help", false},
		{"", false},
		{"use /models in your code", false}, // /model is not the first word
	}

	for _, tt := range tests {
		got := isUnsupportedHeadlessCommand(tt.content)
		if got != tt.want {
			t.Errorf("isUnsupportedHeadlessCommand(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}

func TestHeadlessLocalShellCommandEmitsResult(t *testing.T) {
	to := newTestOut()
	backend := &mockBackend{}
	state := &headlessState{}

	handleHeadlessCommand(headlessCommand{Type: "local_shell", Command: "printf chord-local-shell"}, backend, state, to.writer())

	items := to.drain()
	env := findHeadlessEnvelopeValue(items, "local_shell_result")
	if env == nil {
		t.Fatalf("missing local_shell_result in %#v", items)
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload["command"] != "printf chord-local-shell" {
		t.Fatalf("command = %q", payload["command"])
	}
	if payload["output"] != "chord-local-shell" {
		t.Fatalf("output = %q", payload["output"])
	}
	if payload["failed"] != false {
		t.Fatalf("failed = %v", payload["failed"])
	}
}

func TestHeadlessLocalShellEmptyCommandEmitsFailure(t *testing.T) {
	to := newTestOut()
	backend := &mockBackend{}
	state := &headlessState{}

	handleHeadlessCommand(headlessCommand{Type: "local_shell", Command: "   "}, backend, state, to.writer())

	items := to.drain()
	env := findHeadlessEnvelopeValue(items, "local_shell_result")
	if env == nil {
		t.Fatalf("missing local_shell_result in %#v", items)
	}
	payload, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T", env.Payload)
	}
	if payload["failed"] != true {
		t.Fatalf("failed = %v", payload["failed"])
	}
	if !strings.Contains(payload["error"].(string), "empty local shell command") {
		t.Fatalf("error = %q", payload["error"])
	}
}

func TestHeadlessSendCommandUnsupportedRejection(t *testing.T) {
	state := &headlessState{}

	tests := []struct {
		content string
		want    bool // true = should be rejected (error envelope emitted)
	}{
		{"/models", false},
		{"/models fast", false},
		{"/new session", false},
		{"/resume abc", false},
		{"/export", true},
		{"do something", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			to := newTestOut()
			backend := &mockBackend{}

			cmd := headlessCommand{Type: "send", Content: tt.content}
			handleHeadlessCommand(cmd, backend, state, to.writer())

			items := to.drain()
			if tt.want {
				if len(items) == 0 {
					t.Error("expected error envelope for unsupported command")
				} else if items[0].Type != "error" {
					t.Errorf("envelope type = %q, want error", items[0].Type)
				}
			} else {
				for _, item := range items {
					if item.Type == "error" {
						t.Errorf("unexpected error envelope for supported command: %v", item)
					}
				}
				backend.mu.Lock()
				sent := len(backend.sentMessages)
				backend.mu.Unlock()
				if sent != 1 {
					t.Errorf("expected 1 message sent to backend, got %d", sent)
				}
			}
		})
	}
}

func TestHeadlessStatusCommand(t *testing.T) {
	state := &headlessState{
		sessionID:   "test-session",
		busy:        true,
		phase:       "streaming",
		phaseDetail: "analyzing code",
		lastOutcome: "completed",
		pendingConfirm: &headlessConfirmPayload{
			ToolName:  "Shell",
			RequestID: "req-1",
		},
		pendingHandoff: &headlessHandoffPayload{
			RequestID: "handoff-1",
			PlanPath:  "/tmp/plan.md",
		},
		lastError: "",
		updatedAt: time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC),
	}
	to := newTestOut()
	backend := &mockBackend{}

	cmd := headlessCommand{Type: "status"}
	handleHeadlessCommand(cmd, backend, state, to.writer())

	items := to.drain()
	if len(items) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(items))
	}
	env := items[0]
	if env.Type != "status_response" {
		t.Errorf("type = %q, want status_response", env.Type)
	}

	data, err := json.Marshal(env.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["session_id"] != "test-session" {
		t.Errorf("session_id = %v, want test-session", payload["session_id"])
	}
	if payload["busy"] != true {
		t.Errorf("busy = %v, want true", payload["busy"])
	}
	if payload["phase"] != "streaming" {
		t.Errorf("phase = %v, want streaming", payload["phase"])
	}
	if payload["last_outcome"] != "completed" {
		t.Errorf("last_outcome = %v, want completed", payload["last_outcome"])
	}
	pendingHandoff, ok := payload["pending_handoff"].(map[string]any)
	if !ok {
		t.Fatalf("pending_handoff = %#v, want object", payload["pending_handoff"])
	}
	if pendingHandoff["request_id"] != "handoff-1" || pendingHandoff["plan_path"] != "/tmp/plan.md" {
		t.Errorf("pending_handoff = %#v, want handoff-1 /tmp/plan.md", pendingHandoff)
	}
}

func TestHeadlessStatusResponseLastOutcome(t *testing.T) {
	tests := []struct {
		name           string
		lastOutcome    string
		pendingOutcome string
		want           string
	}{
		{
			name:        "completed",
			lastOutcome: "completed",
			want:        "completed",
		},
		{
			name:        "error",
			lastOutcome: "error",
			want:        "error",
		},
		{
			name:        "cancelled",
			lastOutcome: "cancelled",
			want:        "cancelled",
		},
		{
			name:        "empty (no idle yet)",
			lastOutcome: "",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &headlessState{
				lastOutcome: tt.lastOutcome,
			}
			to := newTestOut()
			backend := &mockBackend{}

			cmd := headlessCommand{Type: "status"}
			handleHeadlessCommand(cmd, backend, state, to.writer())

			items := to.drain()
			if len(items) != 1 {
				t.Fatalf("expected 1 envelope, got %d", len(items))
			}

			data, err := json.Marshal(items[0].Payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}

			got, _ := payload["last_outcome"].(string)
			if got != tt.want {
				t.Errorf("last_outcome = %q, want %q", got, tt.want)
			}

			// last_outcome in status_response must equal state.lastOutcome
			if got != state.lastOutcome {
				t.Errorf("status last_outcome = %q != state.lastOutcome = %q", got, state.lastOutcome)
			}
		})
	}
}
