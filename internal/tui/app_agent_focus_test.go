package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHandleSwitchRoleInvalidatesCachesImmediately(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.cachedStatusKey = "cached-status"
	m.cachedInfoPanelFP = "cached-info"

	m.handleSwitchRole()

	if got := backend.currentRole; got != "planner" {
		t.Fatalf("currentRole = %q, want planner", got)
	}
	if m.cachedStatusKey != "" {
		t.Fatalf("cachedStatusKey = %q, want cleared", m.cachedStatusKey)
	}
	if m.cachedInfoPanelFP != "" {
		t.Fatalf("cachedInfoPanelFP = %q, want cleared", m.cachedInfoPanelFP)
	}
}

func TestHandleSwitchRoleReportsFailedSwitchAsErrorToast(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
		switchRoleErr:  errors.New(`role "planner" is not available`),
	}
	m := NewModelWithSize(backend, 100, 24)
	m.cachedStatusKey = "cached-status"

	m.handleSwitchRole()

	if got := backend.currentRole; got != "builder" {
		t.Fatalf("currentRole = %q, want builder (unchanged after a failed switch)", got)
	}
	if m.cachedStatusKey != "cached-status" {
		t.Fatal("a failed switch must not invalidate draw caches")
	}
	if m.activeToast == nil {
		t.Fatal("a failed switch must surface an error toast")
	}
	if m.activeToast.Level != "error" || !strings.Contains(m.activeToast.Message, `role "planner" is not available`) {
		t.Fatalf("activeToast = %+v, want error toast with the backend message", m.activeToast)
	}
}

func TestHandleSwitchAgentRefreshesInfoPanelModelWithoutEvent(t *testing.T) {
	backend := &sessionControlAgent{
		events:           make(chan agent.AgentEvent, 1),
		currentRole:      "builder",
		providerModelRef: "main/huge",
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
		providerModelRefByFocus: map[string]string{"": "main/huge", "agent-1": "worker/review"},
		runningModelRefByFocus:  map[string]string{"": "main/huge", "agent-1": "worker/review"},
		runningVariantByFocus:   map[string]string{"agent-1": "high"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()

	before := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(before, "huge") || !strings.Contains(before, "Provider: main") {
		t.Fatalf("initial info panel = %q, want main model", before)
	}

	m.handleSwitchAgent()

	if got := m.focusedAgentID; got != "agent-1" {
		t.Fatalf("focusedAgentID = %q, want agent-1", got)
	}
	if got := backend.focused; got != "agent-1" {
		t.Fatalf("backend focused = %q, want agent-1", got)
	}
	after := stripANSI(m.renderInfoPanel(40, 20))
	if !strings.Contains(after, "review@high") || !strings.Contains(after, "Provider: worker") {
		t.Fatalf("info panel after agent switch = %q, want worker/review@high", after)
	}
	if strings.Contains(after, "huge") || strings.Contains(after, "Provider: main") {
		t.Fatalf("info panel after agent switch should not keep stale main model: %q", after)
	}
}

func TestHandleSwitchAgentEscapesVanishedSubAgentToMain(t *testing.T) {
	backend := &sessionControlAgent{
		events:           make(chan agent.AgentEvent, 1),
		currentRole:      "builder",
		providerModelRef: "main/huge",
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()

	ids := m.sidebar.AgentIDs()
	if len(ids) != 1 || ids[0] != "main" {
		t.Fatalf("AgentIDs() = %v, want [\"main\"]", ids)
	}

	m.focusedAgentID = "ghost-agent"
	m.sidebar.focusedID = "ghost-agent"

	m.handleSwitchAgent()

	if m.focusedAgentID != "" {
		t.Fatalf("focusedAgentID = %q, want \"\" (main)", m.focusedAgentID)
	}
	if m.sidebar.focusedID != "" {
		t.Fatalf("sidebar.focusedID = %q, want \"\" (main)", m.sidebar.focusedID)
	}
}

func TestStoppedSubAgentRemainsSwitchableUntilCompletion(t *testing.T) {
	for _, status := range []string{"waiting_main", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			backend := &sessionControlAgent{
				events:      make(chan agent.AgentEvent, 1),
				currentRole: "builder",
				subAgents: []agent.SubAgentInfo{{
					InstanceID:   "agent-1",
					AgentDefName: "reviewer",
					TaskDesc:     "check code",
					State:        "running",
				}},
			}
			m := NewModelWithSize(backend, 100, 24)
			m.refreshSidebar()
			m.setFocusedAgent("agent-1")

			_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStatusEvent{
				AgentID: "agent-1",
				Status:  status,
				Message: "stopped without completing",
			}})

			if m.focusedAgentID != "agent-1" {
				t.Fatalf("focusedAgentID = %q, want stopped sub-agent to remain focused", m.focusedAgentID)
			}
			if got, ok := m.sidebar.FindStatus("agent-1"); !ok || got != status {
				t.Fatalf("sidebar status = %q, %v; want %s, true", got, ok, status)
			}
			ids := m.sidebar.AgentIDs()
			if len(ids) != 2 || ids[0] != "main" || ids[1] != "agent-1" {
				t.Fatalf("AgentIDs() = %v, want [main agent-1]", ids)
			}

			m.handleSwitchAgent()

			if m.focusedAgentID != "" {
				t.Fatalf("focusedAgentID = %q, want \"\" (main)", m.focusedAgentID)
			}
			if backend.focused != "" {
				t.Fatalf("backend.focused = %q, want \"\" (main)", backend.focused)
			}
		})
	}
}

func TestRehydratedSubAgentMigratesFocusedRuntimeState(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.focusedAgentID = "agent-old"
	m.sidebar.focusedID = "agent-old"
	m.activities["agent-old"] = agent.AgentActivityEvent{AgentID: "agent-old", Type: agent.ActivityIdle}
	m.activityLastChanged["agent-old"] = time.Now()
	m.agentComposerStates["agent-old"] = agentComposerState{}
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", AgentID: "agent-old", Content: "follow up"}}
	m.inflightDraft = &queuedDraft{ID: "draft-2", AgentID: "agent-old", Content: "sent"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStartedEvent{
		AgentID:         "agent-new",
		PreviousAgentID: "agent-old",
		TaskID:          "adhoc-1",
	}})

	if m.focusedAgentID != "agent-new" || m.sidebar.focusedID != "agent-new" {
		t.Fatalf("focus = %q/%q, want agent-new", m.focusedAgentID, m.sidebar.focusedID)
	}
	if _, ok := m.activities["agent-old"]; ok {
		t.Fatal("old runtime activity still present")
	}
	if got := m.activities["agent-new"].AgentID; got != "agent-new" {
		t.Fatalf("new runtime activity agent = %q", got)
	}
	if _, ok := m.agentComposerStates["agent-old"]; ok {
		t.Fatal("old runtime composer state still present")
	}
	if _, ok := m.agentComposerStates["agent-new"]; !ok {
		t.Fatal("new runtime composer state missing")
	}
	if m.queuedDrafts[0].AgentID != "agent-new" || m.inflightDraft.AgentID != "agent-new" {
		t.Fatalf("draft runtime IDs = %q/%q, want agent-new", m.queuedDrafts[0].AgentID, m.inflightDraft.AgentID)
	}
}

func TestRoleChangedEventRefreshesCachedStatusBarViewingLabel(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	scr := newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))
	if plain := stripANSI(m.cachedStatusRender.text); !strings.Contains(plain, "builder") {
		t.Fatalf("initial status bar = %q, want builder", plain)
	}

	backend.currentRole = "planner"
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RoleChangedEvent{Role: "planner"}})
	scr = newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))

	plain := stripANSI(m.cachedStatusRender.text)
	if !strings.Contains(plain, "planner") {
		t.Fatalf("status bar after role change = %q, want planner", plain)
	}
	if strings.Contains(plain, "builder") {
		t.Fatalf("status bar after role change should not keep stale builder label: %q", plain)
	}
}

func TestHandleAgentEventRefreshSidebarAlsoInvalidatesUsageCaches(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{{
			InstanceID: "agent-1",
			TaskDesc:   "ship tests",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.statusBarAgentSnapshotDirty = false
	m.usageStats.renderVersion = 7
	m.usageStats.linesCacheWidth = 80
	m.usageStats.linesCacheVer = 9
	m.usageStats.linesCacheLines = []string{"cached"}
	m.usageStats.dialogCacheW = 40
	m.usageStats.dialogCacheH = 10
	m.usageStats.dialogCacheScroll = 3
	m.usageStats.dialogCacheVer = 11
	m.usageStats.dialogCacheTheme = "dark"
	m.usageStats.dialogCacheText = "cached"

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStatusEvent{AgentID: "agent-1", Status: "idle"}})

	if !m.statusBarAgentSnapshotDirty {
		t.Fatal("AgentStatusEvent should dirty status bar snapshot when it refreshes sidebar")
	}
	if got := m.usageStats.renderVersion; got != 8 {
		t.Fatalf("usageStats.renderVersion = %d, want 8", got)
	}
	if m.usageStats.linesCacheWidth != 0 || m.usageStats.linesCacheVer != 0 || m.usageStats.linesCacheLines != nil {
		t.Fatal("AgentStatusEvent should clear usage stats lines cache when it refreshes sidebar")
	}
	if m.usageStats.dialogCacheW != 0 || m.usageStats.dialogCacheH != 0 || m.usageStats.dialogCacheScroll != 0 || m.usageStats.dialogCacheVer != 0 || m.usageStats.dialogCacheTheme != "" || m.usageStats.dialogCacheText != "" {
		t.Fatal("AgentStatusEvent should clear usage stats dialog cache when it refreshes sidebar")
	}
}

func TestSetFocusedAgentRefreshesCachedStatusBarViewingLabel(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()
	scr := newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))
	if plain := stripANSI(m.cachedStatusRender.text); !strings.Contains(plain, "builder") {
		t.Fatalf("initial status bar = %q, want builder", plain)
	}

	m.setFocusedAgent("agent-1")
	scr = newCountingScreen(100, 24)
	m.Draw(scr, image.Rect(0, 0, 100, 24))

	plain := stripANSI(m.cachedStatusRender.text)
	if !strings.Contains(plain, "agent-1") {
		t.Fatalf("status bar after focus switch = %q, want agent-1", plain)
	}
	if strings.Contains(plain, "reviewer") {
		t.Fatalf("status bar after focus switch should not include sub-agent type: %q", plain)
	}
	if strings.Contains(plain, "◉ builder") {
		t.Fatalf("status bar after focus switch should not keep stale builder viewing pill: %q", plain)
	}
}

func TestSetFocusedAgentRestoresComposerStatePerAgent(t *testing.T) {
	backend := &sessionControlAgent{
		events: make(chan agent.AgentEvent, 1),
		messagesByFocus: map[string][]message.Message{
			"":        {{Role: "assistant", Content: "main"}},
			"agent-1": {{Role: "assistant", Content: "worker"}},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.input.SetValue("main draft")
	m.input.syncHeight()
	m.attachments = []Attachment{{FileName: "main.png", MimeType: "image/png", Data: []byte{1}}}
	m.editingQueuedDraftID = "draft-1"

	m.setFocusedAgent("agent-1")

	if got := m.input.Value(); got != "" {
		t.Fatalf("subagent input value = %q, want empty fresh draft", got)
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("len(subagent attachments) = %d, want 0", got)
	}
	if got := m.editingQueuedDraftID; got != "" {
		t.Fatalf("subagent editingQueuedDraftID = %q, want empty", got)
	}

	m.input.SetValue("worker draft")
	m.input.syncHeight()

	m.setFocusedAgent("")

	if got := m.input.Value(); got != "main draft" {
		t.Fatalf("main input value after restore = %q, want main draft", got)
	}
	if got := len(m.attachments); got != 1 {
		t.Fatalf("len(main attachments) = %d, want 1", got)
	}
	if got := m.editingQueuedDraftID; got != "draft-1" {
		t.Fatalf("main editingQueuedDraftID = %q, want draft-1", got)
	}

	m.setFocusedAgent("agent-1")

	if got := m.input.Value(); got != "worker draft" {
		t.Fatalf("worker input value after restore = %q, want worker draft", got)
	}
	if got := len(m.attachments); got != 0 {
		t.Fatalf("len(worker attachments) = %d, want 0", got)
	}
	if got := m.editingQueuedDraftID; got != "" {
		t.Fatalf("worker editingQueuedDraftID after restore = %q, want empty", got)
	}
}

func TestFocusedSubAgentViewHidesMainQueuedDrafts(t *testing.T) {
	backend := &sessionControlAgent{
		events: make(chan agent.AgentEvent, 1),
		messagesByFocus: map[string][]message.Message{
			"":        {{Role: "assistant", Content: "main"}},
			"agent-1": {{Role: "assistant", Content: "worker"}},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.queuedDrafts = []queuedDraft{{
		ID:             "draft-1",
		Content:        "queued main",
		DisplayContent: "queued main",
		QueuedAt:       time.Now(),
	}}

	if got := stripANSI(m.renderQueuedDrafts(40, 3)); !strings.Contains(got, "queued main") {
		t.Fatalf("main renderQueuedDrafts = %q, want queued draft text", got)
	}

	m.setFocusedAgent("agent-1")

	if got := len(m.visibleQueuedDrafts()); got != 0 {
		t.Fatalf("len(visibleQueuedDrafts) in subagent view = %d, want 0", got)
	}
	if got := stripANSI(m.renderQueuedDrafts(40, 3)); got != "" {
		t.Fatalf("subagent renderQueuedDrafts = %q, want empty", got)
	}
	if got := m.generateLayout(m.width, m.height).queue.Dy(); got != 0 {
		t.Fatalf("subagent queue height = %d, want 0", got)
	}

	m.setFocusedAgent("")

	if got := stripANSI(m.renderQueuedDrafts(40, 3)); !strings.Contains(got, "queued main") {
		t.Fatalf("main renderQueuedDrafts after restore = %q, want queued draft text", got)
	}
}

func TestStatusBarViewingPillUsesFocusedAgentColor(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			Color:        "196",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()
	m.setFocusedAgent("agent-1")

	got := m.renderStatusBar()
	want := renderStatusBarViewingPill("agent-1", "196")
	if !strings.Contains(got, want) {
		t.Fatalf("status bar = %q, want colored viewing pill %q", got, want)
	}
}

func TestStatusBarViewingPillColorRefreshesWhenFocusedAgentChanges(t *testing.T) {
	backend := &sessionControlAgent{
		events:      make(chan agent.AgentEvent, 1),
		currentRole: "builder",
		subAgents: []agent.SubAgentInfo{
			{InstanceID: "agent-1", AgentDefName: "reviewer", Color: "196"},
			{InstanceID: "agent-2", AgentDefName: "coder", Color: "81"},
		},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()

	m.setFocusedAgent("agent-1")
	first := m.renderStatusBar()
	wantFirst := renderStatusBarViewingPill("agent-1", "196")
	if !strings.Contains(first, wantFirst) {
		t.Fatalf("first status bar = %q, want %q", first, wantFirst)
	}

	m.setFocusedAgent("agent-2")
	second := m.renderStatusBar()
	wantSecond := renderStatusBarViewingPill("agent-2", "81")
	if !strings.Contains(second, wantSecond) {
		t.Fatalf("second status bar = %q, want %q", second, wantSecond)
	}
	if strings.Contains(second, wantFirst) {
		t.Fatalf("second status bar should not keep stale colored pill %q: %q", wantFirst, second)
	}
}

func TestIsAgentBusyInflightDraftWithoutActivity(t *testing.T) {
	m := NewModel(nil)
	m.activities = map[string]agent.AgentActivityEvent{
		"main": {Type: agent.ActivityIdle, AgentID: "main"},
	}
	d := queuedDraft{Content: "hi"}
	m.inflightDraft = &d
	if !m.isAgentBusy() {
		t.Fatal("isAgentBusy: want true while inflightDraft set (activity can lag or events may drop)")
	}
	if !m.isFocusedAgentBusy() {
		t.Fatal("isFocusedAgentBusy: want true while inflightDraft set")
	}
}

func TestIsFocusedAgentBusyTreatsCompactingAsBusy(t *testing.T) {
	m := NewModel(nil)
	m.activities = map[string]agent.AgentActivityEvent{
		"main": {Type: agent.ActivityCompacting, AgentID: "main"},
	}
	if !m.isFocusedAgentBusy() {
		t.Fatal("isFocusedAgentBusy: want true during compacting so input separator uses busy styling")
	}
}

func TestIsFocusedAgentBusyIgnoresInflightDraftOwnedByOtherAgent(t *testing.T) {
	m := NewModel(nil)
	m.focusedAgentID = "agent-1"
	m.activities = map[string]agent.AgentActivityEvent{
		"agent-1": {Type: agent.ActivityIdle, AgentID: "agent-1"},
		"main":    {Type: agent.ActivityIdle, AgentID: "main"},
	}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}

	if m.isFocusedAgentBusy() {
		t.Fatal("isFocusedAgentBusy: want false when inflight draft belongs to main but focus is agent-1")
	}
}

func TestRenderStatusBarDoesNotUseSyntheticConnectingForOtherAgentDraft(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.focusedAgentID = "agent-1"
	m.activities = map[string]agent.AgentActivityEvent{
		"agent-1": {Type: agent.ActivityIdle, AgentID: "agent-1"},
		"main":    {Type: agent.ActivityIdle, AgentID: "main"},
	}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued", QueuedAt: time.Now()}

	got := stripANSI(m.renderStatusBar())
	if strings.Contains(got, "Connecting") {
		t.Fatalf("status bar should not show synthetic connecting for another agent's inflight draft, got %q", got)
	}
}

func TestOpenModelSelectTargetsFocusedSubAgentPoolList(t *testing.T) {
	backend := &sessionControlAgent{
		subAgents: []agent.SubAgentInfo{{InstanceID: "sub-1", AgentDefName: "reviewer"}},
		focused:   "sub-1",
		poolNamesByFocus: map[string][]string{
			"":         {"thinking", "fast"},
			"reviewer": {"sub-only", "thinking"},
		},
		currentPoolByFocus: map[string]string{
			"":         "fast",
			"reviewer": "thinking",
		},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.focusedAgentID = "sub-1"

	m.openModelSelect()

	if m.mode != ModeModelSelect {
		t.Fatalf("mode = %v, want ModeModelSelect", m.mode)
	}
	wantPools := []string{"sub-only", "thinking"}
	if strings.Join(m.modelSelect.poolNames, ",") != strings.Join(wantPools, ",") {
		t.Fatalf("poolNames = %v, want %v", m.modelSelect.poolNames, wantPools)
	}
	if m.modelSelect.poolCursor != 1 {
		t.Fatalf("poolCursor = %d, want 1 for focused subagent pool", m.modelSelect.poolCursor)
	}
}

func TestNormalModeEscapeCancelsFocusedSubAgentTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.focusedAgentID = "agent-1"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "agent-1"}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd == nil {
		t.Fatal("esc in focused subagent view should cancel busy turn")
	}
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", backend.cancelCalls)
	}
}

func TestOpenModelSelectForAgentOverrideUsesExplicitTarget(t *testing.T) {
	backend := &sessionControlAgent{
		subAgents: []agent.SubAgentInfo{{InstanceID: "sub-1", AgentDefName: "reviewer"}},
		focused:   "sub-1",
		poolNamesByFocus: map[string][]string{
			"reviewer": {"sub-only", "thinking"},
		},
		currentPoolByFocus: map[string]string{
			"reviewer": "thinking",
		},
	}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.focusedAgentID = "sub-1"

	m.openModelSelectFor(agent.ModelPoolSelectorTarget{Kind: agent.ModelPoolSelectorTargetAgentOverride, AgentName: "reviewer"})

	if m.mode != ModeModelSelect {
		t.Fatalf("mode = %v, want ModeModelSelect", m.mode)
	}
	wantPools := []string{"sub-only", "thinking"}
	if strings.Join(m.modelSelect.poolNames, ",") != strings.Join(wantPools, ",") {
		t.Fatalf("poolNames = %v, want %v", m.modelSelect.poolNames, wantPools)
	}
	if m.modelSelect.poolCursor != 1 {
		t.Fatalf("poolCursor = %d, want 1 for current override", m.modelSelect.poolCursor)
	}
}

func TestModelSelectorSingleGJumpsToTop(t *testing.T) {
	backend := &sessionControlAgent{
		mainModelPool:    "gamma",
		poolNamesByFocus: map[string][]string{"": {"alpha", "beta", "gamma"}},
	}
	m := NewModel(backend)
	m.openModelSelect()
	m.modelSelect.poolCursor = 2

	if cmd := m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'})); cmd != nil {
		t.Fatalf("single g in pool selector should not return cmd, got %#v", cmd)
	}
	if got := m.modelSelect.poolCursor; got != 0 {
		t.Fatalf("pool selector cursor = %d, want 0 (first pool)", got)
	}
}

func TestAgentDoneEventRefreshesTaskBlockLastTime(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	old := time.Date(2024, 6, 15, 15, 4, 0, 0, time.Local)
	task := &Block{ID: 1, Type: BlockToolCall, ToolName: "delegate", LinkedAgentID: "agent-1", StartedAt: old, SettledAt: old, ResultDone: true}
	m.viewport.AppendBlock(task)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", Summary: "done"}})

	if task.DoneSummary != "done" {
		t.Fatalf("DoneSummary = %q, want done", task.DoneSummary)
	}
	if !task.SettledAt.After(old) {
		t.Fatalf("task SettledAt = %v, want > %v", task.SettledAt, old)
	}
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Since ") {
		t.Fatalf("status bar should show last time after task completion; got %q", plain)
	}
}

func TestFocusedAgentDoneEventSwitchesToMainAndUpdatesDelegate(t *testing.T) {
	backend := &sessionControlAgent{
		focused: "agent-1",
		messagesByFocus: map[string][]message.Message{
			"": {
				{
					Role: "assistant",
					ToolCalls: []message.ToolCall{{
						ID:   "delegate-1",
						Name: "delegate",
						Args: json.RawMessage(`{"description":"review tests","agent_type":"reviewer"}`),
					}},
				},
				{Role: "tool", ToolCallID: "delegate-1", Content: `{"status":"started","task_id":"adhoc-7","agent_id":"agent-1"}`},
			},
			"agent-1": {
				{Role: "assistant", Content: "worker history"},
			},
		},
	}
	m := NewModelWithSize(backend, 140, 24)
	m.focusedAgentID = "agent-1"
	m.sidebar.focusedID = "agent-1"
	m.viewport.SetFilter("agent-1")
	m.viewport.ReplaceBlocks([]*Block{{ID: 1, Type: BlockAssistant, AgentID: "agent-1", Content: "worker history"}})
	m.nextBlockID = 2

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{
		AgentID: "agent-1",
		TaskID:  "adhoc-7",
		Summary: "done",
	}})

	if m.focusedAgentID != "" {
		t.Fatalf("focusedAgentID = %q, want main", m.focusedAgentID)
	}
	if backend.focused != "" {
		t.Fatalf("backend focused = %q, want main", backend.focused)
	}
	if m.sidebar.focusedID != "main" {
		t.Fatalf("sidebar focusedID = %q, want main", m.sidebar.focusedID)
	}
	task, ok := m.findBlockByLinkedTask("adhoc-7")
	if !ok {
		t.Fatal("expected main Delegate block after focus switched back")
	}
	if task.DoneSummary != "done" {
		t.Fatalf("DoneSummary = %q, want done", task.DoneSummary)
	}
	// The completion card must come from the durable mailbox append, not from
	// this live-only event; see the mailbox card contract on AgentNotifyEvent.
	for _, block := range m.viewport.visibleBlocks() {
		if block.Type == BlockStatus && block.StatusTitle == "AGENT COMPLETE" {
			t.Fatalf("AgentDoneEvent must not create a live-only completion card: %#v", block)
		}
	}
}

func TestRepeatedAgentDoneUpdatesSingleDelegateCard(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	task := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameDelegate, LinkedAgentID: "agent-1", LinkedTaskID: "adhoc-7", ResultDone: true}
	m.viewport.AppendBlock(task)
	m.nextBlockID = 2

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", TaskID: "adhoc-7", Summary: "first completion"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", TaskID: "adhoc-7", Summary: "revised completion"}})

	if task.DoneSummary != "revised completion" {
		t.Fatalf("DoneSummary = %q, want latest completion", task.DoneSummary)
	}
	delegateCards := 0
	liveOnlyCompletionCards := 0
	for _, block := range m.viewport.blocks {
		if block.Type == BlockToolCall && block.ToolName == tools.NameDelegate && block.LinkedTaskID == "adhoc-7" {
			delegateCards++
		}
		if block.Type == BlockStatus && block.StatusTitle == "AGENT COMPLETE" {
			liveOnlyCompletionCards++
		}
	}
	if delegateCards != 1 {
		t.Fatalf("delegate card count = %d, want 1", delegateCards)
	}
	if liveOnlyCompletionCards != 0 {
		t.Fatalf("live-only completion cards = %d, want 0 (the durable mailbox append owns the card)", liveOnlyCompletionCards)
	}

	// Each completion's durable mailbox append produces exactly one card, so
	// two reports still yield two cards (the count the old live-only event
	// used to provide), and the card keeps the persisted message identity.
	for i, summary := range []string{"first completion", "revised completion"} {
		meta := &message.MailboxMetadata{MessageID: fmt.Sprintf("agent-1-%d", i+1), AgentID: "agent-1", TaskID: "adhoc-7", Kind: "completed"}
		_ = m.handleAgentEvent(agentEventMsg{event: agent.MailboxTranscriptAppendedEvent{
			Message:       message.Message{Role: "user", Content: summary, Mailbox: meta},
			TargetAgentID: "main",
			MessageIndex:  i + 1,
		}})
	}
	completionCards := 0
	for _, block := range m.viewport.blocks {
		if block.Type == BlockStatus && block.StatusTitle == "AGENT COMPLETE" {
			completionCards++
		}
	}
	if completionCards != 2 {
		t.Fatalf("completion card count = %d, want one card per durable completion report", completionCards)
	}
}

func TestAgentNotifyDoesNotCreateUnpersistedCard(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentNotifyEvent{
		AgentID:       "worker-1",
		TaskID:        "adhoc-1",
		TargetAgentID: "main",
		Kind:          "risk_alert",
		Message:       "worker cannot continue",
	}})

	blocks := filterBlocksByAgent(m.viewport.blocks, "main")
	if len(blocks) != 0 {
		t.Fatalf("main block count = %d, want no unpersisted card", len(blocks))
	}
}

// Shift+Tab switches the role in Insert mode and the view in Normal mode. Tab
// no longer switches either: it used to change permissions, the prompt surface
// and possibly the model from the terminal's completion key, mid-typing.
func TestShiftTabSwitchesRoleInInsertModeOnly(t *testing.T) {
	newModel := func() (Model, *sessionControlAgent) {
		backend := &sessionControlAgent{
			events:         make(chan agent.AgentEvent, 1),
			currentRole:    "builder",
			availableRoles: []string{"builder", "planner"},
		}
		return NewModelWithSize(backend, 100, 24), backend
	}
	shiftTab := tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
	plainTab := tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})

	m, backend := newModel()
	m.mode = ModeInsert
	if cmd := m.handleInsertKey(shiftTab); cmd == nil {
		t.Fatal("Shift+Tab in Insert mode should return the role-switch toast command")
	}
	if got := backend.currentRole; got != "planner" {
		t.Fatalf("Insert mode Shift+Tab: currentRole = %q, want planner", got)
	}

	m, backend = newModel()
	m.mode = ModeInsert
	_ = m.handleInsertKey(plainTab)
	if got := backend.currentRole; got != "builder" {
		t.Fatalf("Insert mode Tab must not switch role, currentRole = %q", got)
	}

	m, backend = newModel()
	m.mode = ModeNormal
	_ = m.handleNormalKey(shiftTab)
	if got := backend.currentRole; got != "builder" {
		t.Fatalf("Normal mode Shift+Tab must switch the view, not the role; currentRole = %q", got)
	}

	m, backend = newModel()
	m.mode = ModeNormal
	_ = m.handleNormalKey(plainTab)
	if got := backend.currentRole; got != "builder" {
		t.Fatalf("Normal mode Tab must not switch role, currentRole = %q", got)
	}
}

// On a SubAgent view a role switch does not apply, so the key cycles the view
// instead of silently doing nothing.
func TestShiftTabOnSubAgentViewDoesNotSwitchRole(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeInsert
	m.focusedAgentID = "sub-1"

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if got := backend.currentRole; got != "builder" {
		t.Fatalf("role must not change while a SubAgent view is focused, currentRole = %q", got)
	}
}

// A keymap that rebinds switch_role back to tab must still work: the inert-tab
// guard is checked after the configured binding, not before it.
func TestInsertTabStillSwitchesRoleWhenRebound(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeInsert
	m.keyMap.SwitchRole = []string{"tab"}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if got := backend.currentRole; got != "planner" {
		t.Fatalf("rebound tab should switch role, currentRole = %q", got)
	}
}

// The documented escape hatch for the Tab → Shift+Tab move is
// keymap.switch_role: ["tab"], and it must restore *both* halves of the old
// behaviour: Tab switches the role and Shift+Tab goes back to cycling the agent
// view in Insert mode, exactly as it does in Normal mode. Insert mode only
// consulted SwitchRole, so the rebind turned Shift+Tab into a dead key there.
func TestInsertShiftTabCyclesAgentViewWhenSwitchRoleIsRebound(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()
	m.mode = ModeInsert
	m.keyMap.SwitchRole = []string{"tab"}

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if got := m.focusedAgentID; got != "agent-1" {
		t.Fatalf("Insert mode Shift+Tab should cycle the agent view, focusedAgentID = %q", got)
	}
	if got := backend.currentRole; got != "builder" {
		t.Fatalf("Insert mode Shift+Tab must not switch the role, currentRole = %q", got)
	}
}

// The default keymap binds switch_role and switch_agent to the same key, and
// the role must keep winning there: adding the SwitchAgent branch must not
// reorder the default. Shift+Tab on the main view still cycles the role.
func TestInsertShiftTabStillSwitchesRoleUnderTheDefaultKeymap(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder", "planner"},
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.refreshSidebar()
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if got := backend.currentRole; got != "planner" {
		t.Fatalf("default Shift+Tab should switch the role, currentRole = %q", got)
	}
	if got := m.focusedAgentID; got != "" {
		t.Fatalf("default Shift+Tab must not cycle the agent view, focusedAgentID = %q", got)
	}
}

// Cycling a single configured role would land back on the same role, and the
// switch itself is not free: it rebuilds the ruleset and writes a recovery
// snapshot. The no-op is dropped before that cost.
func TestHandleSwitchRoleSkipsSingleRoleCycle(t *testing.T) {
	backend := &sessionControlAgent{
		events:         make(chan agent.AgentEvent, 1),
		currentRole:    "builder",
		availableRoles: []string{"builder"},
	}
	m := NewModelWithSize(backend, 100, 24)
	m.cachedStatusKey = "cached-status"

	if cmd := m.handleSwitchRole(); cmd != nil {
		t.Fatal("single-role cycle should not report a switch")
	}
	if m.cachedStatusKey != "cached-status" {
		t.Fatal("single-role cycle should not invalidate draw caches")
	}
}
