package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/modelref"
)

type statusBarPlacedSegment struct {
	start int
	end   int
	text  string
}

func writeStatusBarSpaces(b *strings.Builder, count int) {
	for count > 0 {
		chunk := count
		if chunk > len(statusBarSpacePad) {
			chunk = len(statusBarSpacePad)
		}
		b.WriteString(statusBarSpacePad[:chunk])
		count -= chunk
	}
}

func formatStatusBarSubAgentLabel(agentDefName, instanceID string) string {
	return instanceID
}

func (m *Model) statusBarDynamicCacheKeyAt(now time.Time) string {
	if m.viewport != nil && m.viewport.HasUserLocalShellPending() {
		return m.visualAnimationCacheKeyAt(now)
	}
	if prog := m.renderRequestProgressSummary(m.focusedAgentIDOrMain()); prog != "" {
		return m.visualAnimationCacheKeyAt(now) + "|" + prog
	}
	if m.activities[m.focusedAgentIDOrMain()].Type == agent.ActivityCompacting {
		return m.visualAnimationCacheKeyAt(now)
	}
	if m.isFocusedAgentBusy() {
		return m.visualAnimationCacheKeyAt(now)
	}
	if _, ok := m.latestStatusStartWall(m.focusedAgentIDOrMain()); ok {
		return fmt.Sprintf("min:%d", now.Unix()/60)
	}
	return ""
}

func (m *Model) visualAnimationCacheKeyAt(now time.Time) string {
	cadence := m.currentCadence()
	if cadence.visualAnimDelay <= 0 {
		return fmt.Sprintf("sec:%d", now.Unix())
	}
	return fmt.Sprintf("frame:%d", now.UnixMilli()/cadence.visualAnimDelay.Milliseconds())
}

func (m *Model) inputAnimationCacheKeyAt(now time.Time) string {
	if !m.isFocusedAgentBusy() {
		return ""
	}
	return m.visualAnimationCacheKeyAt(now)
}

func pendingQuitFingerprintAt(pendingQuitBy string, pendingQuitAt, now time.Time) string {
	if pendingQuitBy == "" || pendingQuitAt.IsZero() || now.Sub(pendingQuitAt) >= pendingQuitWindow {
		return ""
	}
	return pendingQuitBy
}

func (m *Model) pendingQuitFingerprint(now time.Time) string {
	return pendingQuitFingerprintAt(m.pendingQuitBy, m.pendingQuitAt, now)
}

type statusBarAgentSnapshot struct {
	currentRole    string
	viewingLabel   string
	viewingColor   string
	sessionID      string
	modelRef       string
	modelVariant   string
	proxyInUse     bool
	mcpPill        string
	tokenUsage     message.TokenUsage
	cost           float64
	contextCurrent int
	contextLimit   int
}

type statusBarInputs struct {
	Now               time.Time
	ModeText          string
	Snapshot          statusBarAgentSnapshot
	StatusActiveID    string
	StatusActivity    agent.AgentActivityEvent
	InfoPanelVisible  bool
	SessionSwitchKind string
	SessionSwitchID   string
	WorkingDirDisplay string
	PendingQuitFP     string
	ChordDisplay      string
	SearchFP          string
	NextEscHint       string
	LoopState         agent.LoopState
	LoopIteration     int
	LoopMaxIterations int
	DynamicCacheKey   string
	InflightDraft     bool
	LocalShellPending bool
	Width             int
	Height            int
	ViewportOffset    int
}

func (m *Model) statusBarInputs(now time.Time) statusBarInputs {
	snap := m.statusBarSnapshot()
	statusActiveID := m.focusedAgentID
	if statusActiveID == "" {
		statusActiveID = "main"
	}
	loopState := agent.LoopState("")
	loopIteration := 0
	loopMaxIterations := 0
	if m.agent != nil {
		loopState = m.agent.CurrentLoopState()
		loopIteration = m.agent.CurrentLoopIteration()
		loopMaxIterations = m.agent.CurrentLoopMaxIterations()
	}
	return statusBarInputs{
		Now:               now,
		ModeText:          m.statusBarModeText(),
		Snapshot:          snap,
		StatusActiveID:    statusActiveID,
		StatusActivity:    m.activities[statusActiveID],
		InfoPanelVisible:  m.rightPanelVisible && m.mode != ModeDirectory && m.mode != ModeHelp,
		SessionSwitchKind: m.sessionSwitch.kind,
		SessionSwitchID:   m.sessionSwitch.sessionID,
		WorkingDirDisplay: displayWorkingDir(m.workingDir),
		PendingQuitFP:     strings.TrimSpace(m.pendingQuitFingerprint(now)),
		ChordDisplay:      m.chord.display(),
		SearchFP:          m.statusBarSearchFingerprint(),
		NextEscHint:       m.nextEscHint(),
		LoopState:         loopState,
		LoopIteration:     loopIteration,
		LoopMaxIterations: loopMaxIterations,
		DynamicCacheKey:   m.statusBarDynamicCacheKeyAt(now),
		InflightDraft:     m.inflightDraft != nil,
		LocalShellPending: m.viewport != nil && m.viewport.HasUserLocalShellPending(),
		Width:             m.width,
		Height:            m.height,
		ViewportOffset:    m.viewport.offset,
	}
}

func (m *Model) invalidateStatusBarAgentSnapshot() {
	m.statusBarAgentSnapshotDirty = true
}

func (m *Model) computeStatusBarCurrentAgentLabel(currentRole string, subAgents []agent.SubAgentInfo) string {
	if m.focusedAgentID == "" {
		if r := strings.TrimSpace(currentRole); r != "" {
			return r
		}
		return "main"
	}
	id := m.focusedAgentID
	for _, sub := range subAgents {
		if sub.InstanceID == id {
			return formatStatusBarSubAgentLabel(sub.AgentDefName, id)
		}
	}
	if defName, _, ok := m.sidebar.SubAgentLabels(id); ok {
		return formatStatusBarSubAgentLabel(defName, id)
	}
	return id
}

func (m *Model) computeStatusBarCurrentAgentColor() string {
	agentID := m.focusedAgentID
	if agentID == "" {
		agentID = "main"
	}
	for _, entry := range m.sidebar.Agents() {
		if entry.ID == agentID {
			return strings.TrimSpace(entry.Color)
		}
	}
	return ""
}

func (m *Model) statusBarSnapshot() statusBarAgentSnapshot {
	if !m.statusBarAgentSnapshotDirty {
		return m.statusBarAgentSnapshot
	}
	snap := statusBarAgentSnapshot{}
	if m.agent != nil {
		snap.currentRole = strings.TrimSpace(m.agent.CurrentRole())
		subAgents := m.agent.GetSubAgents()
		snap.viewingLabel = m.computeStatusBarCurrentAgentLabel(snap.currentRole, subAgents)
		snap.viewingColor = m.computeStatusBarCurrentAgentColor()
		if summary := m.agent.GetSessionSummary(); summary != nil {
			snap.sessionID = strings.TrimSpace(summary.ID)
		}
		snap.modelRef = strings.TrimSpace(m.agent.RunningModelRef())
		snap.modelRef = modelref.EnsureRefShowsProvider(snap.modelRef, strings.TrimSpace(m.agent.ProviderModelRef()))
		snap.modelVariant = m.agent.RunningVariant()
		ref := snap.modelRef
		if ref == "" {
			ref = strings.TrimSpace(m.agent.ProviderModelRef())
		}
		snap.proxyInUse = m.agent.ProxyInUseForRef(ref)
		if mp, ok := m.agent.(agent.MCPStateProvider); ok {
			snap.mcpPill = renderMCPStatusPill(mp.MCPServerList())
		}
		snap.tokenUsage = m.agent.GetTokenUsage()
		snap.cost = m.agent.GetSidebarUsageStats().EstimatedCost
		snap.contextCurrent, snap.contextLimit = m.agent.GetContextStats()
	} else {
		snap.viewingLabel = m.computeStatusBarCurrentAgentLabel("", nil)
		snap.viewingColor = m.computeStatusBarCurrentAgentColor()
	}
	m.statusBarAgentSnapshot = snap
	m.statusBarAgentSnapshotDirty = false
	return snap
}

func (m *Model) statusBarModeText() string {
	switch m.mode {
	case ModeInsert:
		modeText := "INSERT"
		if lc := m.input.LineCount(); lc > 1 {
			modeText = fmt.Sprintf("INSERT %d/%d", m.input.Line()+1, lc)
		}
		return modeText
	case ModeNormal:
		return "NORMAL"
	case ModeDirectory:
		return "DIR"
	case ModeConfirm:
		return "CONFIRM"
	case ModeQuestion:
		return "QUESTION"
	case ModeSearch:
		return "SEARCH"
	case ModeModelSelect:
		return "MODEL"
	case ModeSessionSelect:
		return "SESSION"
	case ModeSessionDeleteConfirm:
		return "DELETE"
	case ModeHandoffSelect:
		return "HANDOFF"
	case ModeUsageStats:
		return "STATS"
	case ModeHelp:
		return "HELP"
	case ModeImageViewer:
		return "IMAGE"
	case ModeRules:
		return "RULES"
	default:
		return ""
	}
}

func (m *Model) statusBarSearchFingerprint() string {
	if !m.search.State.Active || m.search.State.Query == "" {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", m.search.State.Query, len(m.search.State.Matches), m.search.State.Current)
}

// statusBarFingerprint must include every mutable state read by renderStatusBar.
// New footer pills or transient hints must either be added here or explicitly
// invalidate the draw caches when they change.
func (m *Model) statusBarFingerprint(now time.Time) string {
	var b strings.Builder
	b.Grow(256)
	inputs := m.statusBarInputs(now)
	snap := inputs.Snapshot
	statusActivity := inputs.StatusActivity
	usage := snap.tokenUsage
	fmt.Fprintf(&b, "%d|%d|%d|%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%d|%d|%t|%s|%s|%s|%s|%s|%t|%t|%d|%d|%f|%d|%d|%t|%d|%d",
		inputs.Width,
		inputs.Height,
		m.mode,
		inputs.ViewportOffset,
		inputs.ModeText,
		m.focusedAgentID,
		snap.viewingLabel,
		snap.viewingColor,
		inputs.PendingQuitFP,
		inputs.ChordDisplay,
		inputs.SearchFP,
		inputs.NextEscHint,
		string(inputs.LoopState),
		inputs.DynamicCacheKey,
		inputs.LoopIteration,
		inputs.LoopMaxIterations,
		inputs.InfoPanelVisible,
		inputs.SessionSwitchKind,
		inputs.SessionSwitchID,
		inputs.WorkingDirDisplay,
		snap.sessionID,
		statusActivity.AgentID,
		inputs.InflightDraft,
		inputs.LocalShellPending,
		usage.InputTokens,
		usage.OutputTokens,
		snap.cost,
		snap.contextCurrent,
		snap.contextLimit,
		snap.proxyInUse,
		len(snap.modelRef),
		len(snap.modelVariant),
	)
	b.WriteByte('|')
	b.WriteString(string(statusActivity.Type))
	b.WriteByte('|')
	b.WriteString(statusActivity.Detail)
	b.WriteByte('|')
	b.WriteString(snap.modelRef)
	b.WriteByte('|')
	b.WriteString(snap.modelVariant)
	b.WriteByte('|')
	b.WriteString(snap.mcpPill)
	return b.String()
}

// renderStatusBar builds the bottom status line using pill-styled components.
func (m *Model) renderStatusBar() string {
	var pills []string
	shortHelp := ""
	quitHint := ""
	inputs := m.statusBarInputs(time.Now())
	snap := inputs.Snapshot
	m.statusPath.value = ""
	m.statusPath.display = ""
	m.statusPath.startX = 0
	m.statusPath.endX = 0

	// Exit confirmation hint: "press same key again to quit"
	if m.pendingQuitBy != "" && !m.pendingQuitAt.IsZero() && time.Since(m.pendingQuitAt) < pendingQuitWindow {
		hint := "Press q again to quit"
		if m.pendingQuitBy == "ctrl+c" {
			hint = "Press Ctrl+C again to quit"
		}
		quitHint = hint
	}

	// Mode pill (first)
	var modeText string
	var modeStyle lipgloss.Style
	switch m.mode {
	case ModeInsert:
		modeText = m.statusBarModeText()
		modeStyle = ModeInsertStyle
	case ModeNormal:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	case ModeDirectory:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	case ModeConfirm:
		modeText = m.statusBarModeText()
		modeStyle = ModeConfirmStyle
	case ModeQuestion:
		modeText = m.statusBarModeText()
		modeStyle = ModeQuestionStyle
	case ModeSearch:
		modeText = m.statusBarModeText()
		modeStyle = ModeSearchStyle
	case ModeModelSelect:
		modeText = m.statusBarModeText()
		modeStyle = ModeModelSelectStyle
	case ModeSessionSelect:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	case ModeSessionDeleteConfirm:
		modeText = m.statusBarModeText()
		modeStyle = ModeConfirmStyle
	case ModeHandoffSelect:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	case ModeUsageStats:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	case ModeHelp:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	case ModeImageViewer:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	case ModeRules:
		modeText = m.statusBarModeText()
		modeStyle = ModeNormalStyle
	}
	modePill := ""
	if m.cachedStatusBarModeKey == modeText {
		modePill = m.cachedStatusBarModePill
	} else {
		modePill = modeStyle.Render(modeText)
		m.cachedStatusBarModeKey = modeText
		m.cachedStatusBarModePill = modePill
	}
	pills = append(pills, modePill)

	// Viewing pill: role name for main agent, instance ID for sub-agents.
	// viewing is kept as the raw ID used by downstream model/proxy logic.
	viewing := m.focusedAgentID
	if viewing == "" {
		viewing = "main"
	}
	viewingLabel := snap.viewingLabel
	viewingColor := snap.viewingColor
	viewingPill := ""
	viewingKey := viewingLabel + "|" + viewingColor
	if m.cachedStatusBarViewingKey == viewingKey {
		viewingPill = m.cachedStatusBarViewingPill
	} else {
		viewingPill = renderStatusBarViewingPill(viewingLabel, viewingColor)
		m.cachedStatusBarViewingKey = viewingKey
		m.cachedStatusBarViewingPill = viewingPill
	}
	pills = append(pills, viewingPill)
	if inputs.LoopState != "" {
		label := "LOOP"
		switch string(inputs.LoopState) {
		case "completed", "blocked", "budget_exhausted":
			label = "LOOP"
		default:
			iter := inputs.LoopIteration
			maxIter := inputs.LoopMaxIterations
			if iter > 0 || maxIter > 0 {
				displayIter := iter
				if displayIter == 0 {
					displayIter = 1
				}
				if maxIter > 0 {
					label = fmt.Sprintf("LOOP %d/%d", displayIter, maxIter)
				} else {
					label = fmt.Sprintf("LOOP %d", displayIter)
				}
			}
		}
		pills = append(pills, StatusHintStyle.Render(label))
	}
	sessionID := snap.sessionID

	if m.width >= 80 {
		shortHelp = ""
	}

	// InfoPanel visibility flag
	infoPanelVisible := m.rightPanelVisible && m.mode != ModeDirectory && m.mode != ModeHelp

	// Only show technical details in status bar if InfoPanel is HIDDEN
	if !infoPanelVisible {
		// Compute effective width and left-pills width (sum of already-built pills).
		// We accumulate pill widths directly instead of JoinHorizontal+Width to avoid
		// a throw-away render pass that was the main source of wasted work per frame.
		effW := m.width - statusBarLeftMargin - statusBarRightMargin
		if effW < 0 {
			effW = 0
		}
		var leftW int
		for _, p := range pills {
			leftW += lipgloss.Width(p)
		}

		pills = m.appendStatusBarModelPills(pills, snap, effW, leftW)

	}

	if m.search.State.Active && m.search.State.Query != "" {
		total := len(m.search.State.Matches)
		current := m.search.State.Current + 1
		if total == 0 {
			current = 0
		}
		pills = append(pills, PillStyle.Render(fmt.Sprintf("/%s [%d/%d]", m.search.State.Query, current, total)))
	}

	leftPillsKey := statusBarLeftPillsKey(modeText, viewingLabel, viewingColor, pills[2:])
	leftSide := ""
	leftWidth := 0
	if m.cachedStatusBarPillsKey == leftPillsKey {
		leftSide = m.cachedStatusBarLeftSide
		leftWidth = m.cachedStatusBarLeftW
	} else {
		leftSide = lipgloss.JoinHorizontal(lipgloss.Center, pills...)
		leftWidth = lipgloss.Width(leftSide)
		m.cachedStatusBarPillsKey = leftPillsKey
		m.cachedStatusBarLeftSide = leftSide
		m.cachedStatusBarLeftW = leftWidth
	}
	if shortHelp != "" {
		leftSide = lipgloss.JoinHorizontal(
			lipgloss.Center,
			leftSide,
			DimStyle.Render("  ·  "),
			shortHelp,
		)
		leftWidth = lipgloss.Width(leftSide)
	}
	if m.chord.active() {
		leftSide = lipgloss.JoinHorizontal(
			lipgloss.Center,
			leftSide,
			DimStyle.Render("  ·  "),
			StatusHintStyle.Render(m.chord.display()),
		)
		leftWidth = lipgloss.Width(leftSide)
	}
	if quitHint != "" {
		leftSide = lipgloss.JoinHorizontal(
			lipgloss.Center,
			leftSide,
			DimStyle.Render("  ·  "),
			StatusHintStyle.Render(quitHint),
		)
		leftWidth = lipgloss.Width(leftSide)
	}
	if inputs.NextEscHint != "" {
		leftSide = lipgloss.JoinHorizontal(
			lipgloss.Center,
			leftSide,
			DimStyle.Render("  ·  "),
			DimStyle.Render("esc ⇢ "+inputs.NextEscHint),
		)
		leftWidth = lipgloss.Width(leftSide)
	}

	// Content width: leave margins so the closing paren of elapsed "(Ns)" / "(NmNs)" is not covered by scrollbar.
	effectiveWidth := m.width - statusBarLeftMargin - statusBarRightMargin
	if effectiveWidth < 0 {
		effectiveWidth = 0
	}

	// Activity (center lane)
	statusActiveID := m.focusedAgentID
	if statusActiveID == "" {
		statusActiveID = "main"
	}
	statusActivity := m.activities[statusActiveID]
	separatorWidth := lipgloss.Width(DimStyle.Render(statusBarActivityPathGap))

	pathValue := displayWorkingDir(m.workingDir)
	sessionValue := sessionID
	pathText := ""
	sessionText := ""
	m.statusPath.value = ""
	m.statusPath.display = ""
	m.statusPath.startX = 0
	m.statusPath.endX = 0
	m.statusSession.value = ""
	m.statusSession.display = ""
	m.statusSession.startX = 0
	m.statusSession.endX = 0

	rightReserve := 0
	availableCenter := effectiveWidth - leftWidth - rightReserve - 8
	if availableCenter < 0 {
		availableCenter = 0
	}
	activityText := ""
	activityWidth := 0
	compactIdle := m.width < 100 || !infoPanelVisible
	activityKey := ""
	cacheActivity := false
	rawActivity := agent.AgentActivityEvent{}
	switch {
	case m.sessionSwitch.active():
		m.statusBarSyntheticConnectingLogKey = ""
		activityText = m.sessionSwitchStatusText(availableCenter)
	case statusActivity.Type == agent.ActivityCompacting:
		// Compaction is handled in the background lane, not the central lane
		// Show idle or fall through to check for other activities
		m.statusBarSyntheticConnectingLogKey = ""
		cacheActivity = true
		if ts, ok := m.latestStatusStartWall(statusActiveID); ok {
			activityKey = statusBarActivityKey("idle", availableCenter, compactIdle, ts, agent.AgentActivityEvent{}, time.Time{})
		} else {
			activityKey = statusBarActivityKey("empty", availableCenter, compactIdle, time.Time{}, agent.AgentActivityEvent{}, time.Time{})
		}
	case m.isFocusedAgentBusy():
		// inflightDraft makes us "busy" before AgentActivityEvent arrives; if the
		// activity update was dropped earlier, avoid showing idle + busy animation.
		sa := statusActivity
		if m.inflightDraftBelongsToAgent(statusActiveID) && (sa.Type == "" || sa.Type == agent.ActivityIdle) {
			logKey := statusActiveID + "|" + m.inflightDraft.ID
			if m.statusBarSyntheticConnectingLogKey != logKey {
				log.Debugf("tui status bar using synthetic connecting fallback agent_id=%v draft_id=%v draft_age=%v status_type=%v", statusActiveID, m.inflightDraft.ID, time.Since(m.inflightDraft.QueuedAt).Round(time.Millisecond), sa.Type)
				m.statusBarSyntheticConnectingLogKey = logKey
			}
			sa = agent.AgentActivityEvent{AgentID: statusActiveID, Type: agent.ActivityConnecting}
		} else {
			m.statusBarSyntheticConnectingLogKey = ""
		}
		rawActivity = sa
	case m.viewport != nil && m.viewport.HasUserLocalShellPending():
		m.statusBarSyntheticConnectingLogKey = ""
		activityText = m.renderStatusBarLocalShell(availableCenter)
	default:
		m.statusBarSyntheticConnectingLogKey = ""
		cacheActivity = true
		if ts, ok := m.latestStatusStartWall(statusActiveID); ok {
			activityKey = statusBarActivityKey("idle", availableCenter, compactIdle, ts, agent.AgentActivityEvent{}, time.Time{})
		} else {
			activityKey = statusBarActivityKey("empty", availableCenter, compactIdle, time.Time{}, agent.AgentActivityEvent{}, time.Time{})
		}
	}
	if cacheActivity {
		if m.cachedStatusBarActivityKey == activityKey {
			activityText = m.cachedStatusBarActivityText
			activityWidth = m.cachedStatusBarActivityWidth
		} else {
			if strings.HasPrefix(activityKey, "idle|") {
				if ts, ok := m.latestStatusStartWall(statusActiveID); ok {
					activityText = DimStyle.Render(statusBarIdleLabel(compactIdle) + ts.Format("15:04"))
				}
			}
			activityWidth = lipgloss.Width(activityText)
			m.cachedStatusBarActivityKey = activityKey
			m.cachedStatusBarActivityText = activityText
			m.cachedStatusBarActivityWidth = activityWidth
		}
	} else {
		if rawActivity.Type != "" {
			activityText = m.renderActivity(rawActivity, availableCenter)
		}
		activityWidth = lipgloss.Width(activityText)
	}

	rightKey := statusBarRightKey(effectiveWidth, leftWidth, activityWidth, pathValue, sessionValue)
	rightSide := ""
	rightStart := 0
	if m.cachedStatusBarRightKey == rightKey {
		rightSide = m.cachedStatusBarRightSide
		rightStart = m.cachedStatusBarRightStart
		m.statusPath.value = m.cachedStatusBarPathValue
		m.statusPath.display = m.cachedStatusBarPathShown
		m.statusSession.value = m.cachedStatusBarSessionValue
		m.statusSession.display = m.cachedStatusBarSessionShown
	} else {
		availableRight := effectiveWidth - leftWidth
		if activityWidth > 0 {
			centerStart := max((effectiveWidth-activityWidth)/2, leftWidth+2)
			centerEnd := centerStart + activityWidth
			rightFreeFromCenter := effectiveWidth - centerEnd
			if rightFreeFromCenter > 0 {
				availableRight = min(availableRight, rightFreeFromCenter)
			}
		}
		if availableRight > 0 {
			availableSession := availableRight
			if m.width < statusBarSessionMinVisibleCols {
				availableSession = 0
			}
			if pathValue != "" {
				if availableSession > statusBarSessionMinWidth+separatorWidth {
					availableSession -= statusBarSessionMinWidth + separatorWidth
				} else {
					availableSession = 0
				}
			}
			if sessionValue != "" {
				sessionLabel := "SID " + sessionValue
				if availableSession >= max(statusBarSessionMinWidth, len(sessionLabel)) {
					sessionDisplay := truncateMiddleDisplay(sessionLabel, availableSession)
					if sessionDisplay != "" {
						sessionText = StatusBarPathStyle.Render(sessionDisplay)
						m.statusSession.value = sessionValue
						m.statusSession.display = sessionDisplay
					}
				}
			}
			availablePath := availableRight
			if m.statusSession.display != "" {
				availablePath -= ansi.StringWidth(m.statusSession.display)
				if pathValue != "" {
					availablePath -= separatorWidth
				}
			}
			if pathValue != "" && availablePath > 0 {
				displayPath := truncateMiddleDisplay(pathValue, availablePath)
				if displayPath != "" {
					pathText = StatusBarPathStyle.Render(displayPath)
					m.statusPath.value = pathValue
					m.statusPath.display = displayPath
				}
			}
		}

		rightParts := make([]string, 0, 5)

		// Add compaction background pill (highest priority)
		if compactionPill := m.renderCompactionBackgroundPill(); compactionPill != "" {
			rightParts = append(rightParts, compactionPill)
		}

		if pathText != "" {
			if len(rightParts) > 0 {
				rightParts = append(rightParts, DimStyle.Render(statusBarActivityPathGap))
			}
			rightParts = append(rightParts, pathText)
		}
		if sessionText != "" {
			if len(rightParts) > 0 {
				rightParts = append(rightParts, DimStyle.Render(statusBarActivityPathGap))
			}
			rightParts = append(rightParts, sessionText)
		}
		rightSide = lipgloss.JoinHorizontal(lipgloss.Center, rightParts...)
		rightWidth := lipgloss.Width(rightSide)
		rightStart = effectiveWidth - rightWidth
		if rightStart < 0 {
			rightStart = 0
		}
		m.cachedStatusBarRightKey = rightKey
		m.cachedStatusBarRightSide = rightSide
		m.cachedStatusBarRightWidth = rightWidth
		m.cachedStatusBarRightStart = rightStart
		m.cachedStatusBarPathValue = m.statusPath.value
		m.cachedStatusBarPathShown = m.statusPath.display
		m.cachedStatusBarSessionValue = m.statusSession.value
		m.cachedStatusBarSessionShown = m.statusSession.display
	}
	rightWidth := m.cachedStatusBarRightWidth

	if activityWidth == 0 && leftWidth <= rightStart {
		if m.statusPath.display != "" {
			pathWidth := ansi.StringWidth(m.statusPath.display)
			m.statusPath.startX = statusBarLeftMargin + rightStart
			m.statusPath.endX = m.statusPath.startX + pathWidth
		}
		if m.statusSession.display != "" {
			sessionOffset := 0
			if m.statusPath.display != "" {
				sessionOffset = ansi.StringWidth(m.statusPath.display) + separatorWidth
			}
			sessionWidth := ansi.StringWidth(m.statusSession.display)
			m.statusSession.startX = statusBarLeftMargin + rightStart + sessionOffset
			m.statusSession.endX = m.statusSession.startX + sessionWidth
		}
		statusLine := leftSide + strings.Repeat(" ", max(rightStart-leftWidth, 0)) + rightSide
		if rightWidth == 0 && leftWidth < effectiveWidth {
			statusLine += strings.Repeat(" ", effectiveWidth-leftWidth)
		}
		padded := strings.Repeat(" ", statusBarLeftMargin) + statusLine + strings.Repeat(" ", statusBarRightMargin)
		return StatusBarStyle.Width(m.width).Render(padded)
	}

	if m.statusPath.display != "" {
		pathWidth := ansi.StringWidth(m.statusPath.display)
		m.statusPath.startX = statusBarLeftMargin + rightStart
		m.statusPath.endX = m.statusPath.startX + pathWidth
	}
	if m.statusSession.display != "" {
		sessionOffset := 0
		if m.statusPath.display != "" {
			sessionOffset = ansi.StringWidth(m.statusPath.display) + separatorWidth
		}
		sessionWidth := ansi.StringWidth(m.statusSession.display)
		m.statusSession.startX = statusBarLeftMargin + rightStart + sessionOffset
		m.statusSession.endX = m.statusSession.startX + sessionWidth
	}

	statusLine := renderStatusBarPlacedLine(leftSide, leftWidth, rightStart, rightSide, activityText, activityWidth, effectiveWidth)
	padded := strings.Repeat(" ", statusBarLeftMargin) + statusLine + strings.Repeat(" ", statusBarRightMargin)
	return StatusBarStyle.Width(m.width).Render(padded)
}

func renderStatusBarPlacedLine(leftSide string, leftWidth, rightStart int, rightSide string, activityText string, activityWidth, effectiveWidth int) string {
	if effectiveWidth <= 0 {
		return ""
	}
	activityStart := 0
	activityEnd := 0
	if activityText != "" {
		activityStart = (effectiveWidth - activityWidth) / 2
		minStart := leftWidth + 2
		maxStart := rightStart - activityWidth - 2
		if maxStart < minStart {
			maxStart = minStart
		}
		if activityStart < minStart {
			activityStart = minStart
		}
		if activityStart > maxStart {
			activityStart = maxStart
		}
		if activityStart < 0 {
			activityStart = 0
		}
		activityEnd = min(activityStart+activityWidth, effectiveWidth)
	}

	leftWidth = min(leftWidth, effectiveWidth)
	rightStart = max(0, min(rightStart, effectiveWidth))
	leftSeg := ansi.Cut(leftSide, 0, leftWidth)
	leftEnd := leftWidth
	rightFullWidth := max(0, effectiveWidth-rightStart)
	rightSeg := ansi.Cut(rightSide, 0, rightFullWidth)
	rightWidth := min(lipgloss.Width(rightSide), rightFullWidth)
	rightEnd := min(rightStart+rightWidth, effectiveWidth)

	if activityText != "" {
		if leftEnd > activityStart {
			leftEnd = activityStart
			leftSeg = ansi.Cut(leftSide, 0, leftEnd)
		}
		if rightStart < activityEnd {
			offset := activityEnd - rightStart
			rightSeg = ansi.Cut(rightSeg, offset, rightWidth-offset)
			rightStart = activityEnd
			rightWidth = max(0, rightWidth-offset)
			rightEnd = min(rightStart+rightWidth, effectiveWidth)
		}
	}

	segments := make([]statusBarPlacedSegment, 0, 3)
	if leftEnd > 0 && leftSeg != "" {
		segments = append(segments, statusBarPlacedSegment{start: 0, end: leftEnd, text: leftSeg})
	}
	if activityText != "" && activityEnd > activityStart {
		segments = append(segments, statusBarPlacedSegment{start: activityStart, end: activityEnd, text: activityText})
	}
	if rightEnd > rightStart && rightSeg != "" {
		segments = append(segments, statusBarPlacedSegment{start: rightStart, end: rightEnd, text: rightSeg})
	}
	if len(segments) == 0 {
		return strings.Repeat(" ", effectiveWidth)
	}

	var b strings.Builder
	b.Grow(effectiveWidth + len(leftSeg) + len(rightSeg) + len(activityText))
	cursor := 0
	for _, seg := range segments {
		if seg.start > cursor {
			writeStatusBarSpaces(&b, seg.start-cursor)
			cursor = seg.start
		}
		if seg.end <= cursor || seg.text == "" {
			continue
		}
		text := seg.text
		if seg.end-cursor < ansi.StringWidth(text) {
			text = ansi.Cut(text, 0, seg.end-cursor)
		}
		b.WriteString(text)
		cursor = seg.end
	}
	if cursor < effectiveWidth {
		writeStatusBarSpaces(&b, effectiveWidth-cursor)
	}
	return b.String()
}

func statusBarLeftPillsKey(modeText, viewingLabel, viewingColor string, extraPills []string) string {
	var b strings.Builder
	b.Grow(len(modeText) + len(viewingLabel) + len(viewingColor) + len(extraPills)*16)
	b.WriteString(modeText)
	b.WriteByte('|')
	b.WriteString(viewingLabel)
	b.WriteByte('|')
	b.WriteString(viewingColor)
	for _, pill := range extraPills {
		b.WriteByte('|')
		b.WriteString(pill)
	}
	return b.String()
}

func statusBarActivityKey(mode string, availableCenter int, compactIdle bool, anchorAt time.Time, activity agent.AgentActivityEvent, localShellStartedAt time.Time) string {
	var b strings.Builder
	b.Grow(96)
	b.WriteString(mode)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(availableCenter))
	b.WriteByte('|')
	if compactIdle {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
	b.WriteByte('|')
	if !anchorAt.IsZero() {
		b.WriteString(strconv.FormatInt(anchorAt.Unix(), 10))
	}
	b.WriteByte('|')
	b.WriteString(activity.AgentID)
	b.WriteByte('|')
	b.WriteString(string(activity.Type))
	b.WriteByte('|')
	b.WriteString(activity.Detail)
	b.WriteByte('|')
	if !localShellStartedAt.IsZero() {
		b.WriteString(strconv.FormatInt(localShellStartedAt.Unix(), 10))
	}
	return b.String()
}

func statusBarRightKey(effectiveWidth, leftWidth, activityWidth int, pathValue, sessionValue string) string {
	var b strings.Builder
	b.Grow(len(pathValue) + len(sessionValue) + 48)
	b.WriteString(strconv.Itoa(effectiveWidth))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(leftWidth))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(activityWidth))
	b.WriteByte('|')
	b.WriteString(pathValue)
	b.WriteByte('|')
	b.WriteString(sessionValue)
	return b.String()
}

// formatContextPill formats context usage for the status bar: "42% (72.7K)" or "(72.7K)" when limit is 0.
// Returns "" when current is 0 (including unknown limit) so the footer stays minimal in narrow layouts.
// renderContextPill renders the context pill with color by pressure: < 60% green, 60–85% yellow, > 85% red.
