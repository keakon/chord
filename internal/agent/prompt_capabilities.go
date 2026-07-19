package agent

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func toolPromptName(name string) string { return "`" + name + "`" }

type capabilityPromptAudience int

const (
	capabilityPromptAudienceMain capabilityPromptAudience = iota
	capabilityPromptAudienceSub
)

func buildDynamicCapabilityPromptBlock(visible map[string]struct{}, ruleset permission.Ruleset, audience capabilityPromptAudience) string {
	blocks := make([]string, 0, 4)
	if block := toolSelectionPromptBlock(visible); block != "" {
		blocks = append(blocks, block)
	}
	if block := fileInspectionConstraintsPromptBlock(visible, ruleset, audience); block != "" {
		blocks = append(blocks, block)
	}
	if block := fileModificationConstraintsPromptBlock(visible, ruleset, audience); block != "" {
		blocks = append(blocks, block)
	}
	if block := riskAndReportingPromptBlock(visible, audience); block != "" {
		blocks = append(blocks, block)
	}
	return strings.Join(blocks, "\n\n")
}

func toolSelectionPromptBlock(visible map[string]struct{}) string {
	if len(visible) == 0 {
		return ""
	}

	discoveryTools := visiblePathDiscoveryTools(visible)
	lines := make([]string, 0, 12)
	lines = append(lines, "- Prefer the smallest safe number of tool calls. If one tool call can complete the task clearly and safely, do not split it into multiple steps.")
	if hasVisibleTool(visible, tools.NameRead) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameRead)+" for file contents when the target path is already known or has been verified.")
		lines = append(lines, "- When the user provides complete file contents in a "+"`<file path=...>`"+" reference, treat that content as the working context; do not re-read the same file merely to obtain duplicate contents. Re-read only when the supplied content is incomplete, the file may have changed on disk, or the edit workflow requires fresh file state, and then read only the needed range.")
	}
	// Check for either edit tool (patch or edit/replace)
	editToolName := visibleEditToolName(visible)
	if editToolName != "" {
		lines = append(lines, "- Use "+toolPromptName(editToolName)+" to modify the contents of one existing file with a verified path.")
		switch editToolName {
		case tools.NamePatch:
			lines = append(lines, "- For "+toolPromptName(editToolName)+", keep hunks small and include unique unchanged context; in repeated blocks such as tests or fixtures, include the enclosing function, test, or case name.")
			lines = append(lines, "- If patching a file modified earlier in the turn and the target area is not freshly visible, re-read the small target range before patching.")
		case tools.NameEdit:
			lines = append(lines, "- For "+toolPromptName(editToolName)+", use exact old_string/new_string replacements. Match the file's raw text exactly, including whitespace and newlines; prefer the smallest unique block and set replace_all only when every occurrence should change.")
			lines = append(lines, "- If editing a file modified earlier in the turn and the target area is not freshly visible, re-read the small target range before editing.")
		}
	}
	if hasVisibleTool(visible, tools.NameWrite) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameWrite)+" for whole-file writes.")
	}
	if editToolName != "" && hasVisibleTool(visible, tools.NameWrite) {
		lines = append(lines, "- Do not use "+toolPromptName(tools.NameWrite)+" for local edits to existing files; use "+toolPromptName(editToolName)+" instead.")
	}
	if hasVisibleTool(visible, tools.NameDelete) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameDelete)+" to remove files with verified paths.")
	}
	if hasVisibleTool(visible, tools.NameWrite) && hasVisibleTool(visible, tools.NameDelete) {
		lines = append(lines, "- Choose file tools by final state: use "+toolPromptName(tools.NameWrite)+" directly when a path should still exist afterward with new full contents, and use "+toolPromptName(tools.NameDelete)+" only when the path should no longer exist.")
		lines = append(lines, "- Do not "+toolPromptName(tools.NameDelete)+" a path just to recreate it with "+toolPromptName(tools.NameWrite)+"; that adds unnecessary risk and tool churn.")
	}

	if len(discoveryTools) > 0 {
		lines = append(lines, "- Use "+strings.Join(discoveryTools, " / ")+" for discovery and navigation.")
		if hasVisibleTool(visible, tools.NameGrep) && hasVisibleTool(visible, tools.NameRead) {
			lines = append(lines, "- When "+toolPromptName(tools.NameGrep)+" returns path:line:snippet hits, use those line numbers to read narrow ranges around relevant matches instead of scanning broad file chunks.")
		}
		if pathTools := visibleExistingPathTools(visible); len(pathTools) > 0 {
			lines = append(lines, "- If you are unsure of the exact target path for "+strings.Join(pathTools, " / ")+
				", use "+strings.Join(discoveryTools, " / ")+" to find or verify it before calling the path tool; do not guess plausible-looking paths.")
		}
	}
	if hasVisibleTool(visible, tools.NameSkill) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameSkill)+" to load additional skill instructions on demand when one of the available skills clearly matches the task.")
	}
	if hasVisibleTool(visible, tools.NameShell) {
		lines = append(lines,
			"- Use "+toolPromptName(tools.NameShell)+" mainly for tests, builds, git, and system commands.",
			"- For native filesystem operations with no dedicated built-in tool, "+toolPromptName(tools.NameShell)+" is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive.",
		)
	}
	if len(discoveryTools) > 0 || hasVisibleTool(visible, tools.NameRead) {
		lines = append(lines, "- Minimize LLM round trips. When two or more read-only tool calls are independent, issue them in the same response so they can run in parallel, especially multiple known file/range reads after search results; use serial calls only when a later call depends on an earlier result, the call mutates state, or a command is intentionally high-cost.")
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Tool Selection\n" + strings.Join(lines, "\n")
}

func visiblePathDiscoveryTools(visible map[string]struct{}) []string {
	discoveryTools := make([]string, 0, 3)
	if hasVisibleTool(visible, tools.NameGlob) {
		discoveryTools = append(discoveryTools, toolPromptName(tools.NameGlob))
	}
	if hasVisibleTool(visible, tools.NameGrep) {
		discoveryTools = append(discoveryTools, toolPromptName(tools.NameGrep))
	}
	if hasVisibleTool(visible, tools.NameLsp) {
		discoveryTools = append(discoveryTools, toolPromptName(tools.NameLsp))
	}
	return discoveryTools
}

func visibleExistingPathTools(visible map[string]struct{}) []string {
	pathTools := make([]string, 0, 3)
	if hasVisibleTool(visible, tools.NameRead) {
		pathTools = append(pathTools, toolPromptName(tools.NameRead))
	}
	// Include whichever edit tool is visible
	if editTool := visibleEditToolName(visible); editTool != "" {
		pathTools = append(pathTools, toolPromptName(editTool))
	}
	if hasVisibleTool(visible, tools.NameDelete) {
		pathTools = append(pathTools, toolPromptName(tools.NameDelete))
	}
	return pathTools
}

func fileInspectionConstraintsPromptBlock(visible map[string]struct{}, ruleset permission.Ruleset, audience capabilityPromptAudience) string {
	if len(visible) == 0 {
		return ""
	}

	hasRead := hasVisibleTool(visible, tools.NameRead)
	hasGrep := hasVisibleTool(visible, tools.NameGrep)
	hasGlob := hasVisibleTool(visible, tools.NameGlob)
	hasLsp := hasVisibleTool(visible, tools.NameLsp)
	if hasRead && hasGrep && hasGlob && hasLsp && !hasScopedInspectionPermissions(ruleset) {
		return ""
	}

	var lines []string
	if !hasRead && !hasGrep && !hasGlob && !hasLsp {
		lines = []string{
			"- This role has no direct file inspection or code-navigation tools available in the prompt.",
			"- Do not use " + toolPromptName(tools.NameShell) + ", shell commands, or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.",
			inspectionLimitationEscalation(visible, audience, false),
		}
	} else {
		lines = []string{
			"- File inspection and code-navigation capabilities may be limited in this role. Only use the visible read/search/navigation tools and stay within the allowed permission scope.",
			"- If a needed inspection or navigation action is unavailable, treat that as a real boundary instead of retrying with equivalent shell commands.",
			"- Do not use " + toolPromptName(tools.NameShell) + ", shell commands, or inline scripts to simulate hidden or denied file reading, search, or code navigation capabilities.",
			inspectionLimitationEscalation(visible, audience, true),
		}
	}
	return "## File Inspection Constraints\n" + strings.Join(lines, "\n")
}

func fileModificationConstraintsPromptBlock(visible map[string]struct{}, ruleset permission.Ruleset, audience capabilityPromptAudience) string {
	if len(visible) == 0 {
		return ""
	}

	hasEdit := hasVisibleTool(visible, tools.NameEdit) || hasVisibleTool(visible, tools.NamePatch)
	hasWrite := hasVisibleTool(visible, tools.NameWrite)
	hasDelete := hasVisibleTool(visible, tools.NameDelete)
	if hasEdit && hasWrite && hasDelete && !hasScopedFilePermissions(visible, ruleset) {
		return ""
	}

	var lines []string
	if !hasEdit && !hasWrite && !hasDelete {
		lines = []string{
			"- This role is currently read-only for files: do not edit, write, or delete files.",
			"- Do not use " + toolPromptName(tools.NameShell) + ", shell redirection, or inline scripts to simulate file edits, writes, or deletes.",
			modificationLimitationEscalation(visible, audience, false),
		}
	} else {
		lines = []string{
			"- Available file operations are limited in this role. Only use the visible file tools and stay within the allowed permission scope.",
			"- If a needed file action or target path is unavailable, treat that as a real boundary instead of retrying with equivalent tools.",
			"- Do not use " + toolPromptName(tools.NameShell) + ", shell redirection, or inline scripts to simulate hidden or denied file edits, writes, or deletes.",
			modificationLimitationEscalation(visible, audience, true),
		}
	}
	return "## File Modification Constraints\n" + strings.Join(lines, "\n")
}

func riskAndReportingPromptBlock(visible map[string]struct{}, audience capabilityPromptAudience) string {
	lines := []string{
		"- Be more conservative with irreversible, destructive, shared-state, or high-blast-radius actions.",
	}
	if audience == capabilityPromptAudienceSub {
		hasQuestion := hasVisibleTool(visible, tools.NameQuestion)
		hasEscalate := hasVisibleTool(visible, tools.NameEscalate)
		hasNotify := hasVisibleTool(visible, tools.NameNotify)
		switch {
		case hasQuestion && hasEscalate:
			lines = append(lines, "- Use permission approval for execution authorization. Use "+toolPromptName(tools.NameQuestion)+" only when the user must choose between materially different options; otherwise use "+toolPromptName(tools.NameEscalate)+" when owner-agent intervention or a decision is required.")
		case hasQuestion && hasNotify:
			lines = append(lines, "- Use permission approval for execution authorization. Use "+toolPromptName(tools.NameQuestion)+" only when the user must choose between materially different options; otherwise use "+toolPromptName(tools.NameNotify)+" to surface owner-agent intervention or decision points because "+toolPromptName(tools.NameEscalate)+" is unavailable in this role.")
		case hasQuestion:
			lines = append(lines, "- Use permission approval for execution authorization. Use "+toolPromptName(tools.NameQuestion)+" when the user must choose between materially different options, and clearly explain any remaining owner-agent dependency in assistant text because "+toolPromptName(tools.NameEscalate)+" is unavailable in this role.")
		case hasEscalate:
			lines = append(lines, "- Use permission approval for execution authorization, and use "+toolPromptName(tools.NameEscalate)+" when owner-agent intervention or a materially different decision is required.")
		case hasNotify:
			lines = append(lines, "- Use permission approval for execution authorization, and use "+toolPromptName(tools.NameNotify)+" to surface materially different decisions or owner-agent intervention because "+toolPromptName(tools.NameEscalate)+" is unavailable in this role.")
		default:
			lines = append(lines, "- Use permission approval for execution authorization. If a materially different decision or owner-agent intervention is required, explain the blocker clearly in assistant text because neither "+toolPromptName(tools.NameQuestion)+", "+toolPromptName(tools.NameNotify)+", nor "+toolPromptName(tools.NameEscalate)+" is available in this role.")
		}
	} else if hasVisibleTool(visible, tools.NameQuestion) {
		lines = append(lines, "- Use permission approval for execution authorization; see Structured User Confirmation for when to use "+toolPromptName(tools.NameQuestion)+" versus plain assistant text.")
	} else {
		lines = append(lines, "- Use permission approval for execution authorization, and ask the user for clarification when they need to choose between materially different options.")
	}
	lines = append(lines, "- Report verification status explicitly: passed, failed, not run, or only inspected statically.")
	return "## Risk & Reporting\n" + strings.Join(lines, "\n")
}

func hasScopedInspectionPermissions(ruleset permission.Ruleset) bool {
	if len(ruleset) == 0 {
		return false
	}

	for _, permName := range []string{tools.NameRead, tools.NameGrep, tools.NameGlob, tools.NameLsp} {
		if toolHasScopedRestriction(ruleset, permName) {
			return true
		}
	}
	return false
}

func hasScopedFilePermissions(visible map[string]struct{}, ruleset permission.Ruleset) bool {
	if len(ruleset) == 0 {
		return false
	}

	// Only check scoped permissions for tools that are actually visible
	visibleFileTools := []string{}
	if hasVisibleTool(visible, tools.NameWrite) {
		visibleFileTools = append(visibleFileTools, tools.NameWrite)
	}
	if hasVisibleTool(visible, tools.NameEdit) {
		visibleFileTools = append(visibleFileTools, tools.NameEdit)
	}
	if hasVisibleTool(visible, tools.NamePatch) {
		visibleFileTools = append(visibleFileTools, tools.NamePatch)
	}
	if hasVisibleTool(visible, tools.NameDelete) {
		visibleFileTools = append(visibleFileTools, tools.NameDelete)
	}

	for _, permName := range visibleFileTools {
		if toolHasScopedRestriction(ruleset, permName) {
			return true
		}
	}
	return false
}

func toolHasScopedRestriction(ruleset permission.Ruleset, toolName string) bool {
	globalAction, hasGlobal := lastToolWideRule(ruleset, toolName)
	if !hasGlobal {
		return false
	}
	if globalAction != permission.ActionDeny {
		return false
	}
	toolNames := []string{toolName}
	switch toolName {
	case tools.NameEdit:
		toolNames = append(toolNames, tools.NamePatch)
	case tools.NamePatch:
		toolNames = append(toolNames, tools.NameEdit)
	}
	for _, rule := range ruleset {
		if rule.Pattern == "*" {
			continue
		}
		for _, candidate := range toolNames {
			if rule.Permission == candidate && (rule.Action == permission.ActionAllow || rule.Action == permission.ActionAsk) {
				return true
			}
		}
	}
	return false
}

func lastToolWideRule(ruleset permission.Ruleset, toolName string) (permission.Action, bool) {
	for _, rule := range slices.Backward(ruleset) {

		if rule.Pattern != "*" {
			continue
		}
		if rule.Permission == toolName || rule.Permission == "*" {
			return rule.Action, true
		}
	}
	return permission.ActionDeny, false
}

func hasVisibleTool(visible map[string]struct{}, name string) bool {
	_, ok := visible[name]
	return ok
}

// visibleEditToolName returns the visible edit-family tool name (patch preferred
// over edit), or "" when neither is visible.
func visibleEditToolName(visible map[string]struct{}) string {
	if hasVisibleTool(visible, tools.NamePatch) {
		return tools.NamePatch
	}
	if hasVisibleTool(visible, tools.NameEdit) {
		return tools.NameEdit
	}
	return ""
}

func inspectionLimitationEscalation(visible map[string]struct{}, audience capabilityPromptAudience, limited bool) string {
	if audience == capabilityPromptAudienceSub {
		hasEscalate := hasVisibleTool(visible, tools.NameEscalate)
		hasNotify := hasVisibleTool(visible, tools.NameNotify)
		switch {
		case hasEscalate && limited:
			return "- Explain the limitation and use " + toolPromptName(tools.NameEscalate) + " when the owner agent needs to adjust permissions, scope, or approach for out-of-scope inspection or navigation."
		case hasEscalate:
			return "- If the task needs repository inspection beyond your scope, explain the limitation and use " + toolPromptName(tools.NameEscalate) + " so the owner agent can adjust permissions, scope, or approach."
		case hasNotify && limited:
			return "- Explain the limitation and use " + toolPromptName(tools.NameNotify) + " to surface out-of-scope inspection or navigation blockers because " + toolPromptName(tools.NameEscalate) + " is unavailable in this role."
		case hasNotify:
			return "- If the task needs repository inspection beyond your scope, explain the limitation and use " + toolPromptName(tools.NameNotify) + " so the owner agent knows a scope or permission adjustment is needed."
		default:
			return "- If the task needs repository inspection beyond your scope, explain the limitation clearly in assistant text because " + toolPromptName(tools.NameEscalate) + " and " + toolPromptName(tools.NameNotify) + " are unavailable in this role."
		}
	}
	if limited {
		return "- Explain the limitation and ask to adjust permissions, scope, or approach when the task needs out-of-scope inspection or navigation."
	}
	return "- If the task needs repository inspection, explain the limitation and ask to adjust permissions, scope, or approach."
}

func modificationLimitationEscalation(visible map[string]struct{}, audience capabilityPromptAudience, limited bool) string {
	if audience == capabilityPromptAudienceSub {
		hasEscalate := hasVisibleTool(visible, tools.NameEscalate)
		hasNotify := hasVisibleTool(visible, tools.NameNotify)
		switch {
		case hasEscalate && limited:
			return "- Explain the limitation and use " + toolPromptName(tools.NameEscalate) + " when the owner agent needs to adjust permissions, scope, or approach for out-of-scope changes."
		case hasEscalate:
			return "- If the task requires code changes beyond your scope, explain the limitation and use " + toolPromptName(tools.NameEscalate) + " so the owner agent can adjust permissions, scope, or approach."
		case hasNotify && limited:
			return "- Explain the limitation and use " + toolPromptName(tools.NameNotify) + " to surface out-of-scope change requests because " + toolPromptName(tools.NameEscalate) + " is unavailable in this role."
		case hasNotify:
			return "- If the task requires code changes beyond your scope, explain the limitation and use " + toolPromptName(tools.NameNotify) + " so the owner agent knows a scope or permission adjustment is needed."
		default:
			return "- If the task requires code changes beyond your scope, explain the limitation clearly in assistant text because " + toolPromptName(tools.NameEscalate) + " and " + toolPromptName(tools.NameNotify) + " are unavailable in this role."
		}
	}
	if limited {
		return "- Explain the limitation and ask to adjust permissions, scope, or approach when the task needs out-of-scope changes."
	}
	return "- If the task requires code changes, explain the limitation and ask to adjust permissions, scope, or approach."
}
