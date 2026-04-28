package tui

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// syncVisibleMainUserBlockMsgIndexes reconciles MsgIndex for ordinary main-agent
// user blocks against the current transcript without reordering blocks that
// already match a committed message. This keeps startup-deferred/windowed
// transcripts stable while still letting optimistic user blocks become forkable
// once the backend has committed them to ctxMgr/recovery.
func (m *Model) syncVisibleMainUserBlockMsgIndexes() {
	if m.agent == nil || m.focusedAgentID != "" || m.viewport == nil {
		return
	}
	msgs := m.agent.GetMessages()
	if len(msgs) == 0 {
		return
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		return
	}

	used := make(map[int]struct{}, len(blocks))
	for _, block := range blocks {
		if !mainUserBlockMsgIndexMatches(block, msgs) {
			continue
		}
		used[block.MsgIndex] = struct{}{}
	}

	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block == nil || block.Type != BlockUser || block.IsUserLocalShell() {
			continue
		}
		if mainUserBlockMsgIndexMatches(block, msgs) {
			continue
		}
		if msgIdx, ok := findMatchingMainUserMsgIndex(msgs, block, used); ok {
			block.MsgIndex = msgIdx
			used[msgIdx] = struct{}{}
		}
	}
}

func mainUserBlockMsgIndexMatches(block *Block, msgs []message.Message) bool {
	if block == nil || block.Type != BlockUser || block.IsUserLocalShell() {
		return false
	}
	if block.MsgIndex < 0 || block.MsgIndex >= len(msgs) {
		return false
	}
	msg := msgs[block.MsgIndex]
	if msg.Role != "user" || msg.IsCompactionSummary {
		return false
	}
	return strings.TrimSpace(message.UserPromptPlainText(msg)) == strings.TrimSpace(block.Content)
}

func findMatchingMainUserMsgIndex(msgs []message.Message, block *Block, used map[int]struct{}) (int, bool) {
	if block == nil {
		return 0, false
	}
	target := strings.TrimSpace(block.Content)
	if target == "" {
		return 0, false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if _, ok := used[i]; ok {
			continue
		}
		msg := msgs[i]
		if msg.Role != "user" || msg.IsCompactionSummary {
			continue
		}
		if strings.TrimSpace(message.UserPromptPlainText(msg)) != target {
			continue
		}
		return i, true
	}
	return 0, false
}

// rebuildViewportFromMessages replaces all viewport blocks with blocks built
// from the agent's current message list. Used after SessionRestoredEvent so
// the restored conversation is visible.
func (m *Model) rebuildViewportFromMessages() {
	m.rebuildViewportFromMessagesWithReason("unspecified")
}

func (m *Model) rebuildViewportFromMessagesWithReason(reason string) {
	if m.agent == nil {
		return
	}
	rebuildStarted := time.Now()
	m.finalizeTurn()
	m.attachments = nil
	m.queuedDrafts = nil
	m.agentComposerStates = nil
	m.editingQueuedDraftID = ""
	m.inflightDraft = nil
	m.currentAssistantBlock = nil
	m.assistantBlockAppended = false
	m.resetTimingStateForSessionRestore()
	m.closeAtMention()
	messagesStarted := time.Now()
	msgs := m.agent.GetMessages()
	messagesDuration := time.Since(messagesStarted)
	if len(msgs) == 0 {
		m.viewport.sticky = true
		replaceStarted := time.Now()
		m.viewport.ReplaceBlocks(nil)
		replaceDuration := time.Since(replaceStarted)
		recalcStarted := time.Now()
		m.recalcViewportSize()
		recalcDuration := time.Since(recalcStarted)
		m.logTranscriptRebuildTiming(reason, 0, 0, messagesDuration, 0, 0, replaceDuration, recalcDuration, 0, time.Since(rebuildStarted))
		return
	}
	blockBuildStarted := time.Now()
	blocks := m.rebuildBlocksFromMessages(msgs)
	blockBuildDuration := time.Since(blockBuildStarted)
	if len(blocks) == 0 {
		m.logTranscriptRebuildTiming(reason, len(msgs), 0, messagesDuration, blockBuildDuration, 0, 0, 0, 0, time.Since(rebuildStarted))
		return
	}
	clearSettledStarted := time.Now()
	clearBlocksTiming(blocks)
	clearSettledDuration := time.Since(clearSettledStarted)
	blocks = m.maybeWindowStartupTranscript(reason, blocks)
	m.viewport.sticky = true // show latest messages after restore
	replaceStarted := time.Now()
	m.viewport.ReplaceBlocks(blocks)
	m.revalidateFocusedBlock()
	recalcStarted := time.Now()
	m.recalcViewportSize() // ensure viewport uses current layout width so background blocks align
	forceCompactionFocus := reason == "session_restored" || reason == "startup_restored"
	m.maybeFocusVisibleCompactionSummary(forceCompactionFocus)
	recalcDuration := time.Since(recalcStarted)
	m.maybeEnforceStartupDeferredTranscriptRetention()
	replaceDuration := time.Since(replaceStarted)
	sidebarStarted := time.Now()
	m.rebuildSidebarFileEditsFromMessages(msgs)
	sidebarDuration := time.Since(sidebarStarted)
	m.logTranscriptRebuildTiming(reason, len(msgs), len(blocks), messagesDuration, blockBuildDuration, clearSettledDuration, replaceDuration, recalcDuration, sidebarDuration, time.Since(rebuildStarted))
}

func (m *Model) logTranscriptRebuildTiming(reason string, messageCount, blockCount int, messagesDuration, blockBuildDuration, clearSettledDuration, replaceDuration, recalcDuration, sidebarDuration, totalDuration time.Duration) {
	if strings.TrimSpace(reason) == "" || reason == "unspecified" {
		return
	}
	slog.Debug("tui transcript rebuild timing",
		"reason", reason,
		"messages", messageCount,
		"blocks", blockCount,
		"message_fetch_ms", messagesDuration.Milliseconds(),
		"build_blocks_ms", blockBuildDuration.Milliseconds(),
		"clear_settled_ms", clearSettledDuration.Milliseconds(),
		"replace_blocks_ms", replaceDuration.Milliseconds(),
		"recalc_viewport_ms", recalcDuration.Milliseconds(),
		"sidebar_file_edits_ms", sidebarDuration.Milliseconds(),
		"total_ms", totalDuration.Milliseconds(),
	)
}

// rebuildSidebarFileEditsFromMessages scans the message history and reconstructs
// the sidebar file-edit statistics. Called after session restore so EDITED FILES
// shows historical edits even before any new tool calls arrive.
func (m *Model) rebuildSidebarFileEditsFromMessages(msgs []message.Message) {
	// Reset file edits for main agent (sub-agents manage their own edits live).
	m.sidebar.ClearFileEdits("main")
	// Build tool-call-id → (name, paths) index from assistant messages.
	type callInfo struct {
		name  string
		paths []string
	}
	calls := make(map[string]callInfo)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Name != "Write" && tc.Name != "Edit" && tc.Name != "Delete" {
				continue
			}
			paths := extractTranscriptToolPaths(tc.Name, tc.Args)
			if len(paths) == 0 {
				continue
			}
			calls[tc.ID] = callInfo{name: tc.Name, paths: paths}
		}
	}
	// Walk tool result messages and record file edits.
	for _, msg := range msgs {
		if msg.Role != "tool" {
			continue
		}
		info, ok := calls[msg.ToolCallID]
		if !ok {
			continue
		}
		if info.name == "Delete" {
			groups := tools.ParseDeleteResult(msg.Content)
			for _, path := range groups.Deleted {
				m.sidebar.AddFileEdit("main", path, 0, 1)
			}
			continue
		}
		if msg.ToolDiff == "" {
			continue
		}
		for _, path := range info.paths {
			m.sidebar.AddFileEdit("main", path, msg.ToolDiffAdded, msg.ToolDiffRemoved)
		}
	}
}

func extractTranscriptToolPaths(toolName string, args json.RawMessage) []string {
	switch toolName {
	case "Delete":
		if req, err := tools.DecodeDeleteRequest(args); err == nil {
			return append([]string(nil), req.Paths...)
		}
	default:
		var parsed struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &parsed) == nil && parsed.Path != "" {
			return []string{parsed.Path}
		}
	}
	return nil
}

func (m *Model) ensureViewportCallbacks() {
	if m.viewport == nil {
		return
	}
	m.viewport.SetSpillRecovery(func() []*Block {
		return m.rebuildBlocksFromAgentMessages()
	})
}

func (m *Model) rebuildBlocksFromAgentMessages() []*Block {
	if m.agent == nil {
		return nil
	}
	msgs := m.agent.GetMessages()
	return m.rebuildBlocksFromMessages(msgs)
}

func (m *Model) rebuildBlocksFromMessages(msgs []message.Message) []*Block {
	if len(msgs) == 0 {
		return nil
	}

	var nextID int
	blocks := messagesToBlocks(msgs, &nextID)
	oldBlocks := m.viewport.blocks
	limit := min(len(blocks), len(oldBlocks))
	for i := range limit {
		blocks[i].ID = oldBlocks[i].ID
		preserveRebuiltBlockState(oldBlocks[i], blocks[i])
	}

	nextFreshID := highestBlockID(oldBlocks) + 1
	for i := limit; i < len(blocks); i++ {
		blocks[i].ID = nextFreshID
		nextFreshID++
	}
	if nextFreshID > m.nextBlockID {
		m.nextBlockID = nextFreshID
	}
	return blocks
}

func highestBlockID(blocks []*Block) int {
	maxID := -1
	for _, block := range blocks {
		if block != nil && block.ID > maxID {
			maxID = block.ID
		}
	}
	return maxID
}

func preserveRebuiltBlockState(src, dst *Block) {
	if src == nil || dst == nil || src.Type != dst.Type {
		return
	}
	dst.Focused = src.Focused
	switch dst.Type {
	case BlockCompactionSummary:
		if strings.TrimSpace(src.CompactionSummaryRaw) == "" || strings.TrimSpace(dst.CompactionSummaryRaw) == "" {
			return
		}
		if strings.TrimSpace(src.CompactionSummaryRaw) != strings.TrimSpace(dst.CompactionSummaryRaw) {
			return
		}
		dst.Collapsed = src.Collapsed
		dst.CompactionPreviewLines = src.CompactionPreviewLines
		dst.StartedAt = src.StartedAt
		dst.SettledAt = src.SettledAt
	default:
		dst.Collapsed = src.Collapsed
		dst.ReadContentExpanded = src.ReadContentExpanded
		dst.ToolCallDetailExpanded = src.ToolCallDetailExpanded
		dst.ThinkingCollapsed = src.ThinkingCollapsed
		dst.Streaming = src.Streaming
		dst.UserLocalShellPending = src.UserLocalShellPending
		dst.UserLocalShellFailed = src.UserLocalShellFailed
		dst.StartedAt = src.StartedAt
		dst.SettledAt = src.SettledAt
	}
}

func toolResultStatusFromRestoredContent(content string) agent.ToolResultStatus {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return agent.ToolResultStatusSuccess
	}
	lower := strings.ToLower(trimmed)
	if lower == "cancelled" || strings.HasPrefix(lower, "cancelled\n") {
		return agent.ToolResultStatusCancelled
	}
	if strings.HasPrefix(trimmed, "Error: ") || strings.Contains(trimmed, "\n\nError: ") || strings.HasPrefix(trimmed, "Model stopped before completing this tool call") {
		return agent.ToolResultStatusError
	}
	return agent.ToolResultStatusSuccess
}

// messagesToBlocks converts a slice of conversation messages into viewport
// blocks (user, assistant, tool call/result). Updates nextID for block IDs.
func contentOrPartsText(msg message.Message) string {
	if len(msg.Parts) > 0 {
		return userBlockTextFromParts(msg.Parts, msg.Content)
	}
	return msg.Content
}

func messagesToBlocks(msgs []message.Message, nextID *int) []*Block {
	var blocks []*Block
	toolIDToBlock := make(map[string]*Block)

	for msgIdx, msg := range msgs {
		switch msg.Role {
		case "user":
			if msg.Kind == "loop_notice" {
				raw := contentOrPartsText(msg)
				title, body := "", raw
				if t, b, ok := strings.Cut(raw, "\n"); ok {
					title = strings.TrimSpace(t)
					body = strings.TrimSpace(b)
				}
				blocks = append(blocks, &Block{
					ID:          *nextID,
					Type:        BlockStatus,
					StatusTitle: title,
					Content:     body,
				})
				*nextID++
				continue
			}
			var userBlock *Block
			imgCount := 0
			for _, p := range msg.Parts {
				if p.Type == "image" {
					imgCount++
				}
			}
			content := userBlockTextFromParts(msg.Parts, msg.Content)
			if imgCount == 0 {
				parseContent := msg.Content
				if strings.TrimSpace(parseContent) == "" {
					parseContent = content
				}
				if ul, cmd, out, failed, ok := convformat.TryParseUserShellPersistedMessage(parseContent); ok {
					userBlock = &Block{
						ID:                    *nextID,
						Type:                  BlockUser,
						Content:               ul,
						Collapsed:             true,
						UserLocalShellCmd:     cmd,
						UserLocalShellPending: false,
						UserLocalShellResult:  out,
						UserLocalShellFailed:  failed,
						MsgIndex:              msgIdx,
					}
				}
			}
			if userBlock == nil {
				if msg.IsCompactionSummary {
					userBlock = &Block{
						ID:                     *nextID,
						Type:                   BlockCompactionSummary,
						CompactionSummaryRaw:   content,
						CompactionPreviewLines: maxCompactionSummaryPreviewLines,
						Content:                formatCompactionSummaryDisplay(content, true, maxCompactionSummaryPreviewLines),
						Collapsed:              true,
						MsgIndex:               -1,
					}
				} else {
					userBlock = &Block{
						ID:         *nextID,
						Type:       BlockUser,
						Content:    content,
						FileRefs:   fileRefsFromParts(msg.Parts),
						ImageCount: imgCount,
						ImageParts: imagePartsFromContentParts(msg.Parts),
						MsgIndex:   msgIdx,
					}
				}
			}
			blocks = append(blocks, userBlock)
			*nextID++
		case "assistant":
			// Emit each thinking block as an independent BlockThinking so they
			// can be focused / copied individually.
			for _, tb := range msg.ThinkingBlocks {
				if strings.TrimSpace(tb.Thinking) != "" {
					blocks = append(blocks, &Block{
						ID:      *nextID,
						Type:    BlockThinking,
						Content: tb.Thinking,
					})
					*nextID++
				}
			}
			// Emit assistant body (text) as a separate block.
			if strings.TrimSpace(msg.Content) != "" {
				blocks = append(blocks, &Block{
					ID:      *nextID,
					Type:    BlockAssistant,
					Content: msg.Content,
				})
				*nextID++
			}
			for _, tc := range msg.ToolCalls {
				argsStr := string(tc.Args)
				if argsStr == "" {
					argsStr = "{}"
				}
				b := &Block{
					ID:        *nextID,
					Type:      BlockToolCall,
					Content:   eventToolDisplayArgs(tc.Name, argsStr, ""),
					ToolName:  tc.Name,
					ToolID:    tc.ID,
					Collapsed: true,
				}
				blocks = append(blocks, b)
				toolIDToBlock[tc.ID] = b
				*nextID++
			}
		case "tool":
			if b, ok := toolIDToBlock[msg.ToolCallID]; ok {
				b.ResultContent = msg.Content
				b.ResultStatus = toolResultStatusFromRestoredContent(msg.Content)
				b.ResultDone = true // so restored tool cards stop spinning and render terminal state
				b.ToolExecutionState = ""
				b.Audit = msg.Audit.Clone()
				if msg.ToolDurationMs > 0 {
					b.PersistedDuration = time.Duration(msg.ToolDurationMs) * time.Millisecond
				}
				if b.ToolName == "Skill" {
					b.Content = eventToolDisplayArgs(b.ToolName, b.Content, b.ResultContent)
				}
				if msg.ToolDiff != "" {
					b.Diff = msg.ToolDiff
					b.Collapsed = false
				}
				if b.ToolName == "Read" {
					b.Collapsed = false
				}
			}
		}
	}
	return blocks
}

func formatCompactionSummaryDisplay(content string, collapsed bool, previewLines int) string {
	const header = "[Context Summary]\n"
	const footer = "\n\n[Context compressed]"
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	start := strings.Index(content, header)
	if start == -1 {
		if !collapsed {
			return content
		}
		return compactionSummaryPreview(content, previewLines)
	}
	start += len(header)
	end := strings.Index(content[start:], footer)
	summary := ""
	full := content
	if end == -1 {
		summary = strings.TrimSpace(content[start:])
	} else {
		summary = strings.TrimSpace(content[start : start+end])
	}
	if !collapsed {
		return full
	}
	return compactionSummaryPreview(summary, previewLines)
}

func compactionSummaryPreview(summary string, previewLines int) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	if previewLines <= 0 {
		previewLines = maxCompactionSummaryPreviewLines
	}
	lines := strings.Split(summary, "\n")
	if len(lines) <= previewLines {
		return summary
	}
	preview := strings.Join(lines[:previewLines], "\n")
	return strings.TrimRight(preview, "\n") + "\n…"
}

// parseTaskResultInstanceID extracts the SubAgent instance ID from the Task
// tool result string, e.g. "SubAgent reviewer-4 created and started ...".
// Returns empty string if not found.
func parseTaskResultInstanceID(result string) string {
	var handle struct {
		AgentID string `json:"agent_id"`
	}
	if json.Unmarshal([]byte(result), &handle) == nil && strings.TrimSpace(handle.AgentID) != "" {
		return strings.TrimSpace(handle.AgentID)
	}
	const prefix = "SubAgent "
	if !strings.HasPrefix(result, prefix) {
		return ""
	}
	rest := result[len(prefix):]
	end := strings.IndexAny(rest, " \t\n")
	if end < 0 {
		end = len(rest)
	}
	id := strings.TrimSpace(rest[:end])
	// Accept any ID that contains a dash followed by digits (e.g. "reviewer-4").
	if id == "" {
		return ""
	}
	dashIdx := strings.LastIndex(id, "-")
	if dashIdx < 0 || dashIdx == len(id)-1 {
		return ""
	}
	suffix := id[dashIdx+1:]
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return id
}

// filterBlocksByAgent returns the subset of blocks visible for the given agent.
// If agentID is empty, all blocks are returned (show-all mode). If agentID is
// "main", only blocks with empty AgentID (main agent) are shown:
// main view does not mix in subagent blocks). Otherwise only blocks whose
// AgentID matches or whose AgentID is empty (shared) are included.
func filterBlocksByAgent(blocks []*Block, agentID string) []*Block {
	if agentID == "" {
		return blocks
	}
	filtered := make([]*Block, 0, len(blocks))
	for _, b := range blocks {
		if agentID == "main" {
			if b.AgentID == "" {
				filtered = append(filtered, b)
			}
		} else if b.AgentID == agentID {
			filtered = append(filtered, b)
		}
	}
	return filtered
}
