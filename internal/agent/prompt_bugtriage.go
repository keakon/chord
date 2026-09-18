package agent

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// "review" / "审查" are deliberately excluded: combined with broad issue words
// like "error" / "错误" they would route ordinary code-review requests (e.g.
// "review this error handling") into the bug-triage workflow.
// English keywords are matched on word boundaries (see containsAnyKeyword) and
// carry their common inflections explicitly, so a keyword cannot match inside
// an unrelated word and inflected requests still trigger. A base word and its
// -s / -ed / -ing / -ly forms are separate entries: a missing form silently
// stops routing, which is why TestShouldEnableBugTriagePrompt_KeywordForms
// pins one prompt per form.
var bugTriageAnalysisKeywords = []string{
	"analyze", "analyzes", "analyzing", "analyzed", "analysis", "analyses",
	"investigate", "investigates", "investigating", "investigated", "investigation",
	"debug", "debugs", "debugged", "debugging", "triage", "triaged",
	"why", "root cause", "root causes", "conclusion", "conclusions",
	"分析", "排查", "定位", "调查", "根因", "为什么", "结论", "是否正确",
}

var bugTriageIssueKeywords = []string{
	"bug", "bugs", "buggy", "regression", "regressions", "root cause", "root causes",
	"failure", "failures", "fail", "fails", "failed", "failing", "error", "errors", "broken",
	"wrong", "wrongly", "stale", "incorrect", "incorrectly", "mismatch", "mismatches", "mismatched",
	"not work", "not working", "stopped working", "doesn't work", "cannot",
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

// containsAnyKeyword reports whether text contains any key. Callers pass
// already-lowercased text.
func containsAnyKeyword(text string, keys []string) bool {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if isASCIIKey(key) {
			if containsASCIIWord(text, key) {
				return true
			}
			continue
		}
		if strings.Contains(text, key) {
			return true
		}
	}
	return false
}

// isASCIIKey reports whether key is pure ASCII and therefore has word
// boundaries worth checking. CJK keywords have no such boundaries and keep
// plain substring matching.
func isASCIIKey(key string) bool {
	for i := 0; i < len(key); i++ {
		if key[i] >= 0x80 {
			return false
		}
	}
	return true
}

// containsASCIIWord reports whether text contains key delimited by non-word
// bytes on both sides. Both arguments must already be lowercased. The boundary
// check keeps an incidental substring inside an ordinary word ("correct" in
// "correctly") from standing in for a keyword.
func containsASCIIWord(text, key string) bool {
	for offset := 0; offset+len(key) <= len(text); {
		i := strings.Index(text[offset:], key)
		if i < 0 {
			return false
		}
		i += offset
		end := i + len(key)
		if (i == 0 || !isASCIIAlnum(text[i-1])) && (end == len(text) || !isASCIIAlnum(text[end])) {
			return true
		}
		offset = i + 1
	}
	return false
}

func isASCIIAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
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
	if containsAnyKeyword(text, bugTriageExactPhrases) || bugTriageConclusionComparison(text) {
		return true
	}
	return containsAnyKeyword(text, bugTriageAnalysisKeywords) && containsAnyKeyword(text, bugTriageIssueKeywords)
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
	// A planning role already opens with its own investigation outline, so this
	// block would duplicate it. Keyed on the resolved preset rather than the
	// role name so a user-defined planning role suppresses it too.
	if a.shouldUsePlannerPrompt(a.currentActiveConfig()) {
		return ""
	}
	return "## Bug Triage Workflow\n" +
		"- For non-trivial bug analysis, start with a short 3-5 step investigation outline before the first substantial tool call.\n" +
		"- That outline is a one-time high-level plan, not a reason to narrate every routine command or obvious next step.\n" +
		"- First identify the direct trigger that explains the symptom.\n" +
		"- Only expand into contributing factors or broader design issues after the direct trigger is explained, or when the user explicitly asks.\n" +
		"- Separate confirmed facts from high-confidence inference and anything not yet verified.\n" +
		"- In the final answer, distinguish direct trigger, contributing factors, broader design issue (if any), and verification status.\n"
}
