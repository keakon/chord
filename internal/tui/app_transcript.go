package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/thinkingtranslate"
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
	sidebarStarted := time.Now()
	m.rebuildSidebarFileEditsFromMessages(msgs)
	sidebarDuration := time.Since(sidebarStarted)
	if len(msgs) == 0 {
		m.viewport.sticky = true
		replaceStarted := time.Now()
		m.viewport.ReplaceBlocks(nil)
		replaceDuration := time.Since(replaceStarted)
		recalcStarted := time.Now()
		m.recalcViewportSize()
		recalcDuration := time.Since(recalcStarted)
		m.logTranscriptRebuildTiming(reason, 0, 0, messagesDuration, 0, 0, replaceDuration, recalcDuration, sidebarDuration, time.Since(rebuildStarted))
		return
	}
	blockBuildStarted := time.Now()
	blocks := m.rebuildBlocksFromMessages(msgs)
	blockBuildDuration := time.Since(blockBuildStarted)
	if len(blocks) == 0 {
		m.logTranscriptRebuildTiming(reason, len(msgs), 0, messagesDuration, blockBuildDuration, 0, 0, 0, sidebarDuration, time.Since(rebuildStarted))
		return
	}
	clearSettledStarted := time.Now()
	clearBlocksTiming(blocks)
	clearSettledDuration := time.Since(clearSettledStarted)
	blocks = m.maybeWindowStartupTranscript(reason, blocks)
	m.viewport.sticky = true // show latest messages after restore
	replaceStarted := time.Now()
	m.viewport.ReplaceBlocks(blocks)
	m.rebindLiveViewportBlocks()
	m.revalidateFocusedBlock()
	recalcStarted := time.Now()
	m.recalcViewportSize() // ensure viewport uses current layout width so background blocks align
	forceCompactionFocus := reason == "session_restored" || reason == "startup_restored"
	m.maybeFocusVisibleCompactionSummary(forceCompactionFocus)
	recalcDuration := time.Since(recalcStarted)
	m.maybeEnforceStartupDeferredTranscriptRetention()
	replaceDuration := time.Since(replaceStarted)
	m.logTranscriptRebuildTiming(reason, len(msgs), len(blocks), messagesDuration, blockBuildDuration, clearSettledDuration, replaceDuration, recalcDuration, sidebarDuration, time.Since(rebuildStarted))
}

func (m *Model) logTranscriptRebuildTiming(reason string, messageCount, blockCount int, messagesDuration, blockBuildDuration, clearSettledDuration, replaceDuration, recalcDuration, sidebarDuration, totalDuration time.Duration) {
	if strings.TrimSpace(reason) == "" || reason == "unspecified" {
		return
	}
	log.Debugf("tui transcript rebuild timing reason=%v messages=%v blocks=%v message_fetch_ms=%v build_blocks_ms=%v clear_settled_ms=%v replace_blocks_ms=%v recalc_viewport_ms=%v sidebar_file_edits_ms=%v total_ms=%v", reason, messageCount, blockCount, messagesDuration.Milliseconds(), blockBuildDuration.Milliseconds(), clearSettledDuration.Milliseconds(), replaceDuration.Milliseconds(), recalcDuration.Milliseconds(), sidebarDuration.Milliseconds(), totalDuration.Milliseconds())
}

// rebuildSidebarFileEditsFromMessages scans the message history and reconstructs
// sidebar changed-file statistics from stored diffs. Delete tool calls are not
// restored because older transcripts do not carry reliable deleted-file state.
func (m *Model) rebuildSidebarFileEditsFromMessages(msgs []message.Message) {
	// Reset file edits for main agent (sub-agents manage their own edits live).
	m.sidebar.ClearFileEdits("main")
	// Build tool-call-id → paths index from assistant messages.
	calls := make(map[string][]string)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Name != "Write" && tc.Name != "Edit" {
				continue
			}
			paths := extractTranscriptToolPaths(tc.Args)
			if len(paths) == 0 {
				continue
			}
			calls[tc.ID] = paths
		}
	}
	// Walk tool result messages and record file edits.
	for _, msg := range msgs {
		if msg.Role != "tool" {
			continue
		}
		paths, ok := calls[msg.ToolCallID]
		if !ok || msg.ToolDiff == "" {
			continue
		}
		for _, path := range paths {
			m.sidebar.AddFileEdit("main", path, msg.ToolDiffAdded, msg.ToolDiffRemoved)
		}
	}
}

func extractTranscriptToolPaths(args json.RawMessage) []string {
	var parsed struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &parsed) == nil && parsed.Path != "" {
		return []string{parsed.Path}
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

func (m *Model) loadThinkingTranslationsForTranscript() map[string]ThinkingTranslationView {
	if m == nil || m.agent == nil {
		return nil
	}
	summary := m.agent.GetSessionSummary()
	if summary == nil || strings.TrimSpace(summary.ID) == "" {
		return nil
	}
	sessionDirProvider, ok := m.agent.(interface{ SessionDir() string })
	if !ok {
		return nil
	}
	sessionDir := strings.TrimSpace(sessionDirProvider.SessionDir())
	if sessionDir == "" {
		return nil
	}
	entries, err := recovery.LoadThinkingTranslations(sessionDir)
	if err != nil {
		log.Debugf("load thinking translations failed session=%s err=%v", summary.ID, err)
		return nil
	}
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]ThinkingTranslationView, len(entries))
	for _, entry := range entries {
		translated := thinkingtranslate.ExtractTranslationEnvelope(entry.Translated)
		if strings.TrimSpace(entry.MessageID) == "" || entry.BlockIndex < 0 || translated == "" {
			continue
		}
		key := thinkingTranslationTranscriptKey(entry.MessageID, entry.BlockIndex)
		out[key] = ThinkingTranslationView{
			TargetLang:   strings.TrimSpace(entry.TargetLang),
			Content:      translated,
			OriginalHash: strings.TrimSpace(entry.OriginalHash),
		}
	}
	return out
}

func thinkingTranslationTranscriptKey(messageID string, blockIndex int) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(messageID), blockIndex)
}

func (m *Model) rebuildBlocksFromMessages(msgs []message.Message) []*Block {
	if len(msgs) == 0 {
		return nil
	}

	var nextID int
	blocks := messagesToBlocksWithThinkingTranslations(msgs, &nextID, m.loadThinkingTranslationsForTranscript())
	for _, block := range blocks {
		if block != nil {
			block.displayWorkingDir = m.workingDir
		}
	}
	oldBlocks := m.viewport.blocks
	if shouldResetRebuiltBlockIDsAfterCompaction(oldBlocks, blocks) {
		limit := min(len(blocks), len(oldBlocks))
		for i := range limit {
			preserveRebuiltBlockState(oldBlocks[i], blocks[i])
		}
		m.nextBlockID = highestBlockID(blocks) + 1
		return blocks
	}
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

func shouldResetRebuiltBlockIDsAfterCompaction(oldBlocks, newBlocks []*Block) bool {
	if len(oldBlocks) == 0 || len(newBlocks) == 0 {
		return false
	}
	return newBlocks[0] != nil && newBlocks[0].Type == BlockCompactionSummary &&
		(oldBlocks[0] == nil || oldBlocks[0].Type != BlockCompactionSummary)
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

func toolResultStatusFromRestoredMessage(msg message.Message) agent.ToolResultStatus {
	switch strings.TrimSpace(msg.ToolStatus) {
	case string(agent.ToolResultStatusError):
		return agent.ToolResultStatusError
	case string(agent.ToolResultStatusCancelled):
		return agent.ToolResultStatusCancelled
	case string(agent.ToolResultStatusSuccess):
		return agent.ToolResultStatusSuccess
	default:
		return toolResultStatusFromRestoredContent(msg.Content)
	}
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
	return messagesToBlocksWithThinkingTranslations(msgs, nextID, nil)
}

func messagesToBlocksWithThinkingTranslations(msgs []message.Message, nextID *int, translations map[string]ThinkingTranslationView) []*Block {
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
			for blockIndex, tb := range msg.ThinkingBlocks {
				thinking := strings.TrimSpace(tb.Thinking)
				if thinking != "" {
					block := &Block{
						ID:                 *nextID,
						Type:               BlockThinking,
						Content:            tb.Thinking,
						MsgIndex:           msgIdx,
						ThinkingBlockIndex: blockIndex,
					}
					if len(translations) > 0 {
						messageID := fmt.Sprintf("msgidx:%d", msgIdx)
						if view, ok := translations[thinkingTranslationTranscriptKey(messageID, blockIndex)]; ok && strings.TrimSpace(view.Content) != "" {
							if view.OriginalHash == "" || view.OriginalHash == recovery.ThinkingTranslationOriginalHash(tb.Thinking) {
								block.ThinkingTranslations = make([]ThinkingTranslationView, blockIndex+1)
								block.ThinkingTranslations[blockIndex] = view
							}
						}
					}
					blocks = append(blocks, block)
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
				if tc.Name == tools.NameDone {
					if parsed, err := tools.ParseDoneArgs(tc.Args); err == nil {
						b.DoneReport = strings.TrimSpace(parsed.Report)
					}
				}
				blocks = append(blocks, b)
				toolIDToBlock[tc.ID] = b
				*nextID++
			}
		case "tool":
			if b, ok := toolIDToBlock[msg.ToolCallID]; ok {
				b.ResultContent = msg.Content
				b.ResultStatus = toolResultStatusFromRestoredMessage(msg)
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
				if b.ToolName == tools.NameRead {
					b.Collapsed = false
				}
				if b.ToolName == "Delegate" && b.ResultStatus != agent.ToolResultStatusError && strings.TrimSpace(b.ResultContent) != "" {
					if handle, ok := parseTaskToolHandle(b.ResultContent); ok {
						if handle.AgentID != "" {
							b.LinkedAgentID = handle.AgentID
						}
						if handle.TaskID != "" {
							b.LinkedTaskID = handle.TaskID
						}
					} else if id := parseTaskResultInstanceID(b.ResultContent); id != "" {
						b.LinkedAgentID = id
					}
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
