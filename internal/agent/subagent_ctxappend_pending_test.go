package agent

// Context-append delivery ordering tests around SubAgent tool-batch execution.
//
// A context-only append (background completion, child progress, or TUI !shell
// output) is drained by the SubAgent run loop every iteration regardless of
// whether a tool batch is still pending, and appendContextOnly writes it
// straight into ctxmgr. When such an append lands while a batch is running it
// is therefore placed between the assistant tool_calls message and the tool
// results that close the batch — a sequence that strict chat APIs reject
// (OpenAI chat requires every tool result to follow its function_calls
// message; Anthropic keys block ordering off the same pairing).
//
// The pending-batch scenario below is driven through the real run loop with a
// release-gated tool so the window is deterministic: the tool batch is provably
// outstanding when the append is enqueued, and the tool result only lands after
// the test opens the gate. The fix keeps queued context appends undrained while
// results are outstanding (openToolBatchDefersContextAppends) and drains them
// at the batch-closure boundary, so the assertions lock in that the appends
// (and their mailbox acks) surface only after every tool result is backfilled,
// in enqueue order.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

// releaseGatedTool is a read-only test tool whose Execute blocks until its
// release channel is closed (or its context is done). It keeps a tool batch
// genuinely in flight so the test can deliver a context append inside the
// pending window.
type releaseGatedTool struct {
	name    string
	release chan struct{}
}

func (t releaseGatedTool) Name() string        { return t.name }
func (t releaseGatedTool) Description() string { return "test tool that blocks until released" }
func (t releaseGatedTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t releaseGatedTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	select {
	case <-t.release:
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (t releaseGatedTool) IsReadOnly() bool { return true }

// TestSubAgentCtxAppendDuringPendingToolBatchDeferredToBatchClosure reproduces
// the fixed behavior for the defect window: context appends arriving while a
// regular tool batch is still executing (PendingToolCalls > 0, tool result not
// yet backfilled) must stay queued and must not be interleaved between the
// assistant tool_calls message and the tool results that close the batch.
// They drain at the batch-closure boundary as trailing user messages, in
// enqueue (FIFO) order, and their mailbox acks only fire once the append is
// actually applied and persisted — never while the append is still deferred.
func TestSubAgentCtxAppendDuringPendingToolBatchDeferredToBatchClosure(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	// The mailbox-ack log directory must exist before any consumption ack is
	// recorded, otherwise markSubAgentMailboxConsumed fails silently.
	if err := os.MkdirAll(filepath.Join(parent.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("mkdir subagents dir: %v", err)
	}
	sub := newControllableTestSubAgent(t, parent, "adhoc-ctxappend-pending")
	// The controllable harness leaves turn nil until real input flows; give the
	// sub one explicit turn so the scripted LLM response below is accepted.
	// Mirror newTurn's ctx chaining: the turn context derives from the sub's
	// cancellable ctx so cleanup cancel aborts the batch-closure continuation
	// request. The run loop joins that request goroutine (llmWG.Wait) before
	// closing done, and with an uncancellable background turn ctx the client's
	// empty-response retry backoff could not be aborted, hanging shutdown.
	turnCtx, turnCancel := context.WithCancel(sub.parentCtx)
	sub.turn = &Turn{ID: 1, Epoch: 1, Ctx: turnCtx, Cancel: turnCancel}

	releases := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	toolNames := [2]string{"gatecall1", "gatecall2"}
	for i := 0; i < len(toolNames); i++ {
		sub.tools.Register(releaseGatedTool{name: toolNames[i], release: releases[i]})
	}

	// closeGate guards each release channel with a sync.Once: the test body
	// opens the gates as its steps progress, while the cleanup below must close
	// whatever is still open when the test returns early after a failure.
	// Closing an already-closed channel would panic in the cleanup path.
	var gateOnce [len(releases)]sync.Once
	closeGate := func(i int) {
		gateOnce[i].Do(func() { close(releases[i]) })
	}

	sub.startRunLoop()
	// Cleanups run LIFO: stop the loop first, then open the release gates so the
	// gated tool goroutines can finish (their Execute context is the turn's
	// background context, not the cancelled parent context).
	t.Cleanup(func() {
		for i := range releases {
			closeGate(i)
		}
	})
	t.Cleanup(func() {
		sub.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := sub.waitDone(ctx); err != nil {
			t.Errorf("subagent run loop did not stop after cancel: %v", err)
		}
	})

	const firstText = "[Background job completed]\n\nDescription: repo index build\nStatus: succeeded"
	const secondText = "[Background job completed]\n\nDescription: docs lint\nStatus: succeeded"
	const firstAckID = "ctxappend-ack-1"
	const secondAckID = "ctxappend-ack-2"

	// 1. One LLM response carrying two regular tool calls: the assistant
	// tool_calls message is appended and the batch is dispatched. Both tools
	// block on their gates, so the batch stays pending until they are opened.
	sub.llmCh <- &llmResult{turnID: 1, resp: &message.Response{
		ToolCalls: convertCalls([]messageToolCall{
			mustJSONToolCall(t, "call-1", toolNames[0], map[string]any{}),
			mustJSONToolCall(t, "call-2", toolNames[1], map[string]any{}),
		}),
	}}
	if !waitForSubAgentCondition(t, sub, 5*time.Second, "pending tool batch with assistant tool_calls message", func() bool {
		return sub.turn != nil && sub.turn.PendingToolCalls.Load() > 0 &&
			hasAssistantMessageWithToolCalls(sub.ctxMgr.Snapshot())
	}) {
		t.Fatal("assistant tool_calls message / pending tool batch never appeared")
	}

	// 2. Two producers (background spawn completions) enqueue context-only
	// appends in FIFO order through the exact public entry the production
	// producers use, while both tool calls are still executing.
	if !sub.TryEnqueueContextAppend(message.Message{
		Role:         message.RoleUser,
		Content:      firstText,
		Kind:         message.KindBackgroundResult,
		MailboxAckID: firstAckID,
	}) {
		t.Fatal("first context append enqueue rejected")
	}
	if !sub.TryEnqueueContextAppend(message.Message{
		Role:         message.RoleUser,
		Content:      secondText,
		Kind:         message.KindBackgroundResult,
		MailboxAckID: secondAckID,
	}) {
		t.Fatal("second context append enqueue rejected")
	}

	// 3. Open the first gate so call-1's result is backfilled while call-2 is
	// still outstanding: the batch stays open, so this is exactly the window in
	// which an unconditional drain would have interleaved the appends.
	closeGate(0)
	if !waitForSubAgentCondition(t, sub, 5*time.Second, "tool result for call-1 with batch still open", func() bool {
		return hasToolResultFor(sub.ctxMgr.Snapshot(), "call-1") && sub.turn.PendingToolCalls.Load() == 1
	}) {
		t.Fatal("tool result for call-1 never backfilled while call-2 stayed pending")
	}

	// 4. Deferral window: the run loop woke on call-1's result, yet the queued
	// appends must stay undrained (and unacked) while a tool result is still
	// outstanding. This wait only observes the absence of the old buggy drain;
	// it passes once the loop defers the appends to the batch-closure boundary.
	if waitForSubAgentCondition(t, sub, 500*time.Millisecond, "deferred ctx append drained during pending batch", func() bool {
		msgs := sub.ctxMgr.Snapshot()
		return containsMessageContent(msgs, firstText) || containsMessageContent(msgs, secondText)
	}) {
		t.Fatalf("context append was drained into ctxmgr while PendingToolCalls=%d and call-2 had no result yet; sequence:\n%s", sub.turn.PendingToolCalls.Load(), summarizeMessages(sub.ctxMgr.Snapshot()))
	}
	if hasConsumedAckID(parent, firstAckID) || hasConsumedAckID(parent, secondAckID) {
		t.Fatal("context append was acked while it was still deferred (ack must only fire after the append is applied and persisted)")
	}

	// 5. Pairing invariant while the batch is still open: the tool result for
	// call-1 must directly follow the assistant tool_calls message that
	// declared it, with no context append (or any other non-tool message)
	// interleaved. call-2's result is not backfilled yet — that only happens
	// once step 6 opens its gate — so this step asserts the adjacency of the
	// one result that is present.
	msgs := sub.ctxMgr.Snapshot()
	idxAsst := indexOfAssistantWithToolCallID(msgs, "call-1")
	if idxAsst < 0 {
		t.Fatalf("assistant tool_calls message for call-1 missing; sequence:\n%s", summarizeMessages(msgs))
	}
	if idxAsst+1 >= len(msgs) || !isToolResultForAny(msgs[idxAsst+1], "call-1") {
		t.Fatalf("tool pairing broken while the batch is open: call-1's tool result does not directly follow its assistant tool_calls message; a context append was interleaved mid-batch. Sequence:\n%s", summarizeMessages(msgs))
	}

	// 6. Open the second gate so call-2's result closes the batch. The deferred
	// appends drain at the closure boundary: they land after every tool result,
	// in the exact order the producers enqueued them.
	closeGate(1)
	if !waitForSubAgentCondition(t, sub, 5*time.Second, "deferred ctx appends drained after batch closure", func() bool {
		msgs := sub.ctxMgr.Snapshot()
		return containsMessageContent(msgs, firstText) && containsMessageContent(msgs, secondText)
	}) {
		t.Fatalf("deferred ctx appends never drained after the batch closed; sequence:\n%s", summarizeMessages(sub.ctxMgr.Snapshot()))
	}
	// Full pairing and ordering assertion on the closed batch: the strict
	// sequence must be assistant tool_calls message (declaring both calls),
	// call-1 result, call-2 result, then the drained appends in their exact
	// enqueue order. Every message between the assistant message and the last
	// tool result has to be a tool result for one of the two calls — a
	// deferred append surfacing mid-batch would break that window.
	msgs = sub.ctxMgr.Snapshot()
	idxAsst = indexOfAssistantWithToolCallID(msgs, "call-1")
	tool1 := indexOfToolResultFor(msgs, "call-1")
	tool2 := indexOfToolResultFor(msgs, "call-2")
	first := indexOfContent(msgs, firstText)
	second := indexOfContent(msgs, secondText)
	if idxAsst < 0 || tool1 < 0 || tool2 < 0 || first < 0 || second < 0 {
		t.Fatalf("missing expected message anchors; sequence:\n%s", summarizeMessages(msgs))
	}
	if !(idxAsst < tool1 && tool1 < tool2 && tool2 < first && first < second) {
		t.Fatalf("final sequence out of order: want assistant tool_calls < call-1 result < call-2 result < first append < second append; got assistant=%d tool1=%d tool2=%d first=%d second=%d. Sequence:\n%s", idxAsst, tool1, tool2, first, second, summarizeMessages(msgs))
	}
	for i := idxAsst + 1; i <= tool2; i++ {
		if !isToolResultForAny(msgs[i], "call-1", "call-2") {
			t.Fatalf("non-tool message %q interleaved between the assistant tool_calls message and its tool results; sequence:\n%s", shortLine(msgs[i].Content), summarizeMessages(msgs))
		}
	}

	// 7. Ack timing: neither append was acked during the deferral window, and
	// after the closure drain each mailbox ack fires only once its append has
	// been applied and persisted.
	if !waitForSubAgentCondition(t, sub, 5*time.Second, "mailbox acks for both ctx appends", func() bool {
		return hasConsumedAckID(parent, firstAckID) && hasConsumedAckID(parent, secondAckID)
	}) {
		t.Fatalf("ctx append mailbox acks never recorded after the appends were applied; consumed=%v", consumedAckIDs(parent))
	}
}

// TestSubAgentCtxAppendWithoutPendingToolCallsAppendsAsTrailingUserMessage
// proves the injection path itself is sound when no tool batch is pending: a
// context append enqueued through the production entry is drained by the run
// loop and appended as the trailing user message. This is the behavior a fix
// must preserve outside the pending-batch window.
func TestSubAgentCtxAppendWithoutPendingToolCallsAppendsAsTrailingUserMessage(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, parent, "adhoc-ctxappend-idle")
	sub.startRunLoop()
	t.Cleanup(func() {
		sub.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := sub.waitDone(ctx); err != nil {
			t.Errorf("subagent run loop did not stop after cancel: %v", err)
		}
	})

	const appendText = "[Background job completed]\n\nDescription: docs lint\nStatus: succeeded"
	if !sub.TryEnqueueContextAppend(message.Message{
		Role:    message.RoleUser,
		Content: appendText,
		Kind:    message.KindBackgroundResult,
	}) {
		t.Fatal("context append enqueue rejected")
	}

	if !waitForSubAgentCondition(t, sub, 2*time.Second, "trailing context-append user message", func() bool {
		msgs := sub.ctxMgr.Snapshot()
		return len(msgs) > 0 && msgs[len(msgs)-1].Role == message.RoleUser && msgs[len(msgs)-1].Content == appendText
	}) {
		t.Fatalf("context append was not appended as the trailing user message; sequence:\n%s", summarizeMessages(sub.ctxMgr.Snapshot()))
	}
}

// waitForSubAgentCondition polls a predicate on the running SubAgent until it
// holds or the timeout expires. Polling avoids sleeps; the caller decides
// whether a timeout is a failure or an expected negative observation.
func waitForSubAgentCondition(t *testing.T, sub *SubAgent, timeout time.Duration, desc string, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		runtime.Gosched()
	}
	t.Logf("condition %q not met within %v", desc, timeout)
	return false
}

func hasAssistantMessageWithToolCalls(msgs []message.Message) bool {
	for _, m := range msgs {
		if m.Role == message.RoleAssistant && len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func indexOfAssistantWithToolCallID(msgs []message.Message, callID string) int {
	for i, m := range msgs {
		if m.Role != message.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == callID {
				return i
			}
		}
	}
	return -1
}

func indexOfToolResultFor(msgs []message.Message, callID string) int {
	for i, m := range msgs {
		if m.Role == message.RoleTool && m.ToolCallID == callID {
			return i
		}
	}
	return -1
}

func hasToolResultFor(msgs []message.Message, callID string) bool {
	return indexOfToolResultFor(msgs, callID) >= 0
}

// isToolResultForAny reports whether m is a tool result answering one of the
// given call IDs.
func isToolResultForAny(m message.Message, callIDs ...string) bool {
	if m.Role != message.RoleTool || m.ToolCallID == "" {
		return false
	}
	for _, callID := range callIDs {
		if m.ToolCallID == callID {
			return true
		}
	}
	return false
}

func indexOfContent(msgs []message.Message, content string) int {
	for i, m := range msgs {
		if strings.Contains(m.Content, content) {
			return i
		}
	}
	return -1
}

func containsMessageContent(msgs []message.Message, content string) bool {
	return indexOfContent(msgs, content) >= 0
}

// consumedAckIDs returns the mailbox message IDs the parent has recorded as
// consumed (markSubAgentMailboxConsumed) so far.
func consumedAckIDs(parent *MainAgent) []string {
	if parent == nil {
		return nil
	}
	parent.subAgentMailboxIDsMu.Lock()
	defer parent.subAgentMailboxIDsMu.Unlock()
	ids := make([]string, 0, len(parent.subAgentMailboxConsumed))
	for id := range parent.subAgentMailboxConsumed {
		ids = append(ids, id)
	}
	return ids
}

func hasConsumedAckID(parent *MainAgent, want string) bool {
	for _, id := range consumedAckIDs(parent) {
		if id == want {
			return true
		}
	}
	return false
}

func summarizeMessages(msgs []message.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		b.WriteString(fmt.Sprintf("  [%d] role=%s", i, m.Role))
		if len(m.ToolCalls) > 0 {
			ids := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				ids = append(ids, tc.ID)
			}
			b.WriteString(" tool_calls=")
			b.WriteString(strings.Join(ids, ","))
		}
		if m.ToolCallID != "" {
			b.WriteString(" tool_call_id=")
			b.WriteString(m.ToolCallID)
		}
		if m.Content != "" {
			b.WriteString(fmt.Sprintf(" content=%q", shortLine(m.Content)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// shortLine keeps diagnostics on one line and bounds their length.
func shortLine(content string) string {
	line := content
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	const max = 80
	if len(line) > max {
		line = line[:max] + "..."
	}
	return line
}
