package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// loadAgentsMDWithWorkDir
// ---------------------------------------------------------------------------

func TestLoadAgentsMDWithWorkDir_RootOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "root instructions")

	got := loadAgentsMDWithWorkDir(dir, "")
	if got != "root instructions" {
		t.Fatalf("expected root instructions, got %q", got)
	}
}

func TestLoadAgentsMDWithWorkDir_Hierarchical(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg", "foo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "mid")
	writeFile(t, filepath.Join(sub, "AGENTS.md"), "deep")

	got := loadAgentsMDWithWorkDir(root, sub)
	parts := strings.Split(got, "\n\n")
	if len(parts) != 3 || parts[0] != "root" || parts[1] != "mid" || parts[2] != "deep" {
		t.Fatalf("unexpected layers: %q", got)
	}
}

func TestLoadAgentsMDWithWorkDir_WorkDirNotUnderRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(other, "AGENTS.md"), "other")

	// workDir not under projectRoot — should only read root
	got := loadAgentsMDWithWorkDir(root, other)
	if got != "root" {
		t.Fatalf("expected only root, got %q", got)
	}
}

func TestLoadAgentsMDWithWorkDir_MissingFiles(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only root has AGENTS.md; intermediates do not
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")

	got := loadAgentsMDWithWorkDir(root, sub)
	if got != "root" {
		t.Fatalf("expected only root, got %q", got)
	}
}

func TestTodoWorkflowPromptBlock_HiddenWithoutTodoWriteTool(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}

	if got := a.todoWorkflowPromptBlock(); got != "" {
		t.Fatalf("todoWorkflowPromptBlock() = %q, want empty", got)
	}
}

func TestTodoWorkflowPromptBlock_HiddenWhenTodoWriteDisabled(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
TodoWrite: deny
`)}
	a.rebuildRuleset()

	if got := a.todoWorkflowPromptBlock(); got != "" {
		t.Fatalf("todoWorkflowPromptBlock() = %q, want empty", got)
	}
}

func TestTodoWorkflowPromptBlock_ShownWhenTodoWriteAvailable(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
TodoWrite: allow
`)}
	a.rebuildRuleset()

	got := a.todoWorkflowPromptBlock()
	if !strings.Contains(got, "## Todo workflow") {
		t.Fatalf("todoWorkflowPromptBlock() missing title: %q", got)
	}
	if !strings.Contains(got, "before your final response") {
		t.Fatalf("todoWorkflowPromptBlock() missing final sync guidance: %q", got)
	}
	if !strings.Contains(got, "bug triage") {
		t.Fatalf("todoWorkflowPromptBlock() missing investigation guidance: %q", got)
	}
}

func TestLoopWorkflowPromptBlockHiddenByDefault(t *testing.T) {
	a := &MainAgent{}
	if got := a.loopWorkflowPromptBlock(); got != "" {
		t.Fatalf("loopWorkflowPromptBlock() = %q, want empty", got)
	}
}

func TestLoopWorkflowPromptBlockShownOnlyWhenLoopEnabled(t *testing.T) {
	a := &MainAgent{}
	a.loopState.enableWithTarget("finish current task")
	got := a.loopWorkflowPromptBlock()
	for _, want := range []string{"## Loop Mode", "Loop mode is active", "Do not stop to ask the user"} {
		if !strings.Contains(got, want) {
			t.Fatalf("loopWorkflowPromptBlock() missing %q in %q", want, got)
		}
	}
}

func TestBugTriagePromptBlock_HiddenWhenNotApplicable(t *testing.T) {
	a := &MainAgent{}
	if got := a.bugTriagePromptBlock(); got != "" {
		t.Fatalf("bugTriagePromptBlock() = %q, want empty", got)
	}
}

func TestBugTriagePromptBlock_ShownForBugAnalysisWorkflow(t *testing.T) {
	a := &MainAgent{}
	a.bugTriagePromptActive.Store(true)
	got := a.bugTriagePromptBlock()
	if !strings.Contains(got, "## Bug Triage Workflow") {
		t.Fatalf("bugTriagePromptBlock() missing title: %q", got)
	}
	if !strings.Contains(got, "direct trigger") {
		t.Fatalf("bugTriagePromptBlock() missing direct-trigger guidance: %q", got)
	}
	if !strings.Contains(got, "one-time high-level plan") {
		t.Fatalf("bugTriagePromptBlock() missing anti-narration guard: %q", got)
	}
}

func TestShouldEnableBugTriagePrompt_UsesLatestRealUserMessage(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: "[Context Summary]\n..."},
		{Role: "user", Content: "analyze this bug regression"},
	}
	if !shouldEnableBugTriagePrompt(msgs) {
		t.Fatal("expected bug triage prompt to activate for bug-analysis request")
	}
}

func TestSyncBugTriagePromptFromSnapshot_UsesCurrentContext(t *testing.T) {
	a := &MainAgent{ctxMgr: ctxmgr.NewManager(8192, false, 0)}
	a.ctxMgr.Append(message.Message{Role: "user", Content: "analyze this bug regression"})
	a.syncBugTriagePromptFromSnapshot()
	if !a.bugTriagePromptActive.Load() {
		t.Fatal("expected bug triage prompt to activate from current context snapshot")
	}
}

func TestBugTriagePromptBlock_HiddenForPlannerRole(t *testing.T) {
	a := &MainAgent{}
	a.bugTriagePromptActive.Store(true)
	a.activeConfig = &config.AgentConfig{Name: "planner"}
	if got := a.bugTriagePromptBlock(); got != "" {
		t.Fatalf("planner should not get bug triage block, got %q", got)
	}
}

func TestMainAgentRolePromptBlock_DefaultsEmptyForOtherRoles(t *testing.T) {
	a := &MainAgent{}
	got := a.mainAgentRolePromptBlock()
	if got != "" {
		t.Fatalf("mainAgentRolePromptBlock() = %q, want empty", got)
	}
}

func TestSharedCodingGuidelinesPrompt_ExcludesMainAgentOnlyCommunicationGuidance(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, unwanted := range []string{
		"before substantial work",
		"discover a root cause",
		"change direction",
		"complete a key implementation or verification step",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sharedCodingGuidelinesPrompt should not contain MainAgent-only communication guidance %q in %q", unwanted, got)
		}
	}
	for _, want := range []string{
		"Validate in layers: start with the most targeted check",
		"Do not narrate every routine action or restate obvious next steps",
		"Do not over-explain routine actions",
		"If multiple interpretations exist but one is clearly the best fit",
		"Remove imports, variables, and functions that your own changes made unused",
		"Do not remove pre-existing dead code unless asked",
		"state a brief plan with verifiable success criteria per step",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{
		"Do not narrate progress or restate what you are about to do",
		"Do not over-explain — lead with the action or answer",
		"Default to concise, direct, professional user-facing language",
		"Remove pleasantries, repeated phrasing, and long background setup that do not add information",
		"Do not repeat code, commands, paths, or test results just to sound complete",
		"Keep errors, limitations, unverified status, and risk clearly visible",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sharedCodingGuidelinesPrompt unexpectedly contains legacy guidance %q in %q", unwanted, got)
		}
	}
}

func TestUserConfirmationPromptBlock_UsesQuestionAvailabilitySpecificBranch(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}

	if got := a.userConfirmationPromptBlock(); !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("userConfirmationPromptBlock without Question should use plain-text branch, got %q", got)
	}

	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
Question: deny
`)}
	a.rebuildRuleset()
	if got := a.userConfirmationPromptBlock(); !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("userConfirmationPromptBlock with denied Question should use plain-text branch, got %q", got)
	}

	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Question: allow
`)}
	a.rebuildRuleset()
	got := a.userConfirmationPromptBlock()
	for _, want := range []string{
		"Structured User Confirmation",
		"Default to making ordinary implementation decisions yourself",
		"include enough context for a non-implementer to answer",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("userConfirmationPromptBlock missing %q in %q", want, got)
		}
	}
}

func TestBuildSystemPrompt_IncludesPermissionSpecificUserConfirmationGuidance(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}

	got := a.buildSystemPrompt()
	if strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("buildSystemPrompt unexpectedly included structured question guidance without Question tool: %q", got)
	}
	if !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("buildSystemPrompt missing plain-text confirmation guidance without Question tool: %q", got)
	}
	for _, want := range []string{
		"Because structured confirmation is unavailable in this tool/permission state",
		"include enough context for a non-implementer to answer",
		"their tradeoffs/risks, and your recommended default",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildSystemPrompt missing permission-specific clarification guidance %q without Question tool: %q", want, got)
		}
	}

	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
Question: deny
`)}
	a.rebuildRuleset()
	got = a.buildSystemPrompt()
	if strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("buildSystemPrompt unexpectedly included structured guidance when Question is denied: %q", got)
	}
	if !strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("buildSystemPrompt missing plain-text branch when Question is denied: %q", got)
	}

	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Question: allow
`)}
	a.rebuildRuleset()
	got = a.buildSystemPrompt()
	if !strings.Contains(got, "## Structured User Confirmation") {
		t.Fatalf("buildSystemPrompt missing structured guidance when Question tool is permitted: %q", got)
	}
	if strings.Contains(got, "## Plain-Text User Confirmation") {
		t.Fatalf("buildSystemPrompt should not keep plain-text branch when Question tool is permitted: %q", got)
	}
}

func TestSharedCodingGuidelinesPrompt_RequiresHighQualityClarificationsWithoutQuestionTool(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, want := range []string{
		"When a clarification or decision is necessary",
		"make it easy for a non-implementer to answer",
		"their tradeoffs/risks, and your recommended default",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
}

func TestSharedCodingGuidelinesPrompt_PrefersReasonableAutonomyBeforeAsking(t *testing.T) {
	got := sharedCodingGuidelinesPrompt
	for _, want := range []string{
		"Default to doing the most reasonable low-risk implementation work yourself",
		"If multiple interpretations exist but one is clearly the best fit",
		"Ask before implementing only when missing information is genuinely blocking",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedCodingGuidelinesPrompt missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{
		"present them rather than picking one silently; if assumptions are uncertain, ask before implementing",
		"do only what was asked",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sharedCodingGuidelinesPrompt unexpectedly contains legacy conservative guidance %q in %q", unwanted, got)
		}
	}
}

func TestSharedAgentValuesPrompt_AllowsNecessaryLowRiskAdjacentWork(t *testing.T) {
	got := sharedAgentValuesPrompt
	for _, want := range []string{
		"Complete the requested outcome with the smallest safe change set",
		"targeted regression tests",
		"required doc updates",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sharedAgentValuesPrompt missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Minimal > Comprehensive — do only what was asked") {
		t.Fatalf("sharedAgentValuesPrompt unexpectedly contains legacy minimalism guidance: %q", got)
	}
}

func TestUserConfirmationPromptBlock_RequiresContextTradeoffsAndRecommendation(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Question: allow
`)}
	a.rebuildRuleset()
	got := a.userConfirmationPromptBlock()
	for _, want := range []string{
		"Default to making ordinary implementation decisions yourself",
		"include enough context for a non-implementer to answer",
		"the main options, their tradeoffs/risks, and your recommended default",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("userConfirmationPromptBlock missing %q in %q", want, got)
		}
	}
}

func TestMainAgentCommunicationPrompt_PrefersAutonomyForLowRiskAdjacentWork(t *testing.T) {
	got := mainAgentCommunicationPrompt
	for _, want := range []string{
		"For low-risk, directly related, clearly necessary adjacent work",
		"Ask the user to choose only when there are materially different options",
		"Do not end responses with open-ended optional offers for routine in-scope next steps",
		"This applies to equivalent wording in any language",
		"if the next step is clearly necessary, low-risk, and within scope, do it instead of offering it",
		"keep the user oriented about the current direction; if the next step is still in scope and low-risk, do it instead of offering it as an option",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCommunicationPrompt missing %q in %q", want, got)
		}
	}
}

func TestMainAgentResponseClosurePrompt_RequiresContinueUnlessBlocked(t *testing.T) {
	got := mainAgentResponseClosurePrompt
	for _, want := range []string{
		"Within a normal turn, continue until the current in-scope work package is finished",
		"A regular assistant response is not the end of the task when in-scope work still remains",
		"If more in-scope, low-risk work remains, continue instead of stopping with a partial summary or optional offer",
		"ask exactly the necessary high-context question instead of pretending the task is complete",
		"After reporting completion, stop there; do not append routine in-scope follow-up work as an optional invitation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentResponseClosurePrompt missing %q in %q", want, got)
		}
	}
}

func TestMainAgentRolePromptBlock_UsesPlannerPromptOnlyForPlannerRole(t *testing.T) {
	a := &MainAgent{}
	a.activeConfig = &config.AgentConfig{Name: "planner"}
	got := a.mainAgentRolePromptBlock()
	for _, want := range []string{"Save the plan document to a path like .chord/plans/plan-001.md", "Explore the codebase using the tools and permissions available in this role."} {
		if !strings.Contains(got, want) {
			t.Fatalf("planner prompt missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"Use Read, Grep, Glob to explore the codebase", "Write the plan document using the Write tool", "Call Handoff with the plan file path.", "## Guidelines"} {
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
	a.activeConfig = &config.AgentConfig{Name: "planner"}
	got := a.mainAgentRolePromptBlock()
	if !strings.Contains(got, "If this role cannot write the plan file, explain the limitation and ask the user in plain assistant text to adjust permissions, scope, or approach.") {
		t.Fatalf("planner prompt without Write/Question should explain plain-text limitation handling, got %q", got)
	}
	if !strings.Contains(got, "Handoff is unavailable in this role.") {
		t.Fatalf("planner prompt without Handoff should state that limitation, got %q", got)
	}

	a.tools = tools.NewRegistry()
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.NewQuestionTool(nil))
	a.tools.Register(tools.HandoffTool{})
	a.activeConfig = &config.AgentConfig{Name: "planner", Permission: parsePermissionNode(t, `
"*": deny
Write: allow
Question: allow
Handoff: allow
`)}
	a.rebuildRuleset()
	got = a.mainAgentRolePromptBlock()
	if strings.Contains(got, "If this role cannot write the plan file") {
		t.Fatalf("planner prompt with Write should not emit write-limitation fallback, got %q", got)
	}
	if strings.Contains(got, "Handoff is unavailable in this role.") {
		t.Fatalf("planner prompt with Handoff should not claim it is unavailable, got %q", got)
	}
	if !strings.Contains(got, "If this role supports handoff to execution, do it only after the plan file exists.") {
		t.Fatalf("planner prompt with Handoff should include handoff path, got %q", got)
	}
}

func TestShouldEnableBugTriagePrompt_SupportsWhyAndConclusionReviewQueries(t *testing.T) {
	tests := []string{
		"why did this bug happen?",
		"which bug conclusion is more accurate?",
		"review whether the bug conclusion is correct",
	}
	for _, input := range tests {
		msgs := []message.Message{{Role: "user", Content: input}}
		if !shouldEnableBugTriagePrompt(msgs) {
			t.Fatalf("expected bug triage prompt to activate for %q", input)
		}
	}
}

func TestPrimaryAgentCoordinationPromptBlock_DependsOnVisibleTools(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.rebuildCachedSubAgents()
	if got := a.primaryAgentCoordinationPromptBlock(); got != "" {
		t.Fatalf("primaryAgentCoordinationPromptBlock() = %q, want empty", got)
	}

	a.tools.Register(tools.NewTodoWriteTool(nil))
	got := a.primaryAgentCoordinationPromptBlock()
	if !strings.Contains(got, "## Todo workflow") {
		t.Fatalf("expected todo workflow block, got %q", got)
	}
	if strings.Contains(got, "## Available Agent Types (for Delegate tool)") {
		t.Fatalf("did not expect Delegate block without Delegate tool, got %q", got)
	}

	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "General coding", Mode: "subagent"},
	}
	a.rebuildCachedSubAgents()
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "builder", Description: "General coding"}}}))
	got = a.primaryAgentCoordinationPromptBlock()
	if !strings.Contains(got, "## Available Agent Types (for Delegate tool)") {
		t.Fatalf("expected Delegate block once Delegate is visible, got %q", got)
	}
}

func TestPrimaryAgentCoordinationPromptBlock_ShowsTaskWorkflowWhenTaskVisible(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "builder", Description: "General coding"}}}))
	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "General coding", Mode: "subagent"},
	}
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
Delegate: allow
`)}
	a.rebuildRuleset()
	a.rebuildCachedSubAgents()

	got := a.primaryAgentCoordinationPromptBlock()
	for _, want := range []string{
		"## Available Agent Types (for Delegate tool)",
		"## SubAgent Workflow",
		"Dispatch tasks in parallel only when their write scopes are clearly independent",
		"For implementation tasks, first dispatch all currently independent tasks whose write scopes are clearly disjoint",
		"if there is no new independent task to send, stop doing implementation work in MainAgent",
		"Until you receive Escalate, Complete, or a clear error/blocked signal, do not take over implementation just because a SubAgent is briefly quiet",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in subagent workflow block, got %q", want, got)
		}
	}
}

func TestPrimaryAgentCoordinationPromptBlock_HidesSubAgentWorkflowWhenTaskDisabled(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "builder", Description: "General coding"}}}))
	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "General coding", Mode: "subagent"},
	}
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
Delegate: deny
TodoWrite: allow
`)}
	a.rebuildRuleset()
	a.rebuildCachedSubAgents()

	got := a.primaryAgentCoordinationPromptBlock()
	if strings.Contains(got, "## Available Agent Types (for Delegate tool)") {
		t.Fatalf("did not expect agent types when Delegate is denied, got %q", got)
	}
	if strings.Contains(got, "## SubAgent Workflow") {
		t.Fatalf("did not expect subagent workflow when Task is denied, got %q", got)
	}
}

func TestMainLLMToolDefinitionsIncludeSkillToolListing(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.skillsReady = make(chan struct{})
	a.loadedSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Location: "/tmp/go-expert/SKILL.md", RootDir: "/tmp/go-expert"}}
	a.tools.Register(tools.NewSkillTool(a))

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("mainLLMToolDefinitions() count = %d, want 1", len(defs))
	}
	if defs[0].Name != "Skill" {
		t.Fatalf("tool name = %q, want Skill", defs[0].Name)
	}
	for _, want := range []string{"Load a skill's full instructions on demand", "## Available Skills", "go-expert", "Go language development expert"} {
		if !strings.Contains(defs[0].Description, want) {
			t.Fatalf("missing %q in Skill description %q", want, defs[0].Description)
		}
	}
}

func TestMainLLMToolDefinitionsUseContextualBashDescription(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewBashTool("bash"))

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("mainLLMToolDefinitions() count = %d, want 1", len(defs))
	}
	for _, want := range []string{"Use Bash mainly for tests, builds, git, and other system commands.", "Prefer the smallest safe number of tool calls.", "Bash is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive."} {
		if !strings.Contains(defs[0].Description, want) {
			t.Fatalf("missing %q in Bash description %q", want, defs[0].Description)
		}
	}
	if strings.Contains(defs[0].Description, "use LSP first") {
		t.Fatalf("unexpected LSP hint without Lsp tool: %q", defs[0].Description)
	}

	a.tools.Register(tools.LspTool{})
	a.tools.Register(tools.GrepTool{})
	a.tools.Register(tools.GlobTool{})
	a.tools.Register(tools.ReadTool{})
	defs = a.mainLLMToolDefinitions()
	bashDesc := ""
	for _, def := range defs {
		if def.Name == "Bash" {
			bashDesc = def.Description
			break
		}
	}
	if bashDesc == "" {
		t.Fatal("missing Bash tool definition")
	}
	for _, want := range []string{"use LSP first", "use Grep for repo text search before reaching for rg", "use Glob for file or path discovery before reaching for rg --files or find", "use Read once you have narrowed the target files", "If file-reading, search, or code-navigation tools are hidden or denied in this role, Bash is not a substitute for them.", "Do not use shell commands or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.", "If file-editing tools are hidden or denied in this role, Bash is not a substitute for them.", "For explicit file deletions, prefer `Delete`", "Do not use shell redirection, heredocs, inline scripts, or `rm` as the default way to edit, write, or delete files when dedicated file tools are unavailable."} {
		if !strings.Contains(bashDesc, want) {
			t.Fatalf("missing %q in Bash description %q", want, bashDesc)
		}
	}
}

func TestMainLLMToolDefinitionsExcludeSubAgentOnlyCompleteTool(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.CompleteTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
Complete: allow
Read: allow
`)}
	a.rebuildRuleset()

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("mainLLMToolDefinitions() count = %d, want 1", len(defs))
	}
	if defs[0].Name != "Read" {
		t.Fatalf("visible tool = %q, want Read", defs[0].Name)
	}
}

func TestMainAgentCapabilityPromptBlock_UsesVisibleToolsOnly(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.GrepTool{})
	a.tools.Register(tools.GlobTool{})
	a.tools.Register(tools.NewBashTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Read: allow
Grep: allow
Glob: allow
Bash: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, want := range []string{
		"## Tool Selection",
		"Prefer the smallest safe number of tool calls. If one tool call can complete the task clearly and safely, do not split it into multiple steps.",
		"Use `Read` for file contents.",
		"Use `Glob` / `Grep` for discovery and navigation.",
		"Use `Bash` mainly for tests, builds, git, and system commands.",
		"## File Inspection Constraints",
		"File inspection and code-navigation capabilities may be limited in this role.",
		"Do not use `Bash`, shell commands, or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.",
		"## File Modification Constraints",
		"This role is currently read-only for files",
		"Do not use `Bash`, shell redirection, or inline scripts to simulate file edits, writes, or deletes.",
		"## Risk & Reporting",
		"ask the user for clarification when they need to choose between materially different options.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"Use `Edit`", "Use `Write`"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("mainAgentCapabilityPromptBlock() unexpectedly contains %q in %q", unwanted, got)
		}
	}
}

func TestMainAgentCapabilityPromptBlock_ShowsDeleteVsWriteFinalStateGuidance(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Write: allow
Delete: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, want := range []string{
		"Choose file tools by final state: use `Write` directly when a path should still exist afterward with new full contents, and use `Delete` only when the path should no longer exist.",
		"Do not `Delete` a path just to recreate it with `Write`; that adds unnecessary risk and tool churn.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing %q in %q", want, got)
		}
	}
}

func TestMainAgentCapabilityPromptBlock_ShowsInspectionConstraintsWhenInspectionToolsHiddenButBashVisible(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.GrepTool{})
	a.tools.Register(tools.GlobTool{})
	a.tools.Register(tools.LspTool{})
	a.tools.Register(tools.NewBashTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Bash: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, want := range []string{
		"## File Inspection Constraints",
		"This role has no direct file inspection or code-navigation tools available in the prompt.",
		"Do not use `Bash`, shell commands, or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing %q in %q", want, got)
		}
	}
}
func TestMainAgentCapabilityPromptBlock_UsesQuestionWhenVisible(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.NewBashTool("bash"))
	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
Question: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	if !strings.Contains(got, "see Structured User Confirmation for when to use `Question` versus plain assistant text.") {
		t.Fatalf("mainAgentCapabilityPromptBlock() should reference Structured User Confirmation when Question is visible, got %q", got)
	}
}

func TestMainAgentCapabilityPromptBlock_OmitsQuestionWhenHidden(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.NewBashTool("bash"))
	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
Question: deny
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	if strings.Contains(got, "`Question`") {
		t.Fatalf("mainAgentCapabilityPromptBlock() should not mention hidden Question tool, got %q", got)
	}
	if !strings.Contains(got, "ask the user for clarification when they need to choose between materially different options.") {
		t.Fatalf("mainAgentCapabilityPromptBlock() missing generic clarification guidance, got %q", got)
	}
}

func TestMainAgentCapabilityPromptBlock_ShowsLimitedFileScope(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.NewBashTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
Edit:
  "*": deny
  "internal/tui/*": allow
Write:
  "*": deny
  "internal/tui/*": allow
Delete: deny
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	if !strings.Contains(got, "Available file operations are limited in this role") {
		t.Fatalf("mainAgentCapabilityPromptBlock() missing limited file scope guidance: %q", got)
	}
	if !strings.Contains(got, "Only use the visible file tools and stay within the allowed permission scope") {
		t.Fatalf("mainAgentCapabilityPromptBlock() missing allowed-scope guidance: %q", got)
	}
	if strings.Contains(got, "This role is currently read-only for files") {
		t.Fatalf("mainAgentCapabilityPromptBlock() should not mark scoped-write mode as fully read-only: %q", got)
	}
}

func TestMainAgentCapabilityPromptBlock_OnlyTightenedPathsDoesNotImplyScopedWrites(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.NewBashTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
Bash: allow
Edit:
  "*": allow
  "internal/tui/*": ask
Write:
  "*": allow
  "internal/tui/*": ask
Delete: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	if strings.Contains(got, "## File Modification Constraints") {
		t.Fatalf("mainAgentCapabilityPromptBlock() should not imply scoped write mode when files remain broadly writable, got %q", got)
	}
}

func TestBuildSystemPrompt_AppendsDynamicCapabilitiesAfterCustomPrompt(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.NewBashTool("bash"))
	a.activeConfig = &config.AgentConfig{
		SystemPrompt: "## Custom Role\n- Follow the custom workflow",
		Permission: parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
`),
	}
	a.rebuildRuleset()

	got := a.buildSystemPrompt()
	if !strings.Contains(got, "## Guidelines") {
		t.Fatalf("buildSystemPrompt() missing shared guidelines: %q", got)
	}
	if !strings.Contains(got, "## User Communication") {
		t.Fatalf("buildSystemPrompt() missing main-agent communication block: %q", got)
	}
	for _, want := range []string{
		"Before substantial work",
		"Group related upcoming actions into one short preamble",
		"Skip preambles for trivial single-file reads",
		"discover a root cause",
		"complete a key implementation or verification step",
		"Default to concise, direct, professional user-facing language",
		"Remove pleasantries, repeated phrasing, and long background setup that do not add information",
		"For simple tasks, prefer short paragraphs; expand only for complex tradeoffs or higher-risk changes",
		"Do not repeat code, commands, paths, or test results just to sound complete",
		"Keep errors, limitations, unverified status, and risk clearly visible",
		"Do not assume the user inferred the key conclusion from tool cards or raw command output",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildSystemPrompt() missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, "## Custom Role") {
		t.Fatalf("buildSystemPrompt() missing custom prompt: %q", got)
	}
	if !strings.Contains(got, "## Tool Selection") {
		t.Fatalf("buildSystemPrompt() missing dynamic capability block: %q", got)
	}
	userCommunicationIdx := strings.Index(got, "## User Communication")
	customRoleIdx := strings.Index(got, "## Custom Role")
	toolSelectionIdx := strings.Index(got, "## Tool Selection")
	if userCommunicationIdx == -1 || customRoleIdx == -1 || toolSelectionIdx == -1 {
		t.Fatalf("buildSystemPrompt() missing expected section order markers: %q", got)
	}
	if userCommunicationIdx > customRoleIdx {
		t.Fatalf("buildSystemPrompt() should place main-agent communication before custom role body, got %q", got)
	}
	if customRoleIdx > toolSelectionIdx {
		t.Fatalf("buildSystemPrompt() should append dynamic capabilities after custom role body, got %q", got)
	}
	if !strings.Contains(got, "Use `Read` for file contents.") || !strings.Contains(got, "Use `Bash` mainly for tests, builds, git, and system commands.") || !strings.Contains(got, "Prefer the smallest safe number of tool calls.") {
		t.Fatalf("buildSystemPrompt() missing visible-tool guidance: %q", got)
	}
}

func TestSubAgentBuildSystemPrompt_AppendsDynamicCapabilitiesAfterCustomPrompt(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.NewBashTool("bash"))
	permNode := parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
`)
	ruleset := permission.ParsePermission(&permNode)
	s := &SubAgent{
		tools:        reg,
		ruleset:      ruleset,
		customPrompt: "## Custom SubAgent Role\n- Stay focused on the assigned task",
		workDir:      "/tmp/project",
		taskDesc:     "Inspect the parser and report findings.",
	}

	got := s.buildSystemPrompt()
	for _, want := range []string{subAgentIdentityPrompt, sharedAgentValuesPrompt, "## Guidelines", "## SubAgent Coordination", "## SubAgent Task Closure", "## Custom SubAgent Role", "## Tool Selection", "Prefer the smallest safe number of tool calls. If one tool call can complete the task clearly and safely, do not split it into multiple steps.", "Use `Read` for file contents.", "## Your Task"} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildSystemPrompt() missing %q in %q", want, got)
		}
	}
	for _, want := range []string{
		"`Notify` is unavailable in this role; do not assume you can send non-blocking progress updates to the owner agent",
		"`Escalate` is unavailable in this role; if you cannot proceed independently, explain the blocker clearly in assistant text and wait for owner follow-up",
		"Call `Complete` when the task is done",
		"If you are blocked and no control tool is available, explain the blocker clearly in assistant text and wait for owner follow-up.",
		"Focus on finishing the assigned task or reaching a real blocker; do not stop at a partial summary when in-scope work still remains",
		"continue instead of presenting routine next steps as optional follow-up for the owner agent",
		"include the key result and verification status in that completion",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildSystemPrompt() missing coordination guidance %q in %q", want, got)
		}
	}
	if strings.Contains(got, "## User Communication") {
		t.Fatalf("buildSystemPrompt() should not include MainAgent-only communication block, got %q", got)
	}
	for _, unwanted := range []string{
		"Default to concise, direct, professional user-facing language",
		"Remove pleasantries, repeated phrasing, and long background setup that do not add information",
		"For simple tasks, prefer short paragraphs; expand only for complex tradeoffs or higher-risk changes",
		"Do not repeat code, commands, paths, or test results just to sound complete",
		"Keep errors, limitations, unverified status, and risk clearly visible",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("buildSystemPrompt() should not include MainAgent-only concise communication guidance %q, got %q", unwanted, got)
		}
	}
}

func TestSubAgentBuildSystemPrompt_AdaptsControlGuidanceToVisibleTools(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.NewBashTool("bash"))
	reg.Register(tools.CompleteTool{})
	permNode := parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
`)
	ruleset := permission.ParsePermission(&permNode)
	s := &SubAgent{tools: reg, ruleset: ruleset, workDir: "/tmp/project", taskDesc: "Inspect the parser and report findings."}
	got := s.buildSystemPrompt()
	for _, want := range []string{
		"`Notify` is unavailable in this role; do not assume you can send non-blocking progress updates to the owner agent",
		"`Escalate` is unavailable in this role; if you cannot proceed independently, explain the blocker clearly in assistant text and wait for owner follow-up",
		"If you are blocked and no control tool is available, explain the blocker clearly in assistant text and wait for owner follow-up.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent prompt missing %q in %q", want, got)
		}
	}

	reg.Register(tools.NewEscalateTool(nil))
	reg.Register(tools.NewNotifyTool(nil, nil, true, false))
	permNode = parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
Escalate: allow
Notify: allow
`)
	ruleset = permission.ParsePermission(&permNode)
	s = &SubAgent{tools: reg, ruleset: ruleset, workDir: "/tmp/project", taskDesc: "Inspect the parser and report findings."}
	got = s.buildSystemPrompt()
	for _, want := range []string{
		"Use `Notify` to surface progress, clarifications, or intermediate results",
		"Call `Escalate` when owner-agent intervention, a cross-task dependency, or a decision is required",
		"Call `Escalate` if you are blocked.",
		"continue instead of presenting routine next steps as optional follow-up for the owner agent",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent prompt missing escalatable guidance %q in %q", want, got)
		}
	}
}

func TestMainAndSubCapabilityPromptBlocksUseAudienceSpecificEscalation(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.EditTool{})
	reg.Register(tools.NewBashTool("bash"))
	reg.Register(tools.NewQuestionTool(nil))
	permNode := parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
Question: allow
Edit:
  "*": deny
  "internal/tui/*": allow
`)
	ruleset := permission.ParsePermission(&permNode)

	a := &MainAgent{tools: reg, activeConfig: &config.AgentConfig{Permission: permNode}}
	a.rebuildRuleset()
	s := &SubAgent{tools: reg, ruleset: ruleset}

	mainBlock := a.mainAgentCapabilityPromptBlock()
	subBlock := s.capabilityPromptBlock()
	if !strings.Contains(mainBlock, "ask to adjust permissions, scope, or approach") {
		t.Fatalf("main capability block missing user-facing escalation wording: %q", mainBlock)
	}
	if !strings.Contains(mainBlock, "see Structured User Confirmation for when to use `Question` versus plain assistant text") {
		t.Fatalf("main capability block missing Structured User Confirmation reference: %q", mainBlock)
	}
	if !strings.Contains(subBlock, "Question` when the user must choose between materially different options") && !strings.Contains(subBlock, "Use `Question` when the user must choose between materially different options") {
		t.Fatalf("sub capability block missing Question guidance: %q", subBlock)
	}
	if !strings.Contains(subBlock, "explain the limitation clearly in assistant text because `Escalate` and `Notify` are unavailable in this role") {
		t.Fatalf("sub capability block should acknowledge missing control tools, got %q", subBlock)
	}

	reg.Register(tools.NewNotifyTool(nil, nil, true, false))
	permNode = parsePermissionNode(t, `
"*": deny
Read: allow
Bash: allow
Notify: allow
`)
	ruleset = permission.ParsePermission(&permNode)
	s = &SubAgent{tools: reg, ruleset: ruleset}
	subBlock = s.capabilityPromptBlock()
	if !strings.Contains(subBlock, "use `Notify` to surface materially different decisions or owner-agent intervention because `Escalate` is unavailable") {
		t.Fatalf("sub capability block should fall back to Notify when Escalate is unavailable, got %q", subBlock)
	}
}

func TestExecutionStartInstructionStaysGenericAcrossRoleCapabilities(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	if got := a.executionStartInstruction(); strings.Contains(got, "Delegate") || strings.Contains(got, "directly") {
		t.Fatalf("executionStartInstruction() = %q, should stay generic without hard-coding Delegate/direct execution", got)
	}

	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
TodoWrite: allow
`)}
	a.rebuildRuleset()
	if got := a.executionStartInstruction(); !strings.Contains(got, "TodoWrite") || strings.Contains(got, "Delegate") || strings.Contains(got, "directly") {
		t.Fatalf("executionStartInstruction() = %q, want generic execution + TodoWrite without Delegate/direct wording", got)
	}

	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "General coding"}}}))
	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "Builder role", Mode: "primary"},
		"coder":   {Name: "coder", Description: "General coding", Mode: "subagent"},
	}
	a.rebuildCachedSubAgents()
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
TodoWrite: allow
Delegate: allow
`)}
	a.rebuildRuleset()
	if got := a.executionStartInstruction(); !strings.Contains(got, "TodoWrite") || strings.Contains(got, "Delegate") || strings.Contains(got, "directly") {
		t.Fatalf("executionStartInstruction() = %q, should remain generic even when delegate workflow is available", got)
	}
}

func TestExecutionPacingInstructionStaysGenericAcrossDelegateAccess(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	if got := a.executionPacingInstruction(); strings.Contains(got, "dispatch") || strings.Contains(got, "Delegate") {
		t.Fatalf("executionPacingInstruction() = %q, should not hard-code delegate worker pacing", got)
	}

	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "General coding"}}}))
	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "Builder role", Mode: "primary"},
		"coder":   {Name: "coder", Description: "General coding", Mode: "subagent"},
	}
	a.rebuildCachedSubAgents()
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Delegate: allow
`)}
	a.rebuildRuleset()
	if got := a.executionPacingInstruction(); strings.Contains(got, "dispatch") || strings.Contains(got, "Delegate") {
		t.Fatalf("executionPacingInstruction() = %q, should stay generic even when Delegate is available", got)
	}
}

func TestLoopCompletionRequirementLinesIncludeDoneTagContract(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	joined := strings.Join(a.loopCompletionRequirementLines(), "\n")
	if !strings.Contains(joined, "<done>reason</done>") {
		t.Fatalf("loop completion requirements should mention done tag contract, got %q", joined)
	}
	finalJoined := strings.Join(a.loopFinalCompletionResponseLines(), "\n")
	if !strings.Contains(finalJoined, "<done>reason</done>") {
		t.Fatalf("loop final completion requirements should mention done tag contract, got %q", finalJoined)
	}
}

// ---------------------------------------------------------------------------
// gitStatus injection
// ---------------------------------------------------------------------------

func TestLoopCompletionRequirementLinesUsePermissionSpecificConfirmationGuidance(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	lines := a.loopCompletionRequirementLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "ask in plain assistant text with enough context for a non-implementer to answer") {
		t.Fatalf("loop completion requirements without Question should use plain-text guidance, got %q", joined)
	}
	if strings.Contains(joined, "Question tool") {
		t.Fatalf("loop completion requirements without Question should not require Question tool, got %q", joined)
	}

	a.tools.Register(tools.NewQuestionTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Question: allow
`)}
	a.rebuildRuleset()
	joined = strings.Join(a.loopCompletionRequirementLines(), "\n")
	if !strings.Contains(joined, "call the `Question` tool") {
		t.Fatalf("loop completion requirements with Question should require Question tool, got %q", joined)
	}
}

func TestInjectGitStatusIntoFirstUserMessage_TextMessage(t *testing.T) {
	a := &MainAgent{}
	a.cachedGitStatus = "Git branch: main"

	msgs := []message.Message{{Role: "user", Content: "hello"}}
	if injected := a.injectGitStatusIntoFirstUserMessage(msgs); !injected {
		t.Fatal("expected git status injection to succeed")
	}
	if !strings.HasPrefix(msgs[0].Content, "Git branch: main\n\nhello") {
		t.Fatalf("expected git status prefix, got %q", msgs[0].Content)
	}
	if !a.gitStatusInjected.Load() {
		t.Fatal("expected gitStatusInjected to be true after injection")
	}
}

func TestInjectGitStatusIntoFirstUserMessage_MultipartMessage(t *testing.T) {
	a := &MainAgent{}
	a.cachedGitStatus = "Git branch: main"

	msgs := []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "hello"},
			{Type: "image", MimeType: "image/png", Data: []byte{1, 2, 3}},
		},
	}}
	if injected := a.injectGitStatusIntoFirstUserMessage(msgs); !injected {
		t.Fatal("expected git status injection to succeed for multipart message")
	}
	if len(msgs[0].Parts) != 3 {
		t.Fatalf("expected injected multipart message to have 3 parts, got %d", len(msgs[0].Parts))
	}
	if got := msgs[0].Parts[0]; got.Type != "text" || got.Text != "Git branch: main\n\n" {
		t.Fatalf("unexpected injected first part: %#v", got)
	}
	if got := msgs[0].Parts[1]; got.Type != "text" || got.Text != "hello" {
		t.Fatalf("unexpected original text part after injection: %#v", got)
	}
	if got := msgs[0].Parts[2]; got.Type != "image" || got.MimeType != "image/png" || len(got.Data) != 3 {
		t.Fatalf("unexpected original image part after injection: %#v", got)
	}
	if !a.gitStatusInjected.Load() {
		t.Fatal("expected gitStatusInjected to be true after multipart injection")
	}
}

func TestInjectGitStatusIntoFirstUserMessage_InjectsOnlyOnce(t *testing.T) {
	a := &MainAgent{}
	a.cachedGitStatus = "Git branch: main"

	msg1 := []message.Message{{Role: "user", Content: "hello"}}
	msg2 := []message.Message{{Role: "user", Content: "world"}}

	if injected := a.injectGitStatusIntoFirstUserMessage(msg1); !injected {
		t.Fatal("expected first injection to succeed")
	}
	if injected := a.injectGitStatusIntoFirstUserMessage(msg2); injected {
		t.Fatal("expected second injection to be skipped")
	}
	if strings.Contains(msg2[0].Content, "Git branch") {
		t.Fatalf("second call should not inject git status, got %q", msg2[0].Content)
	}
}

func TestGitStatusInjectedReset(t *testing.T) {
	a := &MainAgent{}
	a.cachedGitStatus = "Git branch: main"
	a.gitStatusInjected.Store(true)

	// Simulate session reset
	a.gitStatusInjected.Store(false)

	if a.gitStatusInjected.Load() {
		t.Fatal("expected gitStatusInjected to be false after reset")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectVenvPath_FindsVenvUnderWorkDir(t *testing.T) {
	tmp := t.TempDir()
	venvDir := filepath.Join(tmp, ".venv")
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(tmp)
	if got != venvDir {
		t.Fatalf("detectVenvPath(%q) = %q, want %q", tmp, got, venvDir)
	}
}

func TestDetectVenvPath_PrefersDotVenv(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{".venv", "venv", "env"} {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := detectVenvPath(tmp)
	want := filepath.Join(tmp, ".venv")
	if got != want {
		t.Fatalf("detectVenvPath(%q) = %q, want %q (.venv takes precedence)", tmp, got, want)
	}
}

func TestDetectVenvPath_ReturnsEmptyWhenNoVenv(t *testing.T) {
	tmp := t.TempDir()

	got := detectVenvPath(tmp)
	if got != "" {
		t.Fatalf("detectVenvPath(%q) = %q, want empty string", tmp, got)
	}
}

func TestDetectVenvPath_ReturnsEmptyWhenDirMissingPyvenvCfg(t *testing.T) {
	tmp := t.TempDir()
	// Create .venv directory but without pyvenv.cfg — not a valid venv.
	if err := os.MkdirAll(filepath.Join(tmp, ".venv"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(tmp)
	if got != "" {
		t.Fatalf("detectVenvPath(%q) = %q, want empty string (no pyvenv.cfg)", tmp, got)
	}
}

func TestDetectVenvPath_ReturnsEmptyForEmptyWorkDir(t *testing.T) {
	got := detectVenvPath("")
	if got != "" {
		t.Fatalf("detectVenvPath(\"\") = %q, want empty string", got)
	}
}

func TestDetectVenvPath_FindsVenvNotEnv(t *testing.T) {
	tmp := t.TempDir()
	// Only "venv" exists (not ".venv").
	venvDir := filepath.Join(tmp, "venv")
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venvDir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := detectVenvPath(tmp)
	if got != venvDir {
		t.Fatalf("detectVenvPath(%q) = %q, want %q", tmp, got, venvDir)
	}
}

func TestBuildSystemPrompt_IncludesVenvWhenDetected(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.cachedVenvPath = "/tmp/project/.venv"

	got := a.buildSystemPrompt()
	if !strings.Contains(got, "Python virtual environment: /tmp/project/.venv") {
		t.Fatalf("buildSystemPrompt() missing venv line when venvPath is set, got:\n%s", got)
	}
}

func TestBuildSystemPrompt_OmitsVenvWhenAbsent(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}

	got := a.buildSystemPrompt()
	if strings.Contains(got, "Python virtual environment") {
		t.Fatalf("buildSystemPrompt() unexpectedly included venv line when venvPath is empty, got:\n%s", got)
	}
}

func TestSubAgentBuildSystemPrompt_IncludesVenv(t *testing.T) {
	reg := tools.NewRegistry()
	s := &SubAgent{
		tools:    reg,
		workDir:  "/tmp/project",
		venvPath: "/tmp/project/.venv",
		taskDesc: "Run tests",
	}

	got := s.buildSystemPrompt()
	if !strings.Contains(got, "Python virtual environment: /tmp/project/.venv") {
		t.Fatalf("SubAgent buildSystemPrompt() missing venv line when venvPath is set, got:\n%s", got)
	}
}

func TestSubAgentBuildSystemPrompt_OmitsVenvWhenAbsent(t *testing.T) {
	reg := tools.NewRegistry()
	s := &SubAgent{
		tools:    reg,
		workDir:  "/tmp/project",
		taskDesc: "Run tests",
	}

	got := s.buildSystemPrompt()
	if strings.Contains(got, "Python virtual environment") {
		t.Fatalf("SubAgent buildSystemPrompt() unexpectedly included venv line when venvPath is empty, got:\n%s", got)
	}
}

func parsePermissionNode(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parse permission: %v", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return *doc.Content[0]
	}
	return doc
}

type taskCreatorStub struct {
	agents []tools.AgentInfo
}

func (s taskCreatorStub) CreateSubAgent(ctx context.Context, description, agentType string, planTaskRef, semanticTaskKey string, expectedWriteScope tools.WriteScope) (tools.TaskHandle, error) {
	return tools.TaskHandle{
		Status:             "started",
		TaskID:             "adhoc-1",
		AgentID:            "stub-subagent",
		Message:            "running in background",
		PlanTaskRef:        planTaskRef,
		SemanticTaskKey:    semanticTaskKey,
		ExpectedWriteScope: expectedWriteScope,
	}, nil
}

func (s taskCreatorStub) AvailableSubAgents() []tools.AgentInfo {
	return s.agents
}
