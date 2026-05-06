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
	return &m, backend
}

func TestBusyPoolSwitchQueuesSwitch(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"

	// Mark agent busy (streaming).
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	// Open pool selector and select "fast".
	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"slow", "fast"},
		poolCursor: 1,
		prevMode:   ModeNormal,
	}
	m.selectPoolAtCursor()

	if m.pendingModelSwitch == nil {
		t.Fatal("expected pendingModelSwitch to be set when agent is busy")
	}
	if m.pendingModelSwitch.pool != "fast" {
		t.Fatalf("pending pool = %q, want %q", m.pendingModelSwitch.pool, "fast")
	}
	if len(backend.setCurrentRolePoolCalls) != 0 {
		t.Fatalf("SetCurrentRolePool called immediately: %v", backend.setCurrentRolePoolCalls)
	}
}

func TestIdlePoolSwitchExecutesImmediately(t *testing.T) {
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
		t.Fatal("expected non-nil command for immediate switch")
	}
	if m.pendingModelSwitch != nil {
		t.Fatal("pendingModelSwitch should not be set when agent is idle")
	}
}

func TestApplyPendingPoolSwitchReturnsResultCmd(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"

	m.pendingModelSwitch = &pendingModelSwitchState{
		target: agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		pool:   "fast",
	}
	cmd := m.applyPendingPoolSwitch()

	if m.pendingModelSwitch != nil {
		t.Fatal("pendingModelSwitch should be nil after applyPendingPoolSwitch")
	}
	if cmd == nil {
		t.Fatal("applyPendingPoolSwitch should return result cmd")
	}
	msgs := runCmdTree(cmd)
	if _, ok := findModelSwitchResult(msgs); !ok {
		t.Fatalf("cmd tree did not include modelSwitchResultMsg: %#v", msgs)
	}
	if len(backend.setCurrentRolePoolCalls) != 1 || backend.setCurrentRolePoolCalls[0] != "fast" {
		t.Fatalf("SetCurrentRolePool calls = %v, want [fast]", backend.setCurrentRolePoolCalls)
	}
}

func TestSendDraftAppliesPendingPoolSwitch(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"

	m.pendingModelSwitch = &pendingModelSwitchState{
		target: agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		pool:   "fast",
	}
	d := queuedDraft{Content: "hello", QueuedAt: time.Now()}
	cmd := m.sendDraft(d)

	if m.pendingModelSwitch != nil {
		t.Fatal("pendingModelSwitch should be nil after sendDraft")
	}
	if len(backend.sentMessages) != 1 || backend.sentMessages[0] != "hello" {
		t.Fatalf("sent messages = %v, want [hello]", backend.sentMessages)
	}
	if cmd == nil {
		t.Fatal("sendDraft should return command batch including pending switch result")
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
	m.selectPoolAtCursor()

	// Current pool is already "fast", so no switch should happen.
	if m.pendingModelSwitch != nil {
		t.Fatal("pendingModelSwitch should not be set for same pool")
	}
}

func TestBusyPoolSwitchOverwritesPrevious(t *testing.T) {
	m, _ := newPoolSwitchModel()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	// First busy select sets pending to "pool-a".
	m.modelSelect = modelSelectState{
		target:     agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		poolNames:  []string{"pool-a", "pool-b", "pool-c"},
		poolCursor: 0,
		prevMode:   ModeNormal,
	}
	m.selectPoolAtCursor()

	// Second busy select (still busy) should overwrite to "pool-c".
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}
	m.modelSelect.poolCursor = 2
	m.selectPoolAtCursor()

	if m.pendingModelSwitch == nil || m.pendingModelSwitch.pool != "pool-c" {
		t.Fatalf("pending pool = %v, want pool-c", m.pendingModelSwitch)
	}
}

func TestDrainQueuedDraftsAppliesPendingPoolSwitch(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"

	m.pendingModelSwitch = &pendingModelSwitchState{
		target: agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		pool:   "fast",
	}
	m.queuedDrafts = []queuedDraft{{
		Content:  "queued msg",
		QueuedAt: time.Now(),
	}}

	cmd := m.drainQueuedDrafts()

	if m.pendingModelSwitch != nil {
		t.Fatal("pendingModelSwitch should be nil after drainQueuedDrafts")
	}
	if cmd == nil {
		t.Fatal("drainQueuedDrafts should return command batch")
	}
	msgs := runCmdTree(cmd)
	if _, ok := findModelSwitchResult(msgs); !ok {
		t.Fatalf("cmd tree did not include modelSwitchResultMsg: %#v", msgs)
	}
	if len(backend.setCurrentRolePoolCalls) != 1 || backend.setCurrentRolePoolCalls[0] != "fast" {
		t.Fatalf("SetCurrentRolePool calls = %v, want [fast]", backend.setCurrentRolePoolCalls)
	}
}

func TestPendingPoolSwitchErrorDoesNotBlockDraft(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainRolePoolNames = []string{"slow", "fast"}
	backend.mainRoleCurrentPool = "slow"
	backend.switchErr = errors.New("simulated failure")

	m.pendingModelSwitch = &pendingModelSwitchState{
		target: agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		pool:   "fast",
	}
	d := queuedDraft{Content: "test", QueuedAt: time.Now()}
	cmd := m.sendDraft(d)

	if len(backend.sentMessages) != 1 {
		t.Fatalf("message should still be sent despite pool switch error, got %d messages", len(backend.sentMessages))
	}
	if cmd == nil {
		t.Fatal("sendDraft should return command batch including pending switch result")
	}

	backend2 := &poolSwitchBackend{switchErr: backend.switchErr}
	m2 := NewModel(backend2)
	m2.pendingModelSwitch = &pendingModelSwitchState{
		target: agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetMainRole},
		pool:   "fast",
	}
	cmd2 := m2.applyPendingPoolSwitch()
	if cmd2 == nil {
		t.Fatal("applyPendingPoolSwitch should return result cmd for error case")
	}
	msgs := runCmdTree(cmd2)
	result, ok := findModelSwitchResult(msgs)
	if !ok {
		t.Fatalf("cmd tree did not include modelSwitchResultMsg: %#v", msgs)
	}
	if !errors.Is(result.err, backend.switchErr) {
		t.Fatalf("modelSwitchResultMsg.err = %v, want %v", result.err, backend.switchErr)
	}
}
