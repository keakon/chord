package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/tools"
)

func TestCheckpointSourceRefsValidateGenerationScopedOrdinals(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Content: "repeat"},
		{Role: message.RoleUser, Content: "repeat"},
	}
	refs, err := buildCheckpointSourceRefs("session-a", "compaction-3", "history-3.md", messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].LegacyOrdinal != 0 || refs[1].LegacyOrdinal != 1 {
		t.Fatalf("refs = %#v", refs)
	}
	if refs[0].CanonicalPayloadHash != refs[1].CanonicalPayloadHash {
		t.Fatal("identical messages should share content hashes")
	}
	if err := validateCheckpointSourceRefs(refs, messages); err != nil {
		t.Fatalf("validate refs: %v", err)
	}
	if err := validateCheckpointSourceRefs(refs[:1], messages); err == nil {
		t.Fatal("expected incomplete source refs to fail validation")
	}
	duplicate := append([]checkpointSourceRef(nil), refs...)
	duplicate[1] = duplicate[0]
	if err := validateCheckpointSourceRefs(duplicate, messages); err == nil {
		t.Fatal("expected duplicate source ordinal to fail validation")
	}

	messages[1].Content = "changed"
	if err := validateCheckpointSourceRefs(refs, messages); err == nil {
		t.Fatal("expected changed source to fail validation")
	}
}

func TestCompactionHistoryReferencesAreAbsolute(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	absPath, _, _, err := a.exportCompactionHistory([]message.Message{{Role: message.RoleUser, Content: "request"}}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(a.sessionDir, "history-2.md"); absPath != want {
		t.Fatalf("exportCompactionHistory path = %q, want %q", absPath, want)
	}
	if !filepath.IsAbs(absPath) {
		t.Fatalf("exportCompactionHistory path is not absolute: %q", absPath)
	}
	if err := os.WriteFile(filepath.Join(a.sessionDir, "history-3.md"), []byte("Session Export\n"), 0o644); err != nil {
		t.Fatalf("write history-3: %v", err)
	}
	refs, err := listHistoryReferences(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{absPath, filepath.Join(a.sessionDir, "history-3.md")}
	if len(refs) != len(want) {
		t.Fatalf("listHistoryReferences = %#v, want %#v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("listHistoryReferences = %#v, want %#v", refs, want)
		}
		if !filepath.IsAbs(refs[i]) {
			t.Fatalf("history reference is not absolute: %q", refs[i])
		}
		abbrev := pathutil.AbbreviateHome(refs[i])
		if !strings.HasPrefix(abbrev, "~") && !filepath.IsAbs(abbrev) {
			t.Fatalf("display form of history reference is neither home-abbreviated nor absolute: %q", abbrev)
		}
	}
}

func TestExportCompactionHistoryWritesSourceProvenance(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	messages := []message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameRead}}},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "file contents"},
	}
	_, refs, fingerprint, err := a.exportCompactionHistory(messages, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 || fingerprint == "" {
		t.Fatalf("returned provenance refs=%#v fingerprint=%q", refs, fingerprint)
	}
	// The tool-call identity is part of the locator, not just the role.
	if refs[2].ToolCallID != "call-1" {
		t.Fatalf("tool ref lost its ToolCallID: %#v", refs[2])
	}
	renamed := slices.Clone(messages)
	renamed[2].ToolCallID = "call-2"
	if err := validateCheckpointSourceRefs(refs, renamed); err == nil {
		t.Fatal("a changed tool_call_id must invalidate the source refs")
	}

	metaPath := filepath.Join(a.sessionDir, "history-4.status.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta compactionHistoryMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.SourceGeneration != "compaction-4" || len(meta.SourceRefs) != 3 || meta.SourceFingerprint == "" {
		t.Fatalf("metadata = %#v", meta)
	}
	if err := validateCheckpointSourceRefs(meta.SourceRefs, messages); err != nil {
		t.Fatalf("exported refs do not validate: %v", err)
	}
}
