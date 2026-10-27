package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestLatestRecoverableUserIntentSkipsCompactionSummary(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	a.ctxMgr.Append(message.Message{Role: "user", Content: "original request"})
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "working"})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "[Context Summary]\nsummary", IsCompactionSummary: true})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "final real user intent"})
	if got := a.latestRecoverableUserIntent(); got != "final real user intent" {
		t.Fatalf("latestRecoverableUserIntent() = %q, want final real user intent", got)
	}
}

func TestCollectEvidenceItemsPreservesLatestDoneRejectedReason(t *testing.T) {
	msgs := []message.Message{
		{Role: "tool", Content: "Done rejected: older request"},
		{Role: "assistant", Content: "working"},
		{Role: "tool", Content: "Done rejected: 把这个方案生成一个plan文档"},
	}
	items := selectEvidenceItems(msgs, 4096)
	if len(items) == 0 {
		t.Fatal("expected done rejection evidence")
	}
	if items[0].Kind != evidenceDoneRejected {
		t.Fatalf("first evidence kind = %q, want %q", items[0].Kind, evidenceDoneRejected)
	}
	if !strings.Contains(items[0].Excerpt, "plan文档") {
		t.Fatalf("latest done rejection not preserved first: %+v", items)
	}
}

func TestCollectEvidenceItemsLatestDoneRejectionOutranksOlderCorrection(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "no, please do not touch the public API"},
		{Role: "assistant", Content: "ok, sticking to CLI"},
		{Role: "tool", Content: "Done rejected: 改成只压缩相邻消息，先把锁拆开"},
	}
	items := selectEvidenceItems(msgs, 4096)
	if len(items) == 0 {
		t.Fatal("expected ranked evidence items")
	}
	if items[0].Kind != evidenceDoneRejected {
		t.Fatalf("first item = %q, want %q (latest Done rejection should outrank older correction)", items[0].Kind, evidenceDoneRejected)
	}
	if !strings.Contains(items[0].Excerpt, "改成只压缩相邻消息") {
		t.Fatalf("latest done rejection text not preserved: %+v", items[0])
	}
}

func TestCollectEvidenceItemsPreservesLatestOrdinaryUserRequest(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "继续调查 rate limit"},
		{Role: "assistant", Content: "working"},
		{Role: "user", Content: "生成压缩目标保持方案 plan"},
		{Role: "user", Content: "[Context Summary]\nold summary", IsCompactionSummary: true},
	}
	items := selectEvidenceItems(msgs, 4096)
	found := false
	for _, item := range items {
		if item.Kind == evidenceUserRequest {
			found = true
			if !strings.Contains(item.Excerpt, "plan") || strings.Contains(item.Excerpt, "rate limit") {
				t.Fatalf("latest user request evidence mismatch: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("selected evidence missing latest user request: %+v", items)
	}
}

func TestBuildGoalAnchorUsesLatestOrdinaryUserRequest(t *testing.T) {
	got := buildGoalAnchor([]message.Message{
		{Role: "user", Content: "继续调查 rate limit"},
		{Role: "assistant", Content: "working"},
		{Role: "user", Content: "[SubAgent agent-2 requests intervention]\n\nReason: merge conflict"},
		{Role: "user", Content: "生成压缩目标保持方案 plan"},
		{Role: "user", Content: "<system-reminder>\nSubAgent mailbox update\n</system-reminder>", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1", Kind: "completed"}},
		{Role: "user", Content: "[Context Summary]\nold summary", IsCompactionSummary: true},
	})
	if !strings.Contains(got, "plan") || strings.Contains(got, "rate limit") {
		t.Fatalf("buildGoalAnchor() = %q, want latest ordinary user request", got)
	}
}

func TestLatestRecoverableUserIntentSkipsSyntheticUserMessages(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "real user request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "loop notice", Kind: message.KindLoopNotice})
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "mailbox", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1"}})
	if got := a.latestRecoverableUserIntent(); got != "real user request" {
		t.Fatalf("latestRecoverableUserIntent() = %q, want real user request", got)
	}
}

func TestCompactionPromptAnchorsLatestRequestAgainstStaleTodo(t *testing.T) {
	input := &compactionInput{
		Transcript:     "transcript",
		GoalAnchor:     buildGoalAnchor([]message.Message{{Role: "user", Content: "继续调查 rate limit"}, {Role: "user", Content: "生成压缩目标保持方案 plan"}}),
		EvidenceItems:  []evidenceItem{{Kind: evidenceDoneRejected, Title: "Latest Done rejection", WhyNeeded: "The rejection reason is recent user feedback/request and may supersede older todos.", Excerpt: "把这个方案生成一个plan文档"}},
		DecisionAnchor: "- (none explicitly extracted; infer from progress and evidence)",
		ProgressAnchor: "- (none extracted)",
	}
	prompt := buildCompactionPromptWithKeyFiles(
		input,
		"history-3.md",
		nil,
		[]tools.TodoItem{{ID: "old", Content: "继续调查 rate limit", Status: "pending"}},
		nil,
		nil,
	)
	for _, want := range []string{
		"Latest user request anchor:\n- 生成压缩目标保持方案 plan",
		"Latest Done rejection",
		"把这个方案生成一个plan文档",
		"These todos are not automatically authoritative after compaction",
		checkpointfmt.TodoSubgroupStaleLine,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestStructuredFallbackSummaryDemotesTodosWhenDoneRejectionChangesTarget(t *testing.T) {
	input := &compactionInput{
		EvidenceItems: []evidenceItem{{Kind: evidenceDoneRejected, Excerpt: "analyze all sessions and collect durable commands"}},
	}
	summary := buildStructuredFallbackSummary(
		"history-2.md",
		input,
		fmt.Errorf("summary quality fallback"),
		nil,
		[]tools.TodoItem{{ID: "old", Content: "update docs and commit the change", Status: "in_progress"}},
		nil,
	)
	for _, want := range []string{
		"## Current User Request\n- Latest Done rejected reason: analyze all sessions and collect durable commands",
		"- Active/relevant to latest request:\n  - (not classified by fallback; the anchor is stated under Current User Request above)",
		"- Stale/superseded:\n  - [in_progress] old: update docs and commit the change",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestSelectEvidenceItemsKeepsDoneRejectedUnderTightBudget(t *testing.T) {
	msgs := []message.Message{
		{Role: "tool", Content: "Error: " + strings.Repeat("long failing output ", 300)},
		{Role: "tool", Content: "Done rejected: 如果连接不上，为什么会推送用量呢？"},
	}
	items := selectEvidenceItems(msgs, 1)
	found := false
	for _, item := range items {
		if item.Kind == evidenceDoneRejected && strings.Contains(item.Excerpt, "推送用量") {
			found = true
		}
	}
	if !found {
		t.Fatalf("selected evidence missing done rejection: %+v", items)
	}
}

func TestCollectEvidenceItemsCapturesToolRejectionReasonAsRequest(t *testing.T) {
	msgs := []message.Message{{Role: "tool", Content: `Error: tool "Write" rejected by user: 改成只分析，不要写文件`}}
	items := selectEvidenceItems(msgs, 4096)
	for _, item := range items {
		if item.Kind == evidenceUserRequest && strings.Contains(item.Excerpt, "只分析") {
			return
		}
	}
	t.Fatalf("items = %+v, want user request from rejection reason", items)
}

func TestRecordEvidenceFromMessageCapturesToolRejectionReasonAsRequest(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.recordEvidenceFromMessage(message.Message{Role: "tool", Content: `Error: tool "Write" rejected by user: 改成只分析，不要写文件`})
	if got := a.evidence.len(); got != 2 {
		t.Fatalf("len(evidenceCandidates) = %d, want request plus error evidence", got)
	}
	if a.evidence.snapshot()[0].Kind != evidenceUserRequest || !strings.Contains(a.evidence.snapshot()[0].Excerpt, "只分析") {
		t.Fatalf("first candidate = %+v, want user request from rejection reason", a.evidence.snapshot()[0])
	}
}

func TestRecordEvidenceFromMessageCapturesDoneRejected(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.recordEvidenceFromMessage(message.Message{Role: "tool", Content: "Done rejected: 分析应该怎么保留正确的工作目标"})
	if got := a.evidence.len(); got != 1 {
		t.Fatalf("len(evidenceCandidates) = %d, want 1", got)
	}
	if a.evidence.snapshot()[0].Kind != evidenceDoneRejected {
		t.Fatalf("candidate kind = %q, want %q", a.evidence.snapshot()[0].Kind, evidenceDoneRejected)
	}
}

func TestLooksLikeUserCorrectionPrecision(t *testing.T) {
	corrections := []string{
		"不要再改这个文件了",
		"别改 config.yaml",
		"只能用标准库",
		"必须保留原有接口",
		"don't touch the parser",
		"use a map instead",
	}
	for _, text := range corrections {
		if !looksLikeUserCorrection(text) {
			t.Errorf("looksLikeUserCorrection(%q) = false, want true", text)
		}
	}

	// Ordinary requests that merely contain 别 / 只 inside common words must
	// not be promoted to priority-100 constraints.
	ordinary := []string{
		"分析一下这两个实现的区别",
		"这个功能特别慢，看看为什么",
		"只是想确认一下当前的行为",
		"只有一个测试失败了，帮我看看",
		"按级别把日志分类",
		"only 3 tests are failing now",
	}
	for _, text := range ordinary {
		if looksLikeUserCorrection(text) {
			t.Errorf("looksLikeUserCorrection(%q) = true, want false", text)
		}
		if !isPlainUserRequestForCompaction(text) {
			t.Errorf("isPlainUserRequestForCompaction(%q) = false, want true", text)
		}
	}
}
