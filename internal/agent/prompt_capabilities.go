package agent

import (
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

	lines := make([]string, 0, 9)
	lines = append(lines, "- Prefer the smallest safe number of tool calls. If one tool call can complete the task clearly and safely, do not split it into multiple steps.")
	if hasVisibleTool(visible, tools.NameRead) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameRead)+" for file contents.")
	}
	if hasVisibleTool(visible, tools.NameEdit) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameEdit)+" to modify the contents of one existing file.")
		lines = append(lines, "- For "+toolPromptName(tools.NameEdit)+", keep hunks small and include unique unchanged context; in repeated blocks such as tests or fixtures, include the enclosing function, test, or case name.")
	}
	if hasVisibleTool(visible, tools.NameWrite) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameWrite)+" for whole-file writes.")
	}
	if hasVisibleTool(visible, tools.NameEdit) && hasVisibleTool(visible, tools.NameWrite) {
		lines = append(lines, "- Do not use "+toolPromptName(tools.NameWrite)+" for local edits to existing files; use "+toolPromptName(tools.NameEdit)+" instead.")
	}
	if hasVisibleTool(visible, tools.NameDelete) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameDelete)+" to remove files.")
	}
	if hasVisibleTool(visible, tools.NameWrite) && hasVisibleTool(visible, tools.NameDelete) {
		lines = append(lines, "- Choose file tools by final state: use "+toolPromptName(tools.NameWrite)+" directly when a path should still exist afterward with new full contents, and use "+toolPromptName(tools.NameDelete)+" only when the path should no longer exist.")
		lines = append(lines, "- Do not "+toolPromptName(tools.NameDelete)+" a path just to recreate it with "+toolPromptName(tools.NameWrite)+"; that adds unnecessary risk and tool churn.")
	}

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
	if len(discoveryTools) > 0 {
		lines = append(lines, "- Use "+strings.Join(discoveryTools, " / ")+" for discovery and navigation.")
	}
	if hasVisibleTool(visible, tools.NameSkill) {
		lines = append(lines, "- Use "+toolPromptName(tools.NameSkill)+" to load additional skill instructions on demand when one of the available skills clearly matches the task.")
	}
	if hasVisibleTool(visible, tools.NameShell) {
		lines = append(lines,
			"- Use "+toolPromptName(tools.NameShell)+" mainly for tests, builds, git, and system commands.",
			"- For native filesystem operations with no dedicated built-in tool, "+toolPromptName(tools.NameShell)+" is appropriate when one direct command is clearly simpler and more atomic, such as move/rename, copy, mkdir, or archive/unarchive.",
		)
		lines = append(lines, "- Do not use "+toolPromptName(tools.NameShell)+" to run edit.")
	}
	if len(discoveryTools) > 0 || hasVisibleTool(visible, tools.NameRead) {
		lines = append(lines, "- Run independent reads/searches in parallel; run dependent operations sequentially.")
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Tool Selection\n" + strings.Join(lines, "\n")
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

	hasEdit := hasVisibleTool(visible, tools.NameEdit)
	hasWrite := hasVisibleTool(visible, tools.NameWrite)
	hasDelete := hasVisibleTool(visible, tools.NameDelete)
	if hasEdit && hasWrite && hasDelete && !hasScopedFilePermissions(ruleset) {
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

func hasScopedFilePermissions(ruleset permission.Ruleset) bool {
	if len(ruleset) == 0 {
		return false
	}

	for _, permName := range []string{tools.NameWrite, tools.NameEdit, tools.NameDelete} {
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
	for _, rule := range ruleset {
		if rule.Permission != toolName || rule.Pattern == "*" {
			continue
		}
		if rule.Action == permission.ActionAllow || rule.Action == permission.ActionAsk {
			return true
		}
	}
	return false
}

func lastToolWideRule(ruleset permission.Ruleset, toolName string) (permission.Action, bool) {
	for i := len(ruleset) - 1; i >= 0; i-- {
		rule := ruleset[i]
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
