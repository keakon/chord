package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestConfirmRequestForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ConfirmRequestEvent{
		ToolName:  tools.NameEdit,
		RequestID: "req-1",
	}})
	if cmd == nil {
		t.Fatal("confirm request should return followup command batch")
	}
	msg := cmd()
	msgs := []tea.Msg{}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			if subMsg := sub(); subMsg != nil {
				msgs = append(msgs, subMsg)
			}
		}
	} else if msg != nil {
		msgs = append(msgs, msg)
	}
	for _, subMsg := range msgs {
		updated, next := m.Update(subMsg)
		model, ok := updated.(*Model)
		if !ok {
			t.Fatalf("Update returned %T, want *Model", updated)
		}
		m = *model
		if next != nil {
			_ = next()
		}
	}
	if !m.streamRenderForceView {
		t.Fatal("confirm request should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("confirm request should clear deferred render state")
	}
}

func TestQuestionRequestForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.QuestionRequestEvent{
		RequestID: "q-1",
		Question:  "continue?",
	}})
	if cmd == nil {
		t.Fatal("question request should return followup command batch")
	}
	msg := cmd()
	msgs := []tea.Msg{}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			if subMsg := sub(); subMsg != nil {
				msgs = append(msgs, subMsg)
			}
		}
	} else if msg != nil {
		msgs = append(msgs, msg)
	}
	for _, subMsg := range msgs {
		updated, next := m.Update(subMsg)
		model, ok := updated.(*Model)
		if !ok {
			t.Fatalf("Update returned %T, want *Model", updated)
		}
		m = *model
		if next != nil {
			_ = next()
		}
	}
	if !m.streamRenderForceView {
		t.Fatal("question request should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("question request should clear deferred render state")
	}
}

func TestWarnToastForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	cmd := m.enqueueToast("careful", "warn")
	if cmd == nil {
		t.Fatal("warn toast should return command")
	}
	_ = cmd()
	if !m.streamRenderForceView {
		t.Fatal("warn toast should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("warn toast should clear deferred render state")
	}
}

func TestToastEventCategoryMergesQueuedNotifications(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToastEvent{Message: "invalidated account 1", Level: "error", Category: "oauth_account_invalidated"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToastEvent{Message: "invalidated account 2", Level: "error", Category: "oauth_account_invalidated"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToastEvent{Message: "invalidated account 3", Level: "error", Category: "oauth_account_invalidated"}})

	if m.activeToast == nil || m.activeToast.Message != "invalidated account 1" {
		t.Fatalf("active toast = %#v, want first notification", m.activeToast)
	}
	if len(m.toastQueue) != 1 || m.toastQueue[0].Message != "invalidated account 3" {
		t.Fatalf("toast queue = %#v, want only latest queued notification", m.toastQueue)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToastEvent{Message: "expired account", Level: "error", Category: "oauth_account_expired"}})
	if len(m.toastQueue) != 2 || m.toastQueue[1].Category != "oauth_account_expired" {
		t.Fatalf("toast queue = %#v, want different category preserved", m.toastQueue)
	}
}

func TestToastDurationsStayWithinThreeToFiveSeconds(t *testing.T) {
	for level, want := range map[string]time.Duration{
		"info":  3 * time.Second,
		"warn":  4 * time.Second,
		"error": 5 * time.Second,
	} {
		if got := toastDurationForLevel(level); got != want {
			t.Fatalf("toastDurationForLevel(%q) = %v, want %v", level, got, want)
		}
	}
}

func TestToastQueueLimitDropsOldestLowestPriority(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activeToast = &toastItem{Message: "active", Level: "error"}
	for i := range maxQueuedToasts {
		level := "warn"
		if i == 0 {
			level = "info"
		}
		_ = m.enqueueToast(fmt.Sprintf("queued %d", i), level)
	}

	_ = m.enqueueToast("new error", "error")

	if len(m.toastQueue) != maxQueuedToasts {
		t.Fatalf("toast queue length = %d, want %d", len(m.toastQueue), maxQueuedToasts)
	}
	for _, toast := range m.toastQueue {
		if toast.Message == "queued 0" {
			t.Fatalf("oldest lowest-priority toast was not dropped: %#v", m.toastQueue)
		}
	}
	if got := m.toastQueue[len(m.toastQueue)-1].Message; got != "new error" {
		t.Fatalf("last queued toast = %q, want new error", got)
	}
}

func TestToolResultForcesPriorityBoundaryFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.width, m.height = 80, 24
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	m.streamRenderDeferred = true
	m.streamRenderDeferNext = true

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID:   "call-boundary-1",
		Name: "read",
	}})

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID: "call-boundary-1",
		Name:   "read",
		Status: agent.ToolResultStatusSuccess,
		Result: "done",
	}})
	if cmd == nil {
		t.Fatal("tool result should return followup command batch")
	}
	_ = cmd()
	if !m.streamRenderForceView {
		t.Fatal("tool result should force next live render")
	}
	if m.streamRenderDeferred {
		t.Fatal("tool result should clear deferred render state")
	}
}
