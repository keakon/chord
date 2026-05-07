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
	setCurrentRolePoolCalls []string
	setAgentModelPoolCalls  map[string]string
	switchErr               error
}

func (b *poolSwitchBackend) SetCurrentRolePool(pool string) error {
	b.setCurrentRolePoolCalls = append(b.setCurrentRolePoolCalls, pool)
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
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"

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
	if len(backend.setCurrentRolePoolCalls) != 1 || backend.setCurrentRolePoolCalls[0] != "fast" {
		t.Fatalf("SetCurrentRolePool calls = %v, want [fast]", backend.setCurrentRolePoolCalls)
	}
}

func TestIdlePoolSwitchSubmitsImmediatelyToAgent(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"

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
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "fast"

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
	if len(backend.setCurrentRolePoolCalls) != 0 {
		t.Fatalf("SetCurrentRolePool calls = %v, want none", backend.setCurrentRolePoolCalls)
	}
}

func TestRepeatedBusyPoolSwitchesAllSubmitToAgent(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"pool-a", "pool-b", "pool-c"}
	backend.mainRoleCurrentPool = "pool-a"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"pool-a", "pool-b", "pool-c"},
		poolCursor: 1,
		prevMode:   ModeNormal,
	}
	runCmdTree(m.selectPoolAtCursor())

	backend.mainRoleCurrentPool = "pool-b"
	m.modelSelect.poolCursor = 2
	runCmdTree(m.selectPoolAtCursor())

	want := []string{"pool-b", "pool-c"}
	if len(backend.setCurrentRolePoolCalls) != len(want) {
		t.Fatalf("SetCurrentRolePool calls = %v, want %v", backend.setCurrentRolePoolCalls, want)
	}
	for i := range want {
		if backend.setCurrentRolePoolCalls[i] != want[i] {
			t.Fatalf("SetCurrentRolePool calls = %v, want %v", backend.setCurrentRolePoolCalls, want)
		}
	}
}

func TestDrainQueuedDraftsDoesNotPerformPoolSwitch(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"
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
	if len(backend.setCurrentRolePoolCalls) != 0 {
		t.Fatalf("SetCurrentRolePool calls = %v, want none", backend.setCurrentRolePoolCalls)
	}
}

func TestPoolSwitchErrorReportsResultWithoutBlockingDraft(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"
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
