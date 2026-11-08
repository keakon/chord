package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

func TestCompactionFileReplayQuotaShrinksAsConversationGrows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	write := func(ch string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Repeat(ch+"\n", 4000)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("a")
	a := newTestMainAgent(t, root)
	enableTestCheckpointFileReplay(a)
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	a.ctxMgr.SetTokenBudgets(80000, 80000, 80000, 0)
	raw := []message.Message{
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "## Files and Evidence\n- notes.md\n"},
		{Role: message.RoleUser, Content: "continue"},
	}
	a.injectCompactionFileContext(raw, "p/m")
	write("b")
	raw = append(raw, message.Message{Role: message.RoleAssistant, RequestBatch: 1, Content: "updated"})
	a.injectCompactionFileContext(raw, "p/m")
	if len(a.compactionFiles.versions) != 2 {
		t.Fatalf("versions=%d, want 2 before the quota shrinks", len(a.compactionFiles.versions))
	}
	raw = append(raw, message.Message{Role: message.RoleAssistant, RequestBatch: 2, Content: strings.Repeat("x", 106500)})
	plan := a.compactionInjectedFileBudgets(raw)
	if plan.maxTotalBytes == 0 {
		t.Fatalf("no injection budget: %+v", plan)
	}
	result := a.injectCompactionFileContext(raw, "p/m")
	var snapshots []message.Message
	for _, msg := range result {
		if isCompactionFileSnapshot(msg) {
			snapshots = append(snapshots, msg)
		}
	}
	limit := compactionFileInjectionCost{bytes: plan.maxTotalBytes, tokens: plan.quotaTokens}
	if cost := plan.injectionCost(snapshots...); !cost.fits(limit) {
		t.Fatalf("conversation growth replays over quota: cost=%+v limit=%+v", cost, limit)
	}
	if len(snapshots) != 1 || !strings.Contains(snapshots[0].Parts[1].Text, "b\n") {
		t.Fatal("quota shrink lost the current file snapshot")
	}
}

func TestCompactionFileReplayReestimatesWhenCalibrationChanges(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"unchanged", "changed"} {
		t.Run(name, func(t *testing.T) {
			mgr := ctxmgr.NewManager(120000, 0)
			mgr.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("x", 1000)})
			mgr.UpdateFromUsage(message.TokenUsage{InputTokens: 100})
			var replay compactionFileReplay
			plan := compactionReinjectionPlan{maxTotalBytes: 48 * 1024, quotaTokens: 20000, ratio: mgr.CalibratedRatio()}
			snapshot := func(ch string) message.Message {
				return message.Message{
					Role: message.RoleUser,
					Kind: message.KindTurnOverlay,
					Parts: []message.ContentPart{
						{Type: message.ContentPartText, Text: compactionFileCtxPrefix},
						{Type: message.ContentPartText, Text: `<file path="notes.md">` + strings.Repeat(ch+"\n", 5500) + `</file>`},
					},
				}
			}
			raw := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: "checkpoint"}}
			replay.replay(raw, 0, "checkpoint", "/root", snapshot("a"), plan)
			raw = append(raw, message.Message{Role: message.RoleAssistant, RequestBatch: 1, Content: "updated"})
			current := snapshot("b")
			replay.replay(raw, 0, "checkpoint", "/root", current, plan)
			if len(replay.versions) != 2 {
				t.Fatal("missing second snapshot before calibration changes")
			}
			for range 3 {
				mgr.UpdateFromUsage(message.TokenUsage{InputTokens: 1000})
			}
			plan.ratio = mgr.CalibratedRatio()
			if plan.ratio != 1 {
				t.Fatalf("ratio=%v, want 1", plan.ratio)
			}
			if name == "changed" {
				current = snapshot("c")
			}
			result := replay.replay(raw, 0, "checkpoint", "/root", current, plan)
			var snapshots []message.Message
			for _, msg := range result {
				if isCompactionFileSnapshot(msg) {
					snapshots = append(snapshots, msg)
				}
			}
			limit := compactionFileInjectionCost{bytes: plan.maxTotalBytes, tokens: plan.quotaTokens}
			if cost := plan.injectionCost(snapshots...); !cost.fits(limit) {
				t.Fatalf("stale calibration costs: cost=%+v limit=%+v", cost, limit)
			}
			if len(snapshots) != 1 || snapshots[0].Parts[1].Text != current.Parts[1].Text {
				t.Fatal("calibration change lost the current snapshot")
			}
		})
	}
}

func TestCompactionFileReplayCountsCombinedTokenRounding(t *testing.T) {
	t.Parallel()
	plan := compactionReinjectionPlan{maxTotalBytes: 1000, ratio: 0.123}
	snapshot := func(body string) message.Message {
		return message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: compactionFileCtxPrefix},
			{Type: message.ContentPartText, Text: `<file path="notes.md">` + body + `</file>`},
		}}
	}
	first, current := snapshot("first"), snapshot("other")
	plan.quotaTokens = plan.injectionCost(first).tokens + plan.injectionCost(current).tokens
	if plan.injectionCost(first, current).tokens <= plan.quotaTokens {
		t.Fatal("fixture does not exercise combined token rounding")
	}
	var replay compactionFileReplay
	raw := []message.Message{{Role: message.RoleUser, IsCompactionSummary: true, Content: "checkpoint"}}
	replay.replay(raw, 0, "checkpoint", "/root", first, plan)
	raw = append(raw, message.Message{Role: message.RoleAssistant, RequestBatch: 1, Content: "updated"})
	result := replay.replay(raw, 0, "checkpoint", "/root", current, plan)
	if len(result) != len(raw)+1 || result[1].Parts[1].Text != current.Parts[1].Text {
		t.Fatal("combined rounding allowed an over-quota historical snapshot")
	}
}

func TestInjectCompactionFileContextShortensFirstFileAtSmallQuota(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		lines   int
		partial bool
	}{
		{"ASCII", strings.Repeat("x", 20000), 1, true},
		{"CRLF", strings.Repeat("abc\r\n", 4000), 4000, false},
		{"UTF8", strings.Repeat("é", 10000), 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "notes.md")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			a := newTestMainAgent(t, root)
			a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
			a.ctxMgr.SetTokenBudgets(12000, 12000, 12000, 0)
			raw := []message.Message{{
				Role: message.RoleUser, IsCompactionSummary: true,
				Content:                 "## Files and Evidence\n- notes.md\n",
				CompactionFileRevisions: map[string]string{"notes.md": computeFileHash(path)},
			}}
			plan := a.compactionInjectedFileBudgets(raw)
			if plan.maxTotalBytes < compactionInjectedFilesMinBytes || plan.maxTotalBytes >= compactionInjectedFileMaxBytes {
				t.Fatalf("expected usable small quota: %+v", plan)
			}
			result := a.injectCompactionFileContext(raw, "p/m")
			if len(result) != len(raw)+1 {
				t.Fatalf("sole note omitted despite usable quota: %+v", plan)
			}
			limit := compactionFileInjectionCost{bytes: plan.maxTotalBytes, tokens: plan.quotaTokens}
			if cost := plan.injectionCost(result[1]); !cost.fits(limit) {
				t.Fatalf("shortened snapshot exceeds quota: %+v", cost)
			}
			part := result[1].Parts[1].Text
			for _, attr := range []string{`truncated="true"`, `total_lines=`, `covered_lines="1-`, `changed_since_checkpoint="false"`, `source="key_file"`} {
				if !strings.Contains(part, attr) {
					t.Fatalf("shortened snapshot missing %s", attr)
				}
			}
			wantTotal := fmt.Sprintf("total_lines=%q", strconv.Itoa(tc.lines))
			if !strings.Contains(part, wantTotal) || strings.Contains(part, `partial_last_line="true"`) != tc.partial {
				t.Fatalf("incorrect shortened line facts: %s", part[:strings.IndexByte(part, '>')])
			}
			_, body, ok := message.ParseSingleFileRefContent(part)
			if !ok {
				t.Fatal("shortened wrapper is not parseable")
			}
			prefix, _, found := strings.Cut(body, "\n[...truncated")
			if !found || prefix == "" || !utf8.ValidString(prefix) || !strings.HasPrefix(tc.body, prefix) {
				t.Fatal("shortened body is not an intact file prefix")
			}
			if tc.name == "CRLF" {
				wantCovered := fmt.Sprintf("covered_lines=%q", fmt.Sprintf("1-%d", strings.Count(prefix, "\r\n")))
				if !strings.HasSuffix(prefix, "\r\n") || !strings.Contains(part, wantCovered) {
					t.Fatal("shortened CRLF coverage disagrees with the body")
				}
			}
			if observation := a.fileTrack.Observation(path, a.instanceID, computeFileHash(path)); observation.Observed {
				t.Fatal("shortened snapshot obtained a full-read observation")
			}
		})
	}
}

func TestFitCompactionFileInjectionRebuildsWithoutAnotherRead(t *testing.T) {
	t.Parallel()
	data := []byte(strings.Repeat("x", 20000))
	reads := 0
	result := filectx.BuildFilePartsWithOptions([]string{"first.txt", "second.txt"}, func(path string) string { return path },
		filectx.BuildFilePartsOptions{MaxFileBytes: 9000, MaxTotalBytes: 18000, ReadFile: func(string) ([]byte, error) {
			reads++
			return data, nil
		}})
	plan := compactionReinjectionPlan{maxFileBytes: 9000, maxTotalBytes: 9000, quotaTokens: 3000}
	rebuilds := 0
	note := message.ContentPart{Type: message.ContentPartText, Text: compactionFileCtxPrefix}
	fitted := fitCompactionFileInjection(note, result.Parts[:result.LoadedFiles], result.OmittedFiles, plan,
		func(budget int) (message.ContentPart, int) {
			rebuilds++
			shorter := filectx.BuildFilePartsWithOptions([]string{"first.txt"}, func(path string) string { return path },
				filectx.BuildFilePartsOptions{MaxFileBytes: budget, ReadFile: func(string) ([]byte, error) { return data, nil }})
			return shorter.Parts[0], shorter.TotalBytes
		})
	if !fitted.ok || fitted.loaded != 1 || fitted.omitted != 1 || reads != 2 || rebuilds == 0 || rebuilds > 4 {
		t.Fatalf("fit=%+v reads=%d rebuilds=%d", fitted, reads, rebuilds)
	}
	if fitted.message.Parts[2].Text != filectx.OmittedFilesNote(1, plan.maxTotalBytes) {
		t.Fatal("shortened snapshot has an incorrect omission note")
	}
}

func TestFitCompactionFileInjectionSkipsWhenEnvelopeDoesNotFit(t *testing.T) {
	t.Parallel()
	data := []byte(strings.Repeat("é", 20))
	result := filectx.BuildFilePartsWithOptions([]string{"sample.txt"}, func(path string) string { return path },
		filectx.BuildFilePartsOptions{MaxFileBytes: 10, ReadFile: func(string) ([]byte, error) { return data, nil }})
	plan := compactionReinjectionPlan{maxFileBytes: 10, maxTotalBytes: 32, quotaTokens: 10}
	rebuilds := 0
	fitted := fitCompactionFileInjection(message.ContentPart{Type: message.ContentPartText, Text: compactionFileCtxPrefix},
		result.Parts, 0, plan, func(budget int) (message.ContentPart, int) {
			rebuilds++
			shorter := filectx.BuildFilePartsWithOptions([]string{"sample.txt"}, func(path string) string { return path },
				filectx.BuildFilePartsOptions{MaxFileBytes: budget, ReadFile: func(string) ([]byte, error) { return data, nil }})
			return shorter.Parts[0], shorter.TotalBytes
		})
	if fitted.ok || rebuilds != 1 {
		t.Fatalf("over-quota envelope fit=%v rebuilds=%d", fitted.ok, rebuilds)
	}
}
