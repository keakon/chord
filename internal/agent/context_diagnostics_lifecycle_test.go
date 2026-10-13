package agent

import (
	"testing"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
)

// Seed a dispatched record, then exercise the real lifecycle operation in the
// caller. The returned check models a completion racing with that operation.
func expectDiagnosticsRetired(t *testing.T, a *MainAgent, clear bool) func() {
	t.Helper()
	binding := contextdiag.Binding{SessionID: a.SessionDir(), TaskID: identity.MainAgentID, Epoch: diagnosticsEpoch.Add(1)}
	store := &contextdiag.Store{}
	a.contextDiagnostics.store = store
	a.contextDiagnostics.collector = llm.NewDiagnosticsCollector(binding, store)
	id := store.BeginRequest(binding, a.ctxMgr.HistoryGeneration())
	id.Attempt = 1
	snapshot := contextdiag.Snapshot{ID: id, State: contextdiag.Dispatched}
	if !store.Publish(snapshot) {
		t.Fatal("failed to seed dispatched observation")
	}
	return func() {
		t.Helper()
		if store.Finish(id, contextdiag.Completed, contextdiag.Usage{State: contextdiag.Reported, FullInput: 999}) {
			t.Fatal("retired binding accepted late usage")
		}
		snapshot.ID.Attempt++
		if store.Publish(snapshot) {
			t.Fatal("retired binding accepted late dispatch")
		}
		if clear && a.FocusedContextDiagnostics().Available {
			t.Fatal("new binding retained old observation")
		}
		if clear {
			d := a.contextDiagnostics.begin(a.SessionDir(), identity.MainAgentID, a.ctxMgr.HistoryGeneration())
			if d == nil || a.contextDiagnostics.store == store {
				t.Fatal("new binding cannot observe requests")
			}
		}
	}
}
