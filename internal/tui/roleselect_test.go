package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func findRoleSwitchResult(msgs []tea.Msg) (roleSwitchResultMsg, bool) {
	for _, msg := range msgs {
		if result, ok := msg.(roleSwitchResultMsg); ok {
			return result, true
		}
	}
	return roleSwitchResultMsg{}, false
}

func TestRoleSelectEventOpensRoleOverlay(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RoleSelectEvent{}})

	if m.mode != ModeRoleSelect {
		t.Fatalf("mode = %v, want ModeRoleSelect", m.mode)
	}
	plain := stripANSI(m.renderRoleSelectDialog())
	if !strings.Contains(plain, "builder") || !strings.Contains(plain, "planner") {
		t.Fatalf("role dialog = %q, want builder and planner entries", plain)
	}
}

func TestOpenRoleSelectPreselectsCurrentRole(t *testing.T) {
	backend := &sessionControlAgent{
		currentRole:    "planner",
		availableRoles: []string{"builder", "planner", "reviewer"},
	}
	m := NewModelWithSize(backend, 100, 24)

	m.openRoleSelect()

	if got := m.roleSelect.roles; len(got) != 3 || got[0] != "builder" || got[1] != "planner" || got[2] != "reviewer" {
		t.Fatalf("roleSelect.roles = %v, want ordered available roles", got)
	}
	if m.roleSelect.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 for current role planner", m.roleSelect.cursor)
	}
}

func TestRoleSelectEnterSwitchesToSelectedRole(t *testing.T) {
	backend := &sessionControlAgent{
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.openRoleSelect()

	// Move down to planner and confirm.
	if cmd := m.handleRoleSelectKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatal("j navigation returned a command")
	}
	if m.roleSelect.cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", m.roleSelect.cursor)
	}
	cmd := m.handleRoleSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	result, ok := findRoleSwitchResult(runCmdTree(cmd))
	if !ok {
		t.Fatal("enter cmd tree did not produce roleSwitchResultMsg")
	}
	if result.err != nil || result.from != "builder" || result.to != "planner" {
		t.Fatalf("roleSwitchResultMsg = %+v, want from=builder to=planner err=nil", result)
	}
	if backend.currentRole != "planner" {
		t.Fatalf("backend.currentRole = %q, want planner", backend.currentRole)
	}
	if m.mode == ModeRoleSelect {
		t.Fatal("overlay must close after selecting a role")
	}
}

func TestRoleSelectEscCancelsAndRestoresInsert(t *testing.T) {
	backend := &sessionControlAgent{
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeInsert
	m.openRoleSelect()
	if m.mode != ModeRoleSelect {
		t.Fatalf("mode after open = %v, want ModeRoleSelect", m.mode)
	}

	_ = m.handleRoleSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))

	if m.mode != ModeInsert {
		t.Fatalf("mode after esc = %v, want ModeInsert", m.mode)
	}
	if backend.currentRole != "builder" {
		t.Fatalf("backend.currentRole = %q, want unchanged builder after cancel", backend.currentRole)
	}
}

func TestRoleSelectEnterOnCurrentRoleIsNoOp(t *testing.T) {
	backend := &sessionControlAgent{
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeInsert
	m.openRoleSelect()
	if m.mode != ModeRoleSelect {
		t.Fatalf("mode after open = %v, want ModeRoleSelect", m.mode)
	}
	// The cursor preselects the current role, so a bare Enter confirms the
	// already-active role.
	if m.roleSelect.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 for current role builder", m.roleSelect.cursor)
	}
	m.cachedStatusKey = "cached-status"

	cmd := m.handleRoleSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	msgs := runCmdTree(cmd)

	if result, ok := findRoleSwitchResult(msgs); ok {
		t.Fatalf("same-role confirm produced roleSwitchResultMsg %+v, want no SwitchRole call", result)
	}
	if backend.currentRole != "builder" {
		t.Fatalf("backend.currentRole = %q, want unchanged builder", backend.currentRole)
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode after no-op confirm = %v, want ModeInsert restored", m.mode)
	}
	if m.activeToast == nil {
		t.Fatal("same-role confirm must surface an info toast")
	}
	if m.activeToast.Level != "info" || m.activeToast.Message != "already the active role: builder" {
		t.Fatalf("toast = %+v, want info \"already the active role: builder\"", m.activeToast)
	}
	if m.cachedStatusKey != "cached-status" {
		t.Fatal("a same-role no-op must not invalidate draw caches")
	}
}

func TestHandleRoleSwitchResultToastsErrorWithoutCacheInvalidation(t *testing.T) {
	backend := &sessionControlAgent{
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
		switchRoleErr:  errors.New(`role "planner" is not available`),
	}
	m := NewModelWithSize(backend, 100, 24)
	m.cachedStatusKey = "cached-status"

	_ = m.handleRoleSwitchResult(roleSwitchResultMsg{from: "builder", to: "planner", err: backend.switchRoleErr})

	if m.cachedStatusKey != "cached-status" {
		t.Fatal("a failed role switch must not invalidate draw caches")
	}
	if m.activeToast == nil {
		t.Fatal("a failed role switch must surface an error toast")
	}
	if m.activeToast.Level != "error" || !strings.Contains(m.activeToast.Message, `role "planner" is not available`) {
		t.Fatalf("error toast = %+v, want role-not-available message", m.activeToast)
	}
}

func TestHandleRoleSwitchResultToastsSuccess(t *testing.T) {
	backend := &sessionControlAgent{
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)

	_ = m.handleRoleSwitchResult(roleSwitchResultMsg{from: "builder", to: "planner", err: nil})

	if m.activeToast == nil {
		t.Fatal("a successful role switch must surface an info toast")
	}
	if m.activeToast.Level != "info" || !strings.Contains(m.activeToast.Message, "role: builder → planner") {
		t.Fatalf("info toast = %+v, want role: builder → planner", m.activeToast)
	}
}
