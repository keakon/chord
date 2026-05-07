package sessionimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/message"
)

type claudeTranscriptEnvelope struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
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

func convertClaudeTranscript(data []byte, toolMode string, reasoningMode string, report *ImportReport) ([]message.Message, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("claude import: empty input")
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	var nodes []claudeNode
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
		if len(bytes.TrimSpace(env.Message)) == 0 {
			report.SkippedEntries++
			continue
		}
		nodes = append(nodes, claudeNode{Envelope: env, Index: lineNo})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("claude import: scan JSONL: %w", err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("claude import: no transcript messages found")
	}

	chain, warnings, err := selectClaudeMainChain(nodes)
	for _, w := range warnings {
		report.warnf("claude: %s", w)
	}
	if err != nil {
		return nil, err
	}

	var out []message.Message
	for _, node := range chain {
		msg, skipped, toolRendered, reasoningSkipped, warns, err := convertClaudeNode(node, toolMode, reasoningMode)
		for _, w := range warns {
			report.warnf("claude line %d: %s", node.Index, w)
		}
		if err != nil {
			return nil, fmt.Errorf("claude import: line %d: %w", node.Index, err)
		}
		if toolRendered {
			report.ToolEntriesRendered++
		}
		if reasoningSkipped {
			report.ReasoningBlocksSkipped++
		}
		if skipped {
			report.SkippedEntries++
			continue
		}
		out = append(out, msg...)
	}
	return out, nil
}

func selectClaudeMainChain(nodes []claudeNode) ([]claudeNode, []string, error) {
	byUUID := make(map[string]claudeNode, len(nodes))
	children := make(map[string]int)
	for _, node := range nodes {
		id := strings.TrimSpace(node.Envelope.UUID)
		if id == "" {
			continue
		}
		byUUID[id] = node
		parent := strings.TrimSpace(node.Envelope.ParentUUID)
		if parent != "" {
			children[parent]++
		}
	}

	leaves := make([]claudeNode, 0)
	for _, node := range nodes {
		id := strings.TrimSpace(node.Envelope.UUID)
		if id == "" {
			continue
		}
		if children[id] == 0 && !node.Envelope.IsSidechain {
			leaves = append(leaves, node)
		}
	}
	if len(leaves) == 0 {
		for _, node := range nodes {
			if !node.Envelope.IsSidechain {
				leaves = append(leaves, node)
			}
		}
	}
	if len(leaves) == 0 {
		return nil, nil, fmt.Errorf("claude import: could not identify a non-sidechain transcript leaf")
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].Index < leaves[j].Index })
	leaf := leaves[len(leaves)-1]
	if len(leaves) > 1 {
		return walkClaudeChain(leaf, byUUID), []string{fmt.Sprintf("multiple candidate leaves detected (%d); imported latest non-sidechain leaf %s", len(leaves), leaf.Envelope.UUID)}, nil
	}
	return walkClaudeChain(leaf, byUUID), nil, nil
}

func walkClaudeChain(leaf claudeNode, byUUID map[string]claudeNode) []claudeNode {
	var rev []claudeNode
	seen := map[string]struct{}{}
	cur := leaf
	for {
		rev = append(rev, cur)
		id := strings.TrimSpace(cur.Envelope.UUID)
		if id != "" {
			seen[id] = struct{}{}
		}
		parent := strings.TrimSpace(cur.Envelope.ParentUUID)
		if parent == "" {
			break
		}
		next, ok := byUUID[parent]
		if !ok {
			break
		}
		if _, dup := seen[parent]; dup {
			break
		}
		cur = next
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

func convertClaudeNode(node claudeNode, toolMode string, reasoningMode string) (msgs []message.Message, skipped bool, toolRendered bool, reasoningSkipped bool, warns []string, err error) {
	var cm claudeMessage
	if err := json.Unmarshal(node.Envelope.Message, &cm); err != nil {
		return nil, false, false, false, nil, fmt.Errorf("parse message: %w", err)
	}
	role := strings.ToLower(strings.TrimSpace(cm.Role))
	if role != "assistant" && role != "user" {
		return nil, true, false, false, []string{"skipped unsupported role=" + role}, nil
	}

	switch role {
	case "assistant":
		assistant := message.Message{Role: "assistant", Provenance: &message.MessageProvenance{Source: "import:claude", Imported: true, WireFamily: "anthropic"}}
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
				assistant.ToolCalls = append(assistant.ToolCalls, message.ToolCall{ID: block.ID, Name: block.Name, Args: append(json.RawMessage(nil), block.Input...)})
			default:
				warns = append(warns, "unsupported assistant content block type="+block.Type)
			}
		}
		if toolMode == ToolModeAuto && len(assistant.ToolCalls) > 0 && len(assistant.ThinkingBlocks) == 0 {
			assistant = downgradeAssistantClaudeToolCalls(assistant)
			toolRendered = true
			warns = append(warns, "assistant tool calls downgraded to text because signed thinking was not complete")
		}
		if strings.TrimSpace(assistant.Content) == "" && len(assistant.ToolCalls) == 0 && len(assistant.ThinkingBlocks) == 0 {
			return nil, true, toolRendered, reasoningSkipped, warns, nil
		}
		return []message.Message{assistant}, false, toolRendered, reasoningSkipped, warns, nil

	case "user":
		user := message.Message{Role: "user", Provenance: &message.MessageProvenance{Source: "import:claude", Imported: true, WireFamily: "anthropic"}}
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
				toolMsgs = append(toolMsgs, message.Message{Role: "tool", ToolCallID: block.ToolUseID, Content: rawContentString(block.Content), Provenance: &message.MessageProvenance{Source: "import:claude", Imported: true, WireFamily: "anthropic"}})
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

func downgradeAssistantClaudeToolCalls(msg message.Message) message.Message {
	if len(msg.ToolCalls) == 0 {
		return msg
	}
	parts := []string{msg.Content}
	for _, tc := range msg.ToolCalls {
		parts = append(parts, joinNonEmpty("[Imported tool call: "+strings.TrimSpace(tc.Name)+"]", strings.TrimSpace(string(tc.Args))))
	}
	msg.Content = joinNonEmpty(parts...)
	msg.ToolCalls = nil
	return msg
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
