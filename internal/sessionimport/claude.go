package sessionimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/message"
)

type claudeTranscriptEnvelope struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	UUID              string          `json:"uuid"`
	ParentUUID        string          `json:"parentUuid"`
	LogicalParentUUID string          `json:"logicalParentUuid"`
	IsSidechain       bool            `json:"isSidechain"`
	AgentID           string          `json:"agentId"`
	Message           json.RawMessage `json:"message"`
	Content           json.RawMessage `json:"content"`
}

type claudeMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

func (m *claudeMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	content := bytes.TrimSpace(raw.Content)
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		m.Content = nil
		return nil
	}
	if len(content) > 0 && content[0] == '[' {
		var blocks []json.RawMessage
		if err := json.Unmarshal(content, &blocks); err != nil {
			return err
		}
		m.Content = blocks
		return nil
	}
	if len(content) > 0 && content[0] == '"' {
		block, err := json.Marshal(map[string]string{
			"type": "text",
			"text": rawContentString(content),
		})
		if err != nil {
			return err
		}
		m.Content = []json.RawMessage{block}
		return nil
	}
	m.Content = []json.RawMessage{append(json.RawMessage(nil), content...)}
	return nil
}

type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type claudeNode struct {
	Envelope claudeTranscriptEnvelope
	Index    int
}

type claudeEntryKind string

const (
	claudeEntryVisibleMessage claudeEntryKind = "visible_message"
	claudeEntryMetadata       claudeEntryKind = "metadata"
	claudeEntryUnsupported    claudeEntryKind = "unsupported"
)

type claudeEntry struct {
	Node                claudeNode
	Kind                claudeEntryKind
	Role                string
	PreservedTailUUID   string
	TombstoneTargetUUID string
}

type claudeChainCandidate struct {
	LeafUUID                  string
	Nodes                     []claudeNode
	MessageCount              int
	LatestIndex               int
	LastRole                  string
	ParentMissing             int
	OrphanToolResults         int
	OrphanToolCalls           int
	PendingLeafToolCalls      int
	IncludesPreservedSegments bool
}

func convertClaudeTranscript(data []byte, toolMode string, reasoningMode string, report *ImportReport) ([]message.Message, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("claude import: empty input")
	}
	if report.Claude == nil {
		report.Claude = &ClaudeImportReport{}
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	var entries []claudeEntry
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var env claudeTranscriptEnvelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			return nil, fmt.Errorf("claude import: line %d: parse JSON: %w", lineNo, err)
		}
		entry, skipped := classifyClaudeEntry(claudeNode{Envelope: env, Index: lineNo}, report.Claude)
		if skipped {
			report.SkippedEntries++
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("claude import: scan JSONL: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("claude import: no transcript entries found")
	}

	chain, warnings, err := selectClaudeMainChain(entries, report.Claude)
	for _, w := range warnings {
		report.warnf("claude: %s", w)
		report.Claude.Diagnostics = append(report.Claude.Diagnostics, w)
	}
	if err != nil {
		fallbackMsgs, ok := convertClaudeUnsupportedEntries(entries, report)
		if ok {
			return fallbackMsgs, nil
		}
		return nil, err
	}

	var out []message.Message
	for _, node := range chain {
		msg, skipped, toolRendered, reasoningSkipped, warns, err := convertClaudeNode(node, toolMode, reasoningMode)
		for _, w := range warns {
			report.warnf("claude line %d: %s", node.Index, w)
			report.Claude.Diagnostics = append(report.Claude.Diagnostics, fmt.Sprintf("line %d: %s", node.Index, w))
		}
		if err != nil {
			return nil, fmt.Errorf("claude import: line %d: %w", node.Index, err)
		}
		if toolRendered {
			report.ToolEntriesRendered++
			report.Claude.DowngradedVisibleEntries++
		}
		if isClaudeUnsupportedVisibleNode(node) && !skipped {
			report.Claude.DowngradedVisibleEntries++
		}
		if reasoningSkipped {
			report.ReasoningBlocksSkipped++
		}
		if skipped {
			report.SkippedEntries++
			continue
		}
		for _, m := range msg {
			if m.Role == "assistant" {
				for _, tc := range m.ToolCalls {
					if claudeToolCallHasUnsupportedMetadata(tc.Args) {
						report.UnsupportedToolCalls++
						continue
					}
					report.StructuredToolCalls++
					report.Claude.StructuredToolCalls++
				}
			}
			if m.Role == "tool" {
				report.StructuredToolResults++
				report.Claude.StructuredToolResults++
			}
		}
		out = append(out, msg...)
	}
	if report.Claude.SidechainMessagesSkipped > 0 {
		diagnostic := fmt.Sprintf("detected %d sidechain messages; excluded from main import", report.Claude.SidechainMessagesSkipped)
		report.Claude.Diagnostics = append(report.Claude.Diagnostics, diagnostic)
		if len(report.Claude.SidechainAgentIDs) > 0 {
			report.Claude.Diagnostics = append(report.Claude.Diagnostics, fmt.Sprintf("sidechain agents detected: %s", strings.Join(report.Claude.SidechainAgentIDs, ", ")))
		}
	}
	if report.Claude.NonSidechainMessages > 0 && len(out) > 0 && len(out)*2 < report.Claude.NonSidechainMessages {
		warning := fmt.Sprintf("selected main conversation span length %d from %d visible non-sidechain messages; transcript may require compaction-aware reconstruction", report.Claude.SelectedSpanLength, report.Claude.NonSidechainMessages)
		report.warnf("claude: %s", warning)
		report.Claude.Diagnostics = append(report.Claude.Diagnostics, warning)
	}
	return out, nil
}

func classifyClaudeEntry(node claudeNode, stats *ClaudeImportReport) (claudeEntry, bool) {
	env := node.Envelope
	entryType := strings.ToLower(strings.TrimSpace(env.Type))
	subtype := strings.ToLower(strings.TrimSpace(env.Subtype))
	if env.IsSidechain {
		if hasVisibleClaudeMessage(env.Message) {
			stats.SidechainMessagesSkipped++
			recordClaudeSidechainAgent(stats, env.AgentID)
		}
		return claudeEntry{}, true
	}
	if subtype == "compact_boundary" || entryType == "tombstone" || entryType == "content-replacement" {
		stats.MetadataEntries++
		if subtype == "compact_boundary" {
			stats.CompactBoundaries++
		}
		if entryType == "tombstone" {
			stats.Tombstones++
		}
		return claudeEntry{Node: node, Kind: claudeEntryMetadata, PreservedTailUUID: parsePreservedTailUUID(env.Content), TombstoneTargetUUID: parseTombstoneTargetUUID(env.Content)}, false
	}
	if !hasVisibleClaudeMessage(env.Message) {
		stats.MetadataEntries++
		return claudeEntry{Node: node, Kind: claudeEntryUnsupported}, false
	}
	var msg claudeMessage
	if err := json.Unmarshal(env.Message, &msg); err != nil {
		stats.MetadataEntries++
		return claudeEntry{Node: node, Kind: claudeEntryUnsupported}, false
	}
	role := strings.ToLower(strings.TrimSpace(msg.Role))
	if role == "assistant" || role == "user" {
		stats.NonSidechainMessages++
		return claudeEntry{Node: node, Kind: claudeEntryVisibleMessage, Role: role}, false
	}
	stats.MetadataEntries++
	return claudeEntry{Node: node, Kind: claudeEntryUnsupported, Role: role}, false
}

func recordClaudeSidechainAgent(stats *ClaudeImportReport, agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	if slices.Contains(stats.SidechainAgentIDs, agentID) {
		return
	}
	stats.SidechainAgentIDs = append(stats.SidechainAgentIDs, agentID)
}

func isClaudeUnsupportedVisibleNode(node claudeNode) bool {
	if !hasVisibleClaudeMessage(node.Envelope.Message) {
		return false
	}
	var cm claudeMessage
	if err := json.Unmarshal(node.Envelope.Message, &cm); err != nil {
		return true
	}
	role := strings.ToLower(strings.TrimSpace(cm.Role))
	return role != "assistant" && role != "user"
}

func convertClaudeUnsupportedEntries(entries []claudeEntry, report *ImportReport) ([]message.Message, bool) {
	msgs := make([]message.Message, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind != claudeEntryUnsupported || !hasVisibleClaudeMessage(entry.Node.Envelope.Message) {
			continue
		}
		var cm claudeMessage
		if err := json.Unmarshal(entry.Node.Envelope.Message, &cm); err != nil {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(cm.Role))
		msg := renderClaudeUnsupportedMessage(entry.Node.Envelope, role)
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		report.Claude.DowngradedVisibleEntries++
		report.warnf("claude line %d: unsupported role=%s; imported as readable fallback", entry.Node.Index, role)
		report.Claude.Diagnostics = append(report.Claude.Diagnostics, fmt.Sprintf("line %d: unsupported role=%s; imported as readable fallback", entry.Node.Index, role))
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		return nil, false
	}
	return msgs, true
}

func selectClaudeMainChain(entries []claudeEntry, stats *ClaudeImportReport) ([]claudeNode, []string, error) {
	tombstoned := make(map[string]struct{})
	for _, entry := range entries {
		if entry.TombstoneTargetUUID != "" {
			tombstoned[entry.TombstoneTargetUUID] = struct{}{}
		}
	}

	byUUID := make(map[string]claudeEntry, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.Node.Envelope.UUID)
		if id == "" {
			continue
		}
		if _, removed := tombstoned[id]; removed {
			continue
		}
		byUUID[id] = entry
	}
	conversationChildren := make(map[string]int)
	for _, entry := range entries {
		if isClaudeCompactBoundary(entry) {
			for _, parentID := range []string{strings.TrimSpace(entry.Node.Envelope.ParentUUID), strings.TrimSpace(entry.Node.Envelope.LogicalParentUUID)} {
				if parentID != "" {
					conversationChildren[parentID]++
				}
			}
			continue
		}
		id := strings.TrimSpace(entry.Node.Envelope.UUID)
		if id == "" || !isClaudeConversationEntry(entry) {
			continue
		}
		if _, removed := tombstoned[id]; removed {
			continue
		}
		if parent, ok := nearestClaudeConversationParent(entry, byUUID); ok {
			parentID := strings.TrimSpace(parent.Node.Envelope.UUID)
			if parentID != "" {
				conversationChildren[parentID]++
			}
		}
	}

	leaves := make([]claudeEntry, 0)
	for _, entry := range entries {
		if !isClaudeConversationEntry(entry) {
			continue
		}
		id := strings.TrimSpace(entry.Node.Envelope.UUID)
		if id == "" {
			continue
		}
		if _, removed := tombstoned[id]; removed {
			continue
		}
		if conversationChildren[id] == 0 {
			leaves = append(leaves, entry)
		}
	}
	if len(leaves) == 0 {
		for _, entry := range entries {
			if isClaudeConversationEntry(entry) {
				id := strings.TrimSpace(entry.Node.Envelope.UUID)
				if _, removed := tombstoned[id]; !removed {
					leaves = append(leaves, entry)
				}
			}
		}
	}
	if len(leaves) == 0 {
		return nil, nil, fmt.Errorf("claude import: could not identify a non-sidechain transcript leaf")
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].Node.Index < leaves[j].Node.Index })
	stats.TerminalCandidates = len(leaves)

	candidates := make([]claudeChainCandidate, 0, len(leaves))
	for _, leaf := range leaves {
		candidates = append(candidates, buildClaudeCandidate(leaf, byUUID))
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if compareClaudeCandidates(candidate, best) > 0 {
			best = candidate
		}
	}
	stats.SelectedSpanLength = best.MessageCount
	stats.SelectedLeafUUID = best.LeafUUID
	stats.SelectionReason = describeClaudeCandidateSelection(best)

	warnings := make([]string, 0, 2)
	if len(leaves) > 1 {
		warnings = append(warnings, fmt.Sprintf("terminal candidates considered: %d; selected leaf %s (%s)", len(leaves), best.LeafUUID, stats.SelectionReason))
	}
	if best.ParentMissing > 0 || best.OrphanToolCalls > 0 || best.OrphanToolResults > 0 {
		warnings = append(warnings, fmt.Sprintf("selected chain has parent_missing=%d orphan_tool_calls=%d orphan_tool_results=%d", best.ParentMissing, best.OrphanToolCalls, best.OrphanToolResults))
	}
	return best.Nodes, warnings, nil
}

func isClaudeConversationEntry(entry claudeEntry) bool {
	return entry.Kind == claudeEntryVisibleMessage || (entry.Kind == claudeEntryUnsupported && hasVisibleClaudeMessage(entry.Node.Envelope.Message))
}

func isClaudeCompactBoundary(entry claudeEntry) bool {
	return strings.ToLower(strings.TrimSpace(entry.Node.Envelope.Subtype)) == "compact_boundary"
}

func buildClaudeCandidate(leaf claudeEntry, byUUID map[string]claudeEntry) claudeChainCandidate {
	candidate := claudeChainCandidate{LeafUUID: strings.TrimSpace(leaf.Node.Envelope.UUID)}
	var rev []claudeNode
	seen := map[string]struct{}{}
	seenToolCalls := map[string]struct{}{}
	seenToolResults := map[string]struct{}{}
	cur := leaf
	for {
		if isClaudeConversationEntry(cur) {
			rev = append(rev, cur.Node)
			candidate.MessageCount++
			candidate.LatestIndex = max(candidate.LatestIndex, cur.Node.Index)
			if candidate.MessageCount == 1 {
				candidate.LastRole = cur.Role
			}
			if cur.Kind == claudeEntryVisibleMessage {
				countClaudeToolConsistency(cur.Node.Envelope.Message, seenToolCalls, seenToolResults)
			}
		}
		id := strings.TrimSpace(cur.Node.Envelope.UUID)
		if id != "" {
			seen[id] = struct{}{}
		}
		next, ok, bridged := nextClaudeParent(cur, byUUID)
		if !ok {
			break
		}
		if bridged {
			candidate.IncludesPreservedSegments = true
		}
		parentID := strings.TrimSpace(next.Node.Envelope.UUID)
		if parentID != "" {
			if _, dup := seen[parentID]; dup {
				break
			}
		}
		cur = next
	}
	parent := strings.TrimSpace(cur.Node.Envelope.ParentUUID)
	logicalParent := strings.TrimSpace(cur.Node.Envelope.LogicalParentUUID)
	if (parent != "" || logicalParent != "") && !isClaudeCompactBoundary(cur) {
		if _, ok, _ := nextClaudeParent(cur, byUUID); !ok {
			candidate.ParentMissing++
		}
	}
	candidate.OrphanToolResults = 0
	for id := range seenToolResults {
		if _, ok := seenToolCalls[id]; !ok {
			candidate.OrphanToolResults++
		}
	}
	candidate.OrphanToolCalls = 0
	for id := range seenToolCalls {
		if _, ok := seenToolResults[id]; !ok {
			candidate.OrphanToolCalls++
		}
	}
	candidate.PendingLeafToolCalls = countClaudeToolUseBlocks(leaf.Node.Envelope.Message)
	if leaf.Role == "assistant" && candidate.PendingLeafToolCalls > 0 {
		candidate.OrphanToolCalls = max(0, candidate.OrphanToolCalls-candidate.PendingLeafToolCalls)
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	candidate.Nodes = rev
	return candidate
}

func compareClaudeCandidates(a, b claudeChainCandidate) int {
	aTier1 := a.ParentMissing == 0 && a.OrphanToolCalls == 0 && a.OrphanToolResults == 0
	bTier1 := b.ParentMissing == 0 && b.OrphanToolCalls == 0 && b.OrphanToolResults == 0
	if aTier1 != bTier1 {
		if aTier1 {
			return 1
		}
		return -1
	}
	if a.MessageCount != b.MessageCount {
		if a.MessageCount > b.MessageCount {
			return 1
		}
		return -1
	}
	if a.LatestIndex != b.LatestIndex {
		if a.LatestIndex > b.LatestIndex {
			return 1
		}
		return -1
	}
	aAssistantEnd := a.LastRole == "assistant"
	bAssistantEnd := b.LastRole == "assistant"
	if aAssistantEnd != bAssistantEnd {
		if aAssistantEnd {
			return 1
		}
		return -1
	}
	if a.IncludesPreservedSegments != b.IncludesPreservedSegments {
		if a.IncludesPreservedSegments {
			return 1
		}
		return -1
	}
	return strings.Compare(a.LeafUUID, b.LeafUUID)
}

func describeClaudeCandidateSelection(c claudeChainCandidate) string {
	parts := []string{fmt.Sprintf("messages=%d", c.MessageCount)}
	if c.ParentMissing == 0 && c.OrphanToolCalls == 0 && c.OrphanToolResults == 0 {
		parts = append(parts, "tier1=complete")
	} else {
		parts = append(parts, fmt.Sprintf("tier1=parent_missing:%d orphan_tool_calls:%d orphan_tool_results:%d", c.ParentMissing, c.OrphanToolCalls, c.OrphanToolResults))
	}
	if c.IncludesPreservedSegments {
		parts = append(parts, "preserved_segment=true")
	}
	return strings.Join(parts, ", ")
}

func nextClaudeParent(entry claudeEntry, byUUID map[string]claudeEntry) (claudeEntry, bool, bool) {
	env := entry.Node.Envelope
	if strings.ToLower(strings.TrimSpace(env.Subtype)) == "compact_boundary" {
		if tail := strings.TrimSpace(entry.PreservedTailUUID); tail != "" {
			if next, ok := byUUID[tail]; ok {
				return next, true, true
			}
		}
		return claudeEntry{}, false, false
	}
	for _, parent := range []string{strings.TrimSpace(env.ParentUUID), strings.TrimSpace(env.LogicalParentUUID)} {
		if parent == "" {
			continue
		}
		if next, ok := byUUID[parent]; ok {
			return next, true, false
		}
	}
	return claudeEntry{}, false, false
}

func nearestClaudeConversationParent(entry claudeEntry, byUUID map[string]claudeEntry) (claudeEntry, bool) {
	seen := map[string]struct{}{}
	cur := entry
	for {
		next, ok, _ := nextClaudeParent(cur, byUUID)
		if !ok {
			return claudeEntry{}, false
		}
		id := strings.TrimSpace(next.Node.Envelope.UUID)
		if id != "" {
			if _, dup := seen[id]; dup {
				return claudeEntry{}, false
			}
			seen[id] = struct{}{}
		}
		if isClaudeConversationEntry(next) {
			return next, true
		}
		cur = next
	}
}

func countClaudeToolConsistency(raw json.RawMessage, seenToolCalls map[string]struct{}, seenToolResults map[string]struct{}) {
	var cm claudeMessage
	if err := json.Unmarshal(raw, &cm); err != nil {
		return
	}
	for _, blockRaw := range cm.Content {
		var block claudeContentBlock
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "tool_use":
			id := strings.TrimSpace(block.ID)
			if id == "" {
				continue
			}
			seenToolCalls[id] = struct{}{}
		case "tool_result":
			id := strings.TrimSpace(block.ToolUseID)
			if id == "" {
				continue
			}
			seenToolResults[id] = struct{}{}
		}
	}
}

func countClaudeToolUseBlocks(raw json.RawMessage) int {
	var cm claudeMessage
	if err := json.Unmarshal(raw, &cm); err != nil {
		return 0
	}
	count := 0
	for _, blockRaw := range cm.Content {
		var block claudeContentBlock
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(block.Type)) == "tool_use" && strings.TrimSpace(block.ID) != "" {
			count++
		}
	}
	return count
}

func convertClaudeNode(node claudeNode, toolMode string, reasoningMode string) (msgs []message.Message, skipped bool, toolRendered bool, reasoningSkipped bool, warns []string, err error) {
	var cm claudeMessage
	if err := json.Unmarshal(node.Envelope.Message, &cm); err != nil {
		return nil, false, false, false, nil, fmt.Errorf("parse message: %w", err)
	}
	role := strings.ToLower(strings.TrimSpace(cm.Role))
	if role != "assistant" && role != "user" {
		fallback := renderClaudeUnsupportedMessage(node.Envelope, role)
		if strings.TrimSpace(fallback.Content) == "" {
			return nil, true, false, false, []string{"skipped unsupported role=" + role}, nil
		}
		return []message.Message{fallback}, false, false, false, []string{"unsupported role=" + role + "; imported as readable fallback"}, nil
	}

	switch role {
	case "assistant":
		assistant := message.Message{Role: "assistant", Provenance: importedClaudeProvenance()}
		for _, raw := range cm.Content {
			var block claudeContentBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				warns = append(warns, "skipped malformed content block")
				continue
			}
			switch strings.ToLower(strings.TrimSpace(block.Type)) {
			case "text":
				assistant.Content = joinNonEmpty(assistant.Content, block.Text)
			case "thinking":
				if reasoningMode == ReasoningOff {
					reasoningSkipped = true
					continue
				}
				if strings.TrimSpace(block.Signature) == "" {
					reasoningSkipped = true
					if reasoningMode == ReasoningVisible {
						assistant.Content = joinNonEmpty(assistant.Content, "[Imported reasoning]", block.Thinking)
					}
					continue
				}
				assistant.ThinkingBlocks = append(assistant.ThinkingBlocks, message.ThinkingBlock{Thinking: block.Thinking, Signature: block.Signature})
			case "redacted_thinking":
				reasoningSkipped = true
				warns = append(warns, "redacted thinking was not imported")
			case "tool_use":
				if toolMode == ToolModeText {
					assistant.Content = joinNonEmpty(assistant.Content, renderImportedToolMarker("tool call", raw))
					toolRendered = true
					continue
				}
				if strings.TrimSpace(block.ID) == "" {
					warns = append(warns, "tool_use missing id; imported as text")
					assistant.Content = joinNonEmpty(assistant.Content, renderImportedToolMarker("tool call", raw))
					toolRendered = true
					continue
				}
				toolCall, unsupported := convertClaudeToolCall(block)
				if unsupported {
					warns = append(warns, "unsupported tool_use name="+strings.TrimSpace(block.Name)+"; imported as tool card")
				}
				assistant.ToolCalls = append(assistant.ToolCalls, toolCall)

			default:
				warns = append(warns, "unsupported assistant content block type="+block.Type)
			}
		}
		if strings.TrimSpace(assistant.Content) == "" && len(assistant.ToolCalls) == 0 && len(assistant.ThinkingBlocks) == 0 {
			return nil, true, toolRendered, reasoningSkipped, warns, nil
		}
		return []message.Message{assistant}, false, toolRendered, reasoningSkipped, warns, nil

	case "user":
		user := message.Message{Role: "user", Provenance: importedClaudeProvenance()}
		var toolMsgs []message.Message
		for _, raw := range cm.Content {
			var block claudeContentBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				warns = append(warns, "skipped malformed content block")
				continue
			}
			switch strings.ToLower(strings.TrimSpace(block.Type)) {
			case "text":
				user.Content = joinNonEmpty(user.Content, block.Text)
			case "tool_result":
				if toolMode == ToolModeText {
					user.Content = joinNonEmpty(user.Content, renderImportedToolMarker("tool result", raw))
					toolRendered = true
					continue
				}
				if strings.TrimSpace(block.ToolUseID) == "" {
					warns = append(warns, "tool_result missing tool_use_id; imported as text")
					user.Content = joinNonEmpty(user.Content, renderImportedToolMarker("tool result", raw))
					toolRendered = true
					continue
				}
				toolMsgs = append(toolMsgs, message.Message{Role: "tool", ToolCallID: block.ToolUseID, Content: rawContentString(block.Content), Provenance: importedClaudeProvenance()})
			default:
				warns = append(warns, "unsupported user content block type="+block.Type)
			}
		}
		if strings.TrimSpace(user.Content) != "" {
			msgs = append(msgs, user)
		}
		msgs = append(msgs, toolMsgs...)
		if len(msgs) == 0 {
			return nil, true, toolRendered, reasoningSkipped, warns, nil
		}
		return msgs, false, toolRendered, reasoningSkipped, warns, nil
	}

	return nil, true, false, false, nil, nil
}

func convertClaudeToolCall(block claudeContentBlock) (message.ToolCall, bool) {
	name := strings.TrimSpace(block.Name)
	args := append(json.RawMessage(nil), block.Input...)
	switch name {
	case "Bash", "Shell":
		if norm := normalizeClaudeBashArgs(block.Input); norm != nil {
			return message.ToolCall{ID: block.ID, Name: "Shell", Args: norm}, false
		}
	case "Read":
		if norm := normalizeClaudeReadArgs(block.Input); norm != nil {
			return message.ToolCall{ID: block.ID, Name: "Read", Args: norm}, false
		}
	case "Write":
		if norm := normalizeClaudeWriteArgs(block.Input); norm != nil {
			return message.ToolCall{ID: block.ID, Name: "Write", Args: norm}, false
		}
	case "Delete", "Remove":
		if norm := normalizeClaudeDeleteArgs(block.Input); norm != nil {
			return message.ToolCall{ID: block.ID, Name: "Delete", Args: norm}, false
		}
	case "Edit", "MultiEdit", "Update":
		if norm := normalizeClaudeApplyPatchArgs(name, block.Input); norm != nil {
			return message.ToolCall{ID: block.ID, Name: "ApplyPatch", Args: norm}, false
		}
	}
	if name == "" {
		name = "unknown"
	}
	unsupportedArgs := map[string]any{
		"unsupported": true,
		"source":      "claude",
		"reason":      "no safe Chord mapping",
	}
	if len(args) > 0 {
		var v any
		if json.Unmarshal(args, &v) == nil {
			unsupportedArgs["arguments"] = v
		} else {
			unsupportedArgs["arguments"] = string(args)
		}
	}
	b, _ := json.Marshal(unsupportedArgs)
	return message.ToolCall{ID: block.ID, Name: name, Args: b}, true
}

func normalizeClaudeBashArgs(raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	command := claudePickString(args, "command")
	if command == "" {
		return nil
	}
	result := map[string]any{"command": command}
	if description := claudePickString(args, "description"); description != "" {
		result["description"] = description
	} else {
		result["description"] = "Imported Claude Bash command"
	}
	b, _ := json.Marshal(result)
	return b
}

func normalizeClaudeReadArgs(raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	path := claudePickString(args, "file_path", "path")
	if path == "" {
		return nil
	}
	result := map[string]any{"path": path}
	if offset := claudePickFloat(args, "offset", "start_line"); offset > 0 {
		result["offset"] = int(offset)
	}
	if limit := claudePickFloat(args, "limit"); limit > 0 {
		result["limit"] = int(limit)
	}
	b, _ := json.Marshal(result)
	return b
}

func claudeToolCallHasUnsupportedMetadata(raw json.RawMessage) bool {
	var args struct {
		Unsupported bool   `json:"unsupported"`
		Source      string `json:"source"`
	}
	return json.Unmarshal(raw, &args) == nil && args.Unsupported && args.Source == "claude"
}

func claudePickString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func claudePickStringList(m map[string]any, keys ...string) []string {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return []string{s}
			}
		case []any:
			out := make([]string, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func claudePickFloat(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			if v > 0 {
				return v
			}
		case int:
			if v > 0 {
				return float64(v)
			}
		}
	}
	return 0
}

func renderClaudeUnsupportedMessage(env claudeTranscriptEnvelope, role string) message.Message {
	content := strings.TrimSpace(rawContentString(env.Message))
	fields := make([]string, 0, 6)
	if role != "" {
		fields = append(fields, "Role: "+role)
	}
	if entryType := strings.TrimSpace(env.Type); entryType != "" {
		fields = append(fields, "Type: "+entryType)
	}
	if subtype := strings.TrimSpace(env.Subtype); subtype != "" {
		fields = append(fields, "Subtype: "+subtype)
	}
	fields = append(fields, "Reason: no safe Chord mapping")
	if content != "" {
		fields = append(fields, "Content:", content)
	}
	return message.Message{Role: "assistant", Content: renderImportedFallbackBlock("[Imported Claude transcript entry]", fields...), Provenance: importedClaudeProvenance()}
}

func normalizeClaudeWriteArgs(raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	path := claudePickString(args, "path", "file_path", "file")
	content := claudePickString(args, "content", "text")
	if path == "" {
		return nil
	}
	result := map[string]any{"path": path, "content": content}
	b, _ := json.Marshal(result)
	return b
}

func normalizeClaudeDeleteArgs(raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	paths := claudePickStringList(args, "paths", "path", "file_path", "file")
	if len(paths) == 0 {
		return nil
	}
	reason := claudePickString(args, "reason")
	if reason == "" {
		reason = "Imported Claude file deletion"
	}
	result := map[string]any{"paths": paths, "reason": reason}
	b, _ := json.Marshal(result)
	return b
}

func normalizeClaudeApplyPatchArgs(name string, raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	if strings.EqualFold(name, "Update") {
		if patch := claudePickString(args, "patch", "input"); patch != "" {
			path, body, ok := splitImportedApplyPatchEnvelope(patch)
			if !ok {
				return nil
			}
			b, _ := json.Marshal(map[string]any{"path": path, "patch": body})
			return b
		}
	}
	path := claudePickString(args, "path", "file_path", "file")
	if path == "" {
		return nil
	}
	if strings.EqualFold(name, "MultiEdit") {
		edits, ok := args["edits"].([]any)
		if !ok || len(edits) == 0 {
			return nil
		}
		var b strings.Builder
		for _, rawEdit := range edits {
			edit, ok := rawEdit.(map[string]any)
			if !ok {
				return nil
			}
			oldText := claudePickString(edit, "old_string", "old", "old_text")
			newText := claudePickString(edit, "new_string", "new", "new_text")
			if oldText == "" {
				return nil
			}
			b.WriteString("@@\n")
			writePatchLines(&b, "-", oldText)
			writePatchLines(&b, "+", newText)
		}
		out, _ := json.Marshal(map[string]any{"path": path, "patch": b.String()})
		return out
	}
	oldText := claudePickString(args, "old_string", "old", "old_text")
	newText := claudePickString(args, "new_string", "new", "new_text")
	if oldText == "" {
		return nil
	}
	patch := buildSingleUpdatePatch(oldText, newText)
	out, _ := json.Marshal(map[string]any{"path": path, "patch": patch})
	return out
}

func rawContentString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return s
	}
	return string(trimmed)
}

func hasVisibleClaudeMessage(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func parsePreservedTailUUID(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var payload struct {
		PreservedSegment *struct {
			TailUUID string `json:"tailUuid"`
		} `json:"preservedSegment"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.PreservedSegment == nil {
		return ""
	}
	return strings.TrimSpace(payload.PreservedSegment.TailUUID)
}

func parseTombstoneTargetUUID(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var payload struct {
		UUID          string `json:"uuid"`
		TargetUUID    string `json:"targetUuid"`
		MessageUUID   string `json:"messageUuid"`
		TombstoneUUID string `json:"tombstoneUuid"`
		Target        *struct {
			UUID string `json:"uuid"`
		} `json:"target"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	for _, id := range []string{payload.TargetUUID, payload.MessageUUID, payload.TombstoneUUID, payload.UUID} {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			return trimmed
		}
	}
	if payload.Target != nil {
		return strings.TrimSpace(payload.Target.UUID)
	}
	return ""
}
