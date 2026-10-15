package tui

import (
	"archive/zip"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

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
	if !strings.Contains(text, "No cache read was confirmed for this request") {
		t.Fatal("zero cache presented as cache miss")
	}
}

// The panel and the sidebar must not read as the same measurement: the panel
// reports one request at a time, the sidebar's cache rows accumulate every
// call.
func TestContextCachePageDisambiguatesRequestFromSessionTotals(t *testing.T) {
	v := agent.ContextDiagnosticsView{
		Available: true, AgentName: "sample", WindowLimit: 200_000,
		SessionCalls: 44, SessionCacheRead: 3_700_000, SessionCacheWrite: 12_000,
		Snapshot: contextdiag.Snapshot{
			ID: contextdiag.RequestID{Binding: contextdiag.Binding{TaskID: "main", Epoch: 1}}, Revision: 3,
			State: contextdiag.Completed, ModelRef: "sample/test-model", WindowLimit: 200_000, Completeness: contextdiag.Estimated,
			Usage: contextdiag.Usage{State: contextdiag.Reported, FullInput: 136_600, CacheRead: 100_000},
			Cache: contextdiag.Cache{BaselineState: contextdiag.Comparable, PrefixState: contextdiag.Append, ExpectedPrefix: 265_468, ExpectedUsable: true, Explanation: contextdiag.ReadObserved},
		},
	}
	// The panel hard-wraps to the dialog width, so compare on collapsed
	// whitespace rather than on rendered line breaks.
	text := strings.Join(strings.Fields(strings.Join(contextDiagnosticLines(v, 2, 100), " ")), " ")
	for _, want := range []string{
		"Service reported 100000 cache-read tokens for this request",
		"cumulative across all 44 calls",
		"Cumulative cache read so far: 3.7M tokens",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cache page missing %q in:\n%s", want, text)
		}
	}
	// The panel must not present a per-request figure under the sidebar's
	// cumulative row label. The scope note names that row on purpose, so only
	// the data lines are checked.
	for _, line := range contextDiagnosticLines(v, 2, 100) {
		if strings.HasPrefix(strings.TrimSpace(line), "Cache R") {
			t.Fatalf("panel adopted the sidebar's cumulative row label: %q", line)
		}
	}

	// A session with no recorded usage carries no scope note, so an untouched
	// task is not annotated with zeroes.
	fresh := v
	fresh.SessionCalls, fresh.SessionCacheRead, fresh.SessionCacheWrite = 0, 0, 0
	if note := contextSessionScopeNote(fresh); note != nil {
		t.Fatal("scope note invented for an empty session", note)
	}
}

// Percentages in the panel must divide by the same window the sidebar gauge
// uses, otherwise the two numbers invite a comparison that cannot hold.
func TestContextCompositionAnchorsEstimatesToTheWindow(t *testing.T) {
	v := agent.ContextDiagnosticsView{
		Available: true, AgentName: "sample", WindowLimit: 200_000,
		Snapshot: contextdiag.Snapshot{
			ID: contextdiag.RequestID{Binding: contextdiag.Binding{TaskID: "main", Epoch: 1}}, Revision: 1,
			State: contextdiag.Completed, ModelRef: "sample/test-model", WindowLimit: 200_000, Completeness: contextdiag.Estimated,
			Usage: contextdiag.Usage{State: contextdiag.Reported, FullInput: 100_000},
			Categories: []contextdiag.Category{
				{Kind: contextdiag.Conversation, Estimate: contextdiag.Estimate{Tokens: 150_000}},
				{Kind: contextdiag.Tools, Estimate: contextdiag.Estimate{Tokens: 50_000}},
			},
		},
	}
	text := strings.Join(contextDiagnosticLines(v, 0, 110), "\n")
	if !strings.Contains(text, "of the "+formatTokens(v.WindowLimit)+" window") {
		t.Fatalf("no window anchor in:\n%s", text)
	}
	// The share of the estimate stays a share of the estimate.
	if !strings.Contains(text, "75.0% of the estimate") {
		t.Fatalf("category share lost its denominator in:\n%s", text)
	}
	// A target with no live context manager has no window to divide by.
	noWindow := v
	noWindow.WindowLimit = 0
	noWindow.Snapshot.WindowLimit = 0
	if share := contextWindowShare(150_000, contextWindowLimit(noWindow)); share != "" {
		t.Fatal("share rendered without a window", share)
	}
}

// A historical request keeps the window it was dispatched against. Dividing an
// old request by the currently focused model's budget misstates how full the
// window was: 180k tokens are 90% of a 200k window but a comfortable 18% of a
// 1M one, and the near-capacity finding must fire on the former.
func TestContextCompositionAnchorsHistoricalSnapshotToItsOwnWindow(t *testing.T) {
	v := agent.ContextDiagnosticsView{
		Available: true, AgentName: "sample", WindowLimit: 1_000_000, Historical: true,
		Snapshot: contextdiag.Snapshot{
			ID: contextdiag.RequestID{Binding: contextdiag.Binding{TaskID: "main", Epoch: 1}}, Revision: 1,
			ModelRef: "sample/small-model", WindowLimit: 200_000,
			State: contextdiag.Completed, Completeness: contextdiag.Estimated,
			Usage: contextdiag.Usage{State: contextdiag.Reported, FullInput: 180_000},
			Categories: []contextdiag.Category{
				{Kind: contextdiag.Conversation, Estimate: contextdiag.Estimate{Tokens: 150_000}},
				{Kind: contextdiag.Tools, Estimate: contextdiag.Estimate{Tokens: 30_000}},
			},
		},
	}
	text := strings.Join(contextDiagnosticLines(v, 0, 110), "\n")
	if !strings.Contains(text, "of the "+formatTokens(200_000)+" window") {
		t.Fatalf("historical snapshot anchored to the current window:\n%s", text)
	}
	if strings.Contains(text, "of the "+formatTokens(1_000_000)+" window") {
		t.Fatalf("historical snapshot adopted the current model's window:\n%s", text)
	}
	suggestions := contextSuggestions(v, v.Snapshot)
	if len(suggestions) == 0 || suggestions[0].severity != "warning" || !strings.HasPrefix(suggestions[0].title, "Context is ") {
		t.Fatalf("near-capacity finding missing for a historical request: %+v", suggestions)
	}
	if got := contextWindowLimit(v); got != 200_000 {
		t.Fatalf("window limit = %d, want the request-time budget", got)
	}
}

// contextCollapseForCompare strips whitespace so assertions survive the
// dialog's hard wrapping, which breaks lines at arbitrary character offsets.
func contextCollapseForCompare(lines []string) string {
	var b strings.Builder
	for _, line := range lines {
		for _, r := range line {
			if !unicode.IsSpace(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func TestContextSuggestionsAreActionableAndWindowRelative(t *testing.T) {
	base := contextdiag.Snapshot{
		ID: contextdiag.RequestID{Binding: contextdiag.Binding{TaskID: "main", Epoch: 1}}, Revision: 1,
		State: contextdiag.Completed, ModelRef: "sample/test-model", WindowLimit: 200_000, Completeness: contextdiag.Estimated,
		Usage: contextdiag.Usage{State: contextdiag.Reported, FullInput: 180_000},
		Categories: []contextdiag.Category{
			{Kind: contextdiag.Conversation, Estimate: contextdiag.Estimate{Tokens: 40_000}},
			{Kind: contextdiag.ToolResults, Estimate: contextdiag.Estimate{Tokens: 120_000}},
			{Kind: contextdiag.Instructions, Estimate: contextdiag.Estimate{Tokens: 8_000}},
		},
	}
	v := agent.ContextDiagnosticsView{Available: true, AgentName: "sample", WindowLimit: 200_000, Snapshot: base}
	suggestions := contextSuggestions(v, base)
	if len(suggestions) < 3 {
		t.Fatalf("expected several findings, got %d", len(suggestions))
	}
	// The near-capacity finding leads even though it carries no savings of its
	// own: it states the emergency the other findings explain.
	if suggestions[0].severity != "warning" || !strings.HasPrefix(suggestions[0].title, "Context is ") {
		t.Fatal("near-capacity warning not first", suggestions[0])
	}
	if !strings.Contains(suggestions[0].detail, "Run /compact to request compaction now.") {
		t.Fatal("near-capacity action does not describe manual compaction", suggestions[0])
	}
	found := map[string]bool{}
	for _, sug := range suggestions {
		if sug.title == "" || sug.detail == "" {
			t.Fatal("finding without an action", sug)
		}
		word, _, _ := strings.Cut(sug.title, " ")
		found[word] = true
	}
	// Every finding must reach the reader with both its claim and its action,
	// not just exist in the slice. The dialog hard-wraps mid-word, so compare
	// with all whitespace removed rather than on rendered line breaks.
	rendered := contextCollapseForCompare(contextDiagnosticLines(v, 0, 110))
	for _, sug := range suggestions {
		if !strings.Contains(rendered, contextCollapseForCompare([]string{sug.title})) ||
			!strings.Contains(rendered, contextCollapseForCompare([]string{sug.detail})) {
			t.Fatalf("finding not rendered with its action: %+v\n%s", sug, rendered)
		}
	}
	if !found["Context"] {
		t.Fatal("near-capacity finding missing")
	}
	if !found["Tool"] {
		t.Fatal("tool-result finding missing")
	}
	// Without a window there is nothing to be a share of, so the panel says so
	// instead of reporting a small share of a window that does not exist.
	noWindowSnapshot := base
	noWindowSnapshot.WindowLimit = 0
	noWindow := agent.ContextDiagnosticsView{Available: true, AgentName: "sample", Snapshot: noWindowSnapshot}
	if got := contextSuggestions(noWindow, base); got != nil {
		t.Fatal("suggestions produced without a window", got)
	}
	if lines := contextSuggestionLines(noWindow, base); len(lines) != 1 || !strings.Contains(lines[0], "no context window") {
		t.Fatalf("suggestion section without a window = %q", lines)
	}
	// A small request against a large window produces no findings at all.
	small := base
	small.Usage.FullInput = 1_000
	small.Categories = []contextdiag.Category{{Kind: contextdiag.Conversation, Estimate: contextdiag.Estimate{Tokens: 900}}}
	if got := contextSuggestions(agent.ContextDiagnosticsView{WindowLimit: 200_000, Snapshot: small}, small); len(got) != 0 {
		t.Fatal("small request produced findings", got)
	}
}

func TestContextSuggestionsDoNotInferSmallInputFromUnknownData(t *testing.T) {
	s := contextdiag.Snapshot{
		Completeness: contextdiag.Partial,
		Usage:        contextdiag.Usage{State: contextdiag.Missing},
		Categories:   []contextdiag.Category{{Kind: contextdiag.Attachments, Estimate: contextdiag.Estimate{UnknownItems: 1}}},
	}
	lines := contextSuggestionLines(agent.ContextDiagnosticsView{WindowLimit: 200_000}, s)
	if len(lines) != 1 || strings.Contains(lines[0], "small share") || !strings.Contains(lines[0], "available data") {
		t.Fatalf("unknown input produced an unsupported conclusion: %q", lines)
	}
}

func TestContextZeroSnapshotBudgetDoesNotBorrowCurrentWindow(t *testing.T) {
	v := agent.ContextDiagnosticsView{
		Available: true, WindowLimit: 200_000,
		Snapshot: contextdiag.Snapshot{WindowLimit: 0},
	}
	if got := contextWindowLimit(v); got != 0 {
		t.Fatalf("zero request budget replaced by current window: %d", got)
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
