package sessionimport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
)

func TestConvertOpenCodeExport_SourceTitle(t *testing.T) {
	const messages = `[{"role":"user","title":"message title","content":"inspect files"},{"role":"assistant","parts":[{"type":"tool","tool":"bash","callID":"call-1","state":{"status":"completed","input":{"command":"pwd"},"output":"/tmp","title":"tool title"}}]}]`
	cases := []struct {
		name  string
		info  string
		title string
	}{
		{name: "trimmed", info: `{"id":"source-1","title":"  final title \n"}`, title: "final title"},
		{name: "missing", info: `{"id":"source-1"}`},
		{name: "empty", info: `{"id":"source-1","title":""}`},
		{name: "blank", info: `{"id":"source-1","title":" \t\n"}`},
		{name: "null", info: `{"id":"source-1","title":null}`},
		{name: "number", info: `{"id":"source-1","title":42}`},
		{name: "boolean", info: `{"id":"source-1","title":true}`},
		{name: "array", info: `{"id":"source-1","title":["nested title"]}`},
		{name: "object", info: `{"id":"source-1","title":{"title":"nested title"}}`},
		{name: "null info", info: `null`},
		{name: "missing info"},
		{name: "array root"},
	}
	var baseline ImportReport
	wantMessages, err := convertOpenCodeExport([]byte(`{"messages":`+messages+`}`), ReasoningStrict, &baseline)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := `{"messages":` + messages + `}`
			if tc.info != "" {
				data = `{"info":` + tc.info + `,"messages":` + messages + `}`
			}
			if tc.name == "array root" {
				data = messages
			}
			var report ImportReport
			msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if report.SourceTitle != tc.title {
				t.Fatalf("SourceTitle=%q, want %q", report.SourceTitle, tc.title)
			}
			wantID := ""
			if strings.HasPrefix(tc.info, "{") {
				wantID = "source-1"
			}
			if report.SourceSessionID != wantID {
				t.Fatalf("SourceSessionID=%q, want %q", report.SourceSessionID, wantID)
			}
			if !reflect.DeepEqual(msgs, wantMessages) {
				t.Fatalf("title metadata changed messages: got %+v, want %+v", msgs, wantMessages)
			}
			report.SourceTitle = ""
			report.SourceSessionID = ""
			if !reflect.DeepEqual(report, baseline) {
				t.Fatalf("title metadata changed conversion statistics: got %+v, want %+v", report, baseline)
			}
		})
	}
}

func TestConvertClaudeTranscript_SourceTitleSurvivesInvalidValues(t *testing.T) {
	const conversation = `{"uuid":"u1","message":{"role":"user","content":"plain question"}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":"answer"}}
`
	for _, invalid := range []string{
		`{"type":"ai-title"}`,
		`{"type":"ai-title","aiTitle":""}`,
		`{"type":"ai-title","aiTitle":" \t\n"}`,
		`{"type":"ai-title","aiTitle":null}`,
		`{"type":"ai-title","aiTitle":42}`,
		`{"type":"ai-title","aiTitle":true}`,
		`{"type":"ai-title","aiTitle":[]}`,
		`{"type":"ai-title","aiTitle":{}}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			for _, later := range []bool{false, true} {
				data := conversation + "{\"type\":\"ai-title\",\"aiTitle\":\"draft title\"}\n" + invalid + "\n"
				wantTitle, wantMetadata := "draft title", 2
				if later {
					data += "{\"type\":\"ai-title\",\"aiTitle\":\"  final title  \"}\n"
					wantTitle, wantMetadata = "final title", 3
				}
				var report ImportReport
				msgs, err := convertClaudeTranscript([]byte(data), ReasoningStrict, &report)
				if err != nil {
					t.Fatalf("convert: %v", err)
				}
				if report.SourceTitle != wantTitle {
					t.Fatalf("SourceTitle=%q, want %q", report.SourceTitle, wantTitle)
				}
				if len(msgs) != 2 || msgs[0].Content != "plain question" || msgs[1].Content != "answer" {
					t.Fatalf("title entries changed messages: %+v", msgs)
				}
				if report.Claude == nil || report.Claude.MetadataEntries != wantMetadata {
					t.Fatalf("metadata count: %+v, want %d", report.Claude, wantMetadata)
				}
				if len(report.Warnings) != 0 {
					t.Fatalf("unexpected warnings: %v", report.Warnings)
				}
			}
		})
	}
}

func TestImport_SourceTitlePersistenceAndOverwrite(t *testing.T) {
	for _, source := range []string{"claude", "opencode"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("CHORD_STATE_DIR", t.TempDir())
			t.Setenv("CHORD_SESSIONS_DIR", "")
			projectRoot := t.TempDir()
			input := filepath.Join(t.TempDir(), "input.json")
			opts := ImportOptions{Source: source, InputPath: input, ProjectRoot: projectRoot, SessionID: "title-session"}
			locator, err := config.DefaultPathLocator()
			if err != nil {
				t.Fatal(err)
			}
			paths, err := locator.EnsureProject(projectRoot)
			if err != nil {
				t.Fatal(err)
			}
			sessionDir := filepath.Join(paths.ProjectSessionsDir, opts.SessionID)
			const sourceID = "source-1"
			previousTitle := ""
			for index, title := range []string{"draft title", "final title", ""} {
				info := map[string]string{"id": sourceID}
				if title != "" {
					info["title"] = "  " + title + "  "
				}
				encodedInfo, err := json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				data := `{"info":` + string(encodedInfo) + `,"messages":[{"role":"user","content":"plain question"},{"role":"assistant","content":"answer"}]}`
				if source == "claude" {
					data = `{"sessionId":"source-1","uuid":"u1","message":{"role":"user","content":"plain question"}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":"answer"}}
`
					if title != "" {
						data += `{"type":"ai-title","aiTitle":"  ` + title + `  "}`
					}
				}
				if writeErr := os.WriteFile(input, []byte(data), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}

				opts.Force = index > 0
				opts.DryRun = true
				before, err := os.ReadDir(paths.ProjectSessionsDir)
				if err != nil {
					t.Fatal(err)
				}
				dry, err := Import(context.Background(), opts)
				if err != nil {
					t.Fatalf("dry-run: %v", err)
				}
				if dry.Report.SourceTitle != title || dry.SessionID != "" || dry.SessionDir != "" {
					t.Fatalf("unexpected dry-run result: %+v", dry)
				}
				after, err := os.ReadDir(paths.ProjectSessionsDir)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.EqualFunc(before, after, func(a, b os.DirEntry) bool { return a.Name() == b.Name() }) {
					t.Fatalf("dry-run changed session entries: before=%v after=%v", before, after)
				}
				if index > 0 {
					assertImportedSourceTitle(t, sessionDir, source, sourceID, previousTitle)
				}

				opts.DryRun = false
				if index == 1 {
					opts.Force = false
					if _, importErr := Import(context.Background(), opts); !errors.Is(importErr, errSessionIDExists) {
						t.Fatalf("overwrite without force: %v", importErr)
					}
					assertImportedSourceTitle(t, sessionDir, source, sourceID, "draft title")
					opts.Force = true
				}
				res, err := Import(context.Background(), opts)
				if err != nil {
					t.Fatalf("import: %v", err)
				}
				if res.Report.SourceTitle != title || res.SessionDir != sessionDir || res.SessionID != opts.SessionID {
					t.Fatalf("unexpected import result: %+v", res)
				}
				assertImportedSourceTitle(t, sessionDir, source, sourceID, title)
				sessions, err := recovery.ListSessions(paths.ProjectSessionsDir, "")
				if err != nil {
					t.Fatalf("list sessions: %v", err)
				}
				if len(sessions) != 1 || sessions[0].ID != opts.SessionID || sessions[0].Title != title {
					t.Fatalf("unexpected listed sessions: %+v", sessions)
				}
				previousTitle = title
			}
		})
	}
}

func assertImportedSourceTitle(t *testing.T, sessionDir, source, sourceID, title string) {
	t.Helper()
	meta, err := recovery.LoadSessionMeta(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.Title != title || meta.ImportedFrom == nil ||
		meta.ImportedFrom.Source != source || meta.ImportedFrom.SourceSessionID != sourceID {
		t.Fatalf("unexpected session metadata: %+v, want title %q and source %s/%s", meta, title, source, sourceID)
	}
	for _, file := range []struct{ name, key string }{
		{"session-meta.json", "title"},
		{importReportFileName, "source_title"},
	} {
		data, err := os.ReadFile(filepath.Join(sessionDir, file.name))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		value, present := fields[file.key]
		if title == "" {
			if present {
				t.Fatalf("%s contains empty %s: %s", file.name, file.key, value)
			}
			continue
		}
		var got string
		if err := json.Unmarshal(value, &got); err != nil || got != title {
			t.Fatalf("%s: %s=%q, want %q (error: %v)", file.name, file.key, got, title, err)
		}
	}
}
