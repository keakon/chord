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
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/modelref"
)

type statusBarPlacedSegment struct {
	start int
	end   int
	text  string
	// group marks the right-aligned group, whose written columns are recorded in
	// the status row's copy placement.
	group bool
}

func writeStatusBarSpaces(b *strings.Builder, count int) {
	for count > 0 {
		chunk := min(count, len(statusBarSpacePad))
		b.WriteString(statusBarSpacePad[:chunk])
		count -= chunk
	}
}

func (m *Model) statusBarDynamicCacheKeyFromState(now time.Time, localShellPending bool, progress string, compacting, busy, latestStatusStart bool) string {
	if localShellPending {
		return m.visualAnimationCacheKeyAt(now)
	}
	if progress != "" {
		return m.visualAnimationCacheKeyAt(now) + "|" + progress
	}
	if compacting {
		return compactionBackgroundStatusFrameKey(now)
	}
	if busy {
		return m.visualAnimationCacheKeyAt(now)
	}
	if latestStatusStart {
		return "min:" + strconv.FormatInt(now.Unix()/60, 10)
	}
	return ""
}

func (m *Model) visualAnimationCacheKeyAt(now time.Time) string {
	cadence := m.currentCadence()
	if cadence.visualAnimDelay <= 0 {
		return "sec:" + strconv.FormatInt(now.Unix(), 10)
	}
	return "frame:" + strconv.FormatInt(now.UnixMilli()/cadence.visualAnimDelay.Milliseconds(), 10)
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
	return pendingQuitFingerprintAt(m.quit.by, m.quit.at, now)
}

type statusBarAgentSnapshot struct {
	currentRole      string
	viewingLabel     string
	viewingColor     string
	sessionID        string
	modelRef         string
	selectedModelRef string
	nextModelRef     string
	modelVariant     string
	busy             bool
	proxyInUse       bool
	mcpPill          string
	tokenUsage       message.TokenUsage
	cost             float64
	contextCurrent   int
	contextLimit     int
	contextReminder  float64
	contextThreshold float64
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
	// WorkingDirDisplay is the checkout path a click copies. The path region
	// renders an identity label instead when a managed worktree is active
	// (WorkDirCheckoutName non-empty): the label names the checkout, while this
	// value keeps the real path for the clipboard.
	WorkingDirDisplay   string
	WorkDirRepoName     string
	WorkDirCheckoutName string
	PendingQuitFP       string
	ChordDisplay        string
	SearchFP            string
	NextEscHint         string
	LoopState           agent.LoopState
	YoloEnabled         bool
	MemoryEnabled       bool
	MemoryDegraded      bool
	PersistenceDegraded bool
	LoopIteration       int
	LoopMaxIterations   int
	ServiceTier         config.ServiceTier
	DynamicCacheKey     string
	InflightDraft       bool
	LocalShellPending   bool
	// RunningJobs and FallbackAgents back the narrow-layout activity pill. They
	// are only populated while the info panel is hidden, so a visible panel (and
	// its own AGENTS/JOBS sections) does not double-report the same counts.
	RunningJobs    int
	FallbackAgents int
	Width          int
	Height         int
	ViewportOffset int
}

func (m *Model) statusBarInputs(now time.Time) statusBarInputs {
	snap := m.statusBarSnapshot()
	statusActiveID := m.focusedAgentID
	if statusActiveID == "" {
		statusActiveID = "main"
	}
	localShellPending := m.viewport != nil && m.viewport.HasUserLocalShellPending()
	latestStatusStart := m.focusedAgentCanShowIdleSince()
	dynamicCacheKey := m.statusBarDynamicCacheKeyFromState(
		now,
		localShellPending,
		m.renderRequestProgressSummary(statusActiveID),
		m.activityForAgent(statusActiveID).Type == agent.ActivityCompacting,
		snap.busy,
		latestStatusStart,
	)
	loopState := agent.LoopState("")
	loopIteration := 0
	loopMaxIterations := 0
	memoryEnabled := false
	memoryDegraded := false
	if m.agent != nil {
		loopState = m.agent.CurrentLoopState()
		loopIteration = m.agent.CurrentLoopIteration()
		loopMaxIterations = m.agent.CurrentLoopMaxIterations()
		memoryEnabled = m.agent.MemoryEnabled()
		memoryDegraded = m.agent.MemoryDegraded()
	}
	infoPanelVisible := m.rightPanelVisible && m.mode != ModeHelp
	runningJobs := 0
	fallbackAgents := 0
	if !infoPanelVisible {
		runningJobs = len(m.activeJobs())
		fallbackAgents = m.activeSidebarWorkerCount()
	}
	return statusBarInputs{
		Now:                 now,
		ModeText:            m.statusBarModeText(),
		Snapshot:            snap,
		StatusActiveID:      statusActiveID,
		StatusActivity:      m.activityForAgent(statusActiveID),
		InfoPanelVisible:    infoPanelVisible,
		SessionSwitchKind:   m.sessionSwitch.kind,
		SessionSwitchID:     m.sessionSwitch.sessionID,
		WorkingDirDisplay:   displayWorkingDir(m.workingDir),
		WorkDirRepoName:     m.workDirRepoShortName(),
		WorkDirCheckoutName: m.workingDirID,
		PendingQuitFP:       strings.TrimSpace(m.pendingQuitFingerprint(now)),
		ChordDisplay:        m.chord.display(),
		SearchFP:            m.statusBarSearchFingerprint(),
		NextEscHint:         m.nextEscHint(),
		LoopState:           loopState,
		YoloEnabled:         m.yoloEnabled(),
		MemoryEnabled:       memoryEnabled,
		MemoryDegraded:      memoryDegraded,
		PersistenceDegraded: m.persistenceDegraded,
		LoopIteration:       loopIteration,
		LoopMaxIterations:   loopMaxIterations,
		ServiceTier:         m.effectiveServiceTier(),
		DynamicCacheKey:     dynamicCacheKey,
		InflightDraft:       m.inflightDraft != nil,
		LocalShellPending:   localShellPending,
		RunningJobs:         runningJobs,
		FallbackAgents:      fallbackAgents,
		Width:               m.width,
		Height:              m.height,
		ViewportOffset:      m.viewport.offset,
	}
}

func (m *Model) invalidateStatusBarAgentSnapshot() {
	m.statusBarAgentSnapshotDirty = true
}

func (m *Model) computeStatusBarCurrentAgentLabel(currentRole string) string {
	if m.focusedAgentID == "" {
		if r := strings.TrimSpace(currentRole); r != "" {
			return r
		}
		return "main"
	}
	return m.focusedAgentID
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
		snap.viewingLabel = m.computeStatusBarCurrentAgentLabel(snap.currentRole)
		snap.viewingColor = m.computeStatusBarCurrentAgentColor()
		if summary := m.agent.GetSessionSummary(); summary != nil {
			snap.sessionID = strings.TrimSpace(summary.ID)
		}
		modelState := m.focusedModelState()
		snap.modelRef, snap.selectedModelRef = m.focusedModelRefs()
		snap.busy = m.isFocusedAgentBusy()
		snap.nextModelRef = strings.TrimSpace(nextRequestModelRefForAgent(m.agent))
		if snap.nextModelRef == "" {
			snap.nextModelRef = snap.selectedModelRef
		}
		snap.modelRef = modelref.EnsureRefShowsProvider(snap.modelRef, snap.selectedModelRef)
		snap.modelVariant = modelState.Variant
		ref := snap.modelRef
		if !snap.busy {
			ref = snap.nextModelRef
		}
		if ref == "" {
			ref = snap.selectedModelRef
		}
		snap.proxyInUse = m.agent.ProxyInUseForRef(ref)
		// MCP pill intentionally omitted from the status bar: space is limited and MCP is not critical status info.
		// Users can view MCP details in the sidebar when needed.
		snap.tokenUsage = m.agent.GetTokenUsage()
		snap.cost = m.agent.GetSidebarUsageStats().EstimatedCost
		snap.contextCurrent, snap.contextLimit = m.agent.GetContextStats()
		snap.contextReminder, snap.contextThreshold = m.contextPressureLinesForFocusedModel()
	} else {
		snap.viewingLabel = m.computeStatusBarCurrentAgentLabel("")
		snap.viewingColor = m.computeStatusBarCurrentAgentColor()
	}
	m.statusBarAgentSnapshot = snap
	m.statusBarAgentSnapshotDirty = false
	return snap
}

func (m *Model) statusBarModePill(modeText string) string {
	modeStyle := ModeNormalStyle
	switch m.mode {
	case ModeInsert:
		modeStyle = ModeInsertStyle
	case ModeConfirm, ModeSessionDeleteConfirm, ModeStopJobConfirm:
		modeStyle = ModeConfirmStyle
	case ModeQuestion:
		modeStyle = ModeQuestionStyle
	case ModeSearch:
		modeStyle = ModeSearchStyle
	case ModeModelSelect:
		modeStyle = ModeModelSelectStyle
	}
	if m.cachedStatusBarModeKey == modeText {
		return m.cachedStatusBarModePill
	}
	modePill := modeStyle.Render(modeText)
	m.cachedStatusBarModeKey = modeText
	m.cachedStatusBarModePill = modePill
	return modePill
}

func (m *Model) statusBarViewingPill(viewingLabel, viewingColor string) string {
	viewingKey := viewingLabel + "|" + viewingColor
	if m.cachedStatusBarViewingKey == viewingKey {
		return m.cachedStatusBarViewingPill
	}
	viewingPill := renderStatusBarViewingPill(viewingLabel, viewingColor)
	m.cachedStatusBarViewingKey = viewingKey
	m.cachedStatusBarViewingPill = viewingPill
	return viewingPill
}

func (m *Model) appendStatusBarLoopPill(pills []string, inputs statusBarInputs) []string {
	if inputs.LoopState == "" {
		return pills
	}
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
	return append(pills, StatusHintStyle.Render(label))
}

func (m *Model) appendStatusBarYoloPill(pills []string, inputs statusBarInputs) []string {
	if !inputs.YoloEnabled {
		return pills
	}
	return append(pills, StatusHintStyle.Render("YOLO"))
}

func (m *Model) appendStatusBarMemoryPill(pills []string, inputs statusBarInputs) []string {
	if !inputs.MemoryEnabled && !inputs.MemoryDegraded {
		return pills
	}
	if inputs.MemoryDegraded {
		// Memory extraction is stalled: setup failed, or the last commit could
		// not proceed. Say so instead of implying a healthy region; the
		// already-indexed memory is still injected.
		return append(pills, ErrorStyle.Render("MEMORY-FAIL"))
	}
	return append(pills, StatusHintStyle.Render("MEMORY"))
}

func (m *Model) appendStatusBarPersistencePill(pills []string, inputs statusBarInputs) []string {
	if !inputs.PersistenceDegraded {
		return pills
	}
	return append(pills, ErrorStyle.Render("PERSIST-FAIL"))
}

func (m *Model) buildStatusBarLeadingPills(inputs statusBarInputs) []string {
	snap := inputs.Snapshot
	pills := []string{
		m.statusBarModePill(inputs.ModeText),
		m.statusBarViewingPill(snap.viewingLabel, snap.viewingColor),
	}
	return m.appendStatusBarPersistencePill(m.appendStatusBarMemoryPill(m.appendStatusBarYoloPill(m.appendStatusBarLoopPill(pills, inputs), inputs), inputs), inputs)
}

func (m *Model) statusBarSearchPill() string {
	if !m.search.State.Active || m.search.State.Query == "" {
		return ""
	}
	total := len(m.search.State.Matches)
	current := m.search.State.Current + 1
	if total == 0 {
		current = 0
	}
	return PillStyle.Render(fmt.Sprintf("/%s [%d/%d]", m.search.State.Query, current, total))
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
	case ModeRoleSelect:
		return "ROLE"
	case ModeMCPSelect:
		return "MCP"
	case ModeSkillSelect:
		return "SKILLS"
	case ModeSessionSelect:
		return "SESSION"
	case ModeSessionDeleteConfirm:
		return "DELETE"
	case ModeHandoffSelect:
		return "HANDOFF"
	case ModeUsageStats:
		return "STATS"
	case ModeErrorPanel:
		return "ERRORS"
	case ModeHelp:
		return "HELP"
	case ModeContentViewer:
		return "VIEW"
	case ModeImageViewer:
		return "IMAGE"
	case ModeRules:
		return "RULES"
	case ModeStopJobConfirm:
		return "STOP"
	case ModeJobsOverlay:
		return "JOBS"
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
	fmt.Fprintf(&b, "%d|%d|%d|%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%d|%d|%t|%t|%t|%t|%t|%s|%s|%s|%s|%s|%t|%t|%d|%d|%d|%d|%f|%d|%d|%f|%f|%t|%d|%d|%d|%d|%t",
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
		string(inputs.ServiceTier),
		inputs.DynamicCacheKey,
		inputs.LoopIteration,
		inputs.LoopMaxIterations,
		inputs.InfoPanelVisible,
		inputs.YoloEnabled,
		inputs.PersistenceDegraded,
		inputs.MemoryEnabled,
		inputs.MemoryDegraded,
		inputs.SessionSwitchKind,
		inputs.SessionSwitchID,
		inputs.WorkingDirDisplay,
		snap.sessionID,
		statusActivity.AgentID,
		inputs.InflightDraft,
		inputs.LocalShellPending,
		usage.InputTokens,
		usage.CacheWriteTokens,
		usage.OutputTokens,
		usage.ReasoningTokens,
		snap.cost,
		snap.contextCurrent,
		snap.contextLimit,
		snap.contextReminder,
		snap.contextThreshold,
		snap.proxyInUse,
		len(snap.modelRef),
		len(snap.selectedModelRef),
		len(snap.nextModelRef),
		len(snap.modelVariant),
		snap.busy,
	)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(inputs.RunningJobs))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(inputs.FallbackAgents))
	b.WriteByte('|')
	b.WriteString(string(statusActivity.Type))
	b.WriteByte('|')
	b.WriteString(statusActivity.Detail)
	b.WriteByte('|')
	b.WriteString(snap.modelRef)
	b.WriteByte('|')
	b.WriteString(snap.selectedModelRef)
	b.WriteByte('|')
	b.WriteString(snap.nextModelRef)
	b.WriteByte('|')
	b.WriteString(snap.modelVariant)
	b.WriteByte('|')
	b.WriteString(snap.mcpPill)
	b.WriteByte('|')
	b.WriteString(inputs.WorkDirRepoName)
	b.WriteByte('|')
	b.WriteString(inputs.WorkDirCheckoutName)
	b.WriteByte('|')
	b.WriteString(compactionBackgroundStatusKey(m.compactionBgStatus))
	if compactionBackgroundStatusVisibleAt(m.compactionBgStatus, now) {
		b.WriteByte('|')
		b.WriteString(compactionBackgroundStatusFrameKey(now))
	}
	return b.String()
}

func (m *Model) resetStatusBarCopyRegions() {
	m.statusPath.value = ""
	m.statusPath.display = ""
	m.statusPath.startX = 0
	m.statusPath.endX = 0
	m.statusSession.value = ""
	m.statusSession.display = ""
	m.statusSession.startX = 0
	m.statusSession.endX = 0
	m.statusJobs.runningJobs = 0
	m.statusJobs.fallbackAgents = 0
	m.statusJobs.display = ""
	m.statusJobs.startX = 0
	m.statusJobs.endX = 0
}

// renderStatusBar builds the bottom status line using pill-styled components.
func (m *Model) renderStatusBar() string {
	var pills []string
	quitHint := ""
	inputs := m.statusBarInputs(time.Now())
	snap := inputs.Snapshot
	m.resetStatusBarCopyRegions()

	// Exit confirmation hint: "press same key again to quit"
	if m.quit.by != "" && !m.quit.at.IsZero() && time.Since(m.quit.at) < pendingQuitWindow {
		hint := "Press q again to quit"
		if m.quit.by == "ctrl+c" {
			hint = "Press Ctrl+C again to quit"
		}
		quitHint = hint
	}

	pills = m.buildStatusBarLeadingPills(inputs)
	sessionID := snap.sessionID

	// Only show technical details in status bar if InfoPanel is HIDDEN.
	if !inputs.InfoPanelVisible {
		effW := max(m.width-statusBarLeftMargin-statusBarRightMargin, 0)
		var leftW int
		for _, p := range pills {
			leftW += lipgloss.Width(p)
		}
		pills = m.appendStatusBarModelPills(pills, snap, effW, leftW)
	}

	if searchPill := m.statusBarSearchPill(); searchPill != "" {
		pills = append(pills, searchPill)
	}

	leftPillsKey := statusBarLeftPillsKey(inputs.ModeText, snap.viewingLabel, snap.viewingColor, pills[2:])
	leftSide := ""
	leftWidth := 0
	if m.cachedStatusBarPillsKey == leftPillsKey {
		leftSide = m.cachedStatusBarLeftSide
		leftWidth = m.cachedStatusBarLeftW
	} else {
		leftSide = strings.Join(pills, " ")
		leftWidth = lipgloss.Width(leftSide)
		m.cachedStatusBarPillsKey = leftPillsKey
		m.cachedStatusBarLeftSide = leftSide
		m.cachedStatusBarLeftW = leftWidth
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
	// Content width: leave margins so the closing paren of elapsed "(Ns)" / "(NmNs)" is not covered by scrollbar.
	effectiveWidth := max(m.width-statusBarLeftMargin-statusBarRightMargin, 0)

	path := statusBarPathForms{value: inputs.WorkingDirDisplay, repo: inputs.WorkDirRepoName, checkout: inputs.WorkDirCheckoutName}
	sessionValue := sessionID
	activityText, activityWidth := m.renderStatusBarActivityLane(inputs, effectiveWidth, leftWidth)
	rightSide, rightStart, rightWidth := m.renderStatusBarRightSide(inputs.Now, effectiveWidth, leftWidth, activityWidth, path, sessionValue, inputs.RunningJobs, inputs.FallbackAgents)
	if inputs.NextEscHint != "" && statusBarCanFitEscHint(leftWidth, rightStart, activityWidth, effectiveWidth, inputs.NextEscHint) {
		leftSide = lipgloss.JoinHorizontal(
			lipgloss.Center,
			leftSide,
			DimStyle.Render("  ·  "),
			DimStyle.Render("esc ⇢ "+inputs.NextEscHint),
		)
		leftWidth = lipgloss.Width(leftSide)
		activityText, activityWidth = m.renderStatusBarActivityLane(inputs, effectiveWidth, leftWidth)
		rightSide, rightStart, rightWidth = m.renderStatusBarRightSide(inputs.Now, effectiveWidth, leftWidth, activityWidth, path, sessionValue, inputs.RunningJobs, inputs.FallbackAgents)
	}
	if activityWidth == 0 && leftWidth <= rightStart {
		// No lane to cut into: the group is drawn whole at rightStart, so only
		// the row's right edge can clip it.
		visible := min(rightWidth, max(effectiveWidth-rightStart, 0))
		placement := statusBarRightPlacement{rowStart: rightStart, groupEnd: visible, drawn: visible > 0}
		m.placeStatusBarRightRegions(placement, m.cachedStatusBarRightOffsets)
		statusLine := leftSide + strings.Repeat(" ", max(rightStart-leftWidth, 0)) + rightSide
		if rightWidth == 0 && leftWidth < effectiveWidth {
			statusLine += strings.Repeat(" ", effectiveWidth-leftWidth)
		}
		padded := strings.Repeat(" ", statusBarLeftMargin) + statusLine + strings.Repeat(" ", statusBarRightMargin)
		return m.renderStatusBarLine(padded)
	}

	statusLine, placement := renderStatusBarPlacedLine(leftSide, leftWidth, rightStart, rightSide, activityText, activityWidth, effectiveWidth)
	m.placeStatusBarRightRegions(placement, m.cachedStatusBarRightOffsets)
	padded := strings.Repeat(" ", statusBarLeftMargin) + statusLine + strings.Repeat(" ", statusBarRightMargin)
	return m.renderStatusBarLine(padded)
}

// statusBarRightOffsets records where each right-side member starts inside the
// assembled group, counted in display columns from the group's left edge. The
// compaction pill is prepended ahead of path/session, so every member after it
// is offset by the running total rather than by the group start alone. A
// negative offset marks a member that was not composed in this pass.
type statusBarRightOffsets struct {
	path    int
	session int
	jobs    int
}

// placeStatusBarRightRegions converts the group-relative member offsets into the
// absolute clickable columns of the status row, which is padded by
// statusBarLeftMargin before the group's left edge. Members the row did not draw
// get an empty region, so a click can never land on a copy target the line does
// not show.
func (m *Model) placeStatusBarRightRegions(placement statusBarRightPlacement, offsets statusBarRightOffsets) {
	if !placement.drawn {
		return
	}
	m.statusPath.startX, m.statusPath.endX = placement.region(offsets.path, m.statusPath.display)
	m.statusSession.startX, m.statusSession.endX = placement.region(offsets.session, m.statusSession.display)
	m.statusJobs.startX, m.statusJobs.endX = placement.region(offsets.jobs, m.statusJobs.display)
}

// renderStatusBarLine renders the assembled status row within drawableLineWidth,
// leaving the terminal's last physical column unwritten. Truncating up front
// keeps the row on a single line: StatusBarStyle.Width wraps (rather than
// truncates) overlong content, and the wrapped tail would land on a second line.
func (m *Model) renderStatusBarLine(padded string) string {
	width := m.drawableLineWidth()
	return StatusBarStyle.Width(width).Render(ansi.Truncate(padded, width, ""))
}

func statusBarCanFitEscHint(leftWidth, rightStart, activityWidth, effectiveWidth int, hint string) bool {
	escWidth := lipgloss.Width(DimStyle.Render("  ·  ")) + lipgloss.Width(DimStyle.Render("esc ⇢ "+hint))
	leftWithEsc := leftWidth + escWidth
	if activityWidth == 0 {
		return leftWithEsc <= rightStart
	}
	activityStart, activityEnd := statusBarActivitySpan(leftWidth, rightStart, activityWidth, effectiveWidth)
	return leftWithEsc <= activityStart && rightStart >= activityEnd
}

func statusBarActivitySpan(leftWidth, rightStart, activityWidth, effectiveWidth int) (int, int) {
	if effectiveWidth <= 0 || activityWidth <= 0 {
		return 0, 0
	}
	activityStart := (effectiveWidth - activityWidth) / 2
	minStart := leftWidth + 2
	maxStart := max(rightStart-activityWidth-2, minStart)
	if activityStart < minStart {
		activityStart = minStart
	}
	if activityStart > maxStart {
		activityStart = maxStart
	}
	if activityStart < 0 {
		activityStart = 0
	}
	activityEnd := min(activityStart+activityWidth, effectiveWidth)
	return activityStart, activityEnd
}

func (m *Model) renderStatusBarActivityLane(inputs statusBarInputs, effectiveWidth, leftWidth int) (string, int) {
	statusActiveID := inputs.StatusActiveID
	statusActivity := inputs.StatusActivity
	availableCenter := max(effectiveWidth-leftWidth-8, 0)
	activityText := ""
	activityWidth := 0
	compactIdle := m.width < 100 || !inputs.InfoPanelVisible
	activityKey := ""
	cacheActivity := false
	rawActivity := agent.AgentActivityEvent{}
	switch {
	case m.sessionSwitch.active():
		m.statusBarSyntheticConnectingLogKey = ""
		activityText = m.sessionSwitchStatusText(availableCenter)
	case statusActivity.Type == agent.ActivityCompacting:
		m.statusBarSyntheticConnectingLogKey = ""
		cacheActivity = true
		// Compaction is not idle. Its progress is rendered in the background
		// pill; never reuse the idle "Since" lane while it runs.
		activityKey = statusBarActivityKey("empty", availableCenter, compactIdle, time.Time{}, agent.AgentActivityEvent{})
	case m.isFocusedAgentBusy():
		sa := statusActivity
		if sa.Type == "" || sa.Type == agent.ActivityIdle {
			if m.inflightDraftBelongsToAgent(statusActiveID) {
				logKey := statusActiveID + "|" + m.inflightDraft.ID
				if m.statusBarSyntheticConnectingLogKey != logKey {
					log.Debugf("tui status bar using synthetic connecting fallback agent_id=%v draft_id=%v draft_age=%v status_type=%v", statusActiveID, m.inflightDraft.ID, time.Since(m.inflightDraft.QueuedAt).Round(time.Millisecond), sa.Type)
					m.statusBarSyntheticConnectingLogKey = logKey
				}
			} else {
				m.statusBarSyntheticConnectingLogKey = ""
			}
			sa = agent.AgentActivityEvent{AgentID: statusActiveID, Type: agent.ActivityConnecting}
		} else {
			m.statusBarSyntheticConnectingLogKey = ""
		}
		rawActivity = sa
	case m.viewport != nil && m.viewport.HasUserLocalShellPending():
		m.statusBarSyntheticConnectingLogKey = ""
		activityText = m.renderStatusBarLocalShell(availableCenter)
	case (statusActivity.Type == "" || statusActivity.Type == agent.ActivityIdle) && m.focusedAgentCanShowIdleSince():
		m.statusBarSyntheticConnectingLogKey = ""
		cacheActivity = true
		if ts, ok := m.latestStatusStartWall(statusActiveID); ok {
			activityKey = statusBarActivityKey("idle", availableCenter, compactIdle, ts, agent.AgentActivityEvent{})
		} else {
			activityKey = statusBarActivityKey("empty", availableCenter, compactIdle, time.Time{}, agent.AgentActivityEvent{})
		}
	default:
		m.statusBarSyntheticConnectingLogKey = ""
		cacheActivity = true
		activityKey = statusBarActivityKey("empty", availableCenter, compactIdle, time.Time{}, agent.AgentActivityEvent{})
	}
	if cacheActivity {
		if m.cachedStatusBarActivityKey == activityKey {
			activityText = m.cachedStatusBarActivityText
			activityWidth = m.cachedStatusBarActivityWidth
		} else {
			if strings.HasPrefix(activityKey, "idle|") {
				if ts, ok := m.latestStatusStartWall(statusActiveID); ok {
					activityText = DimStyle.Render(statusBarIdleLabel() + ts.Format("15:04"))
				}
			}
			activityWidth = lipgloss.Width(activityText)
			m.cachedStatusBarActivityKey = activityKey
			m.cachedStatusBarActivityText = activityText
			m.cachedStatusBarActivityWidth = activityWidth
		}
	} else {
		if rawActivity.Type != "" {
			activityText = m.renderActivityAt(rawActivity, availableCenter, inputs.Now)
		}
		activityWidth = lipgloss.Width(activityText)
	}
	return activityText, activityWidth
}

func (m *Model) renderStatusBarRightSide(now time.Time, effectiveWidth, leftWidth, activityWidth int, path statusBarPathForms, sessionValue string, runningJobs, fallbackAgents int) (string, int, int) {
	separatorWidth := lipgloss.Width(DimStyle.Render(statusBarActivityPathGap))
	rightKey := statusBarRightKey(effectiveWidth, leftWidth, activityWidth, path, sessionValue, runningJobs, fallbackAgents)
	if !compactionBackgroundStatusVisibleAt(m.compactionBgStatus, now) {
		rightKey += "|"
	} else {
		rightKey += "|" + compactionBackgroundStatusKey(m.compactionBgStatus) + "|" + compactionBackgroundStatusFrameKey(now)
	}
	if m.cachedStatusBarRightKey == rightKey {
		m.statusPath.value = m.cachedStatusBarPathValue
		m.statusPath.display = m.cachedStatusBarPathShown
		m.statusSession.value = m.cachedStatusBarSessionValue
		m.statusSession.display = m.cachedStatusBarSessionShown
		m.statusJobs.display = m.cachedStatusJobsDisplay
		m.statusJobs.runningJobs = m.cachedStatusJobsRunning
		m.statusJobs.fallbackAgents = m.cachedStatusJobsAgents
		return m.cachedStatusBarRightSide, m.cachedStatusBarRightStart, m.cachedStatusBarRightWidth
	}

	rightSide := ""
	rightStart := 0
	pathText := ""
	sessionText := ""
	jobsPillText := ""
	availableRight := effectiveWidth - leftWidth
	if activityWidth > 0 {
		centerStart := max((effectiveWidth-activityWidth)/2, leftWidth+2)
		centerEnd := centerStart + activityWidth
		rightFreeFromCenter := effectiveWidth - centerEnd
		if rightFreeFromCenter > 0 {
			availableRight = min(availableRight, rightFreeFromCenter)
		}
	}
	// Reserve the compaction indicator's width before budgeting path/session.
	// The right-aligned group is placed with its left edge against the centered
	// activity lane; without a reservation the pill (its leftmost member) is the
	// first thing sliced off when a foreground request owns the lane and the
	// terminal is narrow, making a long-running compaction look completely
	// absent from the status bar.
	compactionPill := m.renderCompactionBackgroundPill(now)
	if compactionPill != "" {
		availableRight -= lipgloss.Width(compactionPill) + separatorWidth
		if availableRight < 0 {
			availableRight = 0
		}
	}
	// The background-activity fallback pill takes the next reservation (it
	// yields to the compaction indicator): its label shortens to whatever space
	// is left, keeping jobs before agents, and disappears when even the shortest
	// form does not fit.
	if runningJobs > 0 || fallbackAgents > 0 {
		candidate := formatJobsActivityPill(runningJobs, fallbackAgents, max(availableRight-separatorWidth, 0))
		if candidate != "" {
			jobsPillText = candidate
			availableRight -= separatorWidth + ansi.StringWidth(candidate)
			if availableRight < 0 {
				availableRight = 0
			}
		}
	}
	if availableRight > 0 {
		availableSession := availableRight
		if m.width < statusBarSessionMinVisibleCols {
			availableSession = 0
		}
		if path.value != "" {
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
			if path.value != "" {
				availablePath -= separatorWidth
			}
		}
		if displayPath := path.display(availablePath); displayPath != "" {
			pathText = StatusBarPathStyle.Render(displayPath)
			m.statusPath.value = path.value
			m.statusPath.display = displayPath
		}
	}

	// Member offsets are captured while composing the group, because the
	// clickable regions must follow the rendered order. Deriving them after the
	// fact from the group's left edge missed any leading member: with the
	// compaction pill present, path and session were recorded one pill width too
	// far left, so a double click on the visible session ID hit nothing (or its
	// left neighbour).
	rightParts := make([]string, 0, 6)
	offset := 0
	appendPart := func(text string) int {
		if len(rightParts) > 0 {
			rightParts = append(rightParts, DimStyle.Render(statusBarActivityPathGap))
			offset += separatorWidth
		}
		start := offset
		rightParts = append(rightParts, text)
		offset += lipgloss.Width(text)
		return start
	}
	offsets := statusBarRightOffsets{path: -1, session: -1, jobs: -1}
	if compactionPill != "" {
		appendPart(compactionPill)
	}
	if pathText != "" {
		offsets.path = appendPart(pathText)
	}
	if sessionText != "" {
		offsets.session = appendPart(sessionText)
	}
	if jobsPillText != "" {
		offsets.jobs = appendPart(StatusHintStyle.Render(jobsPillText))
		m.statusJobs.display = jobsPillText
		m.statusJobs.runningJobs = runningJobs
		m.statusJobs.fallbackAgents = fallbackAgents
	}
	rightSide = lipgloss.JoinHorizontal(lipgloss.Center, rightParts...)
	rightWidth := lipgloss.Width(rightSide)
	rightStart = max(effectiveWidth-rightWidth, 0)
	m.cachedStatusBarRightKey = rightKey
	m.cachedStatusBarRightSide = rightSide
	m.cachedStatusBarRightWidth = rightWidth
	m.cachedStatusBarRightStart = rightStart
	m.cachedStatusBarRightOffsets = offsets
	m.cachedStatusBarPathValue = m.statusPath.value
	m.cachedStatusBarPathShown = m.statusPath.display
	m.cachedStatusBarSessionValue = m.statusSession.value
	m.cachedStatusBarSessionShown = m.statusSession.display
	m.cachedStatusJobsDisplay = m.statusJobs.display
	m.cachedStatusJobsRunning = m.statusJobs.runningJobs
	m.cachedStatusJobsAgents = m.statusJobs.fallbackAgents
	return rightSide, rightStart, rightWidth
}

// statusBarRightPlacement reports the geometry the placed status row gave the
// right-aligned group: the group columns that reached the row are
// [groupStart, groupEnd), and they were written starting at row column rowStart.
// The activity lane replaces the group's leading columns in place and the row's
// right edge can clip its tail, so both ends of the group can be missing from
// the row. drawn tells whether any of the group was drawn at all.
type statusBarRightPlacement struct {
	rowStart   int
	groupStart int
	groupEnd   int
	drawn      bool
}

// region returns the row columns a member occupies, given its offset inside the
// group and its display text. The member keeps the intersection of its own
// columns with the group columns the row actually shows.
func (p statusBarRightPlacement) region(offset int, display string) (int, int) {
	if offset < 0 || display == "" {
		return 0, 0
	}
	lo := max(offset, p.groupStart)
	hi := min(offset+ansi.StringWidth(display), p.groupEnd)
	if hi <= lo {
		return 0, 0
	}
	return statusBarLeftMargin + p.rowStart + lo - p.groupStart, statusBarLeftMargin + p.rowStart + hi - p.groupStart
}

func renderStatusBarPlacedLine(leftSide string, leftWidth, rightStart int, rightSide string, activityText string, activityWidth, effectiveWidth int) (string, statusBarRightPlacement) {
	if effectiveWidth <= 0 {
		return "", statusBarRightPlacement{}
	}
	activityStart := 0
	activityEnd := 0
	if activityText != "" {
		activityStart, activityEnd = statusBarActivitySpan(leftWidth, rightStart, activityWidth, effectiveWidth)
	}

	leftWidth = min(leftWidth, effectiveWidth)
	rightStart = max(0, min(rightStart, effectiveWidth))
	leftSeg := ansi.Cut(leftSide, 0, leftWidth)
	leftEnd := leftWidth
	rightFullWidth := max(0, effectiveWidth-rightStart)
	rightSeg := ansi.Cut(rightSide, 0, rightFullWidth)
	rightWidth := min(lipgloss.Width(rightSide), rightFullWidth)
	rightEnd := min(rightStart+rightWidth, effectiveWidth)
	// groupStart is the group column the placed group begins at: the activity
	// lane replaces the group's leading columns in place, so the columns it ate
	// stay skipped instead of shifting the members behind them.
	groupStart := 0
	placement := statusBarRightPlacement{}

	if activityText != "" {
		if leftEnd > activityStart {
			leftEnd = activityStart
			leftSeg = ansi.Cut(leftSide, 0, leftEnd)
		}
		if rightStart < activityEnd {
			groupStart = activityEnd - rightStart
			rightSeg = ansi.Cut(rightSeg, groupStart, rightWidth)
			rightStart = activityEnd
			rightWidth = max(0, rightWidth-groupStart)
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
	// The group is drawn by the same writer that lays out everything before it:
	// it starts at the cursor those segments left behind and the row's right edge
	// clips its tail.
	if rightSeg != "" && rightEnd > rightStart {
		segments = append(segments, statusBarPlacedSegment{start: rightStart, end: rightEnd, text: rightSeg, group: true})
	}
	if len(segments) == 0 {
		return strings.Repeat(" ", effectiveWidth), placement
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
		if seg.group {
			// What the writer wrote is the placement: the group columns it shows
			// are [groupStart, groupStart+width) and they landed at the cursor the
			// segments before it left behind.
			placement = statusBarRightPlacement{
				rowStart:   cursor,
				groupStart: groupStart,
				groupEnd:   groupStart + ansi.StringWidth(text),
				drawn:      true,
			}
		}
		b.WriteString(text)
		cursor = seg.end
	}
	if cursor < effectiveWidth {
		writeStatusBarSpaces(&b, effectiveWidth-cursor)
	}
	return b.String(), placement
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

func statusBarActivityKey(mode string, availableCenter int, compactIdle bool, anchorAt time.Time, activity agent.AgentActivityEvent) string {
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
	return b.String()
}

func statusBarRightKey(effectiveWidth, leftWidth, activityWidth int, path statusBarPathForms, sessionValue string, runningJobs, fallbackAgents int) string {
	var b strings.Builder
	b.Grow(len(path.value) + len(path.repo) + len(path.checkout) + len(sessionValue) + 64)
	b.WriteString(strconv.Itoa(effectiveWidth))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(leftWidth))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(activityWidth))
	b.WriteByte('|')
	b.WriteString(path.value)
	b.WriteByte('|')
	b.WriteString(path.repo)
	b.WriteByte('|')
	b.WriteString(path.checkout)
	b.WriteByte('|')
	b.WriteString(sessionValue)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(runningJobs))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(fallbackAgents))
	return b.String()
}

// compactionBackgroundStatusKey folds the mutable compaction background state
// into the right-side cache key so a streaming progress update (bytes/events)
// invalidates the cached pill instead of rendering a stale suffix.
func compactionBackgroundStatusKey(s compactionBackgroundStatus) string {
	if !s.Active && s.Terminal == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(s.Terminal)
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(s.Bytes, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(s.Events, 10))
	return b.String()
}

func compactionBackgroundStatusFrameKey(now time.Time) string {
	return strconv.FormatInt(now.UnixMilli()/compactionPillBreathPhase.Milliseconds(), 10)
}

// formatContextPill formats input-budget usage for the status bar: "42% (72.7K)" or "(72.7K)" when limit is 0.
// Returns "" when current is 0 (including unknown limit) so the footer stays minimal in narrow layouts.
// renderContextPill colors the pill from the agent's pressure lines: green below
// the reminder, yellow from the reminder to the auto-compaction threshold, red
// at the threshold (fixed 50/80% when no lines are configured).
