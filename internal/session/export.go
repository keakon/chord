package session

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
)

// ---------------------------------------------------------------------------
// Export format types
// ---------------------------------------------------------------------------

// CurrentVersion is the format version for exported sessions.
const CurrentVersion = "1"

// ExportedSession is the top-level structure for a session export file.
type ExportedSession struct {
	Version   string            `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	Messages  []ExportedMessage `json:"messages"`
	Stats     *SessionStats     `json:"stats,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ExportedMessage is a simplified representation of a conversation message
// suitable for cross-device migration and offline inspection.
//
// Note: multi-part user attachments (e.g. image ContentParts) are intentionally
// not embedded in export files; only the plain message Content is exported.
type ExportedMessage struct {
	Role            string                  `json:"role"`
	Content         string                  `json:"content"`
	ToolCallID      string                  `json:"tool_call_id,omitempty"`
	ToolDiff        string                  `json:"tool_diff,omitempty"`         // unified diff for Write/Edit results
	ToolDiffAdded   int                     `json:"tool_diff_added,omitempty"`   // full added-line count before diff truncation
	ToolDiffRemoved int                     `json:"tool_diff_removed,omitempty"` // full removed-line count before diff truncation
	ToolDurationMs  int64                   `json:"tool_duration_ms,omitempty"`
	LSPReviews      []message.LSPReview     `json:"lsp_reviews,omitempty"`
	Audit           *message.ToolArgsAudit  `json:"audit,omitempty"`
	ToolCalls       []ExportedToolCall      `json:"tool_calls,omitempty"`
	ThinkingBlocks  []message.ThinkingBlock `json:"thinking_blocks,omitempty"`
	Timestamp       time.Time               `json:"timestamp,omitempty"`
}

// ExportedToolCall is a simplified tool call representation for export.
// The Args field is stored as a raw JSON string rather than json.RawMessage
// so the export file is human-readable.
type ExportedToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// SessionStats holds aggregated usage statistics for a session.
// This is defined in the session package to avoid a dependency on the
// analytics package (which may not exist yet). When analytics is available,
// callers can convert from analytics.SessionStats to this type.
type SessionStats struct {
	InputTokens      int64              `json:"input_tokens"`
	OutputTokens     int64              `json:"output_tokens"`
	CacheReadTokens  int64              `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64              `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64              `json:"reasoning_tokens,omitempty"`
	LLMCalls         int64              `json:"llm_calls"`
	EstimatedCost    float64            `json:"estimated_cost"`
	ByModel          map[string]float64 `json:"by_model,omitempty"`
}

// ---------------------------------------------------------------------------
// Export functions
// ---------------------------------------------------------------------------

// Export creates an ExportedSession snapshot from the given messages and
// optional metadata. The stats parameter is optional (may be nil).
//
// The metadata map can include arbitrary key-value pairs such as:
//   - "model": the LLM model name
//   - "project_path": the project root directory
//   - session_id: persistent session directory name (<state>/sessions/<project-key>/<id>)
//   - "instance_id": main agent instance id (e.g. main-1); distinct from session_id
//   - "chord_version": the build version
func Export(
	messages []message.Message,
	stats *SessionStats,
	metadata map[string]string,
) (*ExportedSession, error) {
	if messages == nil {
		messages = []message.Message{}
	}

	exported := &ExportedSession{
		Version:   CurrentVersion,
		CreatedAt: time.Now().UTC(),
		Stats:     stats,
		Metadata:  metadata,
		Messages:  make([]ExportedMessage, 0, len(messages)),
	}

	now := time.Now().UTC()
	for i, msg := range messages {
		em := ExportedMessage{
			Role:            msg.Role,
			Content:         msg.Content,
			ToolCallID:      msg.ToolCallID,
			ToolDiff:        msg.ToolDiff,
			ToolDiffAdded:   msg.ToolDiffAdded,
			ToolDiffRemoved: msg.ToolDiffRemoved,
			ToolDurationMs:  msg.ToolDurationMs,
			LSPReviews:      append([]message.LSPReview(nil), msg.LSPReviews...),
			Audit:           msg.Audit.Clone(),
			ThinkingBlocks:  msg.ThinkingBlocks,
			// Use incremental timestamps (1µs apart) to preserve ordering
			// since source messages don't carry original timestamps.
			Timestamp: now.Add(time.Duration(i) * time.Microsecond),
		}

		// Convert tool calls to simplified format.
		for _, tc := range msg.ToolCalls {
			em.ToolCalls = append(em.ToolCalls, ExportedToolCall{
				ID:   tc.ID,
				Name: tc.Name,
				Args: string(tc.Args),
			})
		}

		exported.Messages = append(exported.Messages, em)
	}

	return exported, nil
}

// ExportToFile serialises the session to a JSON file at the given path.
// The JSON is pretty-printed for human readability. Parent directories are
// NOT created automatically — the caller should ensure they exist.
func ExportToFile(session *ExportedSession, path string) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Plain-text export (unified with TUI card copy via convformat)
// ---------------------------------------------------------------------------

// ExportToMarkdown converts an ExportedSession to plain text with the same
// label style as TUI card copy (User:, Assistant:, Thinking:, TOOL CALL (name):,
// TOOL RESULT (name):). No emoji or Markdown — one format for both people and models.
func ExportToMarkdown(session *ExportedSession) string {
	if session == nil {
		return ""
	}

	var sb strings.Builder

	// Header (plain lines)
	sb.WriteString("Session Export\n\n")
	if model, ok := session.Metadata["model"]; ok && model != "" {
		fmt.Fprintf(&sb, "Model: %s\n", model)
	}
	fmt.Fprintf(&sb, "Date: %s\n", session.CreatedAt.Format(time.RFC3339))
	if project, ok := session.Metadata["project_path"]; ok && project != "" {
		fmt.Fprintf(&sb, "Project: %s\n", project)
	}
	if session.Stats != nil && session.Stats.EstimatedCost > 0 {
		fmt.Fprintf(&sb, "Cost: $%.4f\n", session.Stats.EstimatedCost)
	}
	if id, ok := session.Metadata["session_id"]; ok && id != "" {
		fmt.Fprintf(&sb, "Session ID: %s\n", id)
	} else if id, ok := session.Metadata["instance_id"]; ok && id != "" {
		fmt.Fprintf(&sb, "Session ID: %s\n", id)
	}
	sb.WriteString(convformat.BlockSep)

	toolCallNames := make(map[string]string)
	for _, em := range session.Messages {
		for _, tc := range em.ToolCalls {
			toolCallNames[tc.ID] = tc.Name
		}
	}

	var needSep bool
	for _, em := range session.Messages {
		switch em.Role {
		case "system":
			continue

		case "user":
			if needSep {
				sb.WriteString(convformat.BlockSep)
			}
			sb.WriteString(convformat.LabelUser)
			sb.WriteString("\n\n")
			sb.WriteString(em.Content)
			sb.WriteString("\n\n")
			needSep = true

		case "assistant":
			if needSep {
				sb.WriteString(convformat.BlockSep)
			}
			sb.WriteString(convformat.LabelAssistant)
			sb.WriteString("\n\n")
			for _, tb := range em.ThinkingBlocks {
				if tb.Thinking == "" {
					continue
				}
				sb.WriteString(convformat.LabelThinking)
				sb.WriteString("\n\n")
				sb.WriteString(tb.Thinking)
				sb.WriteString("\n\n")
			}
			if em.Content != "" {
				sb.WriteString(em.Content)
				sb.WriteString("\n\n")
			}
			for _, tc := range em.ToolCalls {
				sb.WriteString(convformat.ToolCallLabel(tc.Name))
				sb.WriteString("\n\n")
				if tc.Args != "" && tc.Args != "{}" {
					sb.WriteString(tc.Args)
					sb.WriteString("\n\n")
				}
			}
			needSep = true

		case "tool":
			if needSep {
				sb.WriteString(convformat.BlockSep)
			}
			toolName := toolCallNames[em.ToolCallID]
			if toolName == "" {
				toolName = "unknown"
			}
			sb.WriteString(convformat.ToolResultLabel(toolName))
			sb.WriteString("\n\n")
			sb.WriteString(em.Content)
			if em.Content != "" && !strings.HasSuffix(em.Content, "\n") {
				sb.WriteString("\n")
			}
			if em.ToolDiff != "" {
				sb.WriteString("\nDiff:\n")
				sb.WriteString(em.ToolDiff)
				if !strings.HasSuffix(em.ToolDiff, "\n") {
					sb.WriteString("\n")
				}
			}
			sb.WriteString("\n")
			needSep = true
		}
	}

	if session.Stats != nil {
		sb.WriteString(convformat.BlockSep)
		sb.WriteString("Session Statistics\n\n")
		fmt.Fprintf(&sb, "Input Tokens: %d\n", session.Stats.InputTokens)
		fmt.Fprintf(&sb, "Output Tokens: %d\n", session.Stats.OutputTokens)
		if session.Stats.CacheReadTokens > 0 {
			fmt.Fprintf(&sb, "Cache Read Tokens: %d\n", session.Stats.CacheReadTokens)
		}
		if session.Stats.CacheWriteTokens > 0 {
			fmt.Fprintf(&sb, "Cache Write Tokens: %d\n", session.Stats.CacheWriteTokens)
		}
		if session.Stats.ReasoningTokens > 0 {
			fmt.Fprintf(&sb, "Reasoning Tokens: %d\n", session.Stats.ReasoningTokens)
		}
		fmt.Fprintf(&sb, "LLM Calls: %d\n", session.Stats.LLMCalls)
		fmt.Fprintf(&sb, "Estimated Cost: $%.4f\n", session.Stats.EstimatedCost)
	}

	return sb.String()
}

// ExportMarkdownToFile renders the session as Markdown and writes it to the
// given path. Parent directories are NOT created — the caller should ensure
// they exist.
func ExportMarkdownToFile(session *ExportedSession, path string) error {
	if session == nil {
		return fmt.Errorf("session is nil")
	}
	md := ExportToMarkdown(session)
	if err := os.WriteFile(path, []byte(md), 0644); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}
	return nil
}

// ToMessages converts the exported messages back to internal Message format.
// Tool calls are restored from their simplified representation.
func (es *ExportedSession) ToMessages() []message.Message {
	if es == nil || len(es.Messages) == 0 {
		return nil
	}

	msgs := make([]message.Message, 0, len(es.Messages))
	for _, em := range es.Messages {
		msg := message.Message{
			Role:            em.Role,
			Content:         em.Content,
			ToolCallID:      em.ToolCallID,
			ToolDiff:        em.ToolDiff,
			ToolDiffAdded:   em.ToolDiffAdded,
			ToolDiffRemoved: em.ToolDiffRemoved,
			ToolDurationMs:  em.ToolDurationMs,
			LSPReviews:      append([]message.LSPReview(nil), em.LSPReviews...),
			Audit:           em.Audit.Clone(),
			ThinkingBlocks:  em.ThinkingBlocks,
		}

		// Restore tool calls.
		for _, etc := range em.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, message.ToolCall{
				ID:   etc.ID,
				Name: etc.Name,
				Args: json.RawMessage(etc.Args),
			})
		}

		msgs = append(msgs, msg)
	}

	return msgs
}
