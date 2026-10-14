package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

func TestRefreshCompactionFileRevisionsUsesApplyTimeState(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "key.go")
	if err := os.WriteFile(path, []byte("package key\n"), 0o644); err != nil {
		t.Fatalf("WriteFile baseline: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## Files and Evidence\n- key.go\n\n## Next Step\n- continue",
		nil,
		"model_summary",
		nil,
	)
	messages := []message.Message{{
		Role:                    message.RoleUser,
		Content:                 summary,
		IsCompactionSummary:     true,
		CompactionFileRevisions: map[string]string{"key.go": computeFileHash(path)},
	}}
	if err := os.WriteFile(path, []byte("package key\n\nconst applied = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}

	refreshed := a.refreshCompactionFileRevisions(messages)
	want := computeFileHash(path)
	if got := refreshed[0].CompactionFileRevisions["key.go"]; got != want {
		t.Fatalf("apply-time revision = %q, want %q", got, want)
	}
	if got := messages[0].CompactionFileRevisions["key.go"]; got == want {
		t.Fatalf("refresh mutated draft input revision to %q", got)
	}
}

func TestInjectCompactionFileContextStablePerRequest(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "internal", "agent"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(projectRoot, "internal", "agent", "compaction.go")
	if err := os.WriteFile(path, []byte("package agent\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 120000, 0)
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n- internal/agent/compaction.go\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	msgs := []message.Message{
		{
			Role:                    "user",
			IsCompactionSummary:     true,
			Content:                 summary,
			CompactionFileRevisions: captureCompactionFileRevisions([]string{"internal/agent/compaction.go"}, a.resolveCheckpointFilePath),
		},
		{Role: "user", Content: "continue"},
	}
	enableTestCheckpointFileReplay(a)
	first, firstIdx := injectCompactionFileContextForTest(a, msgs)
	if len(first) != 3 {
		t.Fatalf("len(first) = %d, want 3", len(first))
	}
	if firstIdx != 1 {
		t.Fatalf("firstIdx = %d, want insertion right after the checkpoint", firstIdx)
	}
	if len(first[1].Parts) == 0 || !strings.Contains(first[1].Parts[0].Text, "Automatically loaded key files") {
		t.Fatalf("injected message parts = %#v", first[1].Parts)
	}
	if len(first[1].Parts) < 2 || !strings.Contains(first[1].Parts[1].Text, `revision="sha256:`) || !strings.Contains(first[1].Parts[1].Text, `changed_since_checkpoint="false"`) {
		t.Fatalf("injected file revision metadata = %q", first[1].Parts[1].Text)
	}
	if paths := message.FileRefPaths(first[1].Parts[1].Text); !reflect.DeepEqual(paths, []string{"internal/agent/compaction.go"}) {
		t.Fatalf("annotated file-ref paths = %#v", paths)
	}
	second, _ := injectCompactionFileContextForTest(a, msgs)
	if len(second) != 3 {
		t.Fatalf("len(second) = %d, want 3 for stable per-request injection", len(second))
	}
	alreadyInjected, alreadyIdx := injectCompactionFileContextForTest(a, first)
	if len(alreadyInjected) != 3 || alreadyIdx != -1 {
		t.Fatalf("len(alreadyInjected) = %d idx = %d, want 3 and -1 without duplicate injection", len(alreadyInjected), alreadyIdx)
	}
	if err := os.WriteFile(path, []byte("package agent\n\nconst changed = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed revision: %v", err)
	}
	changed, _ := injectCompactionFileContextForTest(a, msgs)
	if len(changed) != 4 || len(changed[3].Parts) < 2 || !strings.Contains(changed[3].Parts[1].Text, `changed_since_checkpoint="true"`) {
		t.Fatalf("changed file metadata = %#v", changed)
	}
}

func TestInjectCompactionFileContextDetectsChangeBeforeFirstInjection(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "key.go")
	if err := os.WriteFile(path, []byte("package key\n"), 0o644); err != nil {
		t.Fatalf("WriteFile baseline: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 120000, 0)
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## Files and Evidence\n- key.go\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	checkpoint := message.Message{
		Role:                    message.RoleUser,
		Content:                 summary,
		IsCompactionSummary:     true,
		CompactionFileRevisions: captureCompactionFileRevisions([]string{"key.go"}, a.resolveCheckpointFilePath),
	}
	if err := os.WriteFile(path, []byte("package key\n\nconst changed = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}

	got, _ := injectCompactionFileContextForTest(a, []message.Message{checkpoint})
	if len(got) != 2 || len(got[1].Parts) < 2 || !strings.Contains(got[1].Parts[1].Text, `changed_since_checkpoint="true"`) {
		t.Fatalf("injected changed metadata = %#v", got)
	}
}

func TestInjectCompactionFileContextTreatsLegacyCheckpointAsChanged(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "key.go"), []byte("package key\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 120000, 0)
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## Files and Evidence\n- key.go\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)

	got, _ := injectCompactionFileContextForTest(a, []message.Message{{Role: message.RoleUser, Content: summary, IsCompactionSummary: true}})
	if len(got) != 2 || len(got[1].Parts) < 2 || !strings.Contains(got[1].Parts[1].Text, `changed_since_checkpoint="true"`) {
		t.Fatalf("legacy checkpoint metadata = %#v", got)
	}
}

func TestInjectCompactionFileContextHonorsByteBudgets(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	var keyFiles []string
	for i := range 5 {
		rel := filepath.Join("pkg", fmt.Sprintf("f%d.txt", i))
		abs := filepath.Join(projectRoot, rel)
		if err := os.WriteFile(abs, []byte(strings.Repeat("abcdefghij\n", 2048)), 0o644); err != nil {
			t.Fatalf("WriteFile(%d): %v", i, err)
		}
		keyFiles = append(keyFiles, filepath.ToSlash(rel))
	}

	var filesSection strings.Builder
	filesSection.WriteString("- Archived history: history-1.md\n")
	for _, rel := range keyFiles {
		filesSection.WriteString("- ")
		filesSection.WriteString(rel)
		filesSection.WriteByte('\n')
	}
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n"+strings.TrimRight(filesSection.String(), "\n")+"\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 120000, 0)
	msgs := []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: summary},
		{Role: "user", Content: "continue"},
	}
	got, _ := injectCompactionFileContextForTest(a, msgs)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	parts := got[1].Parts
	if len(parts) != 6 {
		t.Fatalf("len(parts) = %d, want 6 (system + 4 files + omission note)", len(parts))
	}
	if !strings.Contains(parts[len(parts)-1].Text, "additional files omitted") {
		t.Fatalf("expected omission note, got %q", parts[len(parts)-1].Text)
	}
	for i := 1; i < len(parts)-1; i++ {
		if !strings.Contains(parts[i].Text, "showing first 12 KB only") {
			t.Fatalf("expected per-file truncation note in part %d, got %q", i, parts[i].Text)
		}
	}
	firstPath := filepath.Join(projectRoot, filepath.FromSlash(keyFiles[0]))
	if observation := a.fileTrack.Observation(firstPath, a.instanceID, computeFileHash(firstPath)); observation.Observed {
		t.Fatalf("truncated key file was incorrectly recorded as a complete observation: %+v", observation)
	}
}

func TestInjectCompactionFileContextSkipsWhenRequestBudgetIsExhausted(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "pkg", "f.txt"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetTokenBudgets(16384, 128, 128, 0)
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## User Constraints\n- none\n\n## Progress\n- progress\n\n## Key Decisions\n- decisions\n\n## Files and Evidence\n- Archived history: history-1.md\n- pkg/f.txt\n\n## Todo State\n- none\n\n## SubAgent State\n- none\n\n## Open Problems\n- none\n\n## Next Step\n- continue",
		[]string{".chord/sessions/test/history-1.md"},
		"model_summary",
		nil,
	)
	msgs := []message.Message{
		{Role: "user", IsCompactionSummary: true, Content: summary},
		{Role: "user", Content: strings.Repeat("already large ", 80)},
	}

	got, gotIdx := injectCompactionFileContextForTest(a, msgs)
	if len(got) != len(msgs) || gotIdx != -1 {
		t.Fatalf("len(got) = %d idx = %d, want unchanged %d and -1", len(got), gotIdx, len(msgs))
	}
}
