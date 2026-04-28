package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
)

// infoPanelFingerprint builds a cheap string key over the data that renderInfoPanel reads.
// If this string is unchanged, the rendered output is identical — return the cached result.
func (m *Model) infoPanelFingerprint(width, height int) string {
	var b strings.Builder
	appendInt := func(v int) {
		b.WriteString(strconv.Itoa(v))
	}
	appendInt64 := func(v int64) {
		b.WriteString(strconv.FormatInt(v, 10))
	}
	appendFloat6 := func(v float64) {
		b.WriteString(strconv.FormatFloat(v, 'f', 6, 64))
	}
	appendBool := func(v bool) {
		if v {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	appendSep := func() {
		b.WriteByte('|')
	}

	b.Grow(256)
	appendInt(width)
	appendSep()
	appendInt(height)
	appendSep()

	// Model ref / variant / key stats
	keysConfirmed, keysTotal := m.agent.KeyStats()
	b.WriteString(m.agent.RunningModelRef())
	appendSep()
	b.WriteString(m.agent.RunningVariant())
	appendSep()
	appendInt(keysConfirmed)
	appendSep()
	appendInt(keysTotal)
	appendSep()

	// Rate limit snapshot (1-second bucket for countdown display)
	snap := m.agent.CurrentRateLimitSnapshot()
	if snap != nil {
		ts := time.Now().Unix() // 1-second granularity matches displayed precision
		if snap.Primary != nil {
			b.WriteByte('P')
			appendFloat6(snap.Primary.UsedPercent())
			appendSep()
			appendInt64(snap.Primary.WindowMinutes)
			appendSep()
			appendInt64(snap.Primary.ResetsAt.Unix())
			appendSep()
			appendInt64(ts)
			appendSep()
		}
		if snap.Secondary != nil {
			b.WriteByte('S')
			appendFloat6(snap.Secondary.UsedPercent())
			appendSep()
			appendInt64(snap.Secondary.WindowMinutes)
			appendSep()
			appendInt64(snap.Secondary.ResetsAt.Unix())
			appendSep()
			appendInt64(ts)
			appendSep()
		}
	}

	// Context / token / cost
	cur, limit := m.agent.GetContextStats()
	msgCount := m.agent.GetContextMessageCount()
	stats := m.agent.GetSidebarUsageStats()
	appendInt(cur)
	appendSep()
	appendInt(limit)
	appendSep()
	appendInt(msgCount)
	appendSep()
	appendInt64(stats.InputTokens)
	appendSep()
	appendInt64(stats.OutputTokens)
	appendSep()
	appendInt64(stats.CacheReadTokens)
	appendSep()
	appendInt64(stats.CacheWriteTokens)
	appendSep()
	appendFloat6(stats.EstimatedCost)
	appendSep()

	// LSP rows
	if lp, ok := m.agent.(agent.LSPStateProvider); ok {
		for _, r := range lp.LSPServerList() {
			b.WriteByte('L')
			b.WriteString(r.Name)
			appendBool(r.OK)
			appendBool(r.Pending)
			appendInt(r.Errors)
			appendInt(r.Warnings)
			b.WriteString(r.Err)
			appendSep()
		}
	}
	appendBool(m.isInfoPanelSectionCollapsed(infoPanelSectionLSP))
	appendSep()
	// MCP rows
	if mp, ok := m.agent.(agent.MCPStateProvider); ok {
		for _, r := range mp.MCPServerList() {
			b.WriteByte('M')
			b.WriteString(r.Name)
			appendBool(r.OK)
			appendBool(r.Pending)
			appendBool(r.Retrying)
			appendInt(r.Attempt)
			appendInt(r.MaxAttempts)
			b.WriteString(r.Err)
			appendSep()
		}
	}
	appendBool(m.isInfoPanelSectionCollapsed(infoPanelSectionMCP))
	appendSep()

	// TODOs
	for _, t := range m.agent.GetTodos() {
		b.WriteByte('T')
		b.WriteString(t.Status)
		b.WriteString(t.Content)
		appendSep()
	}
	appendBool(m.isInfoPanelSectionCollapsed(infoPanelSectionTodos))
	appendSep()

	// Invoked skills
	if sp, ok := m.agent.(agent.SkillsStateProvider); ok {
		for _, sk := range sp.ListSkills() {
			if sk == nil {
				continue
			}
			b.WriteByte('K')
			b.WriteString(sk.Name)
			b.WriteString(sk.Description)
			appendSep()
		}
	}
	for _, sk := range m.agent.InvokedSkills() {
		b.WriteByte('S')
		b.WriteString(sk.Name)
		b.WriteString(sk.Description)
		appendBool(sk.Discovered)
		appendSep()
	}
	appendBool(m.isInfoPanelSectionCollapsed(infoPanelSectionSkills))
	appendSep()

	// Edited files
	for _, fe := range m.sidebar.CurrentAgentFiles() {
		b.WriteByte('F')
		b.WriteString(fe.Path)
		appendInt(fe.Added)
		appendInt(fe.Removed)
		appendSep()
	}
	appendBool(m.isInfoPanelSectionCollapsed(infoPanelSectionFiles))
	appendSep()

	// Agents sidebar (use raw entry data, not rendered lines)
	b.WriteString(m.focusedAgentIDOrMain())
	appendSep()
	for _, e := range m.sidebar.Agents() {
		b.WriteByte('A')
		b.WriteString(e.ID)
		b.WriteString(e.AgentDefName)
		b.WriteString(e.TaskDesc)
		b.WriteString(e.Status)
		b.WriteString(e.Color)
		b.WriteString(e.SelectedRef)
		b.WriteString(e.RunningRef)
		b.WriteString(e.Activity)
		b.WriteString(e.LastSummary)
		appendInt(e.UrgentCount)
		b.WriteString(e.LastArtifactRelPath)
		b.WriteString(e.LastArtifactType)
		appendSep()
	}
	if pending := m.sidebar.PendingTasks(); pending > 0 {
		b.WriteString("PT")
		appendInt(pending)
		appendSep()
	}
	appendBool(m.isInfoPanelSectionCollapsed(infoPanelSectionAgents))
	appendSep()

	return b.String()
}

func (m *Model) renderInfoPanel(width int, height int) string {
	if width <= 0 {
		return ""
	}

	// Fingerprint-based cache: skip re-render when inputs are unchanged.
	if fp := m.infoPanelFingerprint(width, height); fp == m.cachedInfoPanelFP &&
		m.cachedInfoPanelW == width && m.cachedInfoPanelH == height {
		return m.cachedInfoPanelOut
	} else {
		m.cachedInfoPanelFP = fp
		m.cachedInfoPanelW = width
		m.cachedInfoPanelH = height
		// (cachedInfoPanelOut set at the end)
	}

	lineW := width - 2
	sep := "\n" + InfoPanelLineBg.Width(lineW).Render("") + "\n"
	m.beginInfoPanelRenderPass()
	blockParts := make([]string, 0, 7)
	appendBlock := func(section infoPanelSectionID, block string) {
		if block == "" {
			return
		}
		if len(blockParts) > 0 {
			blockParts = append(blockParts, sep)
		}
		blockParts = append(blockParts, block)
		m.recordInfoPanelSectionHitBox(section, block)
	}

	appendBlock("", m.buildInfoPanelModelBlock(lineW))
	if snap := m.agent.CurrentRateLimitSnapshot(); snap != nil {
		appendBlock("", m.renderRateLimitBlock(snap, lineW))
	}
	appendBlock("", m.buildInfoPanelUsageBlock(width, lineW))
	appendBlock(infoPanelSectionLSP, m.buildInfoPanelLSPBlock(lineW))
	appendBlock(infoPanelSectionMCP, m.buildInfoPanelMCPBlock(lineW))
	appendBlock(infoPanelSectionTodos, m.buildInfoPanelTodoBlock(lineW))
	appendBlock(infoPanelSectionSkills, m.buildInfoPanelSkillsBlock(lineW))
	appendBlock(infoPanelSectionFiles, m.buildInfoPanelFilesBlock(lineW))
	if agentBlock, agentRows := m.buildInfoPanelAgentListBlockWithHits(lineW); agentBlock != "" {
		baseY := m.infoPanelRenderCursorY
		appendBlock(infoPanelSectionAgents, agentBlock)
		for _, hit := range agentRows {
			m.recordInfoPanelAgentHitBox(hit.agentID, baseY+hit.startLine, baseY+hit.endLine)
		}
	}

	var finalContent string
	switch len(blockParts) {
	case 0:
		finalContent = ""
	case 1:
		finalContent = blockParts[0]
	default:
		var sb strings.Builder
		for _, part := range blockParts {
			sb.WriteString(part)
		}
		finalContent = sb.String()
	}
	out := InfoPanelStyle.
		Width(width).
		Height(height).
		Render(finalContent)
	m.cachedInfoPanelOut = out
	return out
}
