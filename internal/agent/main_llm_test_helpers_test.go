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
)

type scriptedStreamCall struct {
	resp             *message.Response
	err              error
	streams          []message.StreamDelta
	holdAfterStreams bool
}

type blockingStreamProvider struct {
	mu         sync.Mutex
	calls      []scriptedStreamCall
	streamedCh chan struct{}
	releaseCh  chan struct{}
}

func (p *blockingStreamProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	cb llm.StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	if len(p.calls) == 0 {
		p.mu.Unlock()
		return nil, io.ErrUnexpectedEOF
	}
	next := p.calls[0]
	p.calls = p.calls[1:]
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
