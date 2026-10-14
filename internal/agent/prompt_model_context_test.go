package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestModelDrivenContextPromptBlockEmptyWhenDisabled(t *testing.T) {
	a := &MainAgent{}
	if got := a.modelDrivenContextPromptBlock(); got != "" {
		t.Fatalf("disabled model-driven must render no block, got %q", got)
	}
}

// modelDrivenPromptTestAgent builds a MainAgent with model-driven enabled and
// the compact_context tool registered, mirroring the runtime wiring.
func modelDrivenPromptTestAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := &MainAgent{}
	a.modelDrivenCompactionEnabled.Store(true)
	a.tools = tools.NewRegistry()
	a.tools.Register(tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}))
	return a
}

func TestModelDrivenContextPromptBlockInjectedWhenEnabled(t *testing.T) {
	a := modelDrivenPromptTestAgent(t)
	block := a.modelDrivenContextPromptBlock()
	if block == "" {
		t.Fatal("enabled model-driven must render the context-management block")
	}
	for _, want := range []string{
		"## Long-session context management",
		"compact_context",
		"registered task notes",
		"archived history",
		"checkpoint and any injected file content",
		"only for missing or changed information",
		"tool description governs checkpoint timing",
		"required environment variables and arguments",
		"Append verified reusable findings to the existing notes file together with the next action's tool calls rather than in a separate step",
		"replace the short handoff status instead of appending to it",
		"Distinguish passed, failed, skipped and environment-blocked checks",
		"Put an index and a short current handoff",
		"refresh existing task notes only when material state changed and writing is permitted",
		"reconcile stale pending items with results already received",
		"Notes are recovery aids, not proof",
		"Checkpoint worklogs and repository sections are historical snapshots",
		"do not rewrite notes solely to mirror todo status, the current HEAD, a commit, or a final completion banner",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("block must mention %q, got:\n%s", want, block)
		}
	}
	// Task-notes rules have one source: this main-only block. The shared
	// Guidelines (also rendered for SubAgents) and the always-sent tool
	// description must not repeat them.
	prompt := a.buildSystemPrompt()
	description := tools.NewCompactContextTool(tools.CompactContextValidator{ContinuationStateMaxTokens: CompactContinuationStateMaxTokens}).Description()
	for _, sentence := range []string{
		"required environment variables and arguments",
		"Distinguish passed, failed, skipped and environment-blocked checks",
		"refresh existing task notes",
		"reconcile stale pending items",
	} {
		if got := strings.Count(prompt, sentence); got != 1 {
			t.Fatalf("main prompt renders %q %d times, want 1", sentence, got)
		}
		if strings.Contains(description, sentence) {
			t.Fatalf("compact_context description repeats task-notes guidance %q", sentence)
		}
	}
	for _, guidelines := range []string{sharedCodingGuidelinesPrompt, subAgentCodingGuidelinesPrompt} {
		if strings.Contains(guidelines, "task notes") {
			t.Fatalf("shared Guidelines must not carry main-only task-notes rules: %q", guidelines)
		}
	}
	for _, unwanted := range []string{"<system-reminder>", "estimated tokens", "checkpoint_kind=", "Leave state_files empty", "Call it alone", "refresh files", "append 1–3 lines", "separate turn"} {
		if strings.Contains(block, unwanted) {
			t.Fatalf("context block duplicates tool or trust guidance %q: %s", unwanted, block)
		}
	}
}

func TestModelDrivenContextPromptBlockEmptyWhenToolDenied(t *testing.T) {
	// No system prompt may push a tool that permission rules deny — the
	// guidance only renders while compact_context is visible and executable.
	a := modelDrivenPromptTestAgent(t)
	a.ruleset = permission.Ruleset{{Permission: tools.NameCompactContext, Pattern: "*", Action: permission.ActionDeny}}
	if got := a.modelDrivenContextPromptBlock(); got != "" {
		t.Fatalf("denied compact_context must render no block, got %q", got)
	}
}

func TestPlannerModePromptMentionsStateFilesWhenCompactContextVisible(t *testing.T) {
	// P1b: planning mode may default to compact_context, so the planning-mode
	// prompt must tell the model that the plan file it is allowed to write can
	// be listed in state_files for a durable checkpoint.
	a := modelDrivenPromptTestAgent(t)
	block := a.plannerModePromptBlock()
	for _, want := range []string{"compact_context", "state_files", ".chord/plans/"} {
		if !strings.Contains(block, want) {
			t.Fatalf("planning-mode prompt must mention %q when compact_context is visible, got:\n%s", want, block)
		}
	}
}

func TestModelDrivenContextPromptBlockNotInSubAgentPrompt(t *testing.T) {
	// SubAgents never see the compact_context tool, so their prompt must not
	// carry the guidance either; the block is MainAgent-only by construction
	// (compactContextVisible lives on MainAgent).
	s := &SubAgent{}
	if got := s.buildSystemPrompt(); strings.Contains(got, "Long-session context management") {
		t.Fatalf("SubAgent prompt must not include model-driven context guidance")
	}
}
