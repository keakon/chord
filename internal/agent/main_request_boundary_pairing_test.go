package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// gatedMailboxCaptureProvider records the messages of the first request it
// receives and then blocks until released, so a test can inspect the exact
// request surface an asynchronously spawned LLM call assembled.
type gatedMailboxCaptureProvider struct {
	started  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	messages []message.Message
}

func (p *gatedMailboxCaptureProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	messages []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	p.messages = append([]message.Message(nil), messages...)
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func (p *gatedMailboxCaptureProvider) Complete(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	toolDefs []message.ToolDefinition,
	maxTokens int,
	tuning llm.RequestTuning,
) (*message.Response, error) {
	return p.CompleteStream(ctx, apiKey, model, systemPrompt, messages, toolDefs, maxTokens, tuning, nil)
}

func (p *gatedMailboxCaptureProvider) captured() []message.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.messages
}

func conversationContainsUserText(messages []message.Message, want string) bool {
	for _, msg := range messages {
		if msg.Role == message.RoleUser && strings.Contains(msg.Content, want) {
			return true
		}
	}
	return false
}

// malformedToolCallResponse builds a response whose only tool call carries the
// streaming parser's malformed-args sentinel, so every valid/malformed
// classification reaches the discard-and-retry paths.
func malformedToolCallResponse(stopReason string) *LLMResponsePayload {
	return &LLMResponsePayload{
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "shell",
			Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`),
		}},
		StopReason: stopReason,
	}
}

// TestMalformedToolArgsRetryCarriesQueuedUserMessage pins that the
// non-truncated malformed-args retry is a full dispatch boundary: a user
// message queued while the discarded response was in flight must lead the retry
// request instead of waiting for the next idle drain.
func TestMalformedToolArgsRetryCarriesQueuedUserMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	const queued = "queued follow-up while the model retried"
	a.pendingUserMessages = []pendingUserMessage{{Content: queued, FromUser: true}}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: malformedToolCallResponse("stop")})

	if a.turn == nil {
		t.Fatal("the malformed-args retry must keep the turn active")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("queued user message stayed queued across the malformed-args retry: %#v", a.pendingUserMessages)
	}
	if !conversationContainsUserText(a.ctxMgr.Snapshot(), queued) {
		t.Fatal("the malformed-args retry request surface is missing the queued user message")
	}
}

// TestTruncatedLengthRecoveryCarriesQueuedUserMessage pins the same contract on
// the truncated length-recovery dispatch: beginLengthRecoveryRetry must merge
// queued user input before the recovery request is assembled.
func TestTruncatedLengthRecoveryCarriesQueuedUserMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	const queued = "queued follow-up while the model truncated"
	a.pendingUserMessages = []pendingUserMessage{{Content: queued, FromUser: true}}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: malformedToolCallResponse("length")})

	if a.turn == nil || !a.turn.InLengthRecovery {
		t.Fatal("the truncated malformed response must enter length recovery")
	}
	if len(a.pendingUserMessages) != 0 {
		t.Fatalf("queued user message stayed queued across the length-recovery retry: %#v", a.pendingUserMessages)
	}
	if !conversationContainsUserText(a.ctxMgr.Snapshot(), queued) {
		t.Fatal("the length-recovery request surface is missing the queued user message")
	}
}

// TestAutoContinueResumeCarriesMailboxOnlyQueue pins the compaction auto-continue
// resume boundary: when the pending queue holds only a mailbox message (no
// FromUser message), the fresh resume turn must still stage it so its first
// request carries the mailbox instead of waiting for idle.
func TestAutoContinueResumeCarriesMailboxOnlyQueue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	provider := &gatedMailboxCaptureProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
	})
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-1": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "model-1", 1024, "")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	const planID = uint64(12)
	a.startCompactionState(planID, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeAutoContinue})
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("startCompactionState must arm a pending auto-continue call")
	}
	a.resetCompactionState()
	// A real session always has an earlier user message; without it the resume
	// would park on awaitUserInput instead of spawning the fresh turn this test
	// exercises. It lives only in ctxmgr, so the pending user queue stays empty.
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "please continue the refactor"})

	// Only mailbox is queued: with no FromUser message in the pending queue the
	// resume's own drain stages nothing, so the mailbox must be staged
	// explicitly.
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{{
		MessageID: "c-1",
		AgentID:   "worker-1",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "child finished",
	}}

	if !a.resumePendingMainLLMAfterCompaction(pending, true) {
		t.Fatal("the auto-continue resume must handle its own barrier")
	}
	if a.turn == nil {
		t.Fatal("the auto-continue resume must spawn a fresh turn")
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the resume never dispatched an LLM request")
	}
	if got := countSubAgentMailboxMessages(provider.captured(), "c-1"); got != 1 {
		t.Fatalf("the resume request surface carries %d copies of c-1, want 1", got)
	}
}

// TestContinueFromContextStagesMailboxOnlyQueue pins the continue dispatch
// boundary: when the inbox holds only a progress snapshot (no FromUser message,
// so the consume path stages nothing on its own), a continue-initiated turn must
// still stage it so its request surface carries the mailbox instead of deferring
// it to idle.
func TestContinueFromContextStagesMailboxOnlyQueue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	provider := &gatedMailboxCaptureProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
	})
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-1": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "model-1", 1024, "")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	a.replaceProgressMailboxWithinBudget(SubAgentMailboxMessage{
		MessageID: "p-1",
		AgentID:   "worker-1",
		TaskID:    "task-a",
		Kind:      SubAgentMailboxKindProgress,
		Summary:   "still working",
	})

	a.handleContinueFromContext()

	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the continue never dispatched an LLM request")
	}
	if got := countSubAgentMailboxMessages(provider.captured(), "p-1"); got != 1 {
		t.Fatalf("the continue request surface carries %d copies of p-1, want 1", got)
	}
}
