package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// buildSystemPrompt constructs the default system prompt that is injected at
// the start of every conversation. It is fully static: identity, guidelines,
// capabilities, and cache-stable reminder framing live here. Dynamic
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
	// keep the system prompt fully static and maximize prefix cache reuse.
	parts = append(parts, sharedCodingGuidelinesPrompt)
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

const agentsMDInstructionRequirement = "Treat these loaded sections as mandatory scoped workspace instructions and follow every applicable instruction at all times. Do not use file, search, or shell tools to rediscover or reread them. Read an additional AGENTS.md only when entering a directory whose instructions were not loaded. Beyond that, inspect only the task-relevant project files needed to understand, modify, or verify the requested work."

func agentsMDReminderFramingPromptBlock(agentsMD string) string {
	if strings.TrimSpace(agentsMD) == "" {
		return ""
	}
	return "## Workspace Instructions\nEach applicable AGENTS.md is already loaded in the labeled \"# AGENTS.md instructions\" block before the first visible user message. Follow it as mandatory scoped workspace instructions; do not reread AGENTS.md files with file, search, or shell tools."
}

func (a *MainAgent) pendingLoopContinuationPromptBlock() string {
	if a.pendingLoopContinuation == nil {
		return ""
	}
	return "## " + a.pendingLoopContinuation.Title + "\n\n" + a.pendingLoopContinuation.Text
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

func (a *MainAgent) doneToolAvailable() bool {
	visible := a.mainLLMVisibleToolNames()
	if len(visible) == 0 {
		return false
	}
	if _, ok := visible[tools.NameDone]; !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) > 0 && normalizeToolPermissionAction(tools.NameDone, ruleset.Evaluate(tools.NameDone, "*")) == permission.ActionDeny {
		return false
	}
	return true
}

// responseClosurePromptBlock renders the Response Closure section with the
// Done guidance only when the done tool is available in this role (same
// availability source as questionToolAvailable), so the prompt never
// references a tool the model cannot call.
func (a *MainAgent) responseClosurePromptBlock() string {
	return mainAgentResponseClosurePromptText(a.doneToolAvailable())
}

func (a *MainAgent) userConfirmationPromptBlock() string {
	// The asking threshold and the information standard for questions live only
	// in sharedCodingGuidelinesPrompt (single source, visible to both agents);
	// these branches only decide which channel carries a necessary question.
	if a.questionToolAvailable() {
		question := toolPromptName(tools.NameQuestion)
		return `## Structured User Confirmation
- Default to making ordinary implementation decisions yourself; the Guidelines section defines when asking the user is justified and what information a question must carry
- When a necessary confirmation would change scope, permissions, risk, or implementation choice, prefer ` + question + ` so the user gets a structured decision UI instead of an unstructured text question
- Use plain assistant text only for lightweight clarifications that do not materially change the execution path`
	}
	return `## Plain-Text User Confirmation
- Default to making ordinary implementation decisions yourself; the Guidelines section defines when asking the user is justified and what information a question must carry
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
- If your current-session edits introduce non-blocking diagnostics in files you directly modified, prefer low-risk cleanup when it is small and clear; do not expand scope to unrelated historical diagnostics in untouched files unless they directly block the requested task`)
}

func (a *MainAgent) shouldInjectLSPDiagnosticPrompt() bool {
	if ruleset := a.effectiveRuleset(); len(ruleset) > 0 && ruleset.IsDisabled(tools.NameLsp) {
		return false
	}
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
	return "- Continue autonomously from the existing context. Request user input only when a real external decision is strictly required to proceed, and do not ask merely because the automatic " + tools.NameDone + " interception budget is low."
}

func (a *MainAgent) loopCompletionDecisionRequirementLine() string {
	done := toolPromptName(tools.NameDone)
	return "- In this loop workflow, the " + done + " tool is the explicitly required completion signal\n" +
		"- Do not call the " + done + " tool unless the task is actually complete and no unresolved user decision, error, or verification remains; if you are unsure, or still need to investigate, edit, test, or ask the user, continue working instead of calling " + done + "\n" +
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
	fileWriteStep := "5. Save the plan document under .chord/plans/ as plan-<NNN>.md, using the next unused three-digit number (for example .chord/plans/plan-001.md), before handing it off or finishing the planning turn."
	if hasFileWrite {
		fileWriteStep += " Write the plan document with the visible file tools available in this role."
	} else {
		fileWriteStep += " If this role cannot write the plan file, explain the limitation and " + a.plannerPermissionAdjustmentInstruction() + "."
	}
	handoffStep := "6. "
	if hasHandoff {
		handoffStep += "If this role supports handoff to execution, do it only after the plan file exists. Hand off only when the request actually needs execution: never hand off a direct answer or a plan the user only asked to review."
	} else {
		handoffStep += "Handoff is unavailable in this role. Return the saved plan path or the plan content needed for the next step, and explain the limitation clearly."
	}
	return strings.TrimSpace(`

## Planning Mode

You are now in planning mode. Your goal is to analyse the user's request, explore
the codebase as needed, and produce a concrete execution plan — or to answer
directly when the request does not call for one.

### Decide what to deliver first
Answer directly and stop (no plan file, no Handoff) when the user asks for any
of the following:
- An explanation, recommendation, comparison, or diagnosis
- A read-only review or analysis — including verification of a report or existing
  plan — whose deliverable is the conclusion itself
- A change small enough to describe without a task breakdown

Create a plan document only when the request needs concrete implementation work
for an execution role, or when the user explicitly asks for a plan. If the user
asked only for the plan, save the document and return a summary without calling
Handoff.

### Workflow
1. If the user has not yet described what they want to accomplish (their message
   is "I'd like to create a plan. Please ask me what I want to accomplish."),
   greet them and ask what they'd like to plan. Wait for their response before
   proceeding.
2. Explore the codebase using the tools and permissions available in this role.
3. Analyse the requirements and decompose them into concrete, independently-
   executable tasks.
4. For direct-answer requests, stop after the answer — do not create a plan file
   and do not call Handoff.
` + fileWriteStep + `
` + handoffStep + `

### When the user rejects Handoff
The rejection is appended to the conversation as a user message ("Handoff
rejected: <reason>") and becomes the latest request driving this turn:
- Revise the existing plan file referenced by that message; do not create a new
  plan-<NNN>.md number for the same plan.
- Call Handoff again only after addressing the rejection reason.

### Plan Document Format
Write a Markdown document with this structure:

    # <Goal description>

    ## Constraints
    - <constraint 1>
    - <constraint 2>

    ## Tasks

    ### 1. <Task title>
    <Task description>

    ### 2. <Task title> (depends: 1)
    <Task description with dependency>

Rules:
- Each task is a ### heading with a numeric ID followed by a dot
- Dependencies are declared in parentheses: (depends: 1, 2)
- Task IDs are immutable — never renumber existing tasks
- New tasks always take max(existing IDs) + 1
- Make tasks granular enough for independent execution
- Do NOT include status markers — they are added during execution

### Plan quality requirements
- A request you can answer directly does not need a plan document — answer it and stop
- A plan with only 1 step is not a plan: describe the single change directly instead of writing a document, unless the user explicitly asked for a plan
- Each step must name the specific file(s) to modify
- Avoid vague verbs: "handle", "improve", "update" — use "add", "remove", "rename", "extract"
- Include a verification step: how will you know each step succeeded?
`)
}

func (a *MainAgent) mainAgentRolePromptBlock() string {
	activeCfg := a.currentActiveConfig()
	if activeCfg != nil && strings.TrimSpace(activeCfg.SystemPrompt) != "" {
		return activeCfg.SystemPrompt
	}
	if a.shouldUsePlannerPrompt(activeCfg) {
		return a.plannerModePromptBlock()
	}
	return ""
}

func (a *MainAgent) mainAgentCapabilityPromptBlock() string {
	visibleTools := a.mainVisibleLLMTools()
	visible := toolNamesFromVisibleTools(visibleTools)
	return buildDynamicCapabilityPromptBlock(visible, a.effectiveRuleset(), capabilityPromptAudienceMain)
}

func (a *MainAgent) shouldUsePlannerPrompt(activeCfg *config.AgentConfig) bool {
	if activeCfg == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(activeCfg.Name), "planner")
}

func (a *MainAgent) promptMetaSnapshot() (workDir, gitStatus, agentsMD, venvPath string) {
	a.promptMetaMu.RLock()
	defer a.promptMetaMu.RUnlock()
	return a.cachedWorkDir, a.cachedGitStatus, a.cachedAgentsMD, a.cachedVenvPath
}

func (a *MainAgent) cachedAgentsMDSnapshot() string {
	a.promptMetaMu.RLock()
	defer a.promptMetaMu.RUnlock()
	return a.cachedAgentsMD
}

func (a *MainAgent) setCachedGitStatus(status string) {
	a.promptMetaMu.Lock()
	a.cachedGitStatus = status
	a.promptMetaMu.Unlock()
}

func loadAgentsMDWithWorkDir(projectRoot, workDir string) string {
	if projectRoot == "" {
		return ""
	}

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return ""
	}
	displayBase := absRoot

	// Collect candidate directories: projectRoot plus all subdirs on the path
	// to workDir (if workDir is under projectRoot).
	dirs := []string{absRoot}
	if workDir != "" {
		absWork, werr := filepath.Abs(workDir)
		if werr == nil {
			rel, ok := relativePathWithin(absRoot, absWork)
			if ok && rel != "." {
				displayBase = absWork
				// Walk from absRoot down to absWork, collecting intermediate dirs.
				parts := strings.Split(rel, string(filepath.Separator))
				for i := 1; i <= len(parts); i++ {
					dirs = append(dirs, filepath.Join(absRoot, filepath.Join(parts[:i]...)))
				}
			}
		}
	}

	var sections []string
	for _, dir := range dirs {
		path := filepath.Join(dir, "AGENTS.md")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			if !os.IsNotExist(rerr) {
				log.Warnf("failed to read AGENTS.md path=%v error=%v", path, rerr)
			}
			continue
		}
		if c := strings.TrimSpace(string(data)); c != "" {
			log.Debugf("loaded AGENTS.md path=%v size=%v", path, len(c))
			rel := displayPathFromWorkDir(displayBase, path)
			if rel == "" {
				rel = "AGENTS.md"
			}
			sections = append(sections, fmt.Sprintf("## %s\n\n%s", rel, c))
		}
	}
	return strings.Join(sections, "\n\n")
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
	ruleset := a.effectiveRuleset()
	entries := make([]tools.SkillListingEntry, 0, len(loadedSkills))
	for _, s := range loadedSkills {
		if s == nil {
			continue
		}
		if len(ruleset) > 0 && ruleset.Evaluate(tools.NameSkill, s.Name) == permission.ActionDeny {
			log.Debugf("skill denied by permission, skipping from visible list skill=%v", s.Name)
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
// matching git's "is-inside-work-tree" semantics. No git binary is invoked.
func getGitStatus(workDir string) string {
	gitRoot, headPath := findGitHead(workDir)
	if gitRoot == "" {
		return "Is directory a git repo: no"
	}
	_ = gitRoot // used only to establish we're in a repo; HEAD path is what we need
	branch := readGitHeadBranch(headPath)
	var sb strings.Builder
	sb.WriteString("Is directory a git repo: yes")
	if branch != "" {
		fmt.Fprintf(&sb, "\n  Git branch: %s", branch)
	}
	return sb.String()
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
func detectVenvPath(workDir, projectRoot string) string {
	if workDir == "" {
		return ""
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	absRoot := ""
	if projectRoot != "" {
		if root, rerr := filepath.Abs(projectRoot); rerr == nil {
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
