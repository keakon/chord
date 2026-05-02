package agent

import (
	"fmt"
	"github.com/keakon/golog/log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// buildSystemPrompt constructs the default system prompt that is injected at
// the start of every conversation. It includes dynamic environment info, git
// repository status, project-level instructions from AGENTS.md, and any loaded
// skill content.
func (a *MainAgent) buildSystemPrompt() string {
	workDir, _, agentsMD, venvPath := a.promptMetaSnapshot()
	if workDir == "" {
		workDir = "unknown"
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	date := time.Now().Format("Mon Jan 2 2006")

	venvLine := ""
	if venvPath != "" {
		venvLine = fmt.Sprintf("\n  Python virtual environment: %s\n  When running Python commands, prefer the interpreter from this virtual environment.", venvPath)
	}

	var parts []string
	parts = append(parts, mainAgentIdentityPrompt)
	parts = append(parts, sharedAgentValuesPrompt)
	parts = append(parts, fmt.Sprintf(`<env>
  Working directory: %s
  Platform: %s
  Today's date: %s%s
</env>`, workDir, platform, date, venvLine))
	parts = append(parts, sharedCodingGuidelinesPrompt)
	parts = append(parts, mainAgentCommunicationPrompt)
	parts = append(parts, mainAgentResponseClosurePrompt)
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
	if block := a.agentsMDReminderFramingPromptBlock(agentsMD); block != "" {
		parts = append(parts, block)
	}
	// AGENTS.md is injected as a <system-reminder> user message via
	// injectSessionContextReminder to keep the stable system prompt
	// small and cacheable.
	if block := a.availableSkillsPromptBlock(); block != "" {
		parts = append(parts, block)
	}

	a.mcpServersPromptMu.RLock()
	mcpBlock := a.mcpServersPrompt
	a.mcpServersPromptMu.RUnlock()
	if mcpBlock != "" {
		parts = append(parts, mcpBlock)
	}
	// pendingLoopContinuation, bug triage hint, and SubAgent mailbox are
	// per-turn overlays assembled by buildTurnOverlayMessages; they do not
	// belong in the stable system prompt.

	return strings.Join(parts, "\n\n")
}

func (a *MainAgent) agentsMDReminderFramingPromptBlock(agentsMD string) string {
	if strings.TrimSpace(agentsMD) == "" {
		return ""
	}
	return strings.TrimSpace(`## Workspace Instructions
- This workspace provides repository guidance in a <system-reminder> block before the user's first message.
- Treat repository guidance inside that <system-reminder> block as durable workspace context and follow it unless it conflicts with higher-priority system, developer, or user instructions.
- Do not ignore or override that repository guidance just because it appears in a user-context block; it is system-provided workspace context, not ordinary user content.`)
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
	if _, ok := visible["Question"]; !ok {
		return false
	}
	ruleset := a.effectiveRuleset()
	if len(ruleset) > 0 && ruleset.Evaluate("Question", "*") == permission.ActionDeny {
		return false
	}
	return true
}

func (a *MainAgent) userConfirmationPromptBlock() string {
	if a.questionToolAvailable() {
		return `## Structured User Confirmation
- Default to making ordinary implementation decisions yourself; use ` + "`Question`" + ` only when user input is truly required to choose between materially different outcomes, confirm meaningful risk, or supply missing information that blocks correct execution
- Use plain assistant text only for lightweight clarifications that do not materially change the execution path
- When asking the user to decide, keep the same high information standard as ordinary clarifications: include enough context for a non-implementer to answer, summarize the current situation, why a decision is needed, the main options, their tradeoffs/risks, and your recommended default when appropriate
- When a confirmation would change scope, permissions, risk, or implementation choice, prefer ` + "`Question`" + ` so the user gets a structured decision UI instead of an unstructured text question`
	}
	return `## Plain-Text User Confirmation
- Default to making ordinary implementation decisions yourself; ask the user only when input is truly required to choose between materially different outcomes, confirm meaningful risk, or supply missing information that blocks correct execution
- Because structured confirmation is unavailable in this tool/permission state, ask necessary user-decision questions in normal assistant text
- Keep the same high information standard: include enough context for a non-implementer to answer, summarize the current situation, why a decision is needed, the main options, their tradeoffs/risks, and your recommended default when appropriate
- When a clarification does not materially change the execution path, keep it brief and focused`
}

func (a *MainAgent) userDecisionActionPhrase() string {
	if a.questionToolAvailable() {
		return "call the `Question` tool instead of only asking in plain assistant text"
	}
	return "ask in plain assistant text with enough context for a non-implementer to answer, including the main options, their tradeoffs/risks, and your recommended default"
}

func (a *MainAgent) loopUserConfirmationInstructionLine() string {
	return "- If you need user permission, confirmation, or a real decision between materially different options, you must " + a.userDecisionActionPhrase() + "."
}

func (a *MainAgent) loopContinuationDecisionInstructionLine() string {
	return "- When you need user permission, confirmation, or a real decision between materially different options, " + a.userDecisionActionPhrase() + "."
}

func (a *MainAgent) loopCompletionDecisionRequirementLine() string {
	base := "- If user permission, confirmation, or a real decision is still needed, "
	if a.questionToolAvailable() {
		return "- Do not use <done>...</done> unless the task is actually complete and no user decision remains\n" +
			base + "call the `Question` tool instead of ending as completed"
	}
	return "- Do not use <done>...</done> unless the task is actually complete and no user decision remains\n" +
		base + "ask in plain assistant text with enough context for a non-implementer to answer instead of ending as completed"
}

func (a *MainAgent) plannerPermissionAdjustmentInstruction() string {
	if a.questionToolAvailable() {
		return "use `Question` to ask the user to adjust permissions, scope, or approach"
	}
	return "ask the user in plain assistant text to adjust permissions, scope, or approach"
}

func (a *MainAgent) plannerModePromptBlock() string {
	visible := a.mainLLMVisibleToolNames()
	hasWrite := false
	hasHandoff := false
	if len(visible) > 0 {
		_, hasWrite = visible["Write"]
		_, hasHandoff = visible["Handoff"]
	}
	step4 := "4. Save the plan document to a path like .chord/plans/plan-001.md before handing it off or finishing the planning turn."
	if hasWrite {
		step4 += " Write the plan document with the visible file tools available in this role."
	} else {
		step4 += " If this role cannot write the plan file, explain the limitation and " + a.plannerPermissionAdjustmentInstruction() + "."
	}
	step5 := "5. "
	if hasHandoff {
		step5 += "If this role supports handoff to execution, do it only after the plan file exists. Do not stop with only a text response when handoff is available."
	} else {
		step5 += "Handoff is unavailable in this role. Return the saved plan path or the plan content needed for the next step, and explain the limitation clearly."
	}
	return strings.TrimSpace(`

## Planning Mode

You are now in planning mode. Your goal is to analyse the user's request, explore
the codebase as needed, and produce a concrete execution plan.

### Workflow
1. If the user has not yet described what they want to accomplish (their message
   is "I'd like to create a plan. Please ask me what I want to accomplish."),
   greet them and ask what they'd like to plan. Wait for their response before
   proceeding.
2. Explore the codebase using the tools and permissions available in this role.
3. Analyse the requirements and decompose them into concrete, independently-
   executable tasks.
` + step4 + `
` + step5 + `

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

## Plan quality requirements
- A plan with only 1 step is not a plan — just do the task directly
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

	// Collect candidate directories: projectRoot plus all subdirs on the path
	// to workDir (if workDir is under projectRoot).
	dirs := []string{absRoot}
	if workDir != "" {
		absWork, werr := filepath.Abs(workDir)
		if werr == nil && strings.HasPrefix(absWork, absRoot+string(filepath.Separator)) {
			// Walk from absRoot down to absWork, collecting intermediate dirs.
			rel, rerr := filepath.Rel(absRoot, absWork)
			if rerr == nil {
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
			sections = append(sections, c)
		}
	}
	return strings.Join(sections, "\n\n")
}

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
		if len(ruleset) > 0 && ruleset.Evaluate("Skill", s.Name) == permission.ActionDeny {
			log.Debugf("skill denied by permission, skipping from visible list skill=%v", s.Name)
			continue
		}
		entries = append(entries, tools.SkillListingEntry{Name: s.Name, Desc: s.Description})
	}
	if len(entries) == 0 {
		return ""
	}

	const maxTotal = tools.SkillListingMaxTotal
	const maxEntries = tools.SkillListingMaxEntries
	intro := "## Available Skills\nThe `Skill` tool can load additional skill instructions on demand. When a task clearly matches one of these skills, call `Skill` before proceeding.\n\n"
	budget := maxTotal - len(intro)
	if budget < 0 {
		budget = 0
	}
	shown := 0
	var sb strings.Builder
	sb.WriteString(intro)
	for i, e := range entries {
		if shown >= maxEntries {
			break
		}
		desc := tools.TruncateSkillDesc(e.Desc)
		line := fmt.Sprintf("- **%s**: %s\n", e.Name, desc)
		if sb.Len()+len(line)-len(intro) > budget && shown > 0 {
			break
		}
		sb.WriteString(line)
		shown = i + 1
	}
	remaining := len(entries) - shown
	if remaining > 0 {
		fmt.Fprintf(&sb, "+%d more skills available\n", remaining)
	}
	return sb.String()
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

// detectVenvPath searches for a Python virtual environment directory under the
// given working directory. It checks for .venv, venv, and env directories (in
// that order) and returns the absolute path of the first one that exists and
// contains a pyvenv.cfg file. Returns "" if no virtual environment is found.
func detectVenvPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	for _, name := range []string{".venv", "venv", "env"} {
		candidate := filepath.Join(absDir, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		// Verify it is a real virtual environment by checking for pyvenv.cfg.
		cfgPath := filepath.Join(candidate, "pyvenv.cfg")
		if _, err := os.Stat(cfgPath); err != nil {
			continue
		}
		return candidate
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
