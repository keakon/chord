package tui

import (
	"testing"
	"time"
)

func TestInvalidateDrawCachesPreservesRuntimeState(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	deferred := &startupDeferredTranscriptState{
		startedAt:              time.Now(),
		originalViewportBudget: m.viewport.maxHotBytes,
	}
	startedAt := time.Now().Add(-3 * time.Second)
	m.animRunning = true
	m.statusBarTickGeneration = 11
	m.statusBarTickScheduled = true
	m.terminalTitleTickRunning = true
	m.terminalTitleTickGeneration = 13
	m.terminalTitleRequestBlinkOff = true
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, UserLocalShellCmd: "echo hi", UserLocalShellPending: true, StartedAt: startedAt})
	m.startupDeferredTranscript = deferred
	m.startupDeferredPreheatGeneration = 17

	m.cachedMainKey = "main-cache"
	m.cachedMainRender = cachedRenderable{text: "cached main"}
	m.cachedMainSearchBlockIndex = 42
	m.cachedInputKey = "input-cache"
	m.cachedStatusKey = "status-cache"
	m.cachedInfoPanelOut = "panel-cache"
	m.infoPanelHitBoxes = []infoPanelSectionHitBox{{startY: 1}}
	m.statusBarAgentSnapshotDirty = false

	m.invalidateDrawCaches()

	if !m.animRunning {
		t.Fatal("animRunning should survive draw-cache invalidation")
	}
	if m.statusBarTickGeneration != 11 || !m.statusBarTickScheduled {
		t.Fatalf("status bar tick state = generation %d scheduled %t, want generation 11 scheduled true", m.statusBarTickGeneration, m.statusBarTickScheduled)
	}
	if m.terminalTitleTickGeneration != 13 || !m.terminalTitleTickRunning || !m.terminalTitleRequestBlinkOff {
		t.Fatalf("terminal title ticker state = generation %d running %t blinkOff %t, want generation 13 running true blinkOff true", m.terminalTitleTickGeneration, m.terminalTitleTickRunning, m.terminalTitleRequestBlinkOff)
	}
	if got, ok := m.viewport.LatestVisiblePendingUserLocalShellStartedAt(); !ok || !got.Equal(startedAt) {
		t.Fatalf("pending local shell start = %v ok=%t, want %v true", got, ok, startedAt)
	}
	if m.startupDeferredTranscript != deferred || m.startupDeferredPreheatGeneration != 17 {
		t.Fatalf("startup deferred state = %p generation %d, want %p generation 17", m.startupDeferredTranscript, m.startupDeferredPreheatGeneration, deferred)
	}

	if m.cachedMainKey != "" || m.cachedMainRender.text != "" || m.cachedInputKey != "" || m.cachedStatusKey != "" || m.cachedInfoPanelOut != "" {
		t.Fatalf("render cache was not cleared: mainKey=%q mainText=%q inputKey=%q statusKey=%q infoOut=%q", m.cachedMainKey, m.cachedMainRender.text, m.cachedInputKey, m.cachedStatusKey, m.cachedInfoPanelOut)
	}
	if m.cachedMainSearchBlockIndex != -1 {
		t.Fatalf("cachedMainSearchBlockIndex = %d, want -1", m.cachedMainSearchBlockIndex)
	}
	if !m.statusBarAgentSnapshotDirty {
		t.Fatal("statusBarAgentSnapshotDirty should be set after draw-cache invalidation")
	}
	if m.infoPanelHitBoxes != nil {
		t.Fatalf("infoPanelHitBoxes = %#v, want nil", m.infoPanelHitBoxes)
	}
}

func TestSetThemePreservesDeferredStartupTranscriptRuntimeState(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	deferred := &startupDeferredTranscriptState{
		startedAt:              time.Now(),
		originalViewportBudget: m.viewport.maxHotBytes,
	}
	m.startupDeferredTranscript = deferred
	m.startupDeferredPreheatGeneration = 23
	m.viewport.maxHotBytes = startupDeferredTranscriptAggressiveHotBytes

	theme := DefaultTheme()
	theme.Name = "runtime-preserving-test"
	m.SetTheme(theme)

	if m.startupDeferredTranscript != deferred {
		t.Fatalf("startupDeferredTranscript = %p, want %p after SetTheme", m.startupDeferredTranscript, deferred)
	}
	if m.startupDeferredPreheatGeneration != 23 {
		t.Fatalf("startupDeferredPreheatGeneration = %d, want 23 after SetTheme", m.startupDeferredPreheatGeneration)
	}
	if m.viewport.maxHotBytes != startupDeferredTranscriptAggressiveHotBytes {
		t.Fatalf("viewport maxHotBytes = %d, want %d after SetTheme", m.viewport.maxHotBytes, startupDeferredTranscriptAggressiveHotBytes)
	}
}
