package agent

import (
	"sync"
	"sync/atomic"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type llmRequestInput struct {
	Messages          []message.Message
	HistoryGeneration uint64
}

func snapshotLLMRequest(m *ctxmgr.Manager) llmRequestInput {
	messages, generation := m.SnapshotWithGeneration()
	return llmRequestInput{messages, generation}
}

var diagnosticsEpoch atomic.Uint64

type contextDiagnosticsRuntime struct {
	mu        sync.Mutex
	store     *contextdiag.Store
	collector *llm.DiagnosticsCollector
	retired   bool
	reduction contextdiag.Reduction
}

func (d *contextDiagnosticsRuntime) begin(session, task string, generation uint64) *llm.RequestDiagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retired {
		return nil
	}
	if d.collector == nil {
		d.store = &contextdiag.Store{}
		d.collector = llm.NewDiagnosticsCollector(contextdiag.Binding{SessionID: session, TaskID: task, Epoch: diagnosticsEpoch.Add(1)}, d.store)
	}
	return d.collector.BeginRequest(generation)
}
func (d *contextDiagnosticsRuntime) invalidate() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.retired = true
	if d.collector != nil {
		d.collector.Invalidate()
	}
}
func (d *contextDiagnosticsRuntime) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.collector != nil {
		d.collector.Invalidate()
	}
	d.collector = nil
	d.store = nil
	d.retired = false
	d.reduction = contextdiag.Reduction{}
}
func (d *contextDiagnosticsRuntime) snapshot(name, model string, m *ctxmgr.Manager) ContextDiagnosticsView {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := ContextDiagnosticsView{AgentName: contextdiag.DisplayLabel(name), UnavailableReason: "not_observed"}
	if d.store != nil {
		v.Snapshot, v.Available = d.store.Latest()
	}
	if v.Available {
		v.UnavailableReason = ""
		v.Historical = m == nil || v.Snapshot.HistoryGeneration != m.HistoryGeneration() || model != "" && v.Snapshot.ModelRef != model
	}
	return v
}
func (a *MainAgent) contextDiagnosticsSnapshot() ContextDiagnosticsView {
	client, _, _, _ := a.llmSnapshot()
	model := ""
	if client != nil {
		model = client.NextRequestModelRef()
	}
	return a.contextDiagnostics.snapshot(a.currentAgentName(), model, a.ctxMgr)
}
func (s *SubAgent) contextDiagnosticsSnapshot() ContextDiagnosticsView {
	client, _ := s.llmSnapshot()
	model := ""
	if client != nil {
		model = client.NextRequestModelRef()
	}
	return s.contextDiagnostics.snapshot(s.agentDefName, model, s.ctxMgr)
}
func (a *MainAgent) FocusedContextDiagnostics() ContextDiagnosticsView {
	if s := a.focusedAgent.Load(); s != nil {
		return s.contextDiagnosticsSnapshot()
	}
	a.focusedTaskMu.RLock()
	task := a.focusedTaskID
	a.focusedTaskMu.RUnlock()
	if task != "" && task != identity.MainAgentID {
		return ContextDiagnosticsView{AgentName: "worker", UnavailableReason: "not_observed"}
	}
	return a.contextDiagnosticsSnapshot()
}
