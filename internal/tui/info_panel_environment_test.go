package tui

import (
	"strings"
	"testing"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/gitinspect"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestInfoPanelGitBlockHiddenWhenNoRepo(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	plain := terminaltext.StripANSI(m.renderInfoPanel(48, 24))
	if strings.Contains(plain, "GIT") {
		t.Fatalf("git block should be hidden when no git info is present, got:\n%s", plain)
	}
}

func TestInfoPanelGitBlockCollapsedByDefault(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitinspect.Info{Present: true, Branch: "main", Ahead: 14, ChangedFiles: 2, StagedFiles: 1, Stashes: 3}
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▶ GIT")
	if len(section) != 0 {
		t.Fatalf("collapsed git section should not render body lines, got %#v", section)
	}
	plain := terminaltext.StripANSI(m.renderInfoPanel(48, 24))
	if !strings.Contains(plain, "▶ GIT") || !strings.Contains(plain, "main ↑14 +1 !2 *3") {
		t.Fatalf("git summary missing from collapsed header, got:\n%s", plain)
	}
}

func TestInfoPanelGitBlockCollapsedKeepsCountsWithLongBranch(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitinspect.Info{Present: true, Branch: "feature/very-long-branch-name-here", ChangedFiles: 2}
	plain := terminaltext.StripANSI(m.renderInfoPanel(32, 24))
	if !strings.Contains(plain, "... !2") {
		t.Fatalf("long branch should be truncated without hiding the changed count, got:\n%s", plain)
	}
	if strings.Contains(plain, "branch-name-here") {
		t.Fatalf("branch tail should be truncated, got:\n%s", plain)
	}
}

func TestInfoPanelGitBlockExpanded(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.workingDir = "/state/worktrees/repo-abc/fix-ui"
	m.gitStatus.Info = gitinspect.Info{Present: true, Branch: "main", WorktreeName: "fix-ui", Dir: "/state/worktrees/repo-abc/fix-ui", Ahead: 2, Behind: 1, ChangedFiles: 3, StagedFiles: 1, Stashes: 2}
	m.toggleInfoPanelSection(infoPanelSectionGit)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ GIT")
	joined := strings.Join(section, "\n")
	for _, want := range []string{"Branch: main", "Worktree: fix-ui", "Worktree Path: /state/worktrees", "Changes: 3 files", "Staged: 1 files", "Stash: 2 entries", "Sync: ↑2 ↓1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded git section missing %q in:\n%s", want, joined)
		}
	}
	plain := terminaltext.StripANSI(m.renderInfoPanel(48, 24))
	if strings.Contains(plain, "main@fix-ui ↑2 ↓1 !3") {
		t.Fatalf("expanded git header should omit summary, got:\n%s", plain)
	}
	rawSection := infoPanelSectionLines(infoPanelRawLines(m.renderInfoPanel(48, 24)), "▼ GIT")
	for _, line := range rawSection {
		if strings.HasPrefix(line, "Branch:") || strings.HasPrefix(line, "Worktree:") || strings.HasPrefix(line, "Changes:") || strings.HasPrefix(line, "Staged:") || strings.HasPrefix(line, "Stash:") || strings.HasPrefix(line, "Sync:") {
			t.Fatalf("expanded git content should be indented, got raw section %#v", rawSection)
		}
	}
}

func TestInfoPanelGitBlockExpandedHidesZeroNumericRows(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitinspect.Info{Present: true, Branch: "main", ChangedFiles: 2}
	m.toggleInfoPanelSection(infoPanelSectionGit)

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ GIT")
	joined := strings.Join(section, "\n")
	for _, want := range []string{"Branch: main", "Changes: 2 files"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded git section missing %q in:\n%s", want, joined)
		}
	}
	for _, hidden := range []string{"Staged:", "Stash:", "Sync:", "Worktree Path:", "0 files", "0 entries", "↑0", "↓0"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("expanded git section should hide zero value %q in:\n%s", hidden, joined)
		}
	}
}

func TestInfoPanelGitBlockExpandedShowsOneSidedSync(t *testing.T) {
	m := NewModel(newInfoPanelAgent())
	m.gitStatus.Info = gitinspect.Info{Present: true, Branch: "main", Ahead: 2}
	m.toggleInfoPanelSection(infoPanelSectionGit)

	joined := strings.Join(infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(48, 24)), "▼ GIT"), "\n")
	if !strings.Contains(joined, "Sync: ↑2") {
		t.Fatalf("expanded git section should show non-zero ahead count, got:\n%s", joined)
	}
	if strings.Contains(joined, "↓0") {
		t.Fatalf("expanded git section should hide zero behind count, got:\n%s", joined)
	}
}

func TestRenderInfoPanelCollapsedLSPShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true}, {Name: "pyright", Pending: true}}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionLSP] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "▶ LSP")
	if len(section) != 0 {
		t.Fatalf("collapsed LSP section should not render body lines, got %#v", section)
	}
	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "▶ LSP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed LSP header should include count, got %q", plain)
	}
}

func TestRenderInfoPanelCollapsedMCPShowsCountOnly(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true}, {Name: "browser", Pending: true}}
	m := NewModel(backend)
	m.infoPanelCollapsedSections[infoPanelSectionMCP] = true

	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "▶ MCP")
	if len(section) != 0 {
		t.Fatalf("collapsed MCP section should not render body lines, got %#v", section)
	}
	plain := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(plain, "▶ MCP") || !strings.Contains(plain, "2") {
		t.Fatalf("collapsed MCP header should include count, got %q", plain)
	}
}

func TestRenderInfoPanelEnvironmentDotsPreserveInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Pending: true}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", OK: true, Enabled: true}}

	m := NewModel(backend)
	// The LSP row is still pending, so the section defaults to folded; expand
	// it explicitly to pin the pending row rendering next to the OK row.
	m.infoPanelCollapsedSections[infoPanelSectionLSP] = false
	rendered := m.renderInfoPanel(32, 20)

	pendingWant := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelPendingFg)).Render("●")
	if !strings.Contains(rendered, pendingWant) {
		t.Fatalf("pending LSP dot should include info-panel background; want segment %q in %q", pendingWant, rendered)
	}
	pendingBad := "\x1b[38;5;240m●\x1b[m"
	if strings.Contains(rendered, pendingBad) {
		t.Fatalf("pending LSP dot should not be rendered without background; got %q in %q", pendingBad, rendered)
	}

	okWant := currentStyles.GaugeFull.Render("●")
	if !strings.Contains(rendered, okWant) {
		t.Fatalf("OK MCP dot should include info-panel background; want segment %q in %q", okWant, rendered)
	}

	lspSection := infoPanelSectionLines(infoPanelPlainLines(rendered), "LSP")
	if len(lspSection) == 0 || !strings.HasPrefix(lspSection[0], "● gopls") {
		t.Fatalf("LSP rows should remain visible in plain section extraction; got %#v", lspSection)
	}
	mcpSection := infoPanelSectionLines(infoPanelRawLines(rendered), "▼ MCP")
	if len(mcpSection) == 0 || mcpSection[0] != "   ● exa" {
		t.Fatalf("MCP rows should use collapsible content indent; got %#v", mcpSection)
	}
}

func TestRenderInfoPanelLSPRowsShowInlineDiagnostics(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", OK: true, Errors: 2, Warnings: 1}}

	m := NewModel(backend)
	section := infoPanelSectionLines(infoPanelPlainLines(m.renderInfoPanel(40, 20)), "LSP")
	joined := strings.Join(section, "\n")
	if strings.Contains(joined, "LSP:") {
		t.Fatalf("LSP section should not contain a colon-suffixed subheader; got %q", joined)
	}
	if !strings.Contains(joined, "gopls 2 E, 1 W") {
		t.Fatalf("LSP row should include inline diagnostics suffix; got %q", joined)
	}
}

func TestRenderInfoPanelEnvironmentDotsUseThemeInfoPanelBackground(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Pending: true}}

	m := NewModel(backend)
	// A pending LSP row no longer auto-expands the section; expand it
	// explicitly to pin the themed dot rendering.
	m.infoPanelCollapsedSections[infoPanelSectionLSP] = false
	rendered := m.renderInfoPanel(32, 20)
	want := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelPendingFg)).Render("●")
	if !strings.Contains(rendered, want) {
		t.Fatalf("pending LSP dot should use themed info-panel background; want %q in %q", want, rendered)
	}
}

func TestRenderInfoPanelIdleEnvironmentRowsUseDimNotCriticalStyles(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Idle: true}}
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Idle: true}}

	m := NewModel(backend)
	// Idle rows are unloaded, so both sections default to folded; expand them
	// explicitly to pin the dim (not critical) row rendering.
	m.infoPanelCollapsedSections[infoPanelSectionLSP] = false
	m.infoPanelCollapsedSections[infoPanelSectionMCP] = false
	rendered := m.renderInfoPanel(40, 20)
	plain := terminaltext.StripANSI(rendered)
	if !strings.Contains(plain, "gopls") || !strings.Contains(plain, "exa") {
		t.Fatalf("idle environment rows missing from info panel, got %q", plain)
	}
	if strings.Contains(rendered, currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg)).Render(" gopls")) {
		t.Fatalf("idle LSP row should not use critical label style, got %q", rendered)
	}
	if strings.Contains(rendered, currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg)).Render(" exa")) {
		t.Fatalf("idle MCP row should not use critical label style, got %q", rendered)
	}
}

func TestRenderInfoPanelLSPErrorHidesRawMessageAndRefreshesWhenStatusChanges(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Err: "dial tcp", OK: false}}

	m := NewModel(backend)
	// A failed server is not loaded, so the section defaults to folded;
	// expand it explicitly to pin the error-row rendering on both sides of
	// the status change.
	m.infoPanelCollapsedSections[infoPanelSectionLSP] = false
	before := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(before, "gopls") {
		t.Fatalf("info panel before LSP error change = %q, want server name", before)
	}
	if strings.Contains(before, "dial tcp") {
		t.Fatalf("info panel should not show raw LSP error text, got %q", before)
	}

	backend.lspRows = []agent.LSPServerDisplay{{Name: "gopls", Err: "context deadline exceeded", OK: false}}
	after := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(after, "gopls") {
		t.Fatalf("info panel after LSP error change = %q, want server name", after)
	}
	if strings.Contains(after, "context deadline") || strings.Contains(after, "dial tcp") {
		t.Fatalf("info panel should continue hiding raw LSP error text, got %q", after)
	}
}

func TestRenderInfoPanelMCPErrorRefreshesWhenMessageChanges(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "codex_apps", Err: "timeout", OK: false}}

	m := NewModel(backend)
	// A failed server is not connected, so the section defaults to folded;
	// expand it explicitly to pin the error-row rendering on both sides of
	// the message change.
	m.infoPanelCollapsedSections[infoPanelSectionMCP] = false
	before := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(before, "codex_apps") {
		t.Fatalf("info panel before MCP error change = %q, want server name", before)
	}
	if strings.Contains(before, "timeout") {
		t.Fatalf("info panel should not show raw MCP error text, got %q", before)
	}

	backend.mcpRows = []agent.MCPServerDisplay{{Name: "codex_apps", Err: "connection refused", OK: false}}
	after := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(after, "codex_apps") {
		t.Fatalf("info panel after MCP error change = %q, want server name", after)
	}
	if strings.Contains(after, "connection refused") || strings.Contains(after, "timeout") {
		t.Fatalf("info panel should continue hiding raw MCP error text, got %q", after)
	}
}

func TestRenderInfoPanelMCPRetryingUsesWarningLabel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Pending: true, Retrying: true, Attempt: 2, MaxAttempts: 3}}

	m := NewModel(backend)
	// A retrying server has not connected yet, so the section defaults to
	// folded; expand it explicitly to pin the warning label rendering.
	m.infoPanelCollapsedSections[infoPanelSectionMCP] = false
	got := terminaltext.StripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(got, "exa") {
		t.Fatalf("retrying MCP row = %q, want server name", got)
	}

	rendered := m.renderInfoPanel(40, 20)
	wantWarning := currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg)).Render(" exa")
	if !strings.Contains(rendered, wantWarning) {
		t.Fatalf("retrying MCP row should use warning label style; want %q in %q", wantWarning, rendered)
	}
	if strings.Contains(rendered, currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg)).Render(" exa")) {
		t.Fatalf("retrying MCP row should not use critical red label style; got %q", rendered)
	}
}

func TestRenderInfoPanelMCPPendingUsesDimLabel(t *testing.T) {
	backend := newInfoPanelAgent()
	backend.mcpRows = []agent.MCPServerDisplay{{Name: "exa", Pending: true, Retrying: false}}

	m := NewModel(backend)
	// A pending server has not connected yet, so the section defaults to
	// folded; expand it explicitly to pin the dim label rendering.
	m.infoPanelCollapsedSections[infoPanelSectionMCP] = false
	rendered := m.renderInfoPanel(40, 20)
	wantDim := currentStyles.InfoPanelDim.Render(" exa")
	if !strings.Contains(rendered, wantDim) {
		t.Fatalf("pending MCP row should use dim label style; want %q in %q", wantDim, rendered)
	}
	wantWarning := currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg)).Render(" exa")
	if strings.Contains(rendered, wantWarning) {
		t.Fatalf("pending MCP row should not use warning label style; got %q", rendered)
	}
}

func TestRenderLSPDiagSummaryUsesInfoPanelBackgroundOnAllSegments(t *testing.T) {
	got := renderLSPDiagSummary("3 E, 1 W")
	wantError := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelDiagErrorFg)).Render("3 E")
	wantWarn := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelDiagWarnFg)).Render("1 W")
	wantSep := currentStyles.InfoPanelDim.Render(", ")

	if !strings.Contains(got, wantError) {
		t.Fatalf("error segment should include info-panel background; want %q in %q", wantError, got)
	}
	if !strings.Contains(got, wantWarn) {
		t.Fatalf("warn segment should include info-panel background; want %q in %q", wantWarn, got)
	}
	if !strings.Contains(got, wantSep) {
		t.Fatalf("separator should include info-panel background; want %q in %q", wantSep, got)
	}
	badError := "\x1b[38;5;196m3 E\x1b[m"
	if strings.Contains(got, badError) {
		t.Fatalf("diagnostic error segment should not be rendered without background; got %q in %q", badError, got)
	}
}

func TestFormatLSPServerDiagSuffix(t *testing.T) {
	cases := []struct {
		name     string
		errors   int
		warnings int
		want     string
	}{
		{name: "none", want: ""},
		{name: "errors only", errors: 2, want: "2 E"},
		{name: "warnings only", warnings: 3, want: "3 W"},
		{name: "both", errors: 1, warnings: 2, want: "1 E, 2 W"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLSPServerDiagSuffix(tc.errors, tc.warnings); got != tc.want {
				t.Fatalf("formatLSPServerDiagSuffix(%d, %d) = %q, want %q", tc.errors, tc.warnings, got, tc.want)
			}
		})
	}
}

func TestRenderLSPDiagSummaryFollowsThemeInfoPanelBackground(t *testing.T) {
	got := renderLSPDiagSummary("3 E, 1 W")
	wantError := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelDiagErrorFg)).Render("3 E")
	if !strings.Contains(got, wantError) {
		t.Fatalf("diagnostic summary should use themed info-panel background; want %q in %q", wantError, got)
	}
}
