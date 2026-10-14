package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

func TestMainAgentRolePromptBlock_DefaultsEmptyForOtherRoles(t *testing.T) {
	a := &MainAgent{}
	got := a.mainAgentRolePromptBlock()
	if got != "" {
		t.Fatalf("mainAgentRolePromptBlock() = %q, want empty", got)
	}
}

func TestMainAgentRolePromptBlock_UsesPlannerPromptOnlyForPlannerRole(t *testing.T) {
	a := &MainAgent{}
	// The planning preset (which the built-in planner declares explicitly)
	// selects the block; the bare role name no longer does.
	a.activeConfig = &config.AgentConfig{Name: "planner", PromptPreset: config.PromptPresetPlanning}
	got := a.mainAgentRolePromptBlock()
	for _, want := range []string{"Save the plan document under .chord/plans/ as YYYYMMDD-<slug>.md, using today's date and a short descriptive slug derived from the task title", "Explore the codebase using the tools and permissions available in this role.", "Answer directly and stop — no plan document and no `handoff` — when the user\nasks for any", "When the user rejects handoff"} {
		if !strings.Contains(got, want) {
			t.Fatalf("planner prompt missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"Use Read, Grep, Glob to explore the codebase", "Write the plan document using the Write tool", "Call Handoff with the plan file path.", "## Guidelines", "just do the task directly"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("planner prompt should avoid static tool references %q in %q", unwanted, got)
		}
	}

	a.activeConfig = &config.AgentConfig{Name: "reviewer"}
	if got := a.mainAgentRolePromptBlock(); got != "" {
		t.Fatalf("non-planner role should not add extra role body, got %q", got)
	}
}

func TestPlannerModePromptBlock_UsesPermissionSpecificInstructions(t *testing.T) {
	a := &MainAgent{}
	// The planning preset (declared explicitly by the built-in planner) gates
	// the block; the role name no longer decides.
	a.activeConfig = &config.AgentConfig{Name: "planner", PromptPreset: config.PromptPresetPlanning}
	got := a.mainAgentRolePromptBlock()
	if !strings.Contains(got, "If this role cannot write the plan file, explain the limitation and ask the user in plain assistant text to adjust permissions, scope, or approach.") {
		t.Fatalf("planner prompt without Write/Question should explain plain-text limitation handling, got %q", got)
	}
	if !strings.Contains(got, "Handoff is unavailable in this role.") {
		t.Fatalf("planner prompt without Handoff should state that limitation, got %q", got)
	}

	a.tools = tools.NewRegistry()
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(testQuestionTool())
	a.tools.Register(tools.HandoffTool{})
	a.activeConfig = &config.AgentConfig{Name: "planner", PromptPreset: config.PromptPresetPlanning, Permission: parsePermissionNode(t, `
"*": deny
write: allow
question: allow
handoff: allow
`)}
	a.rebuildRuleset()
	got = a.mainAgentRolePromptBlock()
	if strings.Contains(got, "If this role cannot write the plan file") {
		t.Fatalf("planner prompt with Write should not emit write-limitation fallback, got %q", got)
	}
	if strings.Contains(got, "Handoff is unavailable in this role.") {
		t.Fatalf("planner prompt with Handoff should not claim it is unavailable, got %q", got)
	}
	if !strings.Contains(got, "Call `handoff` only after the plan file exists") {
		t.Fatalf("planner prompt with Handoff should include handoff path, got %q", got)
	}
	for _, want := range []string{
		"never hand off a direct answer or a plan the user only asked to review",
		"a plan with a\n  single task is not a plan",
		"Revise the existing plan file referenced by that message",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("planner prompt with Handoff missing %q in %q", want, got)
		}
	}
}

func TestMainAgentRolePromptBlock_PlanningPresetDecidesInsteadOfRoleName(t *testing.T) {
	const planningMarker = "Save the plan document under .chord/plans/ as YYYYMMDD-<slug>.md"

	// A custom role name reuses the built-in block by declaring the preset,
	// which is the whole point of keying on capability rather than on name.
	a := &MainAgent{}
	a.activeConfig = &config.AgentConfig{Name: "architect", PromptPreset: config.PromptPresetPlanning}
	if got := a.mainAgentRolePromptBlock(); !strings.Contains(got, planningMarker) {
		t.Fatalf("architect role with planning preset missing planning block, got %q", got)
	}

	// A role named planner can decline the block.
	a.activeConfig = &config.AgentConfig{Name: "planner", PromptPreset: config.PromptPresetNone}
	if got := a.mainAgentRolePromptBlock(); got != "" {
		t.Fatalf("planner role with prompt_preset none should emit no block, got %q", got)
	}

	// The name alone selects nothing: a role named planner with no preset gets
	// no block. The built-in planner ships with the preset declared; the
	// name-based fallback it once relied on is gone (and pinned gone here, so
	// it cannot silently come back as an implicit capability for other names).
	a.activeConfig = &config.AgentConfig{Name: "planner"}
	if got := a.mainAgentRolePromptBlock(); got != "" {
		t.Fatalf("planner role without an explicit preset must not get the planning block by name, got %q", got)
	}
}

func TestMainAgentRolePromptBlock_PromptAppendLayersOnTopOfBase(t *testing.T) {
	const planningMarker = "Save the plan document under .chord/plans/ as YYYYMMDD-<slug>.md"
	const extra = "Always cite the ADR number."

	// prompt_append keeps the preset block and adds to it, so the role does not
	// have to own the whole block to add one convention.
	a := &MainAgent{}
	a.activeConfig = &config.AgentConfig{
		Name:         "architect",
		PromptPreset: config.PromptPresetPlanning,
		PromptAppend: extra,
	}
	got := a.mainAgentRolePromptBlock()
	if !strings.Contains(got, planningMarker) {
		t.Fatalf("append must not drop the preset block, got %q", got)
	}
	if !strings.Contains(got, extra) {
		t.Fatalf("append missing %q in %q", extra, got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), extra) {
		t.Fatalf("append must come last, got %q", got)
	}

	// system_prompt keeps replacing the base; append then applies to it.
	a.activeConfig = &config.AgentConfig{
		Name:         "architect",
		PromptPreset: config.PromptPresetPlanning,
		SystemPrompt: "Custom body.",
		PromptAppend: extra,
	}
	got = a.mainAgentRolePromptBlock()
	if strings.Contains(got, planningMarker) {
		t.Fatalf("system_prompt must still replace the preset block, got %q", got)
	}
	if !strings.Contains(got, "Custom body.") || !strings.Contains(got, extra) {
		t.Fatalf("replaced base plus append expected, got %q", got)
	}

	// Append alone, with no preset and no system_prompt, is the whole block.
	a.activeConfig = &config.AgentConfig{Name: "reviewer", PromptAppend: extra}
	if got := a.mainAgentRolePromptBlock(); got != extra {
		t.Fatalf("append-only role block = %q, want %q", got, extra)
	}
}
