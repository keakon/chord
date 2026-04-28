package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
)

type rulesAgentStub struct {
	sessionControlAgent
	added      []permission.AddedRule
	removedIdx []int
	removeErr  error
}

func (s *rulesAgentStub) AddedOverlayRules() []permission.AddedRule {
	out := make([]permission.AddedRule, len(s.added))
	copy(out, s.added)
	return out
}

func (s *rulesAgentStub) RemoveOverlayAddedRule(index int) error {
	s.removedIdx = append(s.removedIdx, index)
	if s.removeErr != nil {
		return s.removeErr
	}
	if index < 0 || index >= len(s.added) {
		return nil
	}
	s.added = append(s.added[:index], s.added[index+1:]...)
	return nil
}

func TestOpenRulesLoadsFromAgentOverlay(t *testing.T) {
	ag := &rulesAgentStub{
		sessionControlAgent: sessionControlAgent{
			events:      make(chan agent.AgentEvent),
			currentRole: "builder",
		},
		added: []permission.AddedRule{
			{Role: "builder", Rule: permission.Rule{Permission: "Bash", Pattern: "git *", Action: permission.ActionAllow}, Scope: permission.ScopeSession},
			{Role: "builder", Rule: permission.Rule{Permission: "Write", Pattern: "docs/*", Action: permission.ActionAllow}, Scope: permission.ScopeProject, Path: "/tmp/project/.chord/permissions/builder.yaml"},
		},
	}
	m := NewModel(ag)

	_ = m.openRules()
	if m.mode != ModeRules {
		t.Fatalf("mode = %v, want ModeRules", m.mode)
	}
	if !m.rules.fromAgent {
		t.Fatal("expected /rules to read from agent overlay")
	}
	if got := len(m.rules.rules); got != 2 {
		t.Fatalf("rules count = %d, want 2", got)
	}

	_ = m.handleRulesKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	if got := len(ag.removedIdx); got != 1 || ag.removedIdx[0] != 0 {
		t.Fatalf("removed indices = %v, want [0]", ag.removedIdx)
	}
	if got := len(m.rules.rules); got != 1 {
		t.Fatalf("rules count after delete = %d, want 1", got)
	}
}

func TestOpenRulesWithNoRulesShowsToastAndKeepsMode(t *testing.T) {
	ag := &rulesAgentStub{
		sessionControlAgent: sessionControlAgent{
			events:      make(chan agent.AgentEvent),
			currentRole: "builder",
		},
	}
	m := NewModel(ag)
	m.mode = ModeNormal

	cmd := m.openRules()
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want ModeNormal", m.mode)
	}
	if cmd == nil {
		t.Fatal("expected toast command")
	}
	if m.activeToast == nil || m.activeToast.Message != "No rules added this session" {
		t.Fatalf("active toast = %#v, want no-rules toast", m.activeToast)
	}
}

func TestAddSessionRuleBuildsPersistentPaths(t *testing.T) {
	ag := &sessionControlAgent{
		events:      make(chan agent.AgentEvent),
		currentRole: "builder",
		projectRoot: "/home/user/project-root",
	}
	m := NewModel(ag)

	m.workingDir = "/home/user/project-root/subdir"
	m.addSessionRule("Write", "docs/*", permission.ScopeProject)
	m.addSessionRule("Write", "*.md", permission.ScopeUserGlobal)

	if got := len(m.rules.rules); got != 2 {
		t.Fatalf("rules count = %d, want 2", got)
	}
	projectPath := m.rules.rules[0].Path
	if got, want := projectPath, filepath.Join("/home/user/project-root", ".chord", "permissions", "builder.yaml"); got != want {
		t.Fatalf("project path = %q, want %q", got, want)
	}
	globalPath := m.rules.rules[1].Path
	if !strings.HasSuffix(globalPath, filepath.Join(".chord", "permissions", "builder.yaml")) {
		t.Fatalf("global path = %q, want suffix .chord/permissions/builder.yaml", globalPath)
	}
	if globalPath == projectPath {
		t.Fatalf("global path should differ from project path, both are %q", projectPath)
	}
}

func TestResolveRuleScopePathUsesProjectRootInsteadOfWorkingDir(t *testing.T) {
	ag := &sessionControlAgent{
		events:      make(chan agent.AgentEvent),
		currentRole: "builder",
		projectRoot: "/repo/root",
	}
	m := NewModelWithSize(ag, 120, 30)
	m.mode = ModeConfirm
	m.workingDir = "/repo/root/subdir/deep"
	m.confirm.request = &ConfirmRequest{
		ToolName:  "Write",
		ArgsJSON:  `{"path":"docs/guide.md","content":"..."}`,
		RequestID: "",
	}
	m.enterRulePicker()
	m.confirm.scopeIdx = 1 // project

	plain := stripANSI(m.renderConfirmDialog())
	want := filepath.Join("/repo/root", ".chord", "permissions", "builder.yaml")
	if !strings.Contains(plain, want) {
		t.Fatalf("confirm rule picker project path = %q, want contains %q", plain, want)
	}
	if strings.Contains(plain, filepath.Join("/repo/root/subdir/deep", ".chord", "permissions", "builder.yaml")) {
		t.Fatalf("confirm rule picker should not use workingDir as project root: %q", plain)
	}
}
