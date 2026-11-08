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
	t.Parallel()
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
	t.Parallel()
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
	if !strings.Contains(first[1].Parts[1].Text, `covered_lines="1-1" total_lines="1"`) || strings.Contains(first[1].Parts[1].Text, `truncated=`) {
		t.Fatalf("injected line facts = %q", first[1].Parts[1].Text)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	plan := a.compactionInjectedFileBudgets(msgs)
	completeBytes := 0
	fileParts := 0
	for i, part := range parts {
		completeBytes += len(part.Text)
		if i == 0 || !message.IsFileRefContent(part.Text) {
			continue
		}
		fileParts++
		for _, want := range []string{
			"truncated to fit the 12 KB file-context byte budget",
			`truncated="true"`,
			`total_lines="2048"`,
			`covered_lines="1-`,
			`revision="sha256:`,
			`source="key_file"`,
		} {
			if !strings.Contains(part.Text, want) {
				t.Fatalf("injected part %d missing %q: %q", i, want, part.Text)
			}
		}
	}
	if fileParts == 0 || len(parts) != fileParts+2 {
		t.Fatalf("parts = %d, file parts = %d, want system + files + omission note", len(parts), fileParts)
	}
	// Four truncated bodies fit the raw-body budget, but their tags, hints and
	// annotations do not fit the complete injection cap, so one file is dropped.
	if fileParts >= 4 {
		t.Fatalf("complete-overhead accounting kept %d files", fileParts)
	}
	if !strings.Contains(parts[len(parts)-1].Text, "2 additional files omitted") {
		t.Fatalf("omission note was not regenerated for the dropped file: %q", parts[len(parts)-1].Text)
	}
	if completeBytes > plan.maxTotalBytes {
		t.Fatalf("complete injection = %d bytes, want <= %d (tags, notes and annotations included)", completeBytes, plan.maxTotalBytes)
	}
	firstPath := filepath.Join(projectRoot, filepath.FromSlash(keyFiles[0]))
	if observation := a.fileTrack.Observation(firstPath, a.instanceID, computeFileHash(firstPath)); observation.Observed {
		t.Fatalf("truncated key file was incorrectly recorded as a complete observation: %+v", observation)
	}
}

func TestInjectCompactionFileContextRefreshesCoverageFacts(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "notes.md")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(strings.Repeat("short line\n", 20))
	a := newTestMainAgent(t, projectRoot)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(120000, 120000, 120000, 0)
	summary := buildCompactionCheckpointMessage(
		"## Goal\n- continue\n\n## Files and Evidence\n- notes.md\n\n## Next Step\n- continue",
		nil, "model_summary", nil,
	)
	msgs := []message.Message{
		{Role: message.RoleUser, IsCompactionSummary: true, Content: summary},
		{Role: message.RoleUser, Content: "continue"},
	}
	first, _ := injectCompactionFileContextForTest(a, msgs)
	if len(first) != 3 || !strings.Contains(first[1].Parts[1].Text, `covered_lines="1-20" total_lines="20"`) || strings.Contains(first[1].Parts[1].Text, `truncated=`) {
		t.Fatalf("initial coverage facts = %#v", first)
	}

	write(strings.Repeat("abcdefghij\n", 2000))
	updated, _ := injectCompactionFileContextForTest(a, msgs)
	if len(updated) != 3 {
		t.Fatalf("len(updated) = %d, want 3", len(updated))
	}
	part := updated[1].Parts[1].Text
	if !strings.Contains(part, `total_lines="2000"`) || !strings.Contains(part, `truncated="true"`) || !strings.Contains(part, `changed_since_checkpoint="true"`) {
		t.Fatalf("refresh kept stale coverage or revision facts: %q", part)
	}
	if strings.Contains(part, `covered_lines="1-20"`) {
		t.Fatalf("refresh kept the old covered range: %q", part)
	}

	a.compactionFiles.reset()
	resumed, _ := injectCompactionFileContextForTest(a, msgs)
	if len(resumed) != 3 || !strings.Contains(resumed[1].Parts[1].Text, `total_lines="2000"`) || !strings.Contains(resumed[1].Parts[1].Text, `truncated="true"`) {
		t.Fatalf("fresh runtime did not rebuild the current coverage facts: %#v", resumed)
	}
}

func TestInjectCompactionFileContextSkipsWhenRequestBudgetIsExhausted(t *testing.T) {
	t.Parallel()
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
