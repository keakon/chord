package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
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
	if len(refs) != 2 || refs[0].Ordinal != 0 || refs[1].Ordinal != 1 {
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
	request := []message.Message{{Role: message.RoleUser, Content: "request"}}
	absPath, _, _, err := a.exportCompactionHistory(request, request, 2, nil, a.captureCompactionArchiveMeta())
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
	_, refs, fingerprint, err := a.exportCompactionHistory(messages, messages, 4, nil, a.captureCompactionArchiveMeta())
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

// TestProduceCompactionDraftRefsCoverUnfilteredPrefix pins the usage-driven
// producer contract: the refs must describe the raw prefix the apply validates
// against (currentMessages[:headSplit]), not the filtered archive view. A
// dropped question-state row would otherwise shorten the ref list below that
// prefix and fail every apply.
func TestProduceCompactionDraftRefsCoverUnfilteredPrefix(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.globalConfig.Context.Compaction.Profile = config.CompactionProfileArchival
	a.SetProviderModelRef("sample/compact-model")

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: "stub",
		Models: map[string]config.ModelConfig{
			"compact-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{response: &message.Response{Content: validCompactionSummaryForTest("history-1.md")}}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		return client, "compact-model", 16384, nil
	})

	questionFact := json.RawMessage(`{"transaction_id":"txn-local"}`)
	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, Content: "a1", Question: questionFact},
		{Role: message.RoleSystem, Kind: message.KindQuestionState, Question: questionFact},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleAssistant, Content: "a2"},
		{Role: message.RoleUser, Content: "u3"},
	})
	snapshot := a.ctxMgr.Snapshot()
	headSplit := len(snapshot)

	draft, err := a.produceCompactionDraftAsync(t.Context(), snapshot, false, 1, compactionTarget{sessionEpoch: a.sessionEpoch}, headSplit, compactionProfileArchival, "", nil, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("produceCompactionDraftAsync: %v", err)
	}
	if draft.Skip {
		t.Fatal("expected non-skip compaction draft")
	}
	if len(draft.SourceRefs) != headSplit {
		t.Fatalf("draft provenance refs = %d, want %d: refs must cover the unfiltered prefix", len(draft.SourceRefs), headSplit)
	}
	if err := validateCheckpointSourceRefs(draft.SourceRefs, snapshot[:headSplit]); err != nil {
		t.Fatalf("refs do not validate against the raw prefix: %v", err)
	}
	archived, err := os.ReadFile(draft.AbsHistoryPath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if strings.Contains(string(archived), "txn-local") {
		t.Fatal("archive must stay a filtered view without question facts")
	}
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
}
