package tui

import (
	"strings"
	"testing"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestSidebarStatusCategories(t *testing.T) {
	s := NewSidebar(uitheme.Default())
	s.Update(nil, "main", "orchestrator")
	if s.AgentsSummary() != "0 agents" || s.Visible() {
		t.Fatal("empty child list")
	}
	for _, state := range []string{
		"running", "waiting_main", "waiting_descendant", "completed", "done",
		"failed", "error", "cancelled", "idle", "", "unexpected",
	} {
		s.agents = append(s.agents, SidebarEntry{ID: state, Status: state})
	}
	want := "11"
	if got := s.AgentsSummary(); got != want {
		t.Fatalf("summary=%q want=%q", got, want)
	}
	if statusIndicator("") != "◇" {
		t.Fatal("unknown indicator")
	}
}

func TestSidebarRowsRenderStateAsIconOnly(t *testing.T) {
	for _, tc := range []struct {
		status string
		text   string
	}{
		{"running", "running"},
		{"waiting_main", "awaiting owner"},
		{"waiting_descendant", "awaiting child"},
		{"completed", "delivered"},
		{"failed", "failed"},
		{"cancelled", "cancelled"},
		{"idle", "idle"},
		{"", "unknown"},
	} {
		line, _, _ := sidebarEntryText(SidebarEntry{ID: "worker", TaskDesc: "ship tests", Status: tc.status}, false, 60)
		if !strings.HasPrefix(line, "  "+statusIndicator(tc.status)+" ship tests") {
			t.Fatalf("status %q row = %q, want icon and name", tc.status, line)
		}
		if strings.Contains(line, tc.text) {
			t.Fatalf("status %q row = %q, want no status text", tc.status, line)
		}
	}
}

func TestSidebarSnapshotOverridesStatusCache(t *testing.T) {
	s := NewSidebar(uitheme.Default())
	for _, state := range []string{"running", "completed"} {
		s.Update([]agent.SubAgentInfo{{InstanceID: "worker", State: state}}, "main", "orchestrator")
		s.UpdateStatus("worker", subAgentStatusError)
		s.Update([]agent.SubAgentInfo{{InstanceID: "worker", State: state}}, "main", "orchestrator")
		if got, _ := s.FindStatus("worker"); got != state {
			t.Fatalf("status=%q", got)
		}
	}
	s.UpdateStatus("worker", subAgentStatusError)
	s.Update([]agent.SubAgentInfo{{InstanceID: "worker"}}, "main", "orchestrator")
	if got, _ := s.FindStatus("worker"); got != subAgentStatusError {
		t.Fatalf("empty snapshot status=%q", got)
	}
}

func TestSidebarReasonLayout(t *testing.T) {
	for _, width := range []int{40, 24, 12, 4, 3, 1, 0} {
		for _, depth := range []int{0, 1, 8} {
			entry := SidebarEntry{ID: "worker", TaskDesc: "检查数据库迁移 and compatibility", Status: "waiting_main", TreeDepth: depth, AwaitingOwnerReason: "\x1b[31mNeed\n a\tdecision\x1b[0m \x1b]8;;https://example.org\x1b\\link\x1b]8;;\x1b\\"}
			line, reason, _ := sidebarEntryText(entry, true, width)
			for _, text := range []string{line, reason} {
				if lipgloss.Width(text) > width || strings.ContainsAny(text, "\x1b\n\t") {
					t.Fatalf("width=%d text=%q", width, text)
				}
			}
			if width == 40 && !strings.Contains(line, "检查数据库迁移") {
				t.Fatalf("missing task name: %q", line)
			}
			if reason != "" && strings.TrimSpace(reason) == "" {
				t.Fatal("blank reason row")
			}
			entry.Status = "waiting_descendant"
			_, reason, _ = sidebarEntryText(entry, false, width)
			if reason != "" {
				t.Fatal("child wait showed owner reason")
			}
		}
	}
}

func TestSidebarReasonRowOwnershipAndFocusOrder(t *testing.T) {
	s := NewSidebar(uitheme.Default())
	s.Update([]agent.SubAgentInfo{{InstanceID: "worker", State: "waiting_main", TaskDesc: "Migration", AwaitingOwnerReason: "Choose null handling"}}, "worker", "orchestrator")
	lines := s.buildInfoPanelRenderedLines(40)
	if len(lines) != 3 || lines[1].AgentID != "worker" || lines[2].AgentID != "worker" || !strings.Contains(terminaltext.StripANSI(lines[2].Text), "Choose null handling") {
		t.Fatalf("rows=%+v", lines)
	}
	if strings.Join(s.AgentIDs(), ",") != "main,worker" {
		t.Fatalf("focus order=%v", s.AgentIDs())
	}
	if !strings.Contains(terminaltext.StripANSI(s.ViewCompact(40)), "Choose null handling") {
		t.Fatal("compact omitted reason")
	}
}

func TestSidebarDefaultPanelPreservesNestedNames(t *testing.T) {
	rowWidth := rightPanelWidth - 2 - infoPanelCollapsibleContentInset - 2
	for _, state := range []string{string(agent.SubAgentStateWaitingMain), string(agent.SubAgentStateWaitingDescendant)} {
		for _, depth := range []int{1, 2, 3} {
			entry := SidebarEntry{ID: "worker", TaskDesc: "Implement API", Status: state, TreeDepth: depth, AwaitingOwnerReason: "Choose an interface"}
			line, reason, _ := sidebarEntryText(entry, false, rowWidth)
			if lipgloss.Width(line) > rowWidth || lipgloss.Width(reason) > rowWidth {
				t.Fatalf("state=%s depth=%d exceeds row width: %q / %q", state, depth, line, reason)
			}
			prefix := sidebarIndicator(state, false) + " " + sidebarTreePrefix(entry)
			name := strings.TrimPrefix(line, prefix)
			if lipgloss.Width(name) < 4 {
				t.Fatalf("state=%s depth=%d lost task name: %q", state, depth, line)
			}
			if depth > 1 && !strings.Contains(line, entry.TaskDesc) {
				t.Fatalf("state=%s depth=%d should retain the nested task name: %q", state, depth, line)
			}
			if reason != "" && len(reason)-len(strings.TrimLeft(reason, " ")) != lipgloss.Width(prefix) {
				t.Fatalf("reason is not aligned with name: %q / %q", line, reason)
			}
		}
	}
}

func TestSidebarUsageSubLine(t *testing.T) {
	usage := agent.AgentUsage{
		Tokens:    message.TokenUsage{InputTokens: 45000, OutputTokens: 3200},
		CostUSD:   0.42,
		CostKnown: true,
	}
	_, _, usageLine := sidebarEntryText(SidebarEntry{ID: "worker", TaskDesc: "Ship tests", Status: "running", Usage: usage}, false, 60)
	if !strings.Contains(terminaltext.StripANSI(usageLine), "48.2k tok · $0.42") {
		t.Fatalf("usage line = %q", usageLine)
	}
	if !strings.HasPrefix(usageLine, "    ") {
		t.Fatalf("usage line is not indented under the name: %q", usageLine)
	}
	if _, _, empty := sidebarEntryText(SidebarEntry{ID: "worker", TaskDesc: "Ship tests", Status: "running"}, false, 60); empty != "" {
		t.Fatalf("zero usage rendered %q", empty)
	}
	tokensOnly := usage
	tokensOnly.CostKnown = false
	if _, _, tokensLine := sidebarEntryText(SidebarEntry{ID: "worker", TaskDesc: "Ship tests", Status: "running", Usage: tokensOnly}, false, 60); strings.Contains(tokensLine, "$") {
		t.Fatalf("tokens-only usage rendered a cost: %q", tokensLine)
	}
}

func TestSidebarUsageRowOwnership(t *testing.T) {
	s := NewSidebar(uitheme.Default())
	s.Update([]agent.SubAgentInfo{{
		InstanceID: "worker", State: "running", TaskDesc: "Migration",
		Usage: agent.AgentUsage{Tokens: message.TokenUsage{InputTokens: 1000}, CostUSD: 0.02, CostKnown: true},
	}}, "worker", "orchestrator")
	lines := s.buildInfoPanelRenderedLines(40)
	if len(lines) != 3 || lines[2].AgentID != "worker" {
		t.Fatalf("rows=%+v", lines)
	}
	if !strings.Contains(terminaltext.StripANSI(lines[2].Text), "1.0k tok · $0.02") {
		t.Fatalf("usage row = %q", lines[2].Text)
	}
}

func TestSidebarUsageFormatters(t *testing.T) {
	for _, tc := range []struct {
		tokens int
		want   string
	}{
		{950, "950"},
		{48200, "48.2k"},
		{1_200_000, "1.2M"},
	} {
		if got := formatTokens(tc.tokens); got != tc.want {
			t.Fatalf("formatTokens(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
	for _, tc := range []struct {
		cost float64
		want string
	}{
		{0, "0"},
		{0.42, "0.42"},
		{0.02, "0.02"},
		{0.0009, "0.0009"},
		{1.5, "1.50"},
	} {
		if got := formatSidebarCost(tc.cost); got != tc.want {
			t.Fatalf("formatSidebarCost(%v) = %q, want %q", tc.cost, got, tc.want)
		}
	}
}
