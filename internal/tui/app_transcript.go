package tui

import (
	"encoding/json"
	"fmt"
	"hash/maphash"
	"slices"
	"strconv"
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

	for _, block := range slices.Backward(blocks) {

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
	if !message.IsUserAuthored(msg) {
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
	for i, msg := range slices.Backward(msgs) {
		if _, ok := used[i]; ok {
			continue
		}

		if !message.IsUserAuthored(msg) {
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
	m.rebuildViewportFromMessagesPreservingActivity(reason, false)
}

func (m *Model) rebuildViewportFromMessagesPreservingActivity(reason string, preserveRequestActivity bool) {
	if m.agent == nil {
		return
	}
	rebuildStarted := time.Now()
	m.cancelClipboardAttachmentPaste()
	m.finalizeTurn()
	preserveAttachments := m.preserveAttachmentsOnNextRebuild
	preserveComposer := m.preserveComposerStateOnNextRebuild
	m.pendingSessionRestoreRebuild = false
	m.preserveAttachmentsOnNextRebuild = false
	m.preserveComposerStateOnNextRebuild = false
	if !preserveComposer {
		m.queuedDrafts = nil
		m.agentComposerStates = nil
		m.editingQueuedDraftID = ""
		m.inflightDraft = nil
	}
	if !preserveAttachments && !preserveComposer {
		m.attachments = nil
	}
	m.currentAssistantBlock = nil
	m.assistantBlockAppended = false
	m.currentThinkingBlock = nil
	m.thinkingBlockAppended = false
	m.subAgentStreamStates = nil
	m.resetTimingStateForSessionRestore(preserveRequestActivity)
	m.closeAtMention()
	messagesStarted := time.Now()
	msgs := m.agent.GetMessages()
	messagesDuration := time.Since(messagesStarted)
	sidebarStarted := time.Now()
	m.rebuildSidebarFileEditsFromMessages(msgs)
	sidebarDuration := time.Since(sidebarStarted)
	if len(msgs) == 0 {
		m.setTranscriptDisplaySequences(nil, m.focusedAgentID)
		m.viewport.sticky = true
		replaceStarted := time.Now()
		m.viewport.ReplaceBlocks(nil)
		// The viewport is now empty, so it belongs to the current epoch: a later
		// rebuild in this session must be able to adopt again.
		m.viewportBlockEpoch = m.sessionTranscriptEpoch
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
	m.setTranscriptDisplaySequences(blocks, m.focusedAgentID)
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
// sidebar changed-file statistics from persisted FileState records, falling back
// to stored diffs, apply_patch operations, and delete result text.
func (m *Model) rebuildSidebarFileEditsFromMessages(msgs []message.Message) {
	// Reset file edits for main agent (sub-agents manage their own edits live).
	m.sidebar.ClearFileEdits("main")
	// Build tool-call-id → call index from assistant messages.
	calls := make(map[string]message.ToolCall)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			name := tools.NormalizeName(tc.Name)
			if name != tools.NameWrite && name != tools.NameEdit && name != tools.NameApplyPatch && name != tools.NameDelete {
				continue
			}
			tc.Name = name
			calls[tc.ID] = tc
		}
	}
	// Walk tool result messages and record file edits.
	for _, msg := range msgs {
		if msg.Role != "tool" {
			continue
		}
		call, ok := calls[msg.ToolCallID]
		if !ok {
			continue
		}
		if agent.ToolResultStatus(msg.ToolStatus).IsUnsuccessful() {
			if msg.FileState.HasChanges() {
				m.addSidebarFileState("main", msg.FileState)
			}
			continue
		}
		if call.Name == tools.NameDelete {
			if !m.addSidebarFileState("main", msg.FileState) {
				for _, path := range tools.ParseDeleteResult(msg.Content).Deleted {
					m.sidebar.AddFileDelete("main", path)
				}
			}
			continue
		}
		if call.Name == tools.NameApplyPatch {
			// Older results did not persist a terminal status. Require a diff for
			// those records so failed historical calls are not treated as changes.
			if msg.ToolStatus != "" || msg.ToolDiff != "" {
				m.addApplyPatchSidebarChanges("main", call.Args, msg.Content)
			}
			continue
		}
		if msg.ToolDiff == "" {
			continue
		}
		for _, path := range extractTranscriptToolPaths(call.Args) {
			m.sidebar.AddFileEdit("main", path, msg.ToolDiffAdded, msg.ToolDiffRemoved)
		}
	}
}

func extractTranscriptToolPaths(args json.RawMessage) []string {
	if path := tools.ExtractEditPathFromArgs(args); path != "" {
		return []string{path}
	}
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
	blocks := m.rebuildBlocksFromMessages(msgs)
	m.setTranscriptDisplaySequences(blocks, m.focusedAgentID)
	return blocks
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
	m.adoptRebuiltBlockState(blocks)
	return blocks
}

// adoptRebuiltBlockState carries block IDs and per-card view state from the
// transcript currently in the viewport onto a freshly rebuilt block list built
// from the agent's messages. Cards are matched by durable identity, never by
// position: a durable compaction replaces the archived head with a single
// summary card, so every surviving row shifts and positional pairing would
// copy one card's collapse / detail / focus / timing state onto a different
// card (for example folding an Edit card that should stay expanded). Cards
// with no identity match in the previous transcript — a rewritten transcript
// or a brand-new card — keep their freshly built state and receive an unused
// ID; repeated identities (for example the same text in two transcripts) still
// pair, consumed in transcript order.
//
// Adoption is scoped to one session. A session switch leaves the outgoing
// cards in the viewport, and their content can match the incoming transcript
// exactly (the same first prompt, the same canned reply) without being the same
// card, so a rebuild that crosses a session epoch adopts nothing.
func (m *Model) adoptRebuiltBlockState(blocks []*Block) {
	oldBlocks := m.viewport.blocks
	if m.viewportBlockEpoch != m.sessionTranscriptEpoch {
		oldBlocks = nil
	}
	unmatched := make(map[string][]*Block, len(oldBlocks))
	for _, old := range oldBlocks {
		key := rebuiltBlockIdentityKey(old)
		if key == "" {
			continue
		}
		unmatched[key] = append(unmatched[key], old)
	}
	nextFreshID := max(m.nextBlockID, highestBlockID(oldBlocks)+1)
	for _, block := range blocks {
		if block == nil {
			continue
		}
		key := rebuiltBlockIdentityKey(block)
		if key != "" {
			if queue := unmatched[key]; len(queue) > 0 {
				old := queue[0]
				unmatched[key] = queue[1:]
				block.ID = old.ID
				preserveRebuiltBlockState(old, block)
				continue
			}
		}
		block.ID = nextFreshID
		nextFreshID++
	}
	m.nextBlockID = nextFreshID
	m.viewportBlockEpoch = m.sessionTranscriptEpoch
}

// rebuiltBlockIdentitySeed salts the content digests below. It is generated
// per process, which is enough because identities only pair blocks within a
// single live transcript.
var rebuiltBlockIdentitySeed = maphash.MakeSeed()

// rebuiltBlockIdentityDigest keeps identity keys bounded: keying on the raw
// content would copy the whole transcript on every rebuild, and a spilled card
// does not hold its content at all.
func rebuiltBlockIdentityDigest(content string) string {
	return strconv.FormatUint(maphash.String(rebuiltBlockIdentitySeed, content), 16)
}

// rebuiltBlockIdentityKey returns the durable identity a card carries across a
// transcript rebuild, or "" when the card has none and must be treated as new.
// The identity is the card's own content or tool call ID, not its message
// index: compaction shifts every surviving message index when the archived head
// is replaced by the summary, so index-based matching would mispair rows.
// Tool call IDs are unique per invocation; content keys are consumed one-to-one
// in transcript order, which keeps repeated identical rows paired in order.
// A cold card no longer holds Content, so it falls back to the identity
// captured when it was spilled.
func rebuiltBlockIdentityKey(block *Block) string {
	if block == nil {
		return ""
	}
	if block.spillCold && block.spillIdentityKey != "" {
		return block.spillIdentityKey
	}
	switch block.Type {
	case BlockToolCall:
		if block.ToolID == "" {
			return ""
		}
		return "tool\x00" + block.ToolID
	case BlockCompactionSummary:
		raw := strings.TrimSpace(block.CompactionSummaryRaw)
		if raw == "" {
			return ""
		}
		return "summary\x00" + rebuiltBlockIdentityDigest(raw)
	case BlockUser:
		if block.Content == "" {
			return ""
		}
		return "user\x00" + block.AgentID + "\x00" + rebuiltBlockIdentityDigest(block.Content)
	case BlockAssistant:
		if block.Content == "" {
			return ""
		}
		return "assistant\x00" + block.AgentID + "\x00" + rebuiltBlockIdentityDigest(block.Content)
	case BlockThinking:
		if block.Content == "" {
			return ""
		}
		return "thinking\x00" + block.AgentID + "\x00" + rebuiltBlockIdentityDigest(block.Content)
	case BlockStatus:
		if block.StatusTitle == "" && block.Content == "" {
			return ""
		}
		return "status\x00" + block.AgentID + "\x00" + block.StatusTitle + "\x00" + rebuiltBlockIdentityDigest(block.Content)
	default:
		return ""
	}
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
		// Compaction cards are always fully expanded; only timing is preserved.
		dst.StartedAt = src.StartedAt
		dst.SettledAt = src.SettledAt
	default:
		dst.Collapsed = src.Collapsed
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
	switch message.ClassifyToolResultContent(content) {
	case message.ToolResultClassCancelled:
		return agent.ToolResultStatusCancelled
	case message.ToolResultClassError:
		return agent.ToolResultStatusError
	default:
		return agent.ToolResultStatusSuccess
	}
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

// A durable context notice is persisted wrapped so the model reads it as a
// system reminder, while the live ContextNoticeEvent carries the bare text.
// Restore strips the wrapper so the rebuilt card matches what was shown live.
const (
	contextNoticeReminderOpen  = "<system-reminder>"
	contextNoticeReminderClose = "</system-reminder>"
)

func stripContextNoticeReminder(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, contextNoticeReminderOpen) || !strings.HasSuffix(trimmed, contextNoticeReminderClose) {
		return text
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, contextNoticeReminderOpen), contextNoticeReminderClose)
	return strings.TrimSpace(inner)
}

func assistantThinkingBlocksForTranscript(msg message.Message) []message.ThinkingBlock {
	if len(msg.ThinkingBlocks) > 0 {
		return msg.ThinkingBlocks
	}
	if strings.TrimSpace(msg.ReasoningContent) == "" {
		return nil
	}
	return []message.ThinkingBlock{{Thinking: msg.ReasoningContent}}
}

type transcriptToolResult struct {
	argsJSON string
	result   string
	// payload is the tool's own output before notes; notes are the diagnostic
	// lines the runtime appended. Both are empty in transcripts written before
	// the split, where result holds the combination.
	payload        string
	notes          []string
	status         agent.ToolResultStatus
	audit          *message.ToolArgsAudit
	diff           string
	duration       time.Duration
	doneReport     string
	displayArgs    func(toolName, argsJSON, result string) string
	imageParts     []BlockImagePart
	resetExecution bool
	recoveryState  string
}

func newTranscriptToolCallBlock(nextID int, tc message.ToolCall) *Block {
	argsStr := string(tc.Args)
	if argsStr == "" {
		argsStr = "{}"
	}
	toolName := tools.NormalizeName(tc.Name)
	b := &Block{
		ID:        nextID,
		Type:      BlockToolCall,
		Content:   stableToolDisplayArgs(toolName, argsStr, ""),
		RawArgs:   argsStr,
		ToolName:  toolName,
		ToolID:    tc.ID,
		Collapsed: !toolCardAlwaysExpanded(toolName),
	}
	if toolCardAlwaysExpanded(toolName) && toolName != tools.NameDelegate {
		b.ToolCallDetailExpanded = true
	}
	applyDoneReportFromArgs(b, argsStr, "")
	return b
}

func applyDoneReportFromArgs(block *Block, argsJSON, preferred string) {
	if block == nil || toolNameKey(block.ToolName) != tools.NameDone {
		return
	}
	if strings.TrimSpace(preferred) != "" {
		block.DoneReport = preferred
		return
	}
	if parsed, err := tools.ParseDoneArgs(json.RawMessage(argsJSON)); err == nil && strings.TrimSpace(parsed.Report) != "" {
		block.DoneReport = strings.TrimSpace(parsed.Report)
	}
}

func applyStableToolResultToBlock(block *Block, result transcriptToolResult) {
	if block == nil {
		return
	}
	block.ResultContent = result.result
	// A restored transcript carries the payload and notes separately when they
	// were recorded; older sessions only have the combined text, and the card
	// falls back to it.
	block.ResultPayload = result.payload
	block.ResultNotes = append([]string(nil), result.notes...)
	if result.argsJSON != "" {
		block.RawArgs = result.argsJSON
	}
	block.Audit = result.audit.Clone()
	if result.imageParts != nil {
		block.ImageParts = result.imageParts
	}
	applyDoneReportFromArgs(block, block.RawArgs, result.doneReport)
	if result.displayArgs != nil {
		displayArgsJSON := block.RawArgs
		// RawArgs holds the model's original request doc (captured at start or
		// restored from the session). The card body must show the arguments
		// that actually ran — after user edits, hooks, or automatic sanitizing
		// — while ignored fields are rendered as struck-through callouts from
		// Audit.IgnoredArgs. EffectiveArgsJSON is always that executed
		// document, so prefer it whenever present.
		if block.Audit != nil && strings.TrimSpace(block.Audit.EffectiveArgsJSON) != "" {
			displayArgsJSON = block.Audit.EffectiveArgsJSON
		}
		if displayArgs := result.displayArgs(block.ToolName, displayArgsJSON, block.ResultContent); displayArgs != "" {
			block.Content = displayArgs
		}
	}
	block.ResultStatus = result.status
	block.RecoveryState = result.recoveryState
	block.ResultDone = true
	if result.resetExecution {
		block.ToolExecutionState = ""
		block.ToolQueuedByExecutionEvent = false
		block.ToolProgress = nil
	}
	if result.duration > 0 {
		block.PersistedDuration = result.duration
	}
	if result.diff != "" {
		block.Diff = result.diff
	}
	applyTaskHandleFromResult(block)
}

func applyTaskHandleFromResult(block *Block) {
	if block == nil || block.ToolName != tools.NameDelegate || block.ResultStatus == agent.ToolResultStatusError || strings.TrimSpace(block.ResultContent) == "" {
		return
	}
	if handle, _, ok := parseTaskToolHandle(block.ResultContent); ok {
		if handle.AgentID != "" {
			block.LinkedAgentID = handle.AgentID
		}
		if handle.TaskID != "" {
			block.LinkedTaskID = handle.TaskID
		}
	} else if id := parseTaskResultInstanceID(block.ResultContent); id != "" {
		block.LinkedAgentID = id
	}
}

func messagesToBlocks(msgs []message.Message, nextID *int) []*Block {
	return messagesToBlocksWithThinkingTranslations(msgs, nextID, nil)
}

func messagesToBlocksWithThinkingTranslations(msgs []message.Message, nextID *int, translations map[string]ThinkingTranslationView) []*Block {
	blocks := make([]*Block, 0, len(msgs))
	toolIDToBlock := make(map[string]*Block)

	for msgIdx, msg := range msgs {
		switch msg.Role {
		case "user":
			if msg.Kind == message.KindBackgroundResult {
				content, backgroundID := formatBackgroundResultCardContent(msg.Content, "", "", "", "")
				blocks = append(blocks, &Block{
					ID:                    *nextID,
					Type:                  BlockStatus,
					StatusTitle:           backgroundResultCardTitle,
					Content:               content,
					BackgroundCopyContent: msg.Content,
					BackgroundObjectID:    backgroundID,
					MsgIndex:              msgIdx,
					Collapsed:             true,
				})
				*nextID++
				continue
			}
			if msg.Kind == message.KindSubAgentMailbox && msg.Mailbox != nil {
				block := newSubAgentMailboxBlock(*nextID, msg.Mailbox.Kind, msg.Mailbox.Subtype, msg.Mailbox.AgentID, msg.Mailbox.TaskID, msg.Content, "")
				block.MailboxMessageID = msg.Mailbox.MessageID
				block.MsgIndex = msgIdx
				*nextID++
				blocks = append(blocks, block)
				continue
			}
			if msg.Kind == message.KindLoopNotice {
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
					MsgIndex:    msgIdx,
					Collapsed:   true,
				})
				*nextID++
				continue
			}
			// A durable stream-continuation message is rendered as a status card
			// with the same chrome the live StreamContinueEvent uses, so a
			// restored session shows the card exactly as it appeared live.
			if msg.Kind == message.KindStreamContinue {
				blocks = append(blocks, &Block{
					ID:          *nextID,
					Type:        BlockStatus,
					StatusTitle: streamContinueCardTitle,
					Content:     msg.Content,
					MsgIndex:    msgIdx,
					Collapsed:   true,
				})
				*nextID++
				continue
			}
			// A durable context-pressure notice rebuilds the same card the
			// live ContextNoticeEvent appended, so a restored session shows
			// the notice exactly as it appeared when it dispatched.
			if msg.Kind == message.KindContextNotice {
				blocks = append(blocks, &Block{
					ID:          *nextID,
					Type:        BlockStatus,
					StatusTitle: contextNoticeTitle(msg.NoticeLevel),
					Content:     stripContextNoticeReminder(contentOrPartsText(msg)),
					MsgIndex:    msgIdx,
					NoticeLevel: msg.NoticeLevel,
					Collapsed:   true,
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
						UserLocalShell:        true,
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
						ID:                    *nextID,
						Type:                  BlockCompactionSummary,
						CompactionSummaryRaw:  content,
						CompactionSummaryMode: strings.TrimSpace(msg.CompactionSummaryMode),
						Content:               content,
						MsgIndex:              -1,
					}
				} else {
					userBlock = &Block{
						ID:         *nextID,
						Type:       BlockUser,
						Content:    content,
						FileRefs:   fileRefsFromParts(msg.Parts),
						ImageCount: imgCount,
						ImageParts: imagePartsFromContentParts(msg.Parts),
						PDFNames:   pdfNamesFromContentParts(msg.Parts),
						MsgIndex:   msgIdx,
					}
				}
			}
			blocks = append(blocks, userBlock)
			*nextID++
		case "assistant":
			// Emit each thinking block as an independent BlockThinking so they
			// can be focused / copied individually.
			for blockIndex, tb := range assistantThinkingBlocksForTranscript(msg) {
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
			if assistantContentHasVisibleText(msg.Content) {
				blocks = append(blocks, &Block{
					ID:       *nextID,
					Type:     BlockAssistant,
					Content:  msg.Content,
					MsgIndex: msgIdx,
				})
				*nextID++
			}
			for _, tc := range msg.ToolCalls {
				b := newTranscriptToolCallBlock(*nextID, tc)
				b.MsgIndex = msgIdx
				blocks = append(blocks, b)
				toolIDToBlock[tc.ID] = b
				*nextID++
			}
		case "tool":
			if b, ok := toolIDToBlock[msg.ToolCallID]; ok {
				applyStableToolResultToBlock(b, transcriptToolResult{
					result:         msg.Content,
					payload:        msg.ToolPayload,
					notes:          msg.ToolNotes,
					status:         toolResultStatusFromRestoredMessage(msg),
					audit:          msg.Audit,
					diff:           msg.ToolDiff,
					duration:       time.Duration(msg.ToolDurationMs) * time.Millisecond,
					displayArgs:    stableToolDisplayArgs,
					resetExecution: true,
					recoveryState:  msg.ToolRecoveryState,
				})
			}
		}
	}
	return blocks
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
// "main", only main-agent blocks are shown (empty AgentID, or "main" from
// runtime events that carry identity.MainAgentID): main view does not mix in
// subagent blocks. Otherwise only blocks whose AgentID matches are included.
func filterBlocksByAgent(blocks []*Block, agentID string) []*Block {
	if agentID == "" {
		return blocks
	}
	filtered := make([]*Block, 0, len(blocks))
	for _, b := range blocks {
		if agentID == "main" {
			if b.AgentID == "" || b.AgentID == "main" {
				filtered = append(filtered, b)
			}
		} else if b.AgentID == agentID {
			filtered = append(filtered, b)
		}
	}
	return filtered
}
