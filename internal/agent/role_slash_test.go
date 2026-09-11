package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

// newRoleSlashAgent returns a test MainAgent with builder + planner main-mode
// roles configured and builder active.
func newRoleSlashAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain},
		"planner": {Name: "planner", Mode: config.AgentModeMain},
	})
	return a
}

func drainRoleSlashToasts(events []AgentEvent) (info []ToastEvent, errs []ToastEvent) {
	for _, evt := range events {
		if toast, ok := evt.(ToastEvent); ok {
			if toast.Level == "error" {
				errs = append(errs, toast)
			} else {
				info = append(info, toast)
			}
		}
	}
	return info, errs
}

func TestRoleSlashBareEmitsRoleSelectEvent(t *testing.T) {
	a := newRoleSlashAgent(t)

	if !a.executeLocalOnlySlashCommand("/role", nil, true) {
		t.Fatal("executeLocalOnlySlashCommand(/role) = false, want true")
	}
	found := false
	for _, evt := range drainAgentEvents(a.outputCh) {
		if _, ok := evt.(RoleSelectEvent); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("bare /role must emit RoleSelectEvent for the TUI role selector")
	}
	if got := a.CurrentRole(); got != "builder" {
		t.Fatalf("CurrentRole = %q, want builder (bare /role must not switch)", got)
	}
}

func TestRoleSlashStatusListsCurrentAndAvailableRoles(t *testing.T) {
	a := newRoleSlashAgent(t)

	if !a.executeLocalOnlySlashCommand("/role status", nil, true) {
		t.Fatal("executeLocalOnlySlashCommand(/role status) = false, want true")
	}
	var msg string
	for _, evt := range drainAgentEvents(a.outputCh) {
		if info, ok := evt.(InfoEvent); ok {
			msg = info.Message
		}
	}
	if !strings.Contains(msg, "Current role: builder") {
		t.Fatalf("status text = %q, want current role line", msg)
	}
	if !strings.Contains(msg, "builder (current)") || !strings.Contains(msg, "planner") {
		t.Fatalf("status text = %q, want ordered available roles with current marker", msg)
	}
	// The TUI renders this text as Markdown, so each role must be its own list
	// item; indented plain text would reflow into one paragraph.
	for _, item := range []string{"\n- builder (current)\n", "\n- planner"} {
		if !strings.Contains(msg, item) {
			t.Fatalf("status text = %q, want list item %q", msg, item)
		}
	}
}

func TestRoleSlashSwitchByNameSwitchesAndReports(t *testing.T) {
	a := newRoleSlashAgent(t)

	if !a.executeLocalOnlySlashCommand("/role planner", nil, true) {
		t.Fatal("executeLocalOnlySlashCommand(/role planner) = false, want true")
	}
	events := drainAgentEvents(a.outputCh)
	roleChanged := false
	for _, evt := range events {
		if rc, ok := evt.(RoleChangedEvent); ok && rc.Role == "planner" {
			roleChanged = true
		}
	}
	if !roleChanged {
		t.Fatal("/role <name> switch must emit RoleChangedEvent")
	}
	info, errs := drainRoleSlashToasts(events)
	if len(errs) != 0 {
		t.Fatalf("unexpected error toasts: %v", errs)
	}
	if len(info) != 1 || !strings.Contains(info[0].Message, "role: builder → planner") {
		t.Fatalf("info toasts = %v, want role: builder → planner", info)
	}
	if got := a.CurrentRole(); got != "planner" {
		t.Fatalf("CurrentRole = %q, want planner", got)
	}
}

func TestRoleSlashSameRoleIsNoOpWithInfoToast(t *testing.T) {
	a := newRoleSlashAgent(t)

	if !a.executeLocalOnlySlashCommand("/role builder", nil, true) {
		t.Fatal("executeLocalOnlySlashCommand(/role builder) = false, want true")
	}
	events := drainAgentEvents(a.outputCh)
	for _, evt := range events {
		if _, ok := evt.(RoleChangedEvent); ok {
			t.Fatal("switching to the active role must not emit RoleChangedEvent")
		}
	}
	info, errs := drainRoleSlashToasts(events)
	if len(errs) != 0 {
		t.Fatalf("unexpected error toasts: %v", errs)
	}
	if len(info) != 1 || info[0].Message != "already the active role: builder" {
		t.Fatalf("info toasts = %v, want already-active notice", info)
	}
	if got := a.CurrentRole(); got != "builder" {
		t.Fatalf("CurrentRole = %q, want builder", got)
	}
}

func TestRoleSlashUnknownRoleReportsError(t *testing.T) {
	a := newRoleSlashAgent(t)

	if !a.executeLocalOnlySlashCommand("/role ghost", nil, true) {
		t.Fatal("executeLocalOnlySlashCommand(/role ghost) = false, want true")
	}
	events := drainAgentEvents(a.outputCh)
	for _, evt := range events {
		if _, ok := evt.(RoleChangedEvent); ok {
			t.Fatal("failed switch must not emit RoleChangedEvent")
		}
	}
	info, errs := drainRoleSlashToasts(events)
	if len(info) != 0 {
		t.Fatalf("unexpected info toasts: %v", info)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Message, `unknown role "ghost"`) {
		t.Fatalf("error toasts = %v, want unknown role error", errs)
	}
}

func TestRoleSlashSubAgentOnlyRoleReportsError(t *testing.T) {
	a := newRoleSlashAgent(t)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain},
		"worker":  {Name: "worker", Mode: config.AgentModeSubAgent},
	})

	if !a.executeLocalOnlySlashCommand("/role worker", nil, true) {
		t.Fatal("executeLocalOnlySlashCommand(/role worker) = false, want true")
	}
	events := drainAgentEvents(a.outputCh)
	info, errs := drainRoleSlashToasts(events)
	if len(info) != 0 {
		t.Fatalf("unexpected info toasts: %v", info)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Message, `role "worker" is not available`) {
		t.Fatalf("error toasts = %v, want not-available error", errs)
	}
	if got := a.CurrentRole(); got != "builder" {
		t.Fatalf("CurrentRole = %q, want builder (unchanged)", got)
	}
}
