package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// ReloadAgentsMD reloads project AGENTS.md from disk and marks the startup
// gate (agentsMDReady) so ensureSessionBuilt can proceed. The content is
// consumed the next time ensureSessionBuilt rebuilds the session-context
// reminder (on session-head events). Mid-session edits to AGENTS.md are not
// picked up until the next /new, /resume, or equivalent reset; AGENTS.md is
// treated as a session-scope snapshot.
func (a *MainAgent) ReloadAgentsMD() bool {
	content := loadAgentsMDWithWorkDir(a.contentRoot, a.workDir())

	a.promptMetaMu.Lock()
	if content == a.cachedAgentsMD {
		a.promptMetaMu.Unlock()
		a.markAgentsMDReady()
		return false
	}
	a.cachedAgentsMD = content
	a.promptMetaMu.Unlock()
	a.markAgentsMDReady()
	return true
}

func (a *MainAgent) refreshSystemPrompt() {
	a.llmMu.RLock()
	override := a.systemPromptOverride
	a.llmMu.RUnlock()
	if override != "" {
		a.installSystemPrompt(override)
		return
	}
	a.installSystemPrompt(a.buildSystemPrompt())
}

func (a *MainAgent) setSystemPromptOverride(prompt string) {
	a.llmMu.Lock()
	a.systemPromptOverride = prompt
	a.llmMu.Unlock()
	a.installSystemPrompt(prompt)
}

func (a *MainAgent) clearSystemPromptOverride() {
	a.llmMu.Lock()
	a.systemPromptOverride = ""
	a.llmMu.Unlock()
}

func (a *MainAgent) installSystemPrompt(prompt string) {
	a.llmMu.Lock()
	if prompt == a.installedSysPrompt {
		a.llmMu.Unlock()
		return
	}
	a.installedSysPrompt = prompt
	client := a.llmClient
	a.llmMu.Unlock()

	if client != nil {
		client.SetSystemPrompt(prompt)
	}
	a.ctxMgr.SetSystemPrompt(message.Message{
		Role:    message.RoleSystem,
		Content: prompt,
	})
}

// buildSystemPrompt constructs the default system prompt that is injected at
// the start of every conversation. It is cache-stable framing: it varies with
// Memory load and tool visibility, not with environment or time. Identity,
// guidelines, capabilities, and reminder framing live here. Dynamic
// environment fields (working directory, platform, date, venv) are delivered
// via the session-context reminder before the first user message to keep this
// prefix cache-stable.
func (a *MainAgent) buildSystemPrompt() string {
	_, _, agentsMD, _ := a.promptMetaSnapshot()

	var parts []string
	parts = append(parts, mainAgentIdentityPrompt)
	parts = append(parts, sharedAgentValuesPrompt)
	// Dynamic environment info (working directory, platform, date, venv) is
	// injected via the session-context reminder before the first user message to
	// keep the system prompt cache-stable and maximize prefix cache reuse.
	parts = append(parts, sharedCodingGuidelinesPrompt)
	parts = append(parts, sharedContentTrustPrompt)
	parts = append(parts, sharedReasoningDisciplinePrompt)
	if block := a.lspDiagnosticPromptBlock(); block != "" {
		parts = append(parts, block)
	}
	parts = append(parts, mainAgentCommunicationPrompt)
	parts = append(parts, a.responseClosurePromptBlock())
	if block := a.userConfirmationPromptBlock(); block != "" {
		parts = append(parts, block)
	}
	if block := a.mainAgentRolePromptBlock(); block != "" {
		parts = append(parts, block)
	}
	if block := a.mainAgentCapabilityPromptBlock(); block != "" {
		parts = append(parts, block)
	}
	if block := a.modelDrivenContextPromptBlock(); block != "" {
		parts = append(parts, block)
	}
	if block := a.primaryAgentCoordinationPromptBlock(); block != "" {
		parts = append(parts, block)
	}
	if block := agentsMDReminderFramingPromptBlock(agentsMD); block != "" {
		parts = append(parts, block)
	}
	// AGENTS.md is injected as a meta user message (under a
	// "# AGENTS.md instructions" / <INSTRUCTIONS> self-identifying block) via
	// injectSessionContextReminder to keep the stable system prompt
	// small and cacheable.
	if a.memoryIsActive() {
		// Only the fixed discipline lives in the stable prompt; the actual
		// memory data is injected under an untrusted wrapper in the reminder so
		// a stale/poisoned MEMORY.md can never escalate into instructions. The
		// extraction note is appended only when auto-extraction is on.
		block := memoryStableGuidancePrompt
		if a.memoryExtractEnabled.Load() {
			block += "\n" + memoryExtractionGuidancePrompt
		}
		parts = append(parts, block)
	}
	if block := a.availableSkillsPromptBlock(); block != "" {
		parts = append(parts, block)
	}

	mcpBlock := a.visibleMCPServersPromptBlock()
	if mcpBlock != "" {
		parts = append(parts, mcpBlock)
	}
	// pendingLoopContinuation, bug triage hint, and SubAgent mailbox are
	// per-turn overlays assembled by buildTurnOverlayMessages; they do not
	// belong in the stable system prompt.

	return strings.Join(parts, "\n\n")
}

func (a *MainAgent) visibleMCPServersPromptBlock() string {
	a.mcpServersPromptMu.RLock()
	block := a.mcpServersPrompt
	a.mcpServersPromptMu.RUnlock()
	if block == "" {
		return ""
	}
	servers := mcp.ParseServersPromptBlock(block)
	if servers == nil {
		// Block not produced by the mcp renderer: pass through unfiltered.
		return block
	}

	serverNames := make([]string, 0, len(servers))
	for _, srv := range servers {
		serverNames = append(serverNames, srv.Name)
	}
	visibility := a.mcpVisibilitySnapshot(serverNames)
	for _, srv := range servers {
		visibility.knownTools[srv.Name] = append(visibility.knownTools[srv.Name], srv.Tools...)
	}

	filtered := make([]mcp.ServerTools, 0, len(servers))
	for _, srv := range servers {
		allowed := make(map[string]struct{})
		for _, name := range visibility.visibleToolNames(srv.Name) {
			allowed[name] = struct{}{}
		}
		visibleTools := make([]string, 0, len(srv.Tools))
		for _, name := range srv.Tools {
			if _, ok := allowed[tools.NormalizeName(name)]; ok {
				visibleTools = append(visibleTools, name)
			}
		}
		if len(visibleTools) == 0 {
			continue
		}
		filtered = append(filtered, mcp.ServerTools{Name: srv.Name, Tools: visibleTools})
	}
	if len(filtered) == 0 {
		return ""
	}
	return mcp.RenderServersPromptBlock(filtered)
}

const agentsMDInstructionRequirement = "Treat these loaded sections as mandatory scoped workspace instructions and follow every applicable instruction within the instruction priority order. Do not use file, search, or shell tools to rediscover or reread them. Read an additional AGENTS.md only when entering a directory whose instructions were not loaded. Beyond that, inspect only the task-relevant project files needed to understand, modify, or verify the requested work."

func agentsMDReminderFramingPromptBlock(agentsMD string) string {
	if strings.TrimSpace(agentsMD) == "" {
		return ""
	}
	return "## Workspace Instructions\nEach applicable AGENTS.md is already loaded in the labeled \"# AGENTS.md instructions\" block before the first visible user message; follow the requirement stated at the top of that block."
}

// takePendingLoopContinuationPromptBlock renders and consumes the
// request-scoped continuation note. It runs while the request goroutine
// assembles overlays, and a busy /loop off can clear the note from the event
// loop at the same time, so both sides go through loopReductionMu.
//
// Consumption is one-shot and unacknowledged: a request that never reaches the
// provider (a hook block, a governor rejection, or a transport failure before
// the send) does not re-attach the note to the following request. The Done
// rejection that produced the note stays visible in the tool result, so the
// next request still carries the substance of the feedback.
func (a *MainAgent) takePendingLoopContinuationPromptBlock() string {
	a.loopReductionMu.Lock()
	note := a.pendingLoopContinuation
	a.pendingLoopContinuation = nil
	a.loopReductionMu.Unlock()
	if note == nil {
		return ""
	}
	return "## " + note.Title + "\n\n" + note.Text
}

func (a *MainAgent) setPendingLoopContinuation(note *LoopContinuationNote) {
	a.loopReductionMu.Lock()
	a.pendingLoopContinuation = note
	a.loopReductionMu.Unlock()
}

func (a *MainAgent) questionToolAvailable() bool {
	visible := a.mainLLMVisibleToolNames()
	if len(visible) == 0 {
		return false
	}
	if _, ok := visible[tools.NameQuestion]; !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) > 0 && ruleset.Evaluate(tools.NameQuestion, "*") == permission.ActionDeny {
		return false
	}
	return true
}

// doneToolVisibleNow reports whether done is on the live tool surface, or
// armed to be late-mounted into it, so the model can actually call it on the
// next request. Prompt blocks that name done must gate on this rather than on
// doneToolPermitted: outside a loop the tool is not mounted, and naming it
// would point the model at a tool it cannot call.
func (a *MainAgent) doneToolVisibleNow() bool {
	if !a.doneToolPermitted() {
		return false
	}
	// done is mounted for the duration of a loop (mountDoneForLoopEntry), so an
	// active loop is what makes it callable. Consulting only the live tool
	// surface would race loop entry: the surface is rebuilt on the next
	// request, while the loop's completion contract is rendered the moment the
	// loop starts, and the contract would then describe a role without done.
	if a.loopExitAuthorized() {
		return true
	}
	// Outside a loop, done can still sit on a surface frozen while one was
	// running, or be armed for late mount into it.
	if visible := a.mainLLMVisibleToolNames(); len(visible) > 0 {
		if _, ok := visible[tools.NameDone]; ok {
			return true
		}
	}
	return a.loopDoneLateMount.Load()
}

// responseClosurePromptBlock renders the Response Closure section with the
// Done guidance only when the done tool is available in this role (same
// availability source as questionToolAvailable), so the prompt never
// references a tool the model cannot call.
func (a *MainAgent) responseClosurePromptBlock() string {
	return mainAgentResponseClosurePromptText(a.doneToolVisibleNow())
}

func (a *MainAgent) userConfirmationPromptBlock() string {
	// The asking threshold and the information standard for questions live only
	// in sharedCodingGuidelinesPrompt (single source, visible to both agents);
	// these branches only decide which channel carries a necessary question.
	if a.questionToolAvailable() {
		question := toolPromptName(tools.NameQuestion)
		return `## Structured User Confirmation
- Default to making ordinary implementation decisions yourself; the Guidelines section defines when asking the user is justified and what information a question must carry
` + executionAuthorizationLine + `
- When a necessary confirmation would change scope, risk, implementation choice, or the permission policy itself (not approval for an individual tool call), prefer ` + question + ` so the user gets a structured decision UI instead of an unstructured text question
- Use plain assistant text only for lightweight clarifications that do not materially change the execution path`
	}
	return `## Plain-Text User Confirmation
- Default to making ordinary implementation decisions yourself; the Guidelines section defines when asking the user is justified and what information a question must carry
` + executionAuthorizationLine + `
- Because structured confirmation is unavailable in this tool/permission state, ask necessary user-decision questions in normal assistant text while meeting that same information standard
- When a clarification does not materially change the execution path, keep it brief and focused`
}

func (a *MainAgent) lspDiagnosticPromptBlock() string {
	if !a.shouldInjectLSPDiagnosticPrompt() {
		return ""
	}
	visible := a.mainVisibleLLMToolNames()
	editToolName := visibleEditToolName(visible)
	if editToolName == "" {
		editToolName = tools.NameEdit
	}
	toolRefs := toolPromptName(editToolName)
	if _, ok := visible[tools.NameWrite]; ok {
		toolRefs += " or " + toolPromptName(tools.NameWrite)
	}
	return strings.TrimSpace(`## LSP diagnostic follow-up
- When LSP diagnostics are available after your ` + toolRefs + ` changes, treat new blocking diagnostics in files you directly modified as regressions and fix them before finishing unless the user explicitly asked for a partial/WIP result
- If your current-session edits introduce non-blocking diagnostics in files you directly modified, prefer low-risk cleanup when it is small and clear; do not expand scope to unrelated historical diagnostics in untouched files unless they directly block the requested task
- Coverage is not guaranteed: a language server may be missing, fail to start, refuse the workspace (for example a toolchain the server cannot load), or not be registered for a file type. A tool result without a diagnostics block then means no diagnostics were reported, not that the file is verified: use the project's compiler, linter, or tests when a check matters`)
}

func (a *MainAgent) shouldInjectLSPDiagnosticPrompt() bool {
	visible := a.mainVisibleLLMToolNames()
	if len(visible) == 0 {
		return false
	}
	if visibleEditToolName(visible) == "" {
		if _, ok := visible[tools.NameWrite]; !ok {
			return false
		}
	}
	return hasEnabledLSPServers(a.globalConfig, a.projectConfig)
}

func hasEnabledLSPServers(globalCfg, projectCfg *config.Config) bool {
	seen := make(map[string]struct{})
	if projectCfg != nil {
		for name, srv := range projectCfg.LSP {
			seen[name] = struct{}{}
			if !srv.Disabled {
				return true
			}
		}
	}
	if globalCfg != nil {
		for name, srv := range globalCfg.LSP {
			if _, overridden := seen[name]; overridden {
				continue
			}
			if !srv.Disabled {
				return true
			}
		}
	}
	return false
}

func (a *MainAgent) loopContinuationDecisionInstructionLine() string {
	return "- Continue autonomously from the existing context; ask the user only when the Guidelines' asking threshold is met, never merely because the automatic " + toolPromptName(tools.NameDone) + " interception budget is low."
}

// loopCompletionDecisionRequirementLine renders the loop's exit contract.
// These lines are only ever rendered inside a loop, and entering a loop mounts
// done, so the question here is whether done *can* be mounted rather than
// whether it is on the surface at this instant — the latter would race the
// tool-surface rebuild that loop entry schedules. The fallback covers a
// mid-loop rule change that denies done, because a contract demanding a tool
// the model cannot call would leave the loop with no clean way to finish.
func (a *MainAgent) loopCompletionDecisionRequirementLine() string {
	if !a.doneToolPermitted() {
		return "- The " + toolPromptName(tools.NameDone) + " completion tool is not available in this role; write the complete final Markdown completion report directly in the final assistant response instead"
	}
	done := toolPromptName(tools.NameDone)
	return "- In this loop workflow, the " + done + " tool is the explicitly required completion signal\n" +
		"- Do not call the " + done + " tool while required work or a user decision remains; follow the verification requirements above. If further progress is possible, continue working instead of calling " + done + "\n" +
		"- Pass the complete final Markdown completion report in the " + done + " tool's required `report` argument, following the report structure in its tool description"
}

func (a *MainAgent) plannerPermissionAdjustmentInstruction() string {
	if a.questionToolAvailable() {
		return "use " + toolPromptName(tools.NameQuestion) + " to ask the user to adjust permissions, scope, or approach"
	}
	return "ask the user in plain assistant text to adjust permissions, scope, or approach"
}

func (a *MainAgent) plannerModePromptBlock() string {
	visible := a.mainLLMVisibleToolNames()
	hasFileWrite := false
	hasHandoff := false
	if len(visible) > 0 {
		// apply_patch can create files (`*** Add File:`), so a patch-native
		// surface without the write tool can still save the plan document.
		_, hasWrite := visible[tools.NameWrite]
		_, hasPatch := visible[tools.NameApplyPatch]
		hasFileWrite = hasWrite || hasPatch
		_, hasHandoff = visible[tools.NameHandoff]
	}
	handoff := toolPromptName(tools.NameHandoff)
	fileWriteStep := "3. Save the plan document under .chord/plans/ as YYYYMMDD-<slug>.md, using today's date and a short descriptive slug derived from the task title (for example .chord/plans/20260903-session-key-isolation.md). If a file with the same date and slug already exists (a later revision of the same topic), append -2, -3, and so on. When a new plan document replaces an earlier one, declare it with a supersedes: <old file> line near the top; move finished or superseded documents to .chord/plans/archive/ (archive files keep their original names). Write the plan before handing it off or finishing the planning turn."
	if hasFileWrite {
		fileWriteStep += " Write the plan document with the visible file tools available in this role."
	} else {
		fileWriteStep += " If this role cannot write the plan file, explain the limitation and " + a.plannerPermissionAdjustmentInstruction() + "."
	}
	if a.compactContextVisible() {
		fileWriteStep += " Once saved, this plan document can be listed in the compact_context tool's state_files parameter when a checkpoint is worthwhile, following the tool's file-reference and permission rules. Use planned_state_files only for paths that are not yet written; those paths are not completion evidence."
	}
	handoffStep := "4. "
	if hasHandoff {
		handoffStep += "Call " + handoff + " only after the plan file exists, and only when the request actually needs execution: never hand off a direct answer or a plan the user only asked to review."
	} else {
		handoffStep += "Handoff is unavailable in this role. Return the saved plan path or the plan content needed for the next step, and explain the limitation clearly."
	}
	return strings.TrimSpace(`

## Planning Mode

You are in planning mode: analyse the user's request, explore the codebase as
needed, and either answer directly or produce a concrete execution plan.

### Decide what to deliver first
Answer directly and stop — no plan document and no ` + handoff + ` — when the user
asks for any of the following:
- An explanation, recommendation, comparison, or diagnosis
- A read-only review or analysis — including verification of a report or existing
  plan — whose deliverable is the conclusion itself
- A change small enough to describe without a task breakdown (a plan with a
  single task is not a plan)

Create a plan document only when the request needs concrete implementation work
for an execution role, or when the user explicitly asks for a plan. If the user
asked only for the plan, save the document and return a summary without calling
` + handoff + `.

### Workflow
1. Explore the codebase using the tools and permissions available in this role.
2. Analyse the requirements and decompose them into concrete, independently-
   executable tasks.
` + fileWriteStep + `
` + handoffStep + `

### When the user rejects handoff
The rejection is appended to the conversation as a user message ("Handoff
rejected: <reason>") and becomes the latest request driving this turn:
- Revise the existing plan file referenced by that message; do not create a new
  plan document or rename the existing one for the same plan.
- Call ` + handoff + ` again only after addressing the rejection reason.

### Plan Document Format
Write a Markdown document with this structure:

    # <Goal description>

    ## Constraints
    - <constraint 1>
    - <constraint 2>

    ## Tasks

    ### 1. <Task title>
    <What to change, naming the specific files>
    Verify: <how to tell this task succeeded>

    ### 2. <Task title> (depends: 1)
    <What to change, naming the specific files>
    Verify: <how to tell this task succeeded>

Rules:
- Each task is a ### heading with a numeric ID followed by a dot
- Dependencies are declared in parentheses: (depends: 1, 2)
- Task IDs are immutable — never renumber existing tasks
- New tasks always take max(existing IDs) + 1
- Make tasks granular enough for independent execution; each names the specific
  file(s) to modify and carries a Verify line
- Use concrete verbs such as "add", "remove", "rename", "extract" instead of vague
  ones such as "handle", "improve", "update"
- Do NOT include status markers; execution tracks progress in its own todo list
`)
}

// mainAgentRolePromptBlock renders the active role's prompt block.
//
// Layering: a built-in preset block supplies the base, an agent's own
// prompt/system_prompt replaces that base (unchanged from before prompt_preset
// existed), and prompt_append is added after whichever base survived so a role
// can add project conventions without owning the whole block.
func (a *MainAgent) mainAgentRolePromptBlock() string {
	activeCfg := a.currentActiveConfig()
	base := ""
	if a.shouldUsePlannerPrompt(activeCfg) {
		base = a.plannerModePromptBlock()
	}
	if activeCfg == nil {
		return base
	}
	if custom := strings.TrimSpace(activeCfg.SystemPrompt); custom != "" {
		base = custom
	}
	extra := strings.TrimSpace(activeCfg.PromptAppend)
	switch {
	case extra == "":
		return base
	case base == "":
		return extra
	default:
		return base + "\n\n" + extra
	}
}

func (a *MainAgent) mainAgentCapabilityPromptBlock() string {
	visibleTools := a.mainVisibleLLMTools()
	visible := toolNamesFromVisibleTools(visibleTools)
	return buildDynamicCapabilityPromptBlock(visible, a.effectiveRuleset(), capabilityPromptAudienceMain)
}

// modelDrivenContextPromptBlock renders passive long-session guidance for the
// compact_context tool. It is only injected when the tool is actually visible
// and executable (enabled + registered + not denied by permission rules);
// otherwise the tool does not exist on the model's surface and the guidance
// would have no referent — no system prompt may push a tool that is invisible
// or denied. The tool description owns checkpoint timing and preparation;
// this block owns the long-session principle and recovery reading order.
func (a *MainAgent) modelDrivenContextPromptBlock() string {
	if !a.compactContextVisible() {
		return ""
	}
	return "## Long-session context management\n" +
		"- For long tasks, maintain a concise task-notes file as reusable findings accumulate, within your file permissions. Keep a short current handoff (completed work, remaining work, blockers, next action) separate from reusable details. Record key code locations, verified conclusions and their conditions, successful commands with working directory, required environment variables and arguments, reusable script/log paths, and failed approaches with retry conditions. Replace stale status; link to large results instead of copying them. The compact_context tool description governs checkpoint timing and preparation.\n" +
		"- After a reset, start from the checkpoint and any injected file content. Use the registered task notes as the detail source: read the relevant sections only for missing or changed information needed for the next action, before repeating searches or experiments. Reuse results while their code and conditions remain unchanged and the referenced artifacts remain available. Notes are recovery aids, not proof: current code and verification results take precedence over notes. Read archived history only for exact details unavailable there."
}

// shouldUsePlannerPrompt reports whether the active role gets the built-in
// planning block. The decision is the role's resolved prompt preset, not its
// name, so a role named anything can request the block and a role named
// "planner" can decline it with prompt_preset: none.
func (a *MainAgent) shouldUsePlannerPrompt(activeCfg *config.AgentConfig) bool {
	return activeCfg.ResolvePromptPreset() == config.PromptPresetPlanning
}

func (a *MainAgent) promptMetaSnapshot() (workDir, gitStatus, agentsMD, venvPath string) {
	a.promptMetaMu.RLock()
	defer a.promptMetaMu.RUnlock()
	return a.workDirLocked(), a.cachedGitStatus, a.cachedAgentsMD, a.cachedVenvPath
}

func (a *MainAgent) cachedAgentsMDSnapshot() string {
	a.promptMetaMu.RLock()
	defer a.promptMetaMu.RUnlock()
	return a.cachedAgentsMD
}

// cachedWorkDirSnapshot returns the directory the session started in. Readers
// outside the constructor take the lock like every other prompt-meta reader:
// the async git status fetch and tests pin the field at different times.
func (a *MainAgent) cachedWorkDirSnapshot() string {
	a.promptMetaMu.RLock()
	defer a.promptMetaMu.RUnlock()
	return a.cachedWorkDir
}

func (a *MainAgent) cachedVenvPathSnapshot() string {
	a.promptMetaMu.RLock()
	defer a.promptMetaMu.RUnlock()
	return a.cachedVenvPath
}

func (a *MainAgent) setCachedGitStatus(status string) {
	a.promptMetaMu.Lock()
	a.cachedGitStatus = status
	a.promptMetaMu.Unlock()
}

func (a *MainAgent) setCachedVenvPath(path string) {
	a.promptMetaMu.Lock()
	a.cachedVenvPath = path
	a.promptMetaMu.Unlock()
}

// refreshWorkDirDerivedMeta recomputes the per-checkout facts that every
// request injects: the git status (which names the branch) and the Python
// virtualenv path. Both are derived from the working directory, so every
// publication of a new workDir binding must call this — a switch that leaves
// them at the startup value keeps telling the model it is on the branch of the
// checkout it left, and a session that starts bound to a worktree never gets
// them right at all.
func (a *MainAgent) refreshWorkDirDerivedMeta() {
	if a == nil {
		return
	}
	workDir := a.workDir()
	a.setCachedGitStatus(getGitStatus(workDir))
	// A worktree checkout normally carries no virtualenv and the project's
	// lives in the main worktree, so prefer the active checkout's own
	// environment and fall back to the content root's instead of dropping the
	// hint the moment the agent switches.
	venv := detectVenvPath(workDir, a.contentRoot)
	if venv == "" && strings.TrimSpace(a.contentRoot) != "" && workDir != a.contentRoot {
		venv = detectVenvPath(a.contentRoot, a.contentRoot)
	}
	a.setCachedVenvPath(venv)
}

// loadAgentsMDWithWorkDir loads the AGENTS.md instructions that apply to the
// agent's current checkout, from the checkout root down to workDir.
//
// Every level resolves to exactly one file: the checkout's own copy when it
// exists (a branch may carry its own tracked instructions), otherwise the
// content root's copy (where gitignored local instructions live, and which the
// worktree checkout does not contain). Levels are never loaded from both
// checkouts, so the same instructions are not repeated.
func loadAgentsMDWithWorkDir(contentRoot, workDir string) string {
	if contentRoot == "" {
		return ""
	}

	absRoot, err := filepath.Abs(contentRoot)
	if err != nil {
		return ""
	}
	displayBase := absRoot
	checkoutRoot := absRoot
	// Level "" is the checkout root; deeper levels follow when workDir sits
	// below it.
	levels := []string{""}

	if workDir != "" {
		if absWork, werr := filepath.Abs(workDir); werr == nil {
			// Titles stay relative to the checkout the agent works in, even when
			// that checkout sits outside the content root (a chord-managed
			// worktree under the state directory).
			displayBase = absWork
			// The walk is relative to the checkout workDir sits in: a linked
			// worktree or submodule nested inside the repository has its own
			// root, not the content root's.
			checkoutRoot = pathutil.CheckoutRoot(absWork, absRoot)
			rel, ok := relativePathWithin(checkoutRoot, absWork)
			if !ok {
				rel = "."
			}
			if rel != "." {
				parts := strings.Split(rel, string(filepath.Separator))
				for i := 1; i <= len(parts); i++ {
					levels = append(levels, filepath.Join(parts[:i]...))
				}
			}
		}
	}

	var sections []string
	for _, rel := range levels {
		checkoutPath := filepath.Join(checkoutRoot, rel, "AGENTS.md")
		contentPath := filepath.Join(absRoot, rel, "AGENTS.md")
		path, data, ok := readAgentsMDLevel(checkoutPath, contentPath)
		if !ok {
			continue
		}
		log.Debugf("loaded AGENTS.md path=%v size=%v", path, len(data))
		display := displayPathFromWorkDir(displayBase, path)
		if display == "" {
			display = "AGENTS.md"
		}
		sections = append(sections, fmt.Sprintf("## %s\n\n%s", display, data))
	}
	return strings.Join(sections, "\n\n")
}

// readAgentsMDLevel reads one AGENTS.md level, preferring the checkout's own
// copy and falling back to the content root's copy. It returns the file that
// was read and its trimmed non-empty content.
func readAgentsMDLevel(checkoutPath, contentPath string) (string, string, bool) {
	for _, path := range []string{checkoutPath, contentPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Warnf("failed to read AGENTS.md path=%v error=%v", path, err)
			}
			continue
		}
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			return path, trimmed, true
		}
	}
	return "", "", false
}

func displayPathFromWorkDir(workDir, path string) string {
	if path == "" {
		return ""
	}
	if workDir == "" {
		return filepath.ToSlash(path)
	}
	absWork, werr := filepath.Abs(workDir)
	absPath, perr := filepath.Abs(path)
	if werr != nil || perr != nil {
		return filepath.ToSlash(path)
	}
	rel, rerr := filepath.Rel(absWork, absPath)
	if rerr != nil || rel == "" {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func relativePathWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// mainLLMVisibleToolNames returns the tool names of the frozen tool surface
// (i.e. the tools the model was actually told it can call this turn). Callers
// that need the live, model-appropriate tool set for prompt rendering should use
// mainVisibleLLMToolNames instead.
func (a *MainAgent) mainLLMVisibleToolNames() map[string]struct{} {
	if a.tools == nil {
		return nil
	}
	defs := a.mainLLMToolDefinitions()
	visible := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		visible[def.Name] = struct{}{}
	}
	return visible
}

// mainVisibleLLMToolNames returns the names of the live, model-appropriate
// visible tool set (the same source used to build the tool declarations and the
// capability prompt). Prompt blocks that name a specific tool must derive it
// from this set so the rendered prompt never references a tool that the current
// model is not allowed to use.
func (a *MainAgent) mainVisibleLLMToolNames() map[string]struct{} {
	return toolNamesFromVisibleTools(a.mainVisibleLLMTools())
}

func (a *MainAgent) availableSkillsPromptBlock() string {
	loadedSkills := a.visibleSkillsSnapshot()
	if len(loadedSkills) == 0 {
		return ""
	}
	// visibleSkillsSnapshot already filters through ModelVisibleForRuleset,
	// so no second ruleset check here: SubAgent.availableSkillsPromptBlock
	// uses the same snapshot directly.
	entries := make([]tools.SkillListingEntry, 0, len(loadedSkills))
	for _, s := range loadedSkills {
		if s == nil {
			continue
		}
		entries = append(entries, tools.SkillListingEntry{Name: s.Name, Desc: s.Description})
	}
	if len(entries) == 0 {
		return ""
	}
	header := "## Available Skills\nThe `skill` tool can load additional skill instructions on demand. When a task clearly matches one of these skills, call `skill` before proceeding.\n\n"
	return tools.BuildSkillListing(entries, header)
}

// getGitStatus checks whether the working directory is inside a git repository
// by walking up from workDir to find .git (directory or file for submodules/worktrees),
// matching git's "is-inside-work-tree" semantics. No git binary is invoked. It
// returns the <env> line describing the result; the branch is only read when
// the working directory is set up — at startup and on a working-directory
// switch — so the line says when it was captured, and a branch switched in a
// shell afterwards is not reflected.
func getGitStatus(workDir string) string {
	gitRoot, headPath := findGitHead(workDir)
	if gitRoot == "" {
		return "Git repository: no"
	}
	if branch := readGitHeadBranch(headPath); branch != "" {
		return "Git repository: yes (branch " + branch + ", captured when the working directory was set up)"
	}
	return "Git repository: yes"
}

// findGitHead walks up from dir looking for .git (directory or file). Returns the
// repo root and the path to HEAD for branch reading, or ("", "") if not inside a repo.
func findGitHead(workDir string) (gitRoot, headPath string) {
	if workDir == "" {
		return "", ""
	}
	dir, err := filepath.Abs(workDir)
	if err != nil {
		return "", ""
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil && info != nil {
			if info.IsDir() {
				return dir, filepath.Join(gitPath, "HEAD")
			}
			// .git is a file (submodule or worktree): content is "gitdir: <path>\n"
			content, err := os.ReadFile(gitPath)
			if err != nil {
				return dir, ""
			}
			line := strings.TrimSpace(strings.Split(string(content), "\n")[0])
			const prefix = "gitdir: "
			if !strings.HasPrefix(line, prefix) {
				return dir, ""
			}
			gitDir := strings.TrimSpace(line[len(prefix):])
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(dir, gitDir)
			}
			return dir, filepath.Join(gitDir, "HEAD")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

// detectVenvPath searches for a Python virtual environment directory from the
// given working directory upward. At each level it checks for .venv, venv, and
// env directories (in that order) and returns the absolute path of the first one
// that exists and contains a pyvenv.cfg file. Returns "" if none is found.
func detectVenvPath(workDir, contentRoot string) string {
	if workDir == "" {
		return ""
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	absRoot := ""
	if contentRoot != "" {
		if root, rerr := filepath.Abs(contentRoot); rerr == nil {
			absRoot = root
		}
	}
	if absRoot != "" {
		if _, ok := relativePathWithin(absRoot, absDir); !ok {
			absRoot = absDir
		}
	}
	for dir := absDir; ; dir = filepath.Dir(dir) {
		for _, name := range []string{".venv", "venv", "env"} {
			candidate := filepath.Join(dir, name)
			info, err := os.Stat(candidate)
			if err != nil || !info.IsDir() {
				continue
			}
			cfgPath := filepath.Join(candidate, "pyvenv.cfg")
			if _, err := os.Stat(cfgPath); err != nil {
				continue
			}
			return candidate
		}
		if absRoot != "" && dir == absRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return ""
}

// readGitHeadBranch reads the branch name from a git HEAD file. HEAD contains
// either "ref: refs/heads/<branch>\n" or a SHA (detached HEAD); only the
// former is returned.
func readGitHeadBranch(headPath string) string {
	content, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(content))
	const refPrefix = "ref: refs/heads/"
	if strings.HasPrefix(line, refPrefix) {
		return strings.TrimSpace(line[len(refPrefix):])
	}
	return ""
}
