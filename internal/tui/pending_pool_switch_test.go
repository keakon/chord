package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func runCmdTree(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	out := []tea.Msg{msg}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			out = append(out, runCmdTree(sub)...)
		}
	}
	if nestedCmd, ok := msg.(tea.Cmd); ok {
		out = append(out, runCmdTree(nestedCmd)...)
	}
	return out
}

func findModelSwitchResult(msgs []tea.Msg) (modelSwitchResultMsg, bool) {
	for _, msg := range msgs {
		if result, ok := msg.(modelSwitchResultMsg); ok {
			return result, true
		}
	}
	return modelSwitchResultMsg{}, false
}

type poolSwitchBackend struct {
	sessionControlAgent
	setCurrentModelPoolCalls []string
	setAgentModelPoolCalls   map[string]string
	switchErr                error
}

func (b *poolSwitchBackend) SetCurrentModelPool(pool string) error {
	b.setCurrentModelPoolCalls = append(b.setCurrentModelPoolCalls, pool)
	return b.switchErr
}

func (b *poolSwitchBackend) SetAgentModelPool(agentName, pool string) error {
	if b.setAgentModelPoolCalls == nil {
		b.setAgentModelPoolCalls = make(map[string]string)
	}
	b.setAgentModelPoolCalls[agentName] = pool
	return b.switchErr
}

func newPoolSwitchModel() (*Model, *poolSwitchBackend) {
	backend := &poolSwitchBackend{
		sessionControlAgent: sessionControlAgent{
			events: make(chan agent.AgentEvent, 16),
		},
	}
	m := NewModel(backend)
	m.mode = ModeNormal
	// Make tests deterministic across environments.
	// sendDraft may return nil when the focus-resize-freeze workaround is disabled
	// (it batches only image-protocol and host-redraw commands, both optional).
	m.SetFocusResizeFreezeEnabled(true)
	return &m, backend
}

func TestBusyPoolSwitchSubmitsImmediatelyToAgent(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"slow", "fast"}
	backend.mainModelPool = "slow"

	// Mark agent busy (streaming). Selecting a pool should submit the switch to
	// the agent immediately. Local MainAgent serializes that request on its event
	// loop, so it reaches the next request boundary before later queued work.
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"slow", "fast"},
		poolCursor: 1,
		prevMode:   ModeNormal,
	}
	cmd := m.selectPoolAtCursor()
	if cmd == nil {
		t.Fatal("expected command for immediate busy switch submission")
	}

	msgs := runCmdTree(cmd)
	if _, ok := findModelSwitchResult(msgs); !ok {
		t.Fatalf("cmd tree did not include modelSwitchResultMsg: %#v", msgs)
	}
	if len(backend.setCurrentModelPoolCalls) != 1 || backend.setCurrentModelPoolCalls[0] != "fast" {
		t.Fatalf("SetCurrentModelPool calls = %v, want [fast]", backend.setCurrentModelPoolCalls)
	}
}

func TestIdlePoolSwitchSubmitsImmediatelyToAgent(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"slow", "fast"}
	backend.mainModelPool = "slow"

	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"slow", "fast"},
		poolCursor: 1,
		prevMode:   ModeNormal,
	}
	cmd := m.selectPoolAtCursor()
	if cmd == nil {
		t.Fatal("expected non-nil command for immediate switch submission")
	}
}

func TestDuplicatePoolSwitchIsNoop(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"slow", "fast"}
	backend.mainModelPool = "fast"

	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"slow", "fast"},
		poolCursor: 1,
		prevMode:   ModeNormal,
	}
	cmd := m.selectPoolAtCursor()

	if cmd != nil {
		msgs := runCmdTree(cmd)
		if _, ok := findModelSwitchResult(msgs); ok {
			t.Fatalf("same-pool selection should not switch, got msgs %#v", msgs)
		}
	}
	if len(backend.setCurrentModelPoolCalls) != 0 {
		t.Fatalf("SetCurrentModelPool calls = %v, want none", backend.setCurrentModelPoolCalls)
	}
}

func TestRepeatedBusyPoolSwitchesAllSubmitToAgent(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"pool-a", "pool-b", "pool-c"}
	backend.mainModelPool = "pool-a"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"pool-a", "pool-b", "pool-c"},
		poolCursor: 1,
		prevMode:   ModeNormal,
	}
	runCmdTree(m.selectPoolAtCursor())

	backend.mainModelPool = "pool-b"
	m.modelSelect.poolCursor = 2
	runCmdTree(m.selectPoolAtCursor())

	want := []string{"pool-b", "pool-c"}
	if len(backend.setCurrentModelPoolCalls) != len(want) {
		t.Fatalf("SetCurrentModelPool calls = %v, want %v", backend.setCurrentModelPoolCalls, want)
	}
	for i := range want {
		if backend.setCurrentModelPoolCalls[i] != want[i] {
			t.Fatalf("SetCurrentModelPool calls = %v, want %v", backend.setCurrentModelPoolCalls, want)
		}
	}
}

func TestDrainQueuedDraftsDoesNotPerformPoolSwitch(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"slow", "fast"}
	backend.mainModelPool = "slow"
	m.queuedDrafts = []queuedDraft{{
		Content:  "queued msg",
		QueuedAt: time.Now(),
	}}

	cmd := m.drainQueuedDrafts()
	if cmd == nil {
		t.Fatal("drainQueuedDrafts should return command batch")
	}
	msgs := runCmdTree(cmd)
	if _, ok := findModelSwitchResult(msgs); ok {
		t.Fatalf("draining queued drafts should not perform a model switch: %#v", msgs)
	}
	if len(backend.setCurrentModelPoolCalls) != 0 {
		t.Fatalf("SetCurrentModelPool calls = %v, want none", backend.setCurrentModelPoolCalls)
	}
}

func TestPoolSwitchErrorReportsResultWithoutBlockingDraft(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"slow", "fast"}
	backend.mainModelPool = "slow"
	backend.switchErr = errors.New("simulated failure")

	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"slow", "fast"},
		poolCursor: 1,
		prevMode:   ModeNormal,
	}
	cmd := m.selectPoolAtCursor()
	if cmd == nil {
		t.Fatal("expected switch command")
	}
	msgs := runCmdTree(cmd)
	result, ok := findModelSwitchResult(msgs)
	if !ok {
		t.Fatalf("cmd tree did not include modelSwitchResultMsg: %#v", msgs)
	}
	if !errors.Is(result.err, backend.switchErr) {
		t.Fatalf("modelSwitchResultMsg.err = %v, want %v", result.err, backend.switchErr)
	}

	d := queuedDraft{Content: "test", QueuedAt: time.Now()}
	draftCmd := m.sendDraft(d)
	if len(backend.sentMessages) != 1 {
		t.Fatalf("message should still be sent despite pool switch error, got %d messages", len(backend.sentMessages))
	}
	if draftCmd == nil {
		t.Fatal("sendDraft should still return command batch")
	}
}

func TestModelSwitchResultDoesNotMarkBusyAgentIdle(t *testing.T) {
	m, _ := newPoolSwitchModel()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	cmd := m.handleModelSwitchResult(modelSwitchResultMsg{})
	if cmd != nil {
		t.Fatalf("handleModelSwitchResult returned unexpected cmd %#v", cmd)
	}
	if got := m.activities["main"].Type; got != agent.ActivityStreaming {
		t.Fatalf("main activity = %q, want streaming", got)
	}
}
