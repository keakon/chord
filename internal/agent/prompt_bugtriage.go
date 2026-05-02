package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

var bugTriageAnalysisKeywords = []string{
	"analyze", "analysis", "investigate", "debug", "triage", "review",
	"why", "root cause", "conclusion", "correct",
	"分析", "排查", "定位", "审查", "调查", "根因", "为什么", "结论", "是否正确",
}

var bugTriageIssueKeywords = []string{
	"bug", "regression", "root cause", "failure", "error", "broken",
	"wrong", "stale", "incorrect", "mismatch", "not work", "doesn't work", "cannot",
	"bug结论", "回归", "根因", "失败", "错误", "异常", "报错", "失效", "不工作", "不生效", "无法", "不能", "不对",
}

var bugTriageExactPhrases = []string{
	"为什么会这样",
	"为什么会出现",
	"为什么会发生",
	"哪个结论更对",
	"哪个结论更正确",
	"哪个更对",
	"哪个更正确",
	"结论是否正确",
	"审查结论",
	"调查结果是否正确",
	"分析这个调查结果是否正确",
	"你认为哪个分析出来的bug结论更正确",
}

func containsAnyFold(text string, keys []string) bool {
	for _, key := range keys {
		if key != "" && strings.Contains(text, key) {
			return true
		}
	}
	return false
}

func latestUserPromptForBugTriage(messages []message.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" || msg.IsCompactionSummary {
			continue
		}
		if text := strings.TrimSpace(message.UserPromptPlainText(msg)); text != "" {
			return text
		}
	}
	return ""
}

func shouldEnableBugTriagePrompt(messages []message.Message) bool {
	text := strings.ToLower(strings.TrimSpace(latestUserPromptForBugTriage(messages)))
	if text == "" {
		return false
	}
	if containsAnyFold(text, bugTriageExactPhrases) {
		return true
	}
	return containsAnyFold(text, bugTriageAnalysisKeywords) && containsAnyFold(text, bugTriageIssueKeywords)
}

func (a *MainAgent) setBugTriagePromptActive(active bool) {
	a.bugTriagePromptActive.Store(active)
	// No system-prompt refresh: the bug triage hint is delivered as a per-turn
	// overlay via buildTurnOverlayMessages.
}

func (a *MainAgent) syncBugTriagePromptFromSnapshot() {
	if a == nil || a.ctxMgr == nil {
		return
	}
	a.setBugTriagePromptActive(shouldEnableBugTriagePrompt(a.ctxMgr.Snapshot()))
}

func (a *MainAgent) bugTriagePromptBlock() string {
	if a == nil || !a.bugTriagePromptActive.Load() {
		return ""
	}
	if cfg := a.currentActiveConfig(); cfg != nil {
		name := strings.TrimSpace(cfg.Name)
		if strings.EqualFold(name, "planner") {
			return ""
		}
	}
	return "## Bug Triage Workflow\n" +
		"- For non-trivial bug analysis, start with a short 3-5 step investigation outline before the first substantial tool call.\n" +
		"- That outline is a one-time high-level plan, not a reason to narrate every routine command or obvious next step.\n" +
		"- First identify the direct trigger that explains the symptom.\n" +
		"- Only expand into contributing factors or broader design issues after the direct trigger is explained, or when the user explicitly asks.\n" +
		"- Separate confirmed facts from high-confidence inference and anything not yet verified.\n" +
		"- In the final answer, distinguish direct trigger, contributing factors, broader design issue (if any), and verification status.\n"
}
