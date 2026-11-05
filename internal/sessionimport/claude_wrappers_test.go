package sessionimport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestConvertClaudeTranscript_WrapperBoundaries(t *testing.T) {
	tests := []struct {
		name string
		text string
		drop bool
	}{
		{"command name", "<command-name>/help</command-name>", true},
		{"command message", "<command-message>help</command-message>", true},
		{"stdout", "<local-command-stdout>output</local-command-stdout>", true},
		{"stderr", "<local-command-stderr>error</local-command-stderr>", true},
		{"caveat", "<local-command-caveat>local output</local-command-caveat>", true},
		{"whitespace", " \t<command-name>/help</command-name>\n", true},
		{"empty arguments", "<command-name>/help</command-name>\n<command-args> \t</command-args>", true},
		{"arguments only", "<command-args>explain the function</command-args>", false},
		{"embedded arguments", "<command-name>/explain</command-name>\n<command-args>explain the function</command-args>", false},
		{"message arguments", "<command-message>explain</command-message>\n<command-args>explain the function</command-args>", false},
		{"incomplete arguments", "<command-name>/explain</command-name>\n<command-args>explain the function", false},
		{"repeated arguments", "<command-name>/explain</command-name>\n<command-args></command-args>\n<command-args>explain the function</command-args>", false},
		{"prose", "Explain <command-name>/help</command-name>.", false},
		{"case", "<Command-name>/help</Command-name>", false},
		{"reminder", "<system-reminder>Keep this context.</system-reminder>", false},
		{"interruption", "[Request interrupted by user]", false},
		{"meta body", "Explain the function using the following instructions.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, array := range []bool{false, true} {
				t.Run(fmt.Sprintf("array=%t", array), func(t *testing.T) {
					var content any = tt.text
					if array {
						content = []map[string]string{{"type": claudeBlockTypeText, "text": tt.text}}
					}
					raw, err := json.Marshal(content)
					if err != nil {
						t.Fatal(err)
					}
					data := fmt.Appendf(nil, `{"uuid":"u1","isMeta":true,"message":{"role":"user","content":%s}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":%s}}`, raw, raw)
					var report ImportReport
					msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
					if err != nil {
						t.Fatal(err)
					}
					wantMessages, wantSkipped := 2, 0
					if tt.drop {
						wantMessages, wantSkipped = 1, 1
					}
					if len(msgs) != wantMessages {
						t.Fatalf("messages=%+v, want %d messages", msgs, wantMessages)
					}
					if !tt.drop && (msgs[0].Role != message.RoleUser || msgs[0].Content != strings.TrimSpace(tt.text)) {
						t.Fatalf("user message=%+v, want the source text preserved", msgs[0])
					}
					last := msgs[len(msgs)-1]
					if last.Role != message.RoleAssistant || last.Content != strings.TrimSpace(tt.text) {
						t.Fatalf("assistant message=%+v, want the source text preserved", last)
					}
					if report.Claude.WrappedUserBlocksSkipped != wantSkipped || report.Claude.WrappedUserLinesSkipped != wantSkipped || report.SkippedEntries != wantSkipped {
						t.Fatalf("report=%+v, claude=%+v, want %d skipped", report, report.Claude, wantSkipped)
					}
					if len(report.Warnings) != wantSkipped {
						t.Fatalf("warnings=%v, want %d aggregate warnings", report.Warnings, wantSkipped)
					}
				})
			}
		})
	}
}

func TestConvertClaudeTranscript_WrapperFilteringDoesNotWarnAboutSpan(t *testing.T) {
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":"<local-command-caveat>local output</local-command-caveat>"}}
{"uuid":"u2","parentUuid":"u1","message":{"role":"user","content":"<command-name>/help</command-name>"}}
{"uuid":"u3","parentUuid":"u2","message":{"role":"user","content":"<local-command-stdout>help text</local-command-stdout>"}}
{"uuid":"u4","parentUuid":"u3","message":{"role":"user","content":"Explain the function."}}
{"uuid":"a1","parentUuid":"u4","message":{"role":"assistant","content":"The function returns a value."}}`)
	var report ImportReport
	msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || report.Claude.SelectedSpanLength != 5 || report.Claude.NonSidechainMessages != 5 {
		t.Fatalf("messages=%+v, claude=%+v", msgs, report.Claude)
	}
	if report.Claude.WrappedUserBlocksSkipped != 3 || report.Claude.WrappedUserLinesSkipped != 3 {
		t.Fatalf("claude=%+v, want 3 blocks and 3 source records skipped", report.Claude)
	}
	if len(report.Warnings) != 1 || strings.Contains(strings.Join(report.Warnings, "\n"), "compaction-aware reconstruction") {
		t.Fatalf("warnings=%v, want only the aggregate filtering warning", report.Warnings)
	}
}

func TestImport_ClaudeWrapperOnlyDoesNotCreateSession(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry_run=%t", dryRun), func(t *testing.T) {
			t.Setenv("CHORD_STATE_DIR", t.TempDir())
			t.Setenv("CHORD_SESSIONS_DIR", "")
			projectRoot := t.TempDir()
			input := filepath.Join(t.TempDir(), "claude.jsonl")
			data := []byte(`{"uuid":"u1","message":{"role":"user","content":"<command-name>/help</command-name>"}}`)
			if err := os.WriteFile(input, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var report ImportReport
			msgs, err := convertClaudeTranscript(data, ReasoningStrict, &report)
			if err != nil || len(msgs) != 0 || report.Claude.WrappedUserBlocksSkipped != 1 || report.Claude.WrappedUserLinesSkipped != 1 {
				t.Fatalf("messages=%+v, claude=%+v, err=%v", msgs, report.Claude, err)
			}
			res, err := Import(context.Background(), ImportOptions{Source: "claude", InputPath: input, ProjectRoot: projectRoot, SessionID: "wrapper-only", DryRun: dryRun})
			if err == nil || !strings.Contains(err.Error(), "no messages were imported") || res != nil {
				t.Fatalf("result=%+v, err=%v, want empty import rejected without a report", res, err)
			}
			locator, err := config.DefaultPathLocator()
			if err != nil {
				t.Fatal(err)
			}
			pl, err := locator.LocateProject(projectRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(pl.ProjectSessionsDir, "wrapper-only")); !os.IsNotExist(err) {
				t.Fatalf("session stat error=%v, want no session created", err)
			}
		})
	}
}

func TestImport_ClaudeWrappersPersistReportAndCleanPreview(t *testing.T) {
	t.Setenv("CHORD_STATE_DIR", t.TempDir())
	t.Setenv("CHORD_SESSIONS_DIR", "")
	input := filepath.Join(t.TempDir(), "claude.jsonl")
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":"<command-name>/help</command-name>"}}
{"uuid":"u2","parentUuid":"u1","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"<local-command-stdout>help text</local-command-stdout>"},{"type":"text","text":"Explain the function."}]}}
{"uuid":"a1","parentUuid":"u2","message":{"role":"assistant","content":"The function returns a value."}}`)
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Import(context.Background(), ImportOptions{Source: "claude", InputPath: input, ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(res.SessionDir, importReportFileName))
	if err != nil {
		t.Fatal(err)
	}
	var report ImportReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Claude == nil || report.Claude.WrappedUserBlocksSkipped != 2 || report.Claude.WrappedUserLinesSkipped != 1 || report.ImportedMessages != 2 {
		t.Fatalf("report=%+v, claude=%+v, want 2 blocks and 1 source record skipped", report, report.Claude)
	}
	if len(report.Warnings) != 1 || report.Warnings[0] != "claude: dropped 2 wrapped user text block(s); 1 user message(s) skipped as a result" {
		t.Fatalf("warnings=%v, want one aggregate filtering warning", report.Warnings)
	}
	preview, err := recovery.FirstUserMessageFromFile(filepath.Join(res.SessionDir, identity.MainSessionLogFilename))
	if err != nil || preview != "Explain the function." {
		t.Fatalf("preview=%q, err=%v", preview, err)
	}
}
