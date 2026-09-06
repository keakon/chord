package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// staleReadArchivePolicy reduces a read as soon as it goes stale so the archive
// decision is the only variable under test.
func staleReadArchivePolicy() *config.Config {
	return &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  40,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1 << 20,
	}}}
}

func newStaleReadArchiveAgent(t *testing.T) (*MainAgent, []message.Message) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = staleReadArchivePolicy()
	a.newTurn()
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	// The payload must exceed minRecoverableArchiveBytes for an address to be
	// required at all.
	body := strings.Repeat("const observedRevisionLine = 1\n", 200)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: "READ_RESULT lines=1-200 total=200\n" + body},
	}
	setTestRequestBatch(a, msgs, 2)
	if first := a.prepareMessagesForLLM(msgs); first[2].Content != msgs[2].Content {
		t.Fatalf("still-valid read should stay full before invalidation, got %q", first[2].Content)
	}
	return a, msgs
}

// archivedPayload resolves the artifact reference a reduced marker carries and
// returns the archived bytes. ExtractArtifactReferences yields the whole
// canonical reference line, so the path has to be unwrapped from it.
func archivedPayload(t *testing.T, reduced string) string {
	t.Helper()
	refs := tools.ExtractArtifactReferences(reduced)
	if len(refs) != 1 {
		t.Fatalf("expected exactly one artifact reference in %q, got %v", reduced, refs)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(refs[0], tools.ArtifactReferencePrefix), ".")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("archived payload unreadable at %q: %v", path, err)
	}
	return string(data)
}

// A whole-file write destroys the revision the read observed: write echoes only
// the new content in its arguments and re-reading returns the new bytes, so the
// read output is the last record of the old ones and must be archived.
func TestStaleReadAfterWholeFileWriteIsArchived(t *testing.T) {
	a, msgs := newStaleReadArchiveAgent(t)
	original := msgs[2].Content
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "write", Name: tools.NameWrite, Args: json.RawMessage(`{"path":"a.go","content":"package main\n"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "write", ToolStatus: "success", Content: "Successfully wrote 1 line, 14 bytes"},
	)
	setTestRequestBatch(a, msgs, 3)
	reduced := a.prepareMessagesForLLM(msgs)[2].Content
	if reduced == original {
		t.Fatalf("stale read stayed full: %q", reduced)
	}
	if !strings.Contains(reduced, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("stale read lost its validity marker: %q", reduced)
	}
	if got := archivedPayload(t, reduced); got != original {
		t.Fatalf("archived payload differs: got %d bytes want %d", len(got), len(original))
	}
}

// A mutating shell command replaces the file outside the editing tools: nothing
// in the conversation echoes the bytes it overwrote, so the read output is the
// only record of them. This is the invalidation cause that the two reduction
// contexts used to reach without an ArchiveDir, so it could not archive at all.
func TestStaleReadAfterMutatingShellIsArchived(t *testing.T) {
	a, msgs := newStaleReadArchiveAgent(t)
	original := msgs[2].Content

	// The invalidation is decided against the file on disk, so the read has to
	// carry the revision it observed and that revision has to no longer match.
	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte("package main // rewritten by the shell\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs[2].FileState = &message.ToolFileState{Reads: []message.TrackedFileState{{
		Path:   path,
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		Exists: true,
	}}}

	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "sh", Name: tools.NameShell, Args: json.RawMessage(`{"command":"sed -i '' 's/1/2/' a.go"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "sh", ToolStatus: "success", Content: ""},
	)
	setTestRequestBatch(a, msgs, 3)
	reduced := a.prepareMessagesForLLM(msgs)[2].Content
	if reduced == original {
		t.Fatalf("read invalidated by a mutating shell stayed full: %q", reduced)
	}
	if got := archivedPayload(t, reduced); got != original {
		t.Fatalf("archived payload differs: got %d bytes want %d", len(got), len(original))
	}
}

// A delete leaves no later revision to re-read at all, so the same rule applies.
func TestStaleReadAfterDeleteIsArchived(t *testing.T) {
	a, msgs := newStaleReadArchiveAgent(t)
	original := msgs[2].Content
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "del", Name: tools.NameDelete, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "del", ToolStatus: "success", Content: "deleted a.go"},
	)
	setTestRequestBatch(a, msgs, 3)
	reduced := a.prepareMessagesForLLM(msgs)[2].Content
	if got := archivedPayload(t, reduced); got != original {
		t.Fatalf("archived payload differs: got %d bytes want %d", len(got), len(original))
	}
}

// An edit carries the bytes it replaced in its own old_string, and assistant
// messages are never reduced, so the pre-edit content stays reachable without
// an archive. Archiving here would write a file per edited read for nothing.
func TestStaleReadAfterEditIsNotArchived(t *testing.T) {
	a, msgs := newStaleReadArchiveAgent(t)
	original := msgs[2].Content
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "edit", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "edit", ToolStatus: "success", Content: "Replaced 1 occurrence (100 bytes -> 100 bytes)"},
	)
	setTestRequestBatch(a, msgs, 3)
	reduced := a.prepareMessagesForLLM(msgs)[2].Content
	if reduced == original {
		t.Fatalf("stale read stayed full: %q", reduced)
	}
	if !strings.Contains(reduced, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("stale read lost its validity marker: %q", reduced)
	}
	if refs := tools.ExtractArtifactReferences(reduced); len(refs) != 0 {
		t.Fatalf("edit-invalidated read must not be archived, got %v", refs)
	}
}

// A read superseded by a newer read of the same range is redundant, not lost:
// the newer copy holds the same bytes, so no archive is needed.
func TestSupersededReadIsNotArchived(t *testing.T) {
	a, msgs := newStaleReadArchiveAgent(t)
	body := strings.Repeat("const observedRevisionLine = 1\n", 200)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "read2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "read2", ToolStatus: "success", Content: "READ_RESULT lines=1-200 total=200\n" + body},
	)
	setTestRequestBatch(a, msgs, 3)
	reduced := a.prepareMessagesForLLM(msgs)[2].Content
	if refs := tools.ExtractArtifactReferences(reduced); len(refs) != 0 {
		t.Fatalf("superseded read must not be archived, got %v", refs)
	}
}
