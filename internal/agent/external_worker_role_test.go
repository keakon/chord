package agent

import (
	"slices"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestExternalBackendCannotBecomeMainRole(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	builder := &config.AgentConfig{Name: "builder", Mode: config.AgentModeMain}
	external := &config.AgentConfig{
		Name: "external", Mode: config.AgentModeMain,
		Backend: &config.BackendConfig{Kind: config.BackendKindClaudeCode},
	}
	a.SetAgentConfigs(map[string]*config.AgentConfig{"builder": builder, "external": external})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "Keep this message."})
	before := len(a.ctxMgr.Snapshot())
	for _, clearHistory := range []bool{false, true} {
		if err := a.switchRole("external", clearHistory); err == nil {
			t.Fatal("external backend accepted as main role")
		}
	}
	if err := a.SwitchRole("external"); err == nil {
		t.Fatal("SwitchRole accepted an external backend")
	}
	if a.currentActiveConfig() != builder || len(a.ctxMgr.Snapshot()) != before {
		t.Fatal("failed switch mutated the main role or history")
	}
	if slices.Contains(a.AvailableRoles(), external.Name) || slices.Contains(a.AvailableAgents(), external.Name) {
		t.Fatal("external backend offered as a main role")
	}
	a.activeConfig = external
	a.SetAgentConfigs(map[string]*config.AgentConfig{"builder": builder, "external": external})
	if a.currentActiveConfig() != builder {
		t.Fatal("configuration refresh retained an external main role")
	}
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeSubAgent, Backend: external.Backend},
	})
	if a.currentActiveConfig() != nil {
		t.Fatal("external builder selected as a main role")
	}
}
