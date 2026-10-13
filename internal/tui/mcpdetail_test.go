package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/mcp"
)

type authPanelAgent struct {
	*infoPanelAgent
	snapshot     mcp.RuntimeSnapshot
	url          string
	actions      []agent.MCPAuthAction
	role         string
	roleSnapshot mcp.RuntimeSnapshot
}

func (a *authPanelAgent) MCPRuntimeSnapshot() mcp.RuntimeSnapshot  { return a.snapshot.Clone() }
func (a *authPanelAgent) MCPAuthorizationURL(mcp.ServerRef) string { return a.url }
func (a *authPanelAgent) MCPRuntimeSnapshotForAgent(role string) (mcp.RuntimeSnapshot, error) {
	a.role = role
	return a.roleSnapshot.Clone(), nil
}
func (a *authPanelAgent) RequestMCPAuth(req agent.MCPAuthRequest) {
	a.role = req.AgentDefinition
	a.actions = append(a.actions, req.Action)
}

func TestMCPDetailClosingDoesNotCancelAuthorization(t *testing.T) {
	ref := mcp.ServerRef{WorkspaceID: "workspace", Owner: mcp.MainOwner, ServerName: "sample"}
	backend := &authPanelAgent{infoPanelAgent: newInfoPanelAgent(), snapshot: mcp.RuntimeSnapshot{Servers: []mcp.ServerView{{Ref: ref, Auth: mcp.AuthMissing, Connection: mcp.ConnectionDisabled, Catalog: mcp.CatalogUnknown}}}, url: "https://example.invalid/authorize?state=sample"}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "sample", Manual: true, Disabled: true}}
	model := NewModelWithSize(backend, 110, 40)
	model.openMCPSelect()
	model.openMCPDetail(ref, "")
	if body := model.renderMCPDetailDialog(); !strings.Contains(body, "signing in will not enable") || !strings.Contains(body, "Authentication") {
		t.Fatalf("missing detail state: %s", body)
	}
	model.handleMCPDetailKey(tea.KeyPressMsg(tea.Key{Code: 'l'}))
	model.handleMCPDetailKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if len(backend.actions) != 1 || backend.actions[0] != agent.MCPAuthLogin {
		t.Fatal("closing overlay cancelled authentication")
	}
	model.openMCPDetail(ref, "")
	model.handleMCPDetailKey(tea.KeyPressMsg(tea.Key{Code: 'c'}))
	if len(backend.actions) != 2 || backend.actions[1] != agent.MCPAuthCancel {
		t.Fatal("explicit cancellation was not dispatched")
	}
}

func TestMCPDetailExplicitAgentUsesSelectedView(t *testing.T) {
	ref := mcp.ServerRef{WorkspaceID: "workspace", Owner: "explorer", ServerName: "private"}
	backend := &authPanelAgent{infoPanelAgent: newInfoPanelAgent(), roleSnapshot: mcp.RuntimeSnapshot{Servers: []mcp.ServerView{{Ref: ref, Auth: mcp.AuthReady}}}}
	model := NewModelWithSize(backend, 110, 40)
	model.openMCPDetail(ref, "explorer")
	if model.mode != ModeMCPSelect {
		t.Fatal("private server detail did not open")
	}
	if body := model.renderMCPDetailDialog(); !strings.Contains(body, "Authentication") || strings.Contains(body, "no longer visible") {
		t.Fatalf("wrong role view: %s", body)
	}
	model.handleMCPDetailKey(tea.KeyPressMsg(tea.Key{Code: 'l'}))
	if backend.role != "explorer" || len(backend.actions) != 1 {
		t.Fatal("explicit role lost")
	}
}

func TestMCPDetailShowsDirectOAuthConnectivity(t *testing.T) {
	ref := mcp.ServerRef{WorkspaceID: "workspace", Owner: mcp.MainOwner, ServerName: "sample"}
	backend := &authPanelAgent{infoPanelAgent: newInfoPanelAgent(), snapshot: mcp.RuntimeSnapshot{Servers: []mcp.ServerView{{Ref: ref, Diagnostic: mcp.Diagnostic{ControlNetworkMode: mcp.ControlNetworkDirect, ProxyEnvironmentDetected: true}}}}}
	model := NewModelWithSize(backend, 110, 40)
	model.openMCPDetail(ref, "")
	if body := model.renderMCPDetailDialog(); !strings.Contains(body, "direct connection") || !strings.Contains(body, "Proxy environment detected") {
		t.Fatalf("missing connectivity guidance: %s", body)
	}
}
