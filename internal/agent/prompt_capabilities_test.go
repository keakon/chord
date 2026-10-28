package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// Todo workflow discipline lives entirely in the TodoWrite tool description
// (single source); the system prompt no longer renders a separate todo block.
func TestTodoWriteDescription_CarriesWorkflowDiscipline(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
todo_write: allow
`)}
	a.rebuildRuleset()

	if !a.AllowMultipleInProgressTodos() {
		t.Fatal("AllowMultipleInProgressTodos() = false, want true with TodoWrite available")
	}
	if got := a.primaryAgentCoordinationPromptBlock(); strings.Contains(got, "## Todo workflow") {
		t.Fatalf("coordination block should not duplicate TodoWrite description guidance: %q", got)
	}
	desc := tools.TodoWriteTool{}.Description()
	for _, want := range []string{
		"Before the final response — if you used this tool, sync once more (all completed or cancelled)",
		"do not finish with pending/in_progress items unless you say what is left and why",
		"bug triage",
		"multiple in_progress items are allowed when each maps to a distinct live workstream and has a unique active_form",
		"update each item when its workstream completes, blocks, or is cancelled",
		"so the list stays aligned with real progress",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("TodoWrite description missing %q in %q", want, desc)
		}
	}
	if strings.Contains(desc, "Keep at most one todo item in_progress at a time.") {
		t.Fatalf("TodoWrite description unexpectedly included single in_progress guidance: %q", desc)
	}
}

// Tool Selection routes between tools; how to size a replacement or a hunk is
// each edit tool's own contract, stated once in its description.
func TestToolSelectionPromptBlock_UsesEditSpecificGuidance(t *testing.T) {
	editPrompt := toolSelectionPromptBlock(map[string]struct{}{tools.NameEdit: {}})
	if !strings.Contains(editPrompt, "re-read the small target range before editing") {
		t.Fatalf("edit prompt missing stale target re-read guidance: %q", editPrompt)
	}
	for _, unwanted := range []string{"hunk", "old_string/new_string", "replace_all"} {
		if strings.Contains(editPrompt, unwanted) {
			t.Fatalf("edit prompt restates edit-tool contract %q: %q", unwanted, editPrompt)
		}
	}
	if desc := (tools.EditTool{}).Description(); !strings.Contains(desc, "Prefer the smallest unique 2-4 line block") {
		t.Fatalf("edit description lost its sizing contract: %q", desc)
	}

	patchPrompt := toolSelectionPromptBlock(map[string]struct{}{tools.NameApplyPatch: {}})
	if !strings.Contains(patchPrompt, "re-read the small target range before patching") {
		t.Fatalf("patch prompt missing stale target re-read guidance: %q", patchPrompt)
	}
	for _, unwanted := range []string{"keep hunks small", "old_string/new_string"} {
		if strings.Contains(patchPrompt, unwanted) {
			t.Fatalf("patch prompt restates edit-tool contract %q: %q", unwanted, patchPrompt)
		}
	}
	if desc := (tools.ApplyPatchTool{}).Description(); !strings.Contains(desc, "put the enclosing function, test, or case name on the `@@` line") {
		t.Fatalf("apply_patch description lost the repeated-block anchor hint: %q", desc)
	}
}

func TestTodoWriteDescription_DelegateWorkflowAllowsMultipleInProgress(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewTodoWriteTool(nil))
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "General coding"}}}))
	a.agentConfigs = map[string]*config.AgentConfig{
		"coder": {Name: "coder", Description: "General coding", Mode: "subagent"},
	}
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
`)}
	a.rebuildRuleset()
	a.rebuildCachedSubAgents()

	if !a.AllowMultipleInProgressTodos() {
		t.Fatal("AllowMultipleInProgressTodos() = false, want true when TodoWrite is available")
	}
	desc := tools.TodoWriteTool{}.Description()
	for _, want := range []string{
		"multiple in_progress items are allowed when each maps to a distinct live workstream and has a unique active_form",
		"update each item when its workstream completes, blocks, or is cancelled",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("TodoWrite description missing %q in %q", want, desc)
		}
	}
	if strings.Contains(desc, "Keep at most one todo item in_progress at a time.") {
		t.Fatalf("TodoWrite description should not use single in_progress wording: %q", desc)
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
	if got != "" {
		t.Fatalf("primaryAgentCoordinationPromptBlock() = %q, want empty without Delegate tool (todo guidance lives in the TodoWrite description)", got)
	}

	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "General coding", Mode: "subagent"},
	}
	a.rebuildCachedSubAgents()
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "builder", Description: "General coding"}}}))
	got = a.primaryAgentCoordinationPromptBlock()
	if !strings.Contains(got, "## SubAgent Workflow") {
		t.Fatalf("expected Delegate block once Delegate is visible, got %q", got)
	}
}

func TestPrimaryAgentCoordinationPromptBlock_ShowsTaskWorkflowWhenTaskVisible(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "builder", Description: "General coding"}}}))
	a.tools.Register(tools.NewNotifyTool(nil, nil, false, true))
	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "General coding", Mode: "subagent"},
	}
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
delegate: allow
`)}
	a.rebuildRuleset()
	a.rebuildCachedSubAgents()

	got := a.primaryAgentCoordinationPromptBlock()
	for _, want := range []string{
		"## SubAgent Workflow",
		"prefer `notify` on the existing task instead of creating a new delegate",
		"Dispatch tasks in parallel only when their work is independent",
		"After choosing substantial independent work to delegate",
		"Continue independent work that cannot conflict with it",
		"Until you receive an escalation, a completion, or a clear error/blocked signal from a worker, do not take over implementation just because a SubAgent is briefly quiet",
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
delegate: deny
todo_write: allow
`)}
	a.rebuildRuleset()
	a.rebuildCachedSubAgents()

	got := a.primaryAgentCoordinationPromptBlock()
	if strings.Contains(got, "## SubAgent Workflow") {
		t.Fatalf("did not expect subagent workflow when Task is denied, got %q", got)
	}
}

func TestPrimaryAgentCoordinationPromptBlock_FiltersDeniedDelegateTargets(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.agentConfigs = map[string]*config.AgentConfig{
		"reviewer": {Name: "reviewer", Description: "Review changes", Mode: config.AgentModeSubAgent},
		"tester":   {Name: "tester", Description: "Run tests", Mode: config.AgentModeSubAgent},
	}
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
delegate:
  "*": deny
  reviewer: allow
`)}
	a.rebuildRuleset()
	a.rebuildCachedSubAgents()
	a.tools.Register(tools.NewDelegateTool(a))

	// Allowed-target filtering is asserted on the Delegate tool schema below:
	// the prompt no longer lists agent types (the role list moved into the
	// agent_type parameter description, whose enum is filtered per ruleset).
	tool, ok := a.tools.Get(tools.NameDelegate)
	if !ok {
		t.Fatal("Delegate tool missing")
	}
	properties := tool.Parameters()["properties"].(map[string]any)
	agentType := properties["agent_type"].(map[string]any)
	enum := agentType["enum"].([]string)
	if len(enum) != 1 || enum[0] != "reviewer" {
		t.Fatalf("Delegate agent_type enum = %v, want [reviewer]", enum)
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
	if defs[0].Name != "skill" {
		t.Fatalf("tool name = %q, want skill", defs[0].Name)
	}
	for _, want := range []string{"Load a skill's full instructions on demand", "startup catalog"} {
		if !strings.Contains(defs[0].Description, want) {
			t.Fatalf("missing %q in Skill description %q", want, defs[0].Description)
		}
	}
	for _, unwanted := range []string{"## Available Skills", "go-expert"} {
		if strings.Contains(defs[0].Description, unwanted) {
			t.Fatalf("Skill description should not embed the listing, found %q in %q", unwanted, defs[0].Description)
		}
	}
}

func TestMainLLMToolDefinitionsHideSkillToolWithoutAvailableSkills(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewSkillTool(a))

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 0 {
		t.Fatalf("mainLLMToolDefinitions() count = %d, want 0", len(defs))
	}
}

func TestMainAgentAvailableSkillsPromptBlockListsVisibleSkills(t *testing.T) {
	a := &MainAgent{}
	a.loadedSkills = []*skill.Meta{{Name: "go-expert", Description: "Go language development expert", Location: "/tmp/go-expert/SKILL.md", RootDir: "/tmp/go-expert"}}

	got := a.availableSkillsPromptBlock()
	for _, want := range []string{"## Available Skills", "call `skill` before proceeding", "go-expert", "Go language development expert"} {
		if !strings.Contains(got, want) {
			t.Fatalf("availableSkillsPromptBlock() missing %q in %q", want, got)
		}
	}
}

func TestMainAgentAvailableSkillsPromptBlockEmptyWithoutSkills(t *testing.T) {
	a := &MainAgent{}
	if got := a.availableSkillsPromptBlock(); got != "" {
		t.Fatalf("availableSkillsPromptBlock() = %q, want empty", got)
	}
}

func TestMainLLMToolDefinitionsUseContextualBashDescription(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.NewShellTool("bash"))

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("mainLLMToolDefinitions() count = %d, want 1", len(defs))
	}
	for _, want := range []string{"Use shell mainly for tests, builds, git, and other system commands.", "When one visible built-in tool can do the job directly, use it instead of simulating it in shell.", "shell is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive."} {
		if !strings.Contains(defs[0].Description, want) {
			t.Fatalf("missing %q in Shell description %q", want, defs[0].Description)
		}
	}

	a.tools.Register(tools.GrepTool{})
	a.tools.Register(tools.GlobTool{})
	a.tools.Register(tools.ReadTool{})
	defs = a.mainLLMToolDefinitions()
	bashDesc := ""
	for _, def := range defs {
		if def.Name == "shell" {
			bashDesc = def.Description
			break
		}
	}
	if bashDesc == "" {
		t.Fatal("missing Shell tool definition")
	}
	// The "do not simulate hidden tools" prohibition is owned by the File
	// Inspection / File Modification Constraints blocks; the shell description
	// keeps only the positive routing and must not restate it.
	if strings.Contains(bashDesc, "shell is not a substitute for them") {
		t.Fatalf("Shell description restates the shared simulation prohibition: %q", bashDesc)
	}
	// Discovery routing is owned by the shared Tool Selection block; the
	// shell description must not restate it.
	for _, unwanted := range []string{"reaching for rg", "once you have narrowed the target files"} {
		if strings.Contains(bashDesc, unwanted) {
			t.Fatalf("Shell description repeats shared discovery routing %q: %q", unwanted, bashDesc)
		}
	}
	// With neither delete nor apply_patch on the surface there is no dedicated
	// deletion tool to point at, so the routing hint must be omitted entirely.
	if strings.Contains(bashDesc, "For explicit file deletions") {
		t.Fatalf("unexpected deletion routing hint without a deletion-capable tool: %q", bashDesc)
	}

	a.tools.Register(tools.DeleteTool{})
	defs = a.mainLLMToolDefinitions()
	bashDesc = ""
	for _, def := range defs {
		if def.Name == "shell" {
			bashDesc = def.Description
			break
		}
	}
	if !strings.Contains(bashDesc, "For explicit file deletions, prefer `delete`") {
		t.Fatalf("missing delete routing hint with delete visible: %q", bashDesc)
	}
}

func TestMainLLMToolDefinitionsExcludeSubAgentOnlyCompleteTool(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.CompleteTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
complete: allow
read: allow
`)}
	a.rebuildRuleset()

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("mainLLMToolDefinitions() count = %d, want 1", len(defs))
	}
	if defs[0].Name != "read" {
		t.Fatalf("visible tool = %q, want read", defs[0].Name)
	}
}

func TestMainAgentCapabilityPromptBlock_UsesVisibleToolsOnly(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.ApplyPatchTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.GrepTool{})
	a.tools.Register(tools.GlobTool{})
	a.tools.Register(tools.NewShellTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
grep: allow
glob: allow
shell: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, want := range []string{
		"## Tool Selection",
		"Prefer the smallest safe number of tool calls. If one tool call can complete the task clearly and safely, do not split it into multiple steps.",
		"Use `read` for file contents when the target path is already known or has been verified.",
		"When the user provides complete file contents in a `<file path=...>` reference, treat that content as the working context; do not re-read the same file merely to obtain duplicate contents.",
		"Use `glob` / `grep` for discovery and navigation.",
		"When `grep` returns path:line:snippet hits, use those line numbers to read narrow ranges around relevant matches instead of scanning broad file chunks; when several matches land in the same file or the file is central to the change, one fuller read of that file beats repeated narrow reads.",
		"If you are unsure of the exact target path for `read`, use `glob` / `grep` to find or verify it before calling the path tool; do not guess plausible-looking paths.",
		"Before issuing a lookup, identify the independent read-only calls needed for the current step whose arguments are already known.",
		"Issue those calls together in the same response so they can execute in parallel",
		"If a search must first reveal a path or line range, wait for that result before constructing the dependent read.",
		"Preserve ordering around state-changing calls and keep expensive commands deliberate.",
		"Do not add unrelated lookups or enlarge read ranges merely to fill a batch.",
		"## File Modification Constraints",
		"This role is currently read-only for files",
		"Do not use `shell`, shell redirection, or inline scripts to simulate file edits, writes, or deletes.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"Use `edit`", "Use `write`", "Chord runs non-conflicting calls from the same response concurrently", "## File Inspection Constraints", "## Authorization & Decisions"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("mainAgentCapabilityPromptBlock() unexpectedly contains %q in %q", unwanted, got)
		}
	}
	// The main agent's authorization line lives with its confirmation channel.
	if confirm := a.userConfirmationPromptBlock(); !strings.Contains(confirm, "Execution authorization is handled by the permission system") {
		t.Fatalf("userConfirmationPromptBlock() missing authorization line: %q", confirm)
	}
}

func TestToolSelectionPromptBlockAdvertisesConcurrentSchedulingOnlyForCapableSurfaces(t *testing.T) {
	const guidance = "Chord runs non-conflicting calls from the same response concurrently"

	editSurface := map[string]struct{}{tools.NameEdit: {}, tools.NameRead: {}}
	if got := toolSelectionPromptBlock(editSurface); !strings.Contains(got, guidance) {
		t.Fatalf("edit surface missing concurrent scheduling guidance: %q", got)
	}
	mcpSurface := map[string]struct{}{"mcp_alpha_search": {}, tools.NameRead: {}}
	if got := toolSelectionPromptBlock(mcpSurface); !strings.Contains(got, guidance) {
		t.Fatalf("MCP surface missing concurrent scheduling guidance: %q", got)
	}
	readOnlySurface := map[string]struct{}{tools.NameRead: {}, tools.NameGrep: {}, tools.NameGlob: {}}
	if got := toolSelectionPromptBlock(readOnlySurface); strings.Contains(got, "non-conflicting calls") {
		t.Fatalf("read-only surface must not advertise concurrent scheduling: %q", got)
	}
}

func TestMainAgentCapabilityPromptBlock_PathVerificationMentionsOnlyVisibleDiscoveryTools(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.GrepTool{})
	a.tools.Register(tools.GlobTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
edit: allow
delete: allow
grep: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, want := range []string{
		"Use `grep` for discovery and navigation.",
		"If you are unsure of the exact target path for `read` / `edit` / `delete`, use `grep` to find or verify it before calling the path tool; do not guess plausible-looking paths.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing %q in %q", want, got)
		}
	}
	for _, unwanted := range []string{"`glob`"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("mainAgentCapabilityPromptBlock() unexpectedly mentions unavailable %q in %q", unwanted, got)
		}
	}

	a = &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.GrepTool{})
	a.tools.Register(tools.GlobTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
edit: allow
delete: allow
grep: allow
glob: allow
`)}
	a.rebuildRuleset()

	got = a.mainAgentCapabilityPromptBlock()
	if !strings.Contains(got, "Use `glob` / `grep` for discovery and navigation.") {
		t.Fatalf("mainAgentCapabilityPromptBlock() should include both discovery tools, got %q", got)
	}
}

func TestMainAgentCapabilityPromptBlock_OmitsPathDiscoveryGuidanceWithoutDiscoveryTools(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry(), modelName: "gpt-5.5"} // Set GPT model to use patch tool
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.ApplyPatchTool{})
	a.tools.Register(tools.DeleteTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
patch: allow
delete: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	// gpt-5 family is patch-native, so delete is hidden and its careful-deletion
	// guidance is carried by the `*** Delete File:` line instead.
	for _, want := range []string{
		"Use `read` for file contents when the target path is already known or has been verified.",
		"Use `apply_patch` for all file modifications with verified paths",
		"Only `*** Delete File:` paths you have verified exist",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Use `delete`") {
		t.Fatalf("mainAgentCapabilityPromptBlock() unexpectedly references the hidden delete tool in %q", got)
	}
	for _, unwanted := range []string{"for discovery and navigation", "to find or verify it before calling the path tool", "`glob`", "`grep`"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("mainAgentCapabilityPromptBlock() unexpectedly contains %q in %q", unwanted, got)
		}
	}
}

func TestMainAgentCapabilityPromptBlock_OmitsEditGuidanceWhenEditDenied(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry(), modelName: "claude-opus-4"} // Claude model uses edit tool
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.NewShellTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
edit: deny
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, unwanted := range []string{"Use `edit`", "`edit`"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("mainAgentCapabilityPromptBlock() unexpectedly mentions edit when denied: %q in %q", unwanted, got)
		}
	}
	for _, want := range []string{"Use `read`", "Use `write`", "Use `delete`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing visible tool %q in %q", want, got)
		}
	}
}

func TestMainAgentCapabilityPromptBlock_ShowsDeleteVsWriteFinalStateGuidance(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
write: allow
delete: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, want := range []string{
		"Choose file tools by final state: use `write` directly when a path should still exist afterward with new full contents, and use `delete` only when the path should no longer exist",
		"never `delete` a path just to recreate it with `write`",
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
	a.tools.Register(tools.NewShellTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
shell: allow
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	for _, want := range []string{
		"## File Inspection Constraints",
		"This role has no direct file inspection or code-navigation tools available in the prompt.",
		"Do not use `shell`, shell commands, or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("mainAgentCapabilityPromptBlock() missing %q in %q", want, got)
		}
	}
}
func TestMainAgentCapabilityPromptBlock_UsesQuestionWhenVisible(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.NewShellTool("bash"))
	a.tools.Register(testQuestionTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
question: allow
`)}
	a.rebuildRuleset()

	// The confirmation section owns the question-versus-text choice and the
	// authorization line; the capability block no longer points at it.
	got := a.userConfirmationPromptBlock()
	for _, want := range []string{"## Structured User Confirmation", "prefer `question`", "Execution authorization is handled by the permission system"} {
		if !strings.Contains(got, want) {
			t.Fatalf("userConfirmationPromptBlock() missing %q when Question is visible, got %q", want, got)
		}
	}
	if block := a.mainAgentCapabilityPromptBlock(); strings.Contains(block, "Structured User Confirmation") {
		t.Fatalf("mainAgentCapabilityPromptBlock() should not point at the confirmation section, got %q", block)
	}
}

func TestMainAgentCapabilityPromptBlock_OmitsQuestionWhenHidden(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.NewShellTool("bash"))
	a.tools.Register(testQuestionTool())
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
question: deny
`)}
	a.rebuildRuleset()

	got := a.mainAgentCapabilityPromptBlock()
	if strings.Contains(got, "`question`") {
		t.Fatalf("mainAgentCapabilityPromptBlock() should not mention hidden Question tool, got %q", got)
	}
	// The threshold and information standard for a question live in Guidelines
	// and the confirmation block, which also carries the authorization line.
	confirm := a.userConfirmationPromptBlock()
	for _, want := range []string{"## Plain-Text User Confirmation", "Execution authorization is handled by the permission system"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("userConfirmationPromptBlock() missing %q without Question, got %q", want, confirm)
		}
	}
	if strings.Contains(confirm, "`question`") {
		t.Fatalf("userConfirmationPromptBlock() should not mention hidden Question tool, got %q", confirm)
	}
}

func TestMainAgentCapabilityPromptBlock_ShowsLimitedFileScope(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.ApplyPatchTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.DeleteTool{})
	a.tools.Register(tools.NewShellTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
edit:
  "*": deny
  "internal/tui/*": allow
write:
  "*": deny
  "internal/tui/*": allow
delete: deny
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
	a.tools.Register(tools.NewShellTool("bash"))
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": allow
shell: allow
edit:
  "*": allow
  "internal/tui/*": ask
Write:
  "*": allow
  "internal/tui/*": ask
delete: allow
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
	a.tools.Register(tools.NewShellTool("bash"))
	a.activeConfig = &config.AgentConfig{
		SystemPrompt: "## Custom Role\n- Follow the custom workflow",
		Permission: parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
`),
	}
	a.rebuildRuleset()

	got := a.buildSystemPrompt()
	if !strings.Contains(got, "## Guidelines") {
		t.Fatalf("buildSystemPrompt() missing shared guidelines: %q", got)
	}
	if !strings.Contains(got, "## Reasoning Discipline") {
		t.Fatalf("buildSystemPrompt() missing shared reasoning discipline: %q", got)
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
	guidelinesIdx := strings.Index(got, "## Guidelines")
	reasoningIdx := strings.Index(got, "## Reasoning Discipline")
	userCommunicationIdx := strings.Index(got, "## User Communication")
	if guidelinesIdx == -1 || reasoningIdx == -1 || userCommunicationIdx == -1 || guidelinesIdx > reasoningIdx || reasoningIdx > userCommunicationIdx {
		t.Fatalf("buildSystemPrompt() should keep reasoning discipline after guidelines and before communication, got %q", got)
	}
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
	if !strings.Contains(got, "Use `read` for file contents when the target path is already known or has been verified.") || !strings.Contains(got, "Prefer the smallest safe number of tool calls.") {
		t.Fatalf("buildSystemPrompt() missing visible-tool guidance: %q", got)
	}
}

func TestSubAgentBuildSystemPrompt_AppendsDynamicCapabilitiesAfterCustomPrompt(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.ReadTool{})
	reg.Register(tools.NewShellTool("bash"))
	permNode := parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
`)
	ruleset := permission.ParsePermission(&permNode)
	s := &SubAgent{
		tools:        reg,
		customPrompt: "## Custom SubAgent Role\n- Stay focused on the assigned task",
		workDir:      "/tmp/project",
		taskDesc:     "Inspect the parser and report findings.",
	}
	s.setRuleset(ruleset)

	got := s.buildSystemPrompt()
	for _, want := range []string{subAgentIdentityPrompt, sharedAgentValuesPrompt, "## Guidelines", "## Reasoning Discipline", "## SubAgent Coordination", "## SubAgent Task Closure", "## Custom SubAgent Role", "## Tool Selection", "Prefer the smallest safe number of tool calls. If one tool call can complete the task clearly and safely, do not split it into multiple steps.", "Use `read` for file contents when the target path is already known or has been verified.", "## Your Task", "surface the open product decisions to the owner agent (through the coordination tools available in this role)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("buildSystemPrompt() missing %q in %q", want, got)
		}
	}
	guidelinesIdx := strings.Index(got, "## Guidelines")
	reasoningIdx := strings.Index(got, "## Reasoning Discipline")
	coordinationIdx := strings.Index(got, "## SubAgent Coordination")
	if guidelinesIdx == -1 || reasoningIdx == -1 || coordinationIdx == -1 || guidelinesIdx > reasoningIdx || reasoningIdx > coordinationIdx {
		t.Fatalf("buildSystemPrompt() should keep reasoning discipline after guidelines and before coordination, got %q", got)
	}
	for _, want := range []string{
		"`notify` is unavailable in this role; do not assume you can send non-blocking progress updates to the owner agent",
		"`escalate` and `notify` are unavailable in this role and plain assistant text does not reach the owner agent; if you cannot proceed independently, close the task with `complete` and set `blocked_reason`",
		"Call `complete` when the task is done",
		"Focus on finishing the assigned task or reaching a real blocker; do not stop at a partial summary when in-scope work still remains",
		"continue instead of presenting routine next steps as optional follow-up for the owner agent",
		"Deliver the final task report through `complete`: the key result and verification status in `summary`, the changed files in `files_changed`",
		"progress and blockers go through the coordination channels above",
		"do not compose the report a second time there",
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
	reg.Register(tools.NewShellTool("bash"))
	reg.Register(tools.CompleteTool{})
	permNode := parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
`)
	ruleset := permission.ParsePermission(&permNode)
	s := &SubAgent{tools: reg, workDir: "/tmp/project", taskDesc: "Inspect the parser and report findings."}
	s.setRuleset(ruleset)
	got := s.buildSystemPrompt()
	for _, want := range []string{
		"`notify` is unavailable in this role; do not assume you can send non-blocking progress updates to the owner agent",
		"`escalate` and `notify` are unavailable in this role and plain assistant text does not reach the owner agent; if you cannot proceed independently, close the task with `complete` and set `blocked_reason`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent prompt missing %q in %q", want, got)
		}
	}
	// The Coordination section is the single blocker route; the task section
	// does not repeat it.
	if !strings.Contains(got, "## Your Task\n\nInspect the parser and report findings.\n\nFocus only on this task.") || strings.Contains(got, "if you are blocked") {
		t.Fatalf("task section should defer blocker routing to Coordination: %q", got)
	}

	reg.Register(tools.NewEscalateTool(nil))
	reg.Register(tools.NewNotifyTool(nil, nil, true, false))
	permNode = parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
escalate: allow
notify: allow
`)
	ruleset = permission.ParsePermission(&permNode)
	s = &SubAgent{tools: reg, workDir: "/tmp/project", taskDesc: "Inspect the parser and report findings."}
	s.setRuleset(ruleset)
	got = s.buildSystemPrompt()
	for _, want := range []string{
		"Use `notify` to surface progress, clarifications, or intermediate results",
		"Call `escalate` when owner-agent intervention, a cross-task dependency, or a decision is required",
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
	reg.Register(tools.ApplyPatchTool{})
	reg.Register(tools.NewShellTool("bash"))
	reg.Register(testQuestionTool())
	permNode := parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
question: allow
edit:
  "*": deny
  "internal/tui/*": allow
`)
	ruleset := permission.ParsePermission(&permNode)

	a := &MainAgent{tools: reg, activeConfig: &config.AgentConfig{Permission: permNode}}
	a.rebuildRuleset()
	s := &SubAgent{tools: reg}
	s.setRuleset(ruleset)

	mainBlock := a.mainAgentCapabilityPromptBlock()
	subBlock := s.capabilityPromptBlock(s.visibleToolNames())
	if !strings.Contains(mainBlock, "ask to adjust permissions, scope, or approach") {
		t.Fatalf("main capability block missing user-facing escalation wording: %q", mainBlock)
	}
	if strings.Contains(mainBlock, "## Authorization & Decisions") {
		t.Fatalf("main capability block should leave authorization to the confirmation section: %q", mainBlock)
	}
	if !strings.Contains(subBlock, "Use `question` only for product choices the end user must make; owner-agent decisions — permissions, scope, or next steps — go through the coordination tools named in your SubAgent Coordination section instead") {
		t.Fatalf("sub capability block missing Question guidance: %q", subBlock)
	}
	if !strings.Contains(subBlock, "close the task with `complete` and set `blocked_reason`") || !strings.Contains(subBlock, "`escalate` and `notify` are unavailable in this role") {
		t.Fatalf("sub capability block should route blockers through complete when control tools are missing, got %q", subBlock)
	}

	reg.Register(tools.NewNotifyTool(nil, nil, true, false))
	permNode = parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
notify: allow
`)
	ruleset = permission.ParsePermission(&permNode)
	s = &SubAgent{tools: reg}
	s.setRuleset(ruleset)
	visible := s.visibleToolNames()
	subBlock = s.capabilityPromptBlock(visible)
	_, authBlock, _ := strings.Cut(subBlock, "## Authorization & Decisions")
	if strings.TrimSpace(authBlock) != executionAuthorizationLine {
		t.Fatalf("without question the authorization block must leave decision routing to the coordination section, got %q", authBlock)
	}
	if coordination := subAgentCoordinationPromptText(visible); !strings.Contains(coordination, "use `notify` to surface blockers or owner-agent decisions") {
		t.Fatalf("coordination section should route decisions to Notify when Escalate is unavailable, got %q", coordination)
	}

	// With escalate visible the Coordination section owns the escalate
	// routing; the authorization block must point there, not restate it.
	reg.Register(tools.NewEscalateTool(nil))
	permNode = parsePermissionNode(t, `
"*": deny
read: allow
shell: allow
question: allow
escalate: allow
`)
	ruleset = permission.ParsePermission(&permNode)
	s = &SubAgent{tools: reg}
	s.setRuleset(ruleset)
	visible = s.visibleToolNames()
	_, authBlock, _ = strings.Cut(s.capabilityPromptBlock(visible), "## Authorization & Decisions")
	authBlock, _, _ = strings.Cut(authBlock, "\n## ")
	if strings.Contains(authBlock, "`escalate`") || !strings.Contains(authBlock, "go through the coordination tools named in your SubAgent Coordination section") {
		t.Fatalf("authorization block must defer escalate routing to the coordination section, got %q", authBlock)
	}
	if coordination := subAgentCoordinationPromptText(visible); !strings.Contains(coordination, "`escalate`") {
		t.Fatalf("coordination section must own the escalate routing, got %q", coordination)
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
todo_write: allow
`)}
	a.rebuildRuleset()
	if got := a.executionStartInstruction(); !strings.Contains(got, "todo_write") || strings.Contains(got, "Delegate") || strings.Contains(got, "directly") {
		t.Fatalf("executionStartInstruction() = %q, want generic execution + TodoWrite without Delegate/direct wording", got)
	}

	a.tools.Register(tools.NewDelegateTool(taskCreatorStub{agents: []tools.AgentInfo{{Name: "coder", Description: "General coding"}}}))
	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Description: "Builder role", Mode: config.AgentModeMain},
		"coder":   {Name: "coder", Description: "General coding", Mode: "subagent"},
	}
	a.rebuildCachedSubAgents()
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
todo_write: allow
delegate: allow
`)}
	a.rebuildRuleset()
	if got := a.executionStartInstruction(); !strings.Contains(got, "todo_write") || strings.Contains(got, "Delegate") || strings.Contains(got, "directly") {
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
		"builder": {Name: "builder", Description: "Builder role", Mode: config.AgentModeMain},
		"coder":   {Name: "coder", Description: "General coding", Mode: "subagent"},
	}
	a.rebuildCachedSubAgents()
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
delegate: allow
`)}
	a.rebuildRuleset()
	if got := a.executionPacingInstruction(); strings.Contains(got, "dispatch") || strings.Contains(got, "Delegate") {
		t.Fatalf("executionPacingInstruction() = %q, should stay generic even when Delegate is available", got)
	}
}
