package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

type scriptedStreamCall struct {
	resp             *message.Response
	err              error
	streams          []message.StreamDelta
	holdAfterStreams bool
}

type blockingStreamProvider struct {
	mu           sync.Mutex
	calls        []scriptedStreamCall
	streamedCh   chan struct{}
	releaseCh    chan struct{}
	seenMessages [][]message.Message
	seenTuning   []llm.RequestTuning
}

func cloneMessages(messages []message.Message) []message.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]message.Message, len(messages))
	copy(out, messages)
	return out
}

func (p *blockingStreamProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	messages []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning llm.RequestTuning,
	cb llm.StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	if len(p.calls) == 0 {
		p.mu.Unlock()
		return nil, io.ErrUnexpectedEOF
	}
	next := p.calls[0]
	p.calls = p.calls[1:]
	p.seenMessages = append(p.seenMessages, cloneMessages(messages))
	p.seenTuning = append(p.seenTuning, tuning)
	p.mu.Unlock()

	if cb != nil {
		for _, delta := range next.streams {
			cb(delta)
		}
	}
	if next.holdAfterStreams && p.streamedCh != nil {
		close(p.streamedCh)
		<-p.releaseCh
	}
	if next.err != nil {
		return nil, next.err
	}
	if next.resp != nil {
		return next.resp, nil
	}
	return &message.Response{}, nil
}

func (p *blockingStreamProvider) Complete(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning llm.RequestTuning,
) (*message.Response, error) {
	return p.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, nil)
}

func newReadyTestMainAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	return a
}

func TestAutoContinuePromptIsInjectedAsOneShotOverlay(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.pendingAutoContinuePrompt = autoContinuePrompt()
	overlays := a.buildTurnOverlayMessages()
	if len(overlays) == 0 {
		t.Fatal("expected auto-continue overlay to be present")
	}
	found := false
	for _, o := range overlays {
		if strings.Contains(o.Content, "context compaction completed successfully") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected auto-continue prompt in overlays")
	}
	if a.pendingAutoContinuePrompt != "" {
		t.Fatal("expected auto-continue prompt to be consumed one-shot")
	}
	overlays2 := a.buildTurnOverlayMessages()
	for _, o := range overlays2 {
		if strings.Contains(o.Content, "context compaction completed successfully") {
			t.Fatal("auto-continue overlay should not persist after first use")
		}
	}
}

func TestAutoContinueReplayPromptIsInjectedAsOneShotOverlay(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.pendingAutoContinueReplayPrompt = autoContinueReplayPrompt("finish the refactor safely")
	overlays := a.buildTurnOverlayMessages()
	if len(overlays) == 0 {
		t.Fatal("expected auto-continue replay overlay to be present")
	}
	found := false
	for _, o := range overlays {
		if strings.Contains(o.Content, "finish the refactor safely") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected auto-continue replay prompt in overlays")
	}
	if a.pendingAutoContinueReplayPrompt != "" {
		t.Fatal("expected auto-continue replay prompt to be consumed one-shot")
	}
}

func TestApplyPendingCompactionResumeOverlaysForContinueRestoresReplayPrompt(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.setPendingCompactionResume(&recovery.PendingCompactionResume{
		Kind:       string(compactionResumeAutoContinue),
		Mode:       compactionResumeModeReplayUserIntent,
		UserIntent: "finish the refactor safely",
	})

	a.applyPendingCompactionResumeOverlaysForContinue()

	if got := a.pendingAutoContinuePrompt; got == "" {
		t.Fatal("expected auto-continue prompt to be restored from durable state")
	}
	if got := a.pendingAutoContinueReplayPrompt; !strings.Contains(got, "finish the refactor safely") {
		t.Fatalf("pendingAutoContinueReplayPrompt = %q, want latest user intent", got)
	}
	if a.pendingCompactionResume != nil {
		t.Fatal("expected durable pending compaction resume to be consumed")
	}
}

func TestApplyPendingCompactionResumeOverlaysForContinueSyntheticModeSkipsReplay(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.setPendingCompactionResume(&recovery.PendingCompactionResume{
		Kind:       string(compactionResumeAutoContinue),
		Mode:       compactionResumeModeSyntheticContinue,
		UserIntent: "finish the refactor safely",
	})

	a.applyPendingCompactionResumeOverlaysForContinue()

	if got := a.pendingAutoContinuePrompt; got == "" {
		t.Fatal("expected auto-continue prompt in synthetic continue mode")
	}
	if got := a.pendingAutoContinueReplayPrompt; got != "" {
		t.Fatalf("pendingAutoContinueReplayPrompt = %q, want empty for synthetic continue", got)
	}
}

func TestApplyPendingCompactionResumeOverlaysForContinueRestoresOversizeRetryCountIntoNextTurn(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.setPendingCompactionResume(&recovery.PendingCompactionResume{
		Kind:               string(compactionResumeAutoContinue),
		Mode:               compactionResumeModeReplayUserIntent,
		UserIntent:         "finish the refactor safely",
		OversizeRetryCount: maxOversizeRecoveryAttempts,
	})

	a.applyPendingCompactionResumeOverlaysForContinue()
	a.newTurn()

	if a.turn == nil {
		t.Fatal("expected new turn")
	}
	if got := a.turn.OversizeRecoveryCount; got != maxOversizeRecoveryAttempts {
		t.Fatalf("OversizeRecoveryCount = %d, want %d", got, maxOversizeRecoveryAttempts)
	}
}

func TestChooseCompactionResumeModeUsesSyntheticContinueWhenToolSideEffectsPending(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.newTurn()
	a.turn.PendingToolMeta["call-1"] = PendingToolCall{CallID: "call-1", Name: "write"}
	if got := a.chooseCompactionResumeMode("finish the refactor safely"); got != compactionResumeModeSyntheticContinue {
		t.Fatalf("chooseCompactionResumeMode() = %q, want synthetic_continue", got)
	}
}

func waitForToastEvent(t *testing.T, ch <-chan AgentEvent, want string) ToastEvent {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt := <-ch:
			toast, ok := evt.(ToastEvent)
			if !ok {
				continue
			}
			if strings.Contains(toast.Message, want) {
				return toast
			}
		case <-timeout:
			t.Fatalf("timed out waiting for toast containing %q", want)
		}
	}
}
