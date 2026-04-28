package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func sampleMessages() []message.Message {
	return []message.Message{
		{
			Role:    "user",
			Content: "Hello, can you help me with Go?",
		},
		{
			Role:    "assistant",
			Content: "Of course! What would you like to know?",
			ToolCalls: []message.ToolCall{
				{
					ID:   "call_001",
					Name: "Read",
					Args: json.RawMessage(`{"path":"/tmp/test.go"}`),
				},
			},
		},
		{
			Role:            "tool",
			Content:         "package main\n\nfunc main() {}",
			ToolCallID:      "call_001",
			ToolDiff:        "--- a.go\n+++ a.go\n+package main\n",
			ToolDiffAdded:   1,
			ToolDiffRemoved: 0,
			Audit: &message.ToolArgsAudit{
				OriginalArgsJSON:  `{"path":"/tmp/original.go"}`,
				EffectiveArgsJSON: `{"path":"/tmp/test.go"}`,
				UserModified:      true,
				EditSummary:       "adjusted target path",
			},
		},
		{
			Role:    "assistant",
			Content: "I can see this is a simple Go main package.",
		},
	}
}

func sampleStats() *SessionStats {
	return &SessionStats{
		InputTokens:   1500,
		OutputTokens:  800,
		LLMCalls:      3,
		EstimatedCost: 0.0125,
		ByModel: map[string]float64{
			"claude-opus-4.7": 0.0125,
		},
	}
}

func sampleMetadata() map[string]string {
	return map[string]string{
		"model":         "claude-opus-4.7",
		"project_path":  "/home/user/project",
		"chord_version": "0.1.0",
	}
}

// ---------------------------------------------------------------------------
// Export tests
// ---------------------------------------------------------------------------

func TestExportCreatesValidSession(t *testing.T) {
	msgs := sampleMessages()
	stats := sampleStats()
	meta := sampleMetadata()

	session, err := Export(msgs, stats, meta)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	if session.Version != CurrentVersion {
		t.Errorf("Version = %q, want %q", session.Version, CurrentVersion)
	}
	if session.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if len(session.Messages) != len(msgs) {
		t.Errorf("len(Messages) = %d, want %d", len(session.Messages), len(msgs))
	}
	if session.Stats == nil {
		t.Error("Stats is nil")
	}
	if session.Stats.InputTokens != 1500 {
		t.Errorf("Stats.InputTokens = %d, want 1500", session.Stats.InputTokens)
	}
	if session.Metadata["model"] != "claude-opus-4.7" {
		t.Errorf("Metadata[model] = %q, want %q", session.Metadata["model"], "claude-opus-4.7")
	}
}

func TestExportWithNilMessages(t *testing.T) {
	session, err := Export(nil, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	if session.Messages == nil {
		t.Error("Messages should be non-nil empty slice")
	}
	if len(session.Messages) != 0 {
		t.Errorf("len(Messages) = %d, want 0", len(session.Messages))
	}
}

func TestExportPreservesToolCalls(t *testing.T) {
	msgs := sampleMessages()
	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	// The second message (assistant) has a tool call.
	assistantMsg := session.Messages[1]
	if len(assistantMsg.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(assistantMsg.ToolCalls))
	}
	tc := assistantMsg.ToolCalls[0]
	if tc.ID != "call_001" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_001")
	}
	if tc.Name != "Read" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "Read")
	}
	if tc.Args != `{"path":"/tmp/test.go"}` {
		t.Errorf("ToolCall.Args = %q, unexpected", tc.Args)
	}
}

// ---------------------------------------------------------------------------
// File I/O tests (round-trip)
// ---------------------------------------------------------------------------

func TestRoundTripFileExportImport(t *testing.T) {
	msgs := sampleMessages()
	stats := sampleStats()
	meta := sampleMetadata()

	// Export.
	session, err := Export(msgs, stats, meta)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	// Write to temp file.
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session-test.json")

	if err := ExportToFile(session, path); err != nil {
		t.Fatalf("ExportToFile() error: %v", err)
	}

	// Verify file exists and is valid JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("exported file is empty")
	}
	if !json.Valid(data) {
		t.Fatal("exported file is not valid JSON")
	}

	// Import.
	imported, err := ImportFromFile(path)
	if err != nil {
		t.Fatalf("ImportFromFile() error: %v", err)
	}

	// Verify round-trip fidelity.
	if imported.Version != session.Version {
		t.Errorf("Version: got %q, want %q", imported.Version, session.Version)
	}
	if len(imported.Messages) != len(session.Messages) {
		t.Fatalf("len(Messages): got %d, want %d", len(imported.Messages), len(session.Messages))
	}

	// Check each message.
	for i, orig := range session.Messages {
		got := imported.Messages[i]
		if got.Role != orig.Role {
			t.Errorf("Messages[%d].Role: got %q, want %q", i, got.Role, orig.Role)
		}
		if got.Content != orig.Content {
			t.Errorf("Messages[%d].Content: got %q, want %q", i, got.Content, orig.Content)
		}
		if len(got.ToolCalls) != len(orig.ToolCalls) {
			t.Errorf("Messages[%d].ToolCalls: got %d, want %d", i, len(got.ToolCalls), len(orig.ToolCalls))
		}
		if (got.Audit == nil) != (orig.Audit == nil) {
			t.Errorf("Messages[%d].Audit nil mismatch: got %#v want %#v", i, got.Audit, orig.Audit)
		}
	}

	// Check stats round-trip.
	if imported.Stats == nil {
		t.Fatal("imported Stats is nil")
	}
	if imported.Stats.InputTokens != stats.InputTokens {
		t.Errorf("Stats.InputTokens: got %d, want %d", imported.Stats.InputTokens, stats.InputTokens)
	}
	if imported.Stats.OutputTokens != stats.OutputTokens {
		t.Errorf("Stats.OutputTokens: got %d, want %d", imported.Stats.OutputTokens, stats.OutputTokens)
	}
	if imported.Stats.LLMCalls != stats.LLMCalls {
		t.Errorf("Stats.LLMCalls: got %d, want %d", imported.Stats.LLMCalls, stats.LLMCalls)
	}

	// Check metadata round-trip.
	if imported.Metadata["model"] != meta["model"] {
		t.Errorf("Metadata[model]: got %q, want %q", imported.Metadata["model"], meta["model"])
	}
}

func TestToMessagesPreservesAudit(t *testing.T) {
	session := &ExportedSession{
		Version: CurrentVersion,
		Messages: []ExportedMessage{{
			Role:       "tool",
			Content:    "ok",
			ToolCallID: "tool-1",
			Audit: &message.ToolArgsAudit{
				OriginalArgsJSON:  `{"command":"pwd"}`,
				EffectiveArgsJSON: `{"command":"ls"}`,
				UserModified:      true,
			},
		}},
	}
	msgs := session.ToMessages()
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Audit == nil || !msgs[0].Audit.UserModified || msgs[0].Audit.EffectiveArgsJSON != `{"command":"ls"}` {
		t.Fatalf("msgs[0].Audit = %#v", msgs[0].Audit)
	}
}

func TestRoundTripToMessages(t *testing.T) {
	msgs := sampleMessages()

	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	// Convert back to internal messages.
	restored := session.ToMessages()
	if len(restored) != len(msgs) {
		t.Fatalf("len(restored) = %d, want %d", len(restored), len(msgs))
	}

	for i, orig := range msgs {
		got := restored[i]
		if got.Role != orig.Role {
			t.Errorf("restored[%d].Role: got %q, want %q", i, got.Role, orig.Role)
		}
		if got.Content != orig.Content {
			t.Errorf("restored[%d].Content: got %q, want %q", i, got.Content, orig.Content)
		}
		if got.ToolDiff != orig.ToolDiff {
			t.Errorf("restored[%d].ToolDiff: got %q, want %q", i, got.ToolDiff, orig.ToolDiff)
		}
		if got.ToolDiffAdded != orig.ToolDiffAdded {
			t.Errorf("restored[%d].ToolDiffAdded: got %d, want %d", i, got.ToolDiffAdded, orig.ToolDiffAdded)
		}
		if got.ToolDiffRemoved != orig.ToolDiffRemoved {
			t.Errorf("restored[%d].ToolDiffRemoved: got %d, want %d", i, got.ToolDiffRemoved, orig.ToolDiffRemoved)
		}
		if len(got.ToolCalls) != len(orig.ToolCalls) {
			t.Errorf("restored[%d].ToolCalls: got %d, want %d", i, len(got.ToolCalls), len(orig.ToolCalls))
			continue
		}
		for j, tc := range orig.ToolCalls {
			gotTC := got.ToolCalls[j]
			if gotTC.ID != tc.ID {
				t.Errorf("restored[%d].ToolCalls[%d].ID: got %q, want %q", i, j, gotTC.ID, tc.ID)
			}
			if gotTC.Name != tc.Name {
				t.Errorf("restored[%d].ToolCalls[%d].Name: got %q, want %q", i, j, gotTC.Name, tc.Name)
			}
			if string(gotTC.Args) != string(tc.Args) {
				t.Errorf("restored[%d].ToolCalls[%d].Args: got %q, want %q", i, j, string(gotTC.Args), string(tc.Args))
			}
		}
	}
}

func TestExportImagePartsNotIncludedInExportedMessages(t *testing.T) {
	msgs := []message.Message{{
		Role:    "user",
		Content: "look",
		Parts: []message.ContentPart{
			{Type: "text", Text: "look"},
			{Type: "image", MimeType: "image/png", Data: []byte{1, 2, 3}, ImagePath: "/tmp/sample.png", FileName: "sample.png"},
		},
	}}

	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	if len(session.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(session.Messages))
	}
	if session.Messages[0].Content != "look" {
		t.Fatalf("exported content = %q, want %q", session.Messages[0].Content, "look")
	}

	restored := session.ToMessages()
	if len(restored) != 1 {
		t.Fatalf("len(restored) = %d, want 1", len(restored))
	}
	if len(restored[0].Parts) != 0 {
		t.Fatalf("expected exported/imported message to omit image parts, got %d parts", len(restored[0].Parts))
	}
}

// ---------------------------------------------------------------------------
// Import / validation tests
// ---------------------------------------------------------------------------

func TestImportFromBytesValid(t *testing.T) {
	raw := `{
		"version": "1",
		"created_at": "2025-06-15T10:30:00Z",
		"messages": [
			{"role": "user", "content": "hello"}
		]
	}`
	session, err := ImportFromBytes([]byte(raw))
	if err != nil {
		t.Fatalf("ImportFromBytes() error: %v", err)
	}
	if session.Version != "1" {
		t.Errorf("Version = %q, want %q", session.Version, "1")
	}
	if len(session.Messages) != 1 {
		t.Errorf("len(Messages) = %d, want 1", len(session.Messages))
	}
}

func TestImportFromBytesInvalidJSON(t *testing.T) {
	_, err := ImportFromBytes([]byte(`{invalid json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestValidateSessionMissingVersion(t *testing.T) {
	session := &ExportedSession{
		CreatedAt: time.Now(),
		Messages:  []ExportedMessage{},
	}
	err := ValidateSession(session)
	if err == nil {
		t.Error("expected error for missing version")
	}
}

func TestValidateSessionUnsupportedVersion(t *testing.T) {
	session := &ExportedSession{
		Version:   "99",
		CreatedAt: time.Now(),
		Messages:  []ExportedMessage{},
	}
	err := ValidateSession(session)
	if err == nil {
		t.Error("expected error for unsupported version")
	}
}

func TestValidateSessionMissingCreatedAt(t *testing.T) {
	session := &ExportedSession{
		Version:  "1",
		Messages: []ExportedMessage{},
	}
	err := ValidateSession(session)
	if err == nil {
		t.Error("expected error for missing created_at")
	}
}

func TestValidateSessionNilMessages(t *testing.T) {
	session := &ExportedSession{
		Version:   "1",
		CreatedAt: time.Now(),
		Messages:  nil,
	}
	err := ValidateSession(session)
	if err == nil {
		t.Error("expected error for nil messages")
	}
}

func TestValidateSessionEmptyRole(t *testing.T) {
	session := &ExportedSession{
		Version:   "1",
		CreatedAt: time.Now(),
		Messages: []ExportedMessage{
			{Role: "", Content: "test"},
		},
	}
	err := ValidateSession(session)
	if err == nil {
		t.Error("expected error for empty role")
	}
}

func TestValidateSessionUnknownRole(t *testing.T) {
	session := &ExportedSession{
		Version:   "1",
		CreatedAt: time.Now(),
		Messages: []ExportedMessage{
			{Role: "alien", Content: "test"},
		},
	}
	err := ValidateSession(session)
	if err == nil {
		t.Error("expected error for unknown role")
	}
}

func TestValidateSessionValid(t *testing.T) {
	session := &ExportedSession{
		Version:   "1",
		CreatedAt: time.Now(),
		Messages: []ExportedMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
		},
	}
	if err := ValidateSession(session); err != nil {
		t.Errorf("ValidateSession() unexpected error: %v", err)
	}
}

func TestValidateSessionNil(t *testing.T) {
	err := ValidateSession(nil)
	if err == nil {
		t.Error("expected error for nil session")
	}
}

// ---------------------------------------------------------------------------
// Edge case tests
// ---------------------------------------------------------------------------

func TestExportToFileNilSession(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nil-session.json")
	err := ExportToFile(nil, path)
	if err == nil {
		t.Error("expected error for nil session")
	}
}

func TestExportToFileBadPath(t *testing.T) {
	session := &ExportedSession{
		Version:   "1",
		CreatedAt: time.Now(),
		Messages:  []ExportedMessage{},
	}
	err := ExportToFile(session, "/nonexistent/directory/deep/session.json")
	if err == nil {
		t.Error("expected error for bad path")
	}
}

func TestImportFromFileNonexistent(t *testing.T) {
	_, err := ImportFromFile("/nonexistent/file.json")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestToMessagesNilSession(t *testing.T) {
	var session *ExportedSession
	msgs := session.ToMessages()
	if msgs != nil {
		t.Errorf("expected nil, got %v", msgs)
	}
}

func TestToMessagesEmptyMessages(t *testing.T) {
	session := &ExportedSession{
		Messages: []ExportedMessage{},
	}
	msgs := session.ToMessages()
	if msgs != nil {
		t.Errorf("expected nil for empty messages, got %v", msgs)
	}
}

func TestRoundTripEmptySession(t *testing.T) {
	session, err := Export([]message.Message{}, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty.json")

	if err := ExportToFile(session, path); err != nil {
		t.Fatalf("ExportToFile() error: %v", err)
	}

	imported, err := ImportFromFile(path)
	if err != nil {
		t.Fatalf("ImportFromFile() error: %v", err)
	}

	if len(imported.Messages) != 0 {
		t.Errorf("len(Messages) = %d, want 0", len(imported.Messages))
	}
}

// ---------------------------------------------------------------------------
// Markdown export tests
// ---------------------------------------------------------------------------

func TestExportToMarkdownHeader(t *testing.T) {
	session, err := Export(sampleMessages(), sampleStats(), sampleMetadata())
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	// Check header elements (plain-text format).
	if !strings.Contains(md, "Session Export") {
		t.Error("missing header")
	}
	if !strings.Contains(md, "claude-opus-4.7") {
		t.Error("missing model in header")
	}
	if !strings.Contains(md, "/home/user/project") {
		t.Error("missing project path in header")
	}
	if !strings.Contains(md, "$0.0125") {
		t.Error("missing cost in header")
	}
}

func TestExportToMarkdownSessionIDPrefersSessionIDKey(t *testing.T) {
	meta := map[string]string{
		"session_id":  "1730000000000",
		"instance_id": "main-1",
	}
	session, err := Export(sampleMessages(), nil, meta)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	md := ExportToMarkdown(session)
	if !strings.Contains(md, "Session ID: 1730000000000") {
		t.Errorf("want Session ID from session_id metadata, got:\n%s", md)
	}
	if strings.Contains(md, "Session ID: main-1") {
		t.Error("should not print instance_id when session_id is set")
	}
}

func TestExportToMarkdownSessionIDFallsBackToInstanceID(t *testing.T) {
	meta := map[string]string{"instance_id": "main-1"}
	session, err := Export(sampleMessages(), nil, meta)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}
	md := ExportToMarkdown(session)
	if !strings.Contains(md, "Session ID: main-1") {
		t.Errorf("want Session ID fallback from instance_id, got:\n%s", md)
	}
}

func TestExportToMarkdownUserMessage(t *testing.T) {
	session, err := Export(sampleMessages(), nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "User:\n\n") {
		t.Error("missing user heading")
	}
	if !strings.Contains(md, "Hello, can you help me with Go?") {
		t.Error("missing user message content")
	}
}

func TestExportToMarkdownAssistantMessage(t *testing.T) {
	session, err := Export(sampleMessages(), nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "Assistant:\n\n") {
		t.Error("missing assistant heading")
	}
	if !strings.Contains(md, "Of course! What would you like to know?") {
		t.Error("missing assistant message content")
	}
}

func TestExportToMarkdownToolCall(t *testing.T) {
	session, err := Export(sampleMessages(), nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "TOOL CALL (Read):") {
		t.Error("missing tool call summary")
	}
	if !strings.Contains(md, `"path":"/tmp/test.go"`) {
		t.Error("missing tool call args")
	}
}

func TestExportToMarkdownToolResult(t *testing.T) {
	session, err := Export(sampleMessages(), nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "TOOL RESULT (Read):") {
		t.Error("missing tool result heading with tool name")
	}
	if !strings.Contains(md, "package main") {
		t.Error("missing tool result content")
	}
}

func TestExportToMarkdownThinkingBlock(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "think about this"},
		{
			Role:    "assistant",
			Content: "Here's my answer.",
			ThinkingBlocks: []message.ThinkingBlock{
				{Thinking: "Let me reason about this...", Signature: "sig123"},
			},
		},
	}

	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "Thinking:\n\n") {
		t.Error("missing thinking header")
	}
	if !strings.Contains(md, "Let me reason about this...") {
		t.Error("missing thinking content")
	}
}

func TestExportToMarkdownSkipsSystemMessages(t *testing.T) {
	msgs := []message.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello!"},
	}

	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if strings.Contains(md, "You are a helpful assistant") {
		t.Error("system message content should not appear in markdown")
	}
	if strings.Contains(md, "System") {
		t.Error("system heading should not appear in markdown")
	}
}

func TestExportToMarkdownStats(t *testing.T) {
	session, err := Export(sampleMessages(), sampleStats(), nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "Session Statistics") {
		t.Error("missing stats section")
	}
	if !strings.Contains(md, "1500") {
		t.Error("missing input tokens")
	}
	if !strings.Contains(md, "800") {
		t.Error("missing output tokens")
	}
	if !strings.Contains(md, "$0.0125") {
		t.Error("missing cost in stats")
	}
}

func TestExportToMarkdownNilSession(t *testing.T) {
	md := ExportToMarkdown(nil)
	if md != "" {
		t.Errorf("expected empty string for nil session, got %q", md)
	}
}

func TestExportToMarkdownEmptySession(t *testing.T) {
	session, err := Export([]message.Message{}, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "Session Export") {
		t.Error("empty session should still have header")
	}
}

func TestExportMarkdownToFile(t *testing.T) {
	session, err := Export(sampleMessages(), sampleStats(), sampleMetadata())
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session-test.md")

	if err := ExportMarkdownToFile(session, path); err != nil {
		t.Fatalf("ExportMarkdownToFile() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "Session Export") {
		t.Error("file missing header")
	}
	if !strings.Contains(content, "User:\n\n") {
		t.Error("file missing user message")
	}
}

func TestExportMarkdownToFileNilSession(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nil-session.md")
	err := ExportMarkdownToFile(nil, path)
	if err == nil {
		t.Error("expected error for nil session")
	}
}

func TestExportMarkdownToFileBadPath(t *testing.T) {
	session := &ExportedSession{
		Version:   "1",
		CreatedAt: time.Now(),
		Messages:  []ExportedMessage{},
	}
	err := ExportMarkdownToFile(session, "/nonexistent/directory/deep/session.md")
	if err == nil {
		t.Error("expected error for bad path")
	}
}

func TestExportToMarkdownToolResultUnknownTool(t *testing.T) {
	// Tool result with no matching assistant tool call should show "unknown".
	msgs := []message.Message{
		{Role: "user", Content: "test"},
		{
			Role:       "tool",
			Content:    "result data",
			ToolCallID: "orphan_call_999",
		},
	}

	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "TOOL RESULT (unknown):") {
		t.Error("orphan tool result should show (unknown)")
	}
}

func TestExportToMarkdownShortToolResult(t *testing.T) {
	// Short tool results should NOT be wrapped in code blocks.
	msgs := []message.Message{
		{Role: "user", Content: "test"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Bash", Args: json.RawMessage(`{"command":"echo hi"}`)},
			},
		},
		{
			Role:       "tool",
			Content:    "hi",
			ToolCallID: "c1",
		},
	}

	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "TOOL RESULT (Bash):") {
		t.Error("missing tool result heading")
	}
	if !strings.Contains(md, "hi") {
		t.Error("missing tool result content")
	}
}

func TestExportToMarkdownMultilineToolResult(t *testing.T) {
	// Multi-line tool results should be wrapped in code blocks.
	msgs := []message.Message{
		{Role: "user", Content: "test"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Read", Args: json.RawMessage(`{}`)},
			},
		},
		{
			Role:       "tool",
			Content:    "line 1\nline 2\nline 3",
			ToolCallID: "c1",
		},
	}

	session, err := Export(msgs, nil, nil)
	if err != nil {
		t.Fatalf("Export() error: %v", err)
	}

	md := ExportToMarkdown(session)

	if !strings.Contains(md, "line 1\nline 2\nline 3") {
		t.Error("multi-line tool result content should be present")
	}
}
