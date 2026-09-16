package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
)

func TestNormalModePageKeysScrollMainViewport(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeNormal
	m.viewport.height = 5
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("line\n", 30)})
	m.viewport.ScrollToTop()

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown})); cmd != nil {
		t.Fatalf("PgDown should not schedule command without inline images, got %#v", cmd)
	}
	if got := m.viewport.offset; got <= 0 {
		t.Fatalf("offset after PgDown = %d, want > 0", got)
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp})); cmd != nil {
		t.Fatalf("PgUp should not schedule command without inline images, got %#v", cmd)
	}
	if got := m.viewport.offset; got != 0 {
		t.Fatalf("offset after PgUp = %d, want 0", got)
	}
}

func TestCtrlCHintClearsOnNonCtrlCKeyAcrossModes(t *testing.T) {
	m := NewModel(nil)
	m.quit.at = time.Now()
	m.quit.by = "ctrl+c"

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))

	if !m.quit.at.IsZero() || m.quit.by != "" {
		t.Fatalf("pending quit = (%v, %q), want cleared", m.quit.at, m.quit.by)
	}
}

func TestPendingQuitTimerDoesNotClearNewerState(t *testing.T) {
	// Test that a stale clearPendingQuitMsg from a previous quit attempt
	// does not clear a newer pending quit state.
	m := NewModel(nil)
	m.mode = ModeNormal

	// First ctrl+c sets pending quit with gen=1.
	_ = m.handleCtrlC()
	if m.quit.gen != 1 {
		t.Fatalf("pendingQuitGen = %d, want 1", m.quit.gen)
	}
	if m.quit.by != "ctrl+c" {
		t.Fatalf("pendingQuitBy = %q, want ctrl+c", m.quit.by)
	}

	// Clear the state, but keep generation monotonic.
	m.clearPendingQuit()
	if m.quit.gen != 1 {
		t.Fatalf("pendingQuitGen = %d after clear, want 1", m.quit.gen)
	}

	// Second ctrl+c must allocate a newer generation.
	_ = m.handleCtrlC()
	if m.quit.gen != 2 {
		t.Fatalf("pendingQuitGen = %d, want 2", m.quit.gen)
	}

	// Now simulate the old timer from the first attempt (gen=1) firing.
	// This should NOT clear the current state (gen=2).
	_, cmd := m.Update(clearPendingQuitMsg{generation: 1})
	if cmd != nil {
		t.Fatalf("Update(clearPendingQuitMsg{generation: 1}) returned non-nil cmd")
	}
	if m.quit.by != "ctrl+c" {
		t.Fatalf("stale timer cleared pending quit; pendingQuitBy = %q, want ctrl+c", m.quit.by)
	}

	// But a matching generation should clear it.
	_, cmd = m.Update(clearPendingQuitMsg{generation: 2})
	if cmd != nil {
		t.Fatalf("Update(clearPendingQuitMsg{generation: 2}) returned non-nil cmd")
	}
	if m.quit.by != "" {
		t.Fatalf("matching timer did not clear pending quit; pendingQuitBy = %q", m.quit.by)
	}
}

func TestPendingQuitTimerGenerationForQ(t *testing.T) {
	// Test that 'q' also uses generation-based timer protection.
	m := NewModel(nil)
	m.mode = ModeNormal

	// First q sets pending quit with gen=1.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	if m.quit.gen != 1 {
		t.Fatalf("pendingQuitGen = %d, want 1", m.quit.gen)
	}
	if m.quit.by != "q" {
		t.Fatalf("pendingQuitBy = %q, want q", m.quit.by)
	}

	// Clear the state, but keep generation monotonic.
	m.clearPendingQuit()
	if m.quit.gen != 1 {
		t.Fatalf("pendingQuitGen = %d after clear, want 1", m.quit.gen)
	}

	// Second q sets pending quit with gen=2.
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "q", Code: 'q'}))
	if m.quit.gen != 2 {
		t.Fatalf("pendingQuitGen = %d, want 2", m.quit.gen)
	}

	// Stale timer from the first attempt (gen=1) should not clear.
	_, _ = m.Update(clearPendingQuitMsg{generation: 1})
	if m.quit.by != "q" {
		t.Fatalf("stale timer cleared pending quit; pendingQuitBy = %q, want q", m.quit.by)
	}

	// Matching timer should clear.
	_, _ = m.Update(clearPendingQuitMsg{generation: 2})
	if m.quit.by != "" {
		t.Fatalf("matching timer did not clear pending quit; pendingQuitBy = %q", m.quit.by)
	}
}

func TestHandleAgentEventClosedAfterExpectedDoneCompletionDoesNotToastReconnect(t *testing.T) {
	m := NewModel(nil)
	m.agentHadEvent = true
	m.expectedAgentClose = true
	m.activities[m.focusedAgentIDOrMain()] = agent.AgentActivityEvent{Type: agent.ActivityExecuting}

	cmd := m.handleAgentEvent(agentEventMsg{closed: true})
	if cmd != nil {
		t.Fatalf("handleAgentEvent(closed) returned cmd, want nil for expected close")
	}
	if m.expectedAgentClose {
		t.Fatal("expectedAgentClose should be cleared after expected close")
	}
	if m.activeToast != nil {
		t.Fatalf("activeToast = %+v, want nil", m.activeToast)
	}
	if got := m.activities[m.focusedAgentIDOrMain()].Type; got != agent.ActivityIdle {
		t.Fatalf("activity = %q, want idle", got)
	}
	if m.isAgentBusy() {
		t.Fatal("model should not remain busy after expected Done close")
	}
}

func TestViewRefreshesStatusBarForCtrlCHintWhileToastActive(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.activeToast = &toastItem{Message: "toast", Level: "info"}
	m.recalcViewportSize()

	initial := stripANSI(m.View().Content)
	if strings.Contains(initial, "Press Ctrl+C again to quit") {
		t.Fatalf("initial View() unexpectedly contains quit hint: %q", initial)
	}

	m.quit.at = time.Now()
	m.quit.by = "ctrl+c"

	updated := stripANSI(m.View().Content)
	if !strings.Contains(updated, "Press Ctrl+C again to quit") {
		t.Fatalf("updated View() missing quit hint while toast active: %q", updated)
	}
}

func TestViewRefreshesStatusBarForTransientStateChanges(t *testing.T) {
	type testCase struct {
		name   string
		mutate func(m *Model, backend *sessionControlAgent)
		want   string
	}

	tests := []testCase{
		{
			name: "pending quit q hint",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.quit.at = time.Now()
				m.quit.by = "q"
			},
			want: "Press q again to quit",
		},
		{
			name: "search pill",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.search.State.Active = true
				m.search.State.Query = "grep"
				m.search.State.Matches = []MatchPosition{{BlockIndex: 0}, {BlockIndex: 1}, {BlockIndex: 2}}
				m.search.State.Current = 1
			},
			want: "/grep [2/3]",
		},
		{
			name: "chord pill",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.chord = chordState{count: 12, op: chordE, startAt: time.Now()}
			},
			want: "12e",
		},
		{
			name: "insert mode text",
			mutate: func(m *Model, backend *sessionControlAgent) {
				m.mode = ModeInsert
				m.input.SetValue("first\nsecond")
				m.input.syncHeight()
			},
			want: "INSERT 2/2",
		},
		{
			name: "hidden model pill",
			mutate: func(m *Model, backend *sessionControlAgent) {
				backend.providerModelRef = "anthropic/claude-opus-4.6"
				m.invalidateStatusBarAgentSnapshot()
			},
			want: "claude-opus-4.6",
		},
		{
			name: "hidden usage/context pills",
			mutate: func(m *Model, backend *sessionControlAgent) {
				backend.tokenUsage = message.TokenUsage{InputTokens: 123, OutputTokens: 456}
				backend.sidebarUsage = analytics.SessionStats{EstimatedCost: 1.25}
				backend.contextCurrent = 2048
				backend.contextLimit = 8192
				m.invalidateStatusBarAgentSnapshot()
			},
			want: "↑ 123",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := &sessionControlAgent{providerModelRef: "anthropic/claude-opus-4.7"}
			m := NewModelWithSize(backend, 100, 24)
			m.rightPanelVisible = false
			m.layout = m.generateLayout(m.width, m.height)

			initial := stripANSI(m.View().Content)
			tc.mutate(&m, backend)
			updated := stripANSI(m.View().Content)

			if initial == updated {
				t.Fatalf("View() did not change after transient state mutation; content=%q", updated)
			}
			if !strings.Contains(updated, tc.want) {
				t.Fatalf("updated View() missing %q, got %q", tc.want, updated)
			}
		})
	}
}

func TestNormalModeCountPrefixStartsWithOneToNine(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "0", Code: '0'})); cmd != nil {
		t.Fatalf("0 should not start a chord count, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("0 should not start a pending chord")
	}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "5", Code: '5'}))
	if cmd == nil {
		t.Fatal("5 should start a chord count")
	}
	if m.chord.count != 5 || m.chord.op != chordNone {
		t.Fatalf("chord = %+v, want count=5 op=none", m.chord)
	}
}

func TestNormalModeEscClearsChordBeforeCancellingBusyTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}
	m.chord = chordState{op: chordG, startAt: time.Now()}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd != nil {
		t.Fatalf("esc with pending chord should not cancel turn, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("esc should clear pending chord")
	}
	if backend.cancelCalls != 0 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 0", backend.cancelCalls)
	}

	cmd = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd == nil {
		t.Fatal("second esc should cancel busy turn")
	}
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", backend.cancelCalls)
	}
}

func TestNormalModeJKNavigatesSkipsErrorCards(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockError, Content: "e1"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("a\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockError, Content: "e2"})
	m.viewport.AppendBlock(&Block{ID: 4, Type: BlockAssistant, Content: strings.Repeat("b\n", 4)})

	m.viewport.ScrollToTop()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after j = %d, want 2", m.focusedBlockID)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 4 {
		t.Fatalf("focusedBlockID after second j = %d, want 4", m.focusedBlockID)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'})); cmd != nil {
		t.Fatalf("k should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after k = %d, want 2", m.focusedBlockID)
	}
}

func TestNormalModeJKNavigatesBlocksByDefault(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("a\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("b\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("c\n", 4)})
	entries := m.viewport.MessageDirectory()
	m.viewport.ScrollToTop()

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after j = %d, want 2", m.focusedBlockID)
	}
	if m.viewport.offset != entries[1].LineOffset {
		t.Fatalf("viewport offset after j = %d, want %d", m.viewport.offset, entries[1].LineOffset)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'})); cmd != nil {
		t.Fatalf("k should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after k = %d, want 1", m.focusedBlockID)
	}
	if m.viewport.offset != entries[0].LineOffset {
		t.Fatalf("viewport offset after k = %d, want %d", m.viewport.offset, entries[0].LineOffset)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'})); cmd == nil {
		t.Fatal("2 should start count prefix")
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("2j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != 3 {
		t.Fatalf("focusedBlockID after 2j = %d, want 3", m.focusedBlockID)
	}
	if m.viewport.offset != entries[2].LineOffset {
		t.Fatalf("viewport offset after 2j = %d, want %d", m.viewport.offset, entries[2].LineOffset)
	}
}

func TestNormalModeJCanTraverseLastScreenWithoutFurtherScroll(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.mode = ModeNormal
	for i := 1; i <= 10; i++ {
		m.viewport.AppendBlock(&Block{ID: i, Type: BlockAssistant, Content: fmt.Sprintf("card-%02d", i)})
	}
	m.viewport.ScrollToBottom()
	entries := m.viewport.MessageDirectory()
	visible := m.viewport.visibleBlocks()
	if len(visible) < 4 {
		t.Fatalf("visible blocks at bottom = %d, want at least 4", len(visible))
	}
	firstVisible := visible[0]
	secondVisible := visible[1]
	lastVisible := visible[len(visible)-1]
	if entries[len(entries)-1].BlockID != lastVisible.ID {
		t.Fatalf("last visible block id = %d, want last entry block id %d", lastVisible.ID, entries[len(entries)-1].BlockID)
	}

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.focusedBlockID != secondVisible.ID {
		t.Fatalf("focusedBlockID after j at bottom = %d, want second visible block %d (first visible %d)", m.focusedBlockID, secondVisible.ID, firstVisible.ID)
	}

	for m.focusedBlockID != lastVisible.ID {
		if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
			t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
		}
	}
	if m.focusedBlockID != lastVisible.ID {
		t.Fatalf("focusedBlockID at bottom traversal end = %d, want last visible block %d", m.focusedBlockID, lastVisible.ID)
	}
}

func TestViewRefreshesForViewportAppendAndNavigation(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "alpha"})
	first := m.View().Content
	if !strings.Contains(first, "alpha") {
		t.Fatalf("initial view = %q, want alpha", first)
	}

	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("beta\n", 6)})
	second := m.View().Content
	if !strings.Contains(second, "beta") {
		t.Fatalf("view after append = %q, want beta", second)
	}

	entries := m.viewport.MessageDirectory()
	if len(entries) < 2 {
		t.Fatalf("visible entries = %d, want >= 2", len(entries))
	}
	m.viewport.ScrollToTop()
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'})); cmd != nil {
		t.Fatalf("j should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != entries[1].LineOffset {
		t.Fatalf("viewport offset after j = %d, want %d", m.viewport.offset, entries[1].LineOffset)
	}

	third := m.View().Content
	if third == second {
		t.Fatalf("view should change after navigation; before=%q after=%q", second, third)
	}
	if !strings.Contains(third, "beta") {
		t.Fatalf("view after navigation = %q, want beta visible", third)
	}
}

func TestNormalModeCountedTopJumpsToVisibleBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: strings.Repeat("a\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("b\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("c\n", 4)})
	entries := m.viewport.MessageDirectory()
	if len(entries) < 3 {
		t.Fatalf("visible entries = %d, want >=3", len(entries))
	}
	m.viewport.ScrollToBottom()
	m.focusedBlockID = 3
	m.refreshBlockFocus()

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'}))
	cmd1 := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd1 == nil {
		t.Fatal("second key in 2gg should keep chord alive")
	}
	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd != nil {
		t.Fatalf("2gg should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != entries[1].LineOffset {
		t.Fatalf("viewport offset = %d, want %d", m.viewport.offset, entries[1].LineOffset)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID = %d, want 2 after absolute jump", m.focusedBlockID)
	}
	if m.chord.active() {
		t.Fatal("2gg should clear chord state")
	}
}

func TestNormalModeCountedGMatchesCountedGG(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	for i := 1; i <= 4; i++ {
		m.viewport.AppendBlock(&Block{ID: i, Type: BlockAssistant, Content: strings.Repeat(string(rune('a'+i-1))+"\n", 3)})
	}
	entries := m.viewport.MessageDirectory()
	want := entries[2].LineOffset

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'})); cmd != nil {
		t.Fatalf("3G should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != want {
		t.Fatalf("3G offset = %d, want %d", m.viewport.offset, want)
	}

	m.viewport.ScrollToBottom()
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	cmd1 := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd1 == nil {
		t.Fatal("3gg should keep chord alive after first g")
	}
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'})); cmd != nil {
		t.Fatalf("3gg should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != want {
		t.Fatalf("3gg offset = %d, want %d", m.viewport.offset, want)
	}
}

func TestNormalModeGGFocusesFirstVisibleBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: strings.Repeat("first\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("second\n", 4)})
	m.viewport.ScrollToBottom()
	m.focusedBlockID = 2
	m.refreshBlockFocus()

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'})); cmd != nil {
		t.Fatalf("gg should move synchronously without extra cmd, got %#v", cmd)
	}
	if m.viewport.offset != 0 {
		t.Fatalf("viewport offset after gg = %d, want 0", m.viewport.offset)
	}
	if m.focusedBlockID != 1 {
		t.Fatalf("focusedBlockID after gg = %d, want 1", m.focusedBlockID)
	}
	if block := m.viewport.GetFocusedBlock(1); block == nil || !block.Focused {
		t.Fatalf("first block focus flag not set: %#v", block)
	}
}

func TestNormalModeGScrollsToBottomAndFocusesLastCard(t *testing.T) {
	m := NewModelWithSize(nil, 80, 8)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: strings.Repeat("first\n", 4)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("second\n", 4)})
	entries := m.viewport.MessageDirectory()
	if len(entries) < 2 {
		t.Fatalf("visible entries = %d, want >=2", len(entries))
	}
	m.viewport.ScrollToTop()
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'})); cmd != nil {
		t.Fatalf("G should move synchronously without extra cmd, got %#v", cmd)
	}
	if !m.viewport.atBottom() {
		t.Fatalf("viewport should be at bottom after G, offset=%d total=%d height=%d", m.viewport.offset, m.viewport.TotalLines(), m.viewport.height)
	}
	if m.focusedBlockID != 2 {
		t.Fatalf("focusedBlockID after G = %d, want 2", m.focusedBlockID)
	}
	if block := m.viewport.GetFocusedBlock(2); block == nil || !block.Focused {
		t.Fatalf("last block focus flag not set: %#v", block)
	}
}

func TestNormalModeCountedDDClearsInputAndAttachments(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.input.SetValue("draft")
	m.attachments = []Attachment{{FileName: "image.png", MimeType: "image/png", Data: []byte{1}}}

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'})); cmd != nil {
		t.Fatalf("3dd should clear input inline, got %#v", cmd)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty", got)
	}
	if len(m.attachments) != 0 {
		t.Fatalf("attachments len = %d, want 0", len(m.attachments))
	}
	if m.chord.active() {
		t.Fatal("3dd should clear chord state")
	}
}

func TestNormalModeInvalidChordClearsStateWithoutSideEffects(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "one"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "two"})
	m.viewport.ScrollToBottom()
	prevOffset := m.viewport.offset

	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "5", Code: '5'}))
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'})); cmd != nil {
		t.Fatalf("gx should not execute a command, got %#v", cmd)
	}
	if m.chord.active() {
		t.Fatal("invalid chord should clear state")
	}
	if m.viewport.offset != prevOffset {
		t.Fatalf("viewport offset = %d, want unchanged %d", m.viewport.offset, prevOffset)
	}
}

func TestNormalModeChordClearsOnModeSwitch(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal

	m.clearChordState()
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Text: "4", Code: '4'}))
	if !m.chord.active() {
		t.Fatal("count prefix should activate chord state")
	}
	m.switchModeWithIME(ModeInsert)
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if m.chord.active() {
		t.Fatal("switching mode should clear chord state")
	}
}

func TestChordTimeoutClearsPendingState(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.chord = chordState{op: chordY, startAt: time.Now()}
	m.chordTickGeneration = 7

	updated, cmd := m.Update(chordTimeoutMsg{generation: 7})
	if cmd != nil {
		_ = cmd
	}
	model := updated.(*Model)
	if model.chord.active() {
		t.Fatal("timeout message should clear pending chord state")
	}
}

func TestChordTimeoutStaleGenerationDoesNotClearState(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeNormal
	m.chord = chordState{op: chordY, startAt: time.Now()}
	m.chordTickGeneration = 7

	updated, _ := m.Update(chordTimeoutMsg{generation: 6})
	model := updated.(*Model)
	if !model.chord.active() {
		t.Fatal("stale timeout should not clear chord state")
	}
}
