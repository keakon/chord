package agent

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// "review" / "审查" are deliberately excluded: combined with broad issue words
// like "error" / "错误" they would route ordinary code-review requests (e.g.
// "review this error handling") into the bug-triage workflow.
var bugTriageAnalysisKeywords = []string{
	"analyze", "analysis", "investigate", "debug", "triage",
	"why", "root cause", "conclusion", "correct",
	"分析", "排查", "定位", "调查", "根因", "为什么", "结论", "是否正确",
}

var bugTriageIssueKeywords = []string{
	"bug", "regression", "root cause", "failure", "error", "broken",
	"wrong", "stale", "incorrect", "mismatch", "not work", "doesn't work", "cannot",
	"bug结论", "回归", "根因", "失败", "错误", "异常", "报错", "失效", "不工作", "不生效", "无法", "不能", "不对",
}

// bugTriageExactPhrases are standalone triggers for analysis questions that
// lack an explicit issue keyword. Every phrase must keep a failure or
// conclusion-review qualifier: bare substrings like "是否正确" or "为什么会"
// also match ordinary review and design questions ("检查这个配置是否正确",
// "为什么会选择这个 API"), which must not enter the bug-triage workflow.
// Longer observed sentences are covered as superstrings (e.g.
// "分析这个调查结果是否正确" contains "调查结果是否正确").
var bugTriageExactPhrases = []string{
	"为什么会这样",
	"为什么会出现",
	"为什么会发生",
	"结论是否正确",
	"调查结果是否正确",
	"审查结论",
}

// bugTriageConclusionComparison matches "which conclusion is more correct"
// style questions (哪个结论更对 / 你认为哪个分析出来的bug结论更正确 / …)
// without enumerating each full sentence.
func bugTriageConclusionComparison(text string) bool {
	return strings.Contains(text, "哪个") &&
		(strings.Contains(text, "更对") || strings.Contains(text, "更正确"))
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
	for _, msg := range slices.Backward(messages) {

		if !message.IsUserAuthored(msg) {
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
	if containsAnyFold(text, bugTriageExactPhrases) || bugTriageConclusionComparison(text) {
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
