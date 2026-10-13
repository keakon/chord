package tui

import (
	"archive/zip"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/contextdiag"
)

type contextDiagnosticAgent struct {
	*infoPanelAgent
	view  agent.ContextDiagnosticsView
	reads int
}

func (a *contextDiagnosticAgent) FocusedContextDiagnostics() agent.ContextDiagnosticsView {
	a.reads++
	return a.view
}
func TestContextCommandStaysLocalWithAttachments(t *testing.T) {
	for _, command := range []string{"/context", "  /CoNtExT  ", "/context extra"} {
		backend := &loopBusyAgentStub{}
		m := NewModelWithSize(backend, 80, 24)
		m.attachments = []Attachment{{FileName: "sample.png", Data: []byte{1, 2, 3}}}
		m.input.SetValue(command)
		enter := tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
		m.handleInsertKey(enter)
		if m.mode == ModeInsert && command != "/context extra" {
			m.handleInsertKey(enter)
		}
		if len(m.attachments) != 1 || len(m.queuedDrafts) != 0 {
			t.Fatal("command sent attachments or queued a turn")
		}
		if command == "/context extra" {
			if m.mode != ModeInsert || m.input.Value() != command {
				t.Fatal("invalid syntax did not preserve input")
			}
		} else if m.mode != ModeContext || m.input.Value() != "" {
			t.Fatal("command not handled locally", m.mode)
		}
	}
}
func TestContextOverlayPagesTicksAndLayout(t *testing.T) {
	for _, width := range []int{20, 40, 80, 140} {
		backend := &contextDiagnosticAgent{infoPanelAgent: newInfoPanelAgent(), view: agent.ContextDiagnosticsView{Available: true, AgentName: "sample", Snapshot: contextdiag.Snapshot{ID: contextdiag.RequestID{Binding: contextdiag.Binding{TaskID: "main", Epoch: 1}}, Revision: 1, StartedAt: time.Now(), State: contextdiag.Completed, ModelRef: "sample/test-model", EstimateScope: contextdiag.ProviderInput, Completeness: contextdiag.Partial, Categories: []contextdiag.Category{{Kind: contextdiag.Conversation, Estimate: contextdiag.Estimate{Tokens: 10, UnknownItems: 1}}}, Usage: contextdiag.Usage{State: contextdiag.Missing}, Reduction: contextdiag.Reduction{State: contextdiag.Unavailable}}}}
		m := NewModelWithSize(backend, width, 16)
		m.input.SetValue("sample draft")
		m.View()
		before := m.viewport.height
		if m.openContextOverlay() == nil {
			t.Fatal("no polling command")
		}
		generation := m.contextOverlay.generation
		m.View()
		if m.viewport.height != before {
			t.Fatal("overlay changed flow")
		}
		reads := backend.reads
		m.View()
		if backend.reads != reads {
			t.Fatal("View read backend")
		}
		m.handleContextOverlayKey(tea.KeyPressMsg(tea.Key{Text: "2", Code: '2'}))
		m.View()
		if m.contextOverlay.page != 1 || m.contextOverlay.scroll != 0 {
			t.Fatal("page switch")
		}
		m.handleContextOverlayKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
		if m.contextOverlay.page != 2 {
			t.Fatal("tab cycle")
		}
		m.handleContextOverlayKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'}))
		m.View()
		if m.contextOverlay.scroll < 0 {
			t.Fatal("bad scroll")
		}
		m.closeContextOverlay()
		if m.input.Value() != "sample draft" || m.mode != ModeInsert {
			t.Fatal("composer not restored")
		}
		_, cmd := m.Update(contextOverlayTickMsg{generation: generation})
		if cmd != nil {
			t.Fatal("closed overlay revived tick")
		}
		m.openContextOverlay()
		reads = backend.reads
		_, cmd = m.Update(contextOverlayTickMsg{generation: generation})
		if cmd != nil || backend.reads != reads {
			t.Fatal("old tick entered new chain")
		}
		m.contextOverlay.scroll = 5
		backend.view.Snapshot.ID.Binding.Epoch++
		m.refreshContextOverlay()
		if m.contextOverlay.scroll != 0 {
			t.Fatal("binding switch kept scroll")
		}
	}
}
func TestContextDiagnosticUnknownAndExport(t *testing.T) {
	v := agent.ContextDiagnosticsView{Available: true, AgentName: "private-agent", Snapshot: contextdiag.Snapshot{ID: contextdiag.RequestID{Binding: contextdiag.Binding{SessionID: "private-session", TaskID: "private-task", Epoch: 8}, Seq: 2, Attempt: 3}, Revision: 7, ModelRef: "private-model", State: contextdiag.Completed, Usage: contextdiag.Usage{State: contextdiag.Missing}, Categories: []contextdiag.Category{{Kind: contextdiag.Tools, Sources: []contextdiag.Source{{Kind: contextdiag.Tool, Label: "private-tool"}, {Kind: contextdiag.Skill, Label: "private-skill"}}}}, Reduction: contextdiag.Reduction{Rules: []contextdiag.Rule{{Tool: "private-tool", Reason: "read_like"}}}}}
	data := exportContextDiagnostics(v)
	if strings.Contains(string(data), "private-") || strings.Contains(string(data), "SessionID") || strings.Contains(string(data), "Epoch") {
		t.Fatal("private identity leaked", string(data))
	}
	for _, value := range []string{"agent-1", "model-1", "tool-1", "skill-1", `"revision": 7`, `"schema_version": 1`} {
		if !strings.Contains(string(data), value) {
			t.Fatal("missing export field", value)
		}
	}
	var envelope map[string]any
	if err := json.Unmarshal(exportContextDiagnostics(agent.ContextDiagnosticsView{UnavailableReason: "raw unsafe reason"}), &envelope); err != nil || envelope["available"] != false || envelope["unavailable_reason"] != "not_observed" {
		t.Fatal("empty export", envelope)
	}
	for _, width := range []int{1, 20, 80} {
		for _, line := range contextDiagnosticLines(v, 2, width) {
			if ansi.StringWidth(line) > width {
				t.Fatal("line overflows", line)
			}
		}
	}
	text := strings.Join(contextDiagnosticLines(v, 2, 100), "\n")
	if !strings.Contains(text, "原因未知") {
		t.Fatal("zero cache presented as cache miss")
	}
}

func TestContextOverlayExportsVisibleSnapshotWithConfiguredShortcut(t *testing.T) {
	for _, key := range []string{"ctrl+g", "ctrl+y"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"CHORD_CONFIG_HOME", "CHORD_STATE_DIR", "CHORD_CACHE_DIR", "CHORD_SESSIONS_DIR"} {
				t.Setenv(name, filepath.Join(root, name))
			}
			backend := &contextDiagnosticAgent{infoPanelAgent: newInfoPanelAgent(), view: agent.ContextDiagnosticsView{Available: true, Snapshot: contextdiag.Snapshot{ID: contextdiag.RequestID{Binding: contextdiag.Binding{TaskID: "main"}, Seq: 2, Attempt: 3}, Revision: 7}}}
			m := NewModelWithSize(backend, 80, 24)
			m.workingDir = root
			m.keyMap.Diagnostics = []string{key}
			m.openContextOverlay()
			backend.view.Snapshot.Revision = 8
			backend.view.Snapshot.ID.Attempt = 4
			reads := backend.reads
			cmd := m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: rune(key[len(key)-1]), Mod: tea.ModCtrl}))
			if cmd == nil {
				t.Fatal("export shortcut ignored")
			}
			result, ok := cmd().(diagnosticsBundleMsg)
			if !ok || result.err != nil {
				t.Fatalf("export failed: %+v", result)
			}
			bundle, err := zip.OpenReader(result.path)
			if err != nil {
				t.Fatal(err)
			}
			defer bundle.Close()
			found := false
			for _, f := range bundle.File {
				if f.Name != "context.json" {
					continue
				}
				r, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					t.Fatal(err)
				}
				var envelope struct {
					Snapshot struct {
						Revision uint64
						Attempt  uint64
					}
				}
				if err := json.Unmarshal(data, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Snapshot.Revision != 7 || envelope.Snapshot.Attempt != 3 {
					t.Fatalf("export used live snapshot: %s", data)
				}
				found = true
			}
			if !found || backend.reads != reads || m.mode != ModeContext {
				t.Fatal("export must preserve the visible snapshot and overlay")
			}
			m.keyMap.Diagnostics = nil
			if m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl})) != nil {
				t.Fatal("disabled binding exported diagnostics")
			}
		})
	}
}
