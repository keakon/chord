package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestBuildSystemPrompt_IncludesLSPDiagnosticGuidanceOnlyWhenLSPConfiguredAndWritable(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.WriteTool{})

	a := &MainAgent{tools: reg, globalConfig: &config.Config{LSP: config.LSPConfig{"gopls": {Command: "gopls"}}}}
	got := a.buildSystemPrompt()
	if !strings.Contains(got, "## LSP diagnostic follow-up") {
		t.Fatalf("buildSystemPrompt() missing LSP diagnostic guidance when LSP is configured and Write is visible: %q", got)
	}

	a.globalConfig = &config.Config{}
	got = a.buildSystemPrompt()
	if strings.Contains(got, "## LSP diagnostic follow-up") {
		t.Fatalf("buildSystemPrompt() unexpectedly included LSP diagnostic guidance without LSP config: %q", got)
	}

	a.globalConfig = &config.Config{LSP: config.LSPConfig{"gopls": {Command: "gopls"}}}
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
write: deny
edit: deny
`)}
	a.rebuildRuleset()
	got = a.buildSystemPrompt()
	if strings.Contains(got, "## LSP diagnostic follow-up") {
		t.Fatalf("buildSystemPrompt() unexpectedly included LSP diagnostic guidance without Edit/Write permission: %q", got)
	}
}

func TestHasEnabledLSPServers_ProjectOverrideCanDisableGlobalServer(t *testing.T) {
	globalCfg := &config.Config{LSP: config.LSPConfig{"gopls": {Command: "gopls"}}}
	projectCfg := &config.Config{LSP: config.LSPConfig{"gopls": {Disabled: true}}}
	if hasEnabledLSPServers(globalCfg, projectCfg) {
		t.Fatal("hasEnabledLSPServers() = true, want false when project config disables the inherited server")
	}

	projectCfg = &config.Config{LSP: config.LSPConfig{"pyright": {Command: "pyright-langserver"}}}
	if !hasEnabledLSPServers(globalCfg, projectCfg) {
		t.Fatal("hasEnabledLSPServers() = false, want true when any effective LSP server remains enabled")
	}
}

func TestShouldQueueLSPDiagnosticOverlay_RequiresRelevantChangedDiagnostics(t *testing.T) {
	payload := &ToolResultPayload{
		Name:       tools.NameWrite,
		ArgsJSON:   `{"path":"main.go"}`,
		FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/main.go", Exists: true}}},
		LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 1, Warnings: 0}},
	}
	if !shouldQueueLSPDiagnosticOverlay(nil, payload) {
		t.Fatal("shouldQueueLSPDiagnosticOverlay() = false, want true for first non-zero review on a written file")
	}

	history := []message.Message{{
		Role:       "tool",
		FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/main.go", Exists: true}}},
		LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 1, Warnings: 0}},
	}}
	if shouldQueueLSPDiagnosticOverlay(history, payload) {
		t.Fatal("shouldQueueLSPDiagnosticOverlay() = true, want false when the review state is unchanged")
	}

	payload.LSPReviews = []message.LSPReview{{ServerID: "gopls", Errors: 1, Warnings: 1}}
	if !shouldQueueLSPDiagnosticOverlay(history, payload) {
		t.Fatal("shouldQueueLSPDiagnosticOverlay() = false, want true when the review state changes")
	}

	payload.LSPReviews = []message.LSPReview{{ServerID: "gopls", Errors: 0, Warnings: 0}}
	if shouldQueueLSPDiagnosticOverlay(history, payload) {
		t.Fatal("shouldQueueLSPDiagnosticOverlay() = true, want false for zero diagnostics")
	}
}

func TestLSPDiagnosticOverlay_IsInjectedAsOneShotOverlay(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.tools.Register(tools.EditTool{})
	a.globalConfig = &config.Config{LSP: config.LSPConfig{"gopls": {Command: "gopls"}}}
	payload := &ToolResultPayload{
		Name:       tools.NameEdit,
		ArgsJSON:   `{"path":"pkg/foo.go"}`,
		FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/pkg/foo.go", Exists: true}}},
		LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 2, Warnings: 1}},
	}

	a.queueLSPDiagnosticOverlay(nil, payload)
	overlays := a.buildTurnOverlayMessages()
	if len(overlays) == 0 {
		t.Fatal("expected LSP diagnostic overlay to be present")
	}
	found := false
	for _, o := range overlays {
		if strings.Contains(o.Content, pendingLSPDiagnosticOverlayText) {
			found = true
			if strings.Contains(o.Content, "pkg/foo.go") || strings.Contains(o.Content, "2 errors") {
				t.Fatalf("expected generic LSP diagnostic overlay without per-tool details, got %q", o.Content)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected LSP diagnostic overlay in overlays, got %#v", overlays)
	}
	if a.pendingLSPDiagnosticOverlay != "" {
		t.Fatal("expected LSP diagnostic overlay to be consumed one-shot")
	}
	for _, o := range a.buildTurnOverlayMessages() {
		if strings.Contains(o.Content, pendingLSPDiagnosticOverlayText) {
			t.Fatal("LSP diagnostic overlay should not persist after first use")
		}
	}
}

func TestLSPDiagnosticOverlay_MultipleQueuedResultsStillProduceSingleGenericReminder(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.EditTool{})
	a.globalConfig = &config.Config{LSP: config.LSPConfig{"gopls": {Command: "gopls"}}}
	first := &ToolResultPayload{
		Name:       tools.NameWrite,
		ArgsJSON:   `{"path":"pkg/foo.go"}`,
		FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/pkg/foo.go", Exists: true}}},
		LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 1, Warnings: 0}},
	}
	second := &ToolResultPayload{
		Name:       tools.NameEdit,
		ArgsJSON:   `{"path":"pkg/bar.go"}`,
		FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/pkg/bar.go", Exists: true}}},
		LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 0, Warnings: 2}},
	}

	a.queueLSPDiagnosticOverlay(nil, first)
	a.queueLSPDiagnosticOverlay(nil, second)
	overlays := a.buildTurnOverlayMessages()
	count := 0
	for _, o := range overlays {
		if strings.Contains(o.Content, pendingLSPDiagnosticOverlayText) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one generic LSP diagnostic overlay, got %d (%#v)", count, overlays)
	}
}

func TestLSPDiagnosticOverlay_IsDroppedWhenCurrentRoleNoLongerQualifies(t *testing.T) {
	t.Run("write permission removed", func(t *testing.T) {
		a := newReadyTestMainAgent(t)
		a.tools.Register(tools.WriteTool{})
		a.globalConfig = &config.Config{LSP: config.LSPConfig{"gopls": {Command: "gopls"}}}
		payload := &ToolResultPayload{
			Name:       tools.NameWrite,
			ArgsJSON:   `{"path":"pkg/foo.go"}`,
			FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/pkg/foo.go", Exists: true}}},
			LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 1, Warnings: 0}},
		}
		a.queueLSPDiagnosticOverlay(nil, payload)
		a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, "\n\"*\": deny\nWrite: deny\nEdit: deny\n")}
		a.rebuildRuleset()

		for _, o := range a.buildTurnOverlayMessages() {
			if strings.Contains(o.Content, pendingLSPDiagnosticOverlayText) {
				t.Fatalf("unexpected LSP diagnostic overlay after write permission was removed: %q", o.Content)
			}
		}
		if a.pendingLSPDiagnosticOverlay != "" {
			t.Fatal("expected stale LSP diagnostic overlay to be cleared when current role lacks Edit/Write")
		}
	})

	t.Run("LSP disabled", func(t *testing.T) {
		a := newReadyTestMainAgent(t)
		a.tools.Register(tools.WriteTool{})
		a.globalConfig = &config.Config{LSP: config.LSPConfig{"gopls": {Command: "gopls"}}}
		payload := &ToolResultPayload{
			Name:       tools.NameWrite,
			ArgsJSON:   `{"path":"pkg/foo.go"}`,
			FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "/repo/pkg/foo.go", Exists: true}}},
			LSPReviews: []message.LSPReview{{ServerID: "gopls", Errors: 1, Warnings: 0}},
		}
		a.queueLSPDiagnosticOverlay(nil, payload)
		a.globalConfig = &config.Config{LSP: config.LSPConfig{"gopls": {Disabled: true}}}

		for _, o := range a.buildTurnOverlayMessages() {
			if strings.Contains(o.Content, pendingLSPDiagnosticOverlayText) {
				t.Fatalf("unexpected LSP diagnostic overlay after LSP was disabled: %q", o.Content)
			}
		}
		if a.pendingLSPDiagnosticOverlay != "" {
			t.Fatal("expected stale LSP diagnostic overlay to be cleared when no enabled LSP remains")
		}
	})
}
