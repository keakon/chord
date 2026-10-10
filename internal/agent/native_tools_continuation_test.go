package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

func appendPendingNativeTurn(mgr *ctxmgr.Manager) {
	mgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_lookup"}}, NativeTools: &message.NativeToolHistory{Protocol: config.ProviderTypeMessages, Calls: []message.HostedCall{{ID: "search-1", Name: "web_search"}}}})
	mgr.Append(message.Message{Role: message.RoleTool, ToolCallID: "call-1", Content: "Sample result"})
}

func TestMainDefersContextAppendWithoutExpandingCommands(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	appendPendingNativeTurn(a.ctxMgr)
	msg := message.Message{Role: message.RoleUser, Content: "/model sample/model", Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "Sample output"}}}
	a.handleAppendContext(Event{Payload: msg})
	if a.ctxMgr.MessageCount() != 2 || len(a.pendingNativeContextAppends) != 1 {
		t.Fatal("context-only input entered an unfinished native turn")
	}
	appendCompletedNativeTurn(a.ctxMgr)
	request := append(a.ctxMgr.Snapshot(), message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "Sample hint"})
	request = a.consumePendingUserMessagesForRequest(request, 1)
	if len(request) != 5 || !reflect.DeepEqual(request[3], msg) || request[4].Content != "Sample hint" || a.ctxMgr.MessageCount() != 4 || len(a.pendingNativeContextAppends) != 0 {
		t.Fatal("context-only input lost its identity, ordering, or content")
	}
	a.flushPendingNativeContextAppends(nil, 0)
	if a.ctxMgr.MessageCount() != 4 {
		t.Fatal("deferred context input was appended twice")
	}
}

func TestMainDeferredNativeContextDoesNotCrossSessionBoundary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	appendPendingNativeTurn(a.ctxMgr)
	a.handleAppendContext(Event{Payload: "Sample output"})
	a.resetSessionRuntimeState()
	a.flushPendingNativeContextAppends(nil, 0)
	if len(a.pendingNativeContextAppends) != 0 || a.ctxMgr.MessageCount() != 0 {
		t.Fatal("deferred context input leaked into another session")
	}
}

func appendCompletedNativeTurn(mgr *ctxmgr.Manager) {
	mgr.Append(message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Protocol: config.ProviderTypeMessages, Calls: []message.HostedCall{{ID: "search-1", Name: "web_search", Result: json.RawMessage(`[]`)}}}})
}

func TestMainDefersIndependentInputDuringNativeContinuation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	appendPendingNativeTurn(a.ctxMgr)
	a.pendingUserMessages = []pendingUserMessage{{Content: "New request", FromUser: true}}
	mailbox := &SubAgentMailboxMessage{MessageID: "mailbox-1", AgentID: "worker-1", Payload: "Worker result"}
	a.pendingSubAgentMailboxes = []*SubAgentMailboxMessage{mailbox}
	before := a.ctxMgr.MessageCount()
	request := a.consumePendingUserMessagesForRequest(a.ctxMgr.Snapshot(), 0)
	if len(request) != before || a.ctxMgr.MessageCount() != before || len(a.pendingUserMessages) != 1 {
		t.Fatal("queued user input entered an unfinished native turn")
	}
	if a.stageNextSubAgentMailboxBatch() || len(a.takePendingSubAgentMailboxes()) != 0 || len(a.pendingSubAgentMailboxes) != 1 {
		t.Fatal("mailbox batch was claimed before native turn completion")
	}
	appendCompletedNativeTurn(a.ctxMgr)
	request = a.consumePendingUserMessagesForRequest(a.ctxMgr.Snapshot(), 0)
	if len(a.pendingUserMessages) != 0 || request[len(request)-1].Content != "New request" || a.ctxMgr.MessageCount() != before+2 {
		t.Fatal("queued user input did not resume after native completion")
	}
	mailboxes := a.takePendingSubAgentMailboxes()
	if len(mailboxes) != 1 || mailboxes[0] != mailbox || len(a.takePendingSubAgentMailboxes()) != 0 {
		t.Fatal("deferred mailbox was lost or delivered twice")
	}
}

func TestSubDefersInputAndContextAppendsDuringNativeContinuation(t *testing.T) {
	parent := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, parent, "sample-task")
	appendPendingNativeTurn(sub.ctxMgr)
	sub.inputCh <- pendingUserMessage{Content: "New request"}
	sub.inputQueueBytes = pendingUserMessageBytes(pendingUserMessage{Content: "New request"})
	if !sub.TryEnqueueContextAppend(message.Message{Role: message.RoleUser, Content: "Context note"}) {
		t.Fatal("context append was rejected")
	}
	before := sub.ctxMgr.MessageCount()
	queuedBytes := sub.inputQueueBytes
	sub.drainContextAppendsBeforeTurn()
	request := sub.messagesForLLMContinuation()
	if !sub.openToolBatchDefersContextAppends() || len(sub.inputCh) != 1 || sub.inputQueueBytes != queuedBytes || len(request) != before || sub.ctxMgr.MessageCount() != before || len(sub.ctxAppendCh) != 1 {
		t.Fatal("independent input drained before native turn completion")
	}
	appendCompletedNativeTurn(sub.ctxMgr)
	sub.drainContextAppendsBeforeTurn()
	request = sub.messagesForLLMContinuation()
	if len(sub.inputCh) != 0 || sub.inputQueueBytes != 0 || len(sub.ctxAppendCh) != 0 || request[len(request)-1].Content != "New request" || sub.ctxMgr.MessageCount() != before+3 {
		t.Fatal("deferred subagent input did not resume after native completion")
	}
}
