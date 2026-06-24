package tui

import (
	"strings"
	"testing"
)

func hasDiagnosticEvent(events []tuiDiagnosticEvent, kind, detailSubstr string) bool {
	for _, evt := range events {
		if evt.Kind != kind {
			continue
		}
		if detailSubstr == "" || strings.Contains(evt.Detail, detailSubstr) {
			return true
		}
	}
	return false
}

func TestToastBoundaryAdjustsViewportOnToastAppearAndDisappear(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	oldHeight := m.viewport.height

	cmd := m.enqueueToast("diagnostics bundle exported", "info")
	if cmd == nil {
		t.Fatal("enqueueToast should schedule toast timer")
	}
	if got, want := m.viewport.height, oldHeight-1; got != want {
		t.Fatalf("viewport height after toast appear = %d, want %d", got, want)
	}
	if !hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "toast-boundary", "active=true") {
		t.Fatalf("recent events missing toast-boundary active=true marker: %#v", m.snapshotTUIDiagnosticEvents())
	}

	cmd = m.handleToastTick()
	if cmd != nil {
		t.Fatalf("toast expiry should not need a follow-up command, got %#v", cmd)
	}
	if m.activeToast != nil {
		t.Fatalf("activeToast = %#v, want nil", m.activeToast)
	}
	if got, want := m.viewport.height, oldHeight; got != want {
		t.Fatalf("viewport height after toast disappear = %d, want %d", got, want)
	}
	if !hasDiagnosticEvent(m.snapshotTUIDiagnosticEvents(), "toast-boundary", "active=false") {
		t.Fatalf("recent events missing toast-boundary active=false marker: %#v", m.snapshotTUIDiagnosticEvents())
	}
}
