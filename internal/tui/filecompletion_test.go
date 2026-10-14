package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/filecompletion"
	"github.com/keakon/chord/internal/tui/overlaylist"
)

func atMentionKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Text: "@", Code: '@'})
}

func TestModelInitPreloadsAtMentionFiles(t *testing.T) {
	m := NewModel(nil)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() cmd = nil, want preload command")
	}
	if !m.atMentionLoading {
		t.Fatal("atMentionLoading = false, want true after Init()")
	}

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init() msg = %T, want tea.BatchMsg", msg)
	}
	if len(batch) == 0 {
		t.Fatal("Init() batch is empty")
	}
	found := false
	for _, child := range batch {
		if child == nil {
			continue
		}
		if _, ok := child().(atMentionFilesLoadedMsg); ok {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Init() batch missing atMention file preload command")
	}
}

func TestStartAtMentionFileLoadIsSingleFlight(t *testing.T) {
	m := NewModel(nil)

	first := m.startAtMentionFileLoad()
	if first == nil {
		t.Fatal("first startAtMentionFileLoad() = nil, want command")
	}
	if !m.atMentionLoading {
		t.Fatal("atMentionLoading = false after first load start")
	}
	if second := m.startAtMentionFileLoad(); second != nil {
		t.Fatal("second startAtMentionFileLoad() should be nil while loading")
	}

	updated, _ := m.Update(atMentionFilesLoadedMsg{files: []string{"docs/ARCHITECTURE.md"}, workDir: m.workingDir, generation: m.workingDirGeneration})
	model := updated.(*Model)
	if model.atMentionLoading {
		t.Fatal("atMentionLoading = true after load completion, want false")
	}
	if !model.atMentionLoaded {
		t.Fatal("atMentionLoaded = false after load completion, want true")
	}
	if next := model.startAtMentionFileLoad(); next != nil {
		t.Fatal("startAtMentionFileLoad() immediately after load completion should be nil")
	}
	model.atMentionLoadedAt = time.Now().Add(-atMentionRefreshTTL)
	if next := model.startAtMentionFileLoad(); next == nil {
		t.Fatal("startAtMentionFileLoad() after TTL should return refresh command")
	}
}

func TestAtMentionFilesLoadedDropsOlderGeneration(t *testing.T) {
	for _, generation := range []uint64{0, 1} {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			m := NewModel(nil)
			m.workingDir = "/tmp/current-checkout"
			m.workingDirGeneration = 2
			m.atMentionLoading = true

			updated, _ := m.Update(atMentionFilesLoadedMsg{
				files:      []string{"old/checkout.go"},
				workDir:    "/tmp/current-checkout",
				generation: generation,
			})
			model := updated.(*Model)
			if model.atMentionLoaded {
				t.Fatal("stale at-mention result marked index loaded")
			}
			if model.atMentionFiles != nil {
				t.Fatalf("stale at-mention files = %#v, want nil", model.atMentionFiles)
			}
		})
	}
}

func TestApplyWorkDirSnapshotInvalidatesAtMentionOnGenerationChange(t *testing.T) {
	m := NewModel(nil)
	m.workingDir = "/tmp/checkouts/project"
	m.workingDirID = "main"
	m.workingDirGeneration = 4
	m.atMentionLoaded = true
	m.atMentionLoadedAt = time.Now()
	m.atMentionFiles = []string{"old.go"}
	m.atMentionFilesLower = map[string]string{"old.go": "old.go"}

	if !m.applyWorkDirSnapshot(agent.WorkDirSnapshot{
		Path:       "/tmp/checkouts/project",
		WorktreeID: "main",
		Generation: 5,
	}) {
		t.Fatal("generation-only checkout change was not reported")
	}
	if m.atMentionLoaded || m.atMentionLoading || m.atMentionFiles != nil {
		t.Fatalf("at-mention index survived generation change: loaded=%v loading=%v files=%#v", m.atMentionLoaded, m.atMentionLoading, m.atMentionFiles)
	}
}

func TestAtMentionFilesLoadedKeepsBareAtPopupOpen(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetValue("@")
	m.input.SetCursorPosition(0, 1)
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1
	m.atMentionLoading = true

	updated, _ := m.Update(atMentionFilesLoadedMsg{files: []string{"docs/ARCHITECTURE.md"}, workDir: m.workingDir, generation: m.workingDirGeneration})
	model := updated.(*Model)

	if !model.atMentionOpen {
		t.Fatal("atMentionOpen = false, want true for bare @ after load")
	}
	if got := model.atMentionQuery; got != "" {
		t.Fatalf("atMentionQuery = %q, want empty query for bare @", got)
	}
	if model.atMentionList == nil || model.atMentionList.Len() == 0 {
		t.Fatal("atMentionList should be populated for bare @ after load")
	}
}

func TestSubmitPlainAtMentionSendsFileParts(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "AGENTS.md"), "agent rules\n")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir(%q) error = %v", wd, err)
	}

	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 80, 24)
	m.mode = ModeInsert
	m.focusedAgentID = "worker-1"
	m.input.SetValue("update @AGENTS.md")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))

	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if got := len(backend.sentMultipart); got != 1 {
		t.Fatalf("SendUserMessageWithParts() calls = %d, want 1", got)
	}
	parts := backend.sentMultipart[0]
	if len(parts) != 2 {
		t.Fatalf("sent parts = %#v, want prompt text plus file part", parts)
	}
	if got := parts[0].Text; got != "update @AGENTS.md" {
		t.Fatalf("prompt part text = %q, want original prompt", got)
	}
	if !message.IsFileRefContent(parts[1].Text) || !strings.Contains(parts[1].Text, `<file path="AGENTS.md">`) || !strings.Contains(parts[1].Text, "agent rules") {
		t.Fatalf("file part = %q, want injected AGENTS.md content", parts[1].Text)
	}
}

func TestCanTriggerAtMention(t *testing.T) {
	tests := []struct {
		name string
		row  string
		col  int
		want bool
	}{
		{name: "line start", row: "", col: 0, want: true},
		{name: "after space", row: "hello ", col: 6, want: true},
		{name: "after multibyte and space", row: "你好 ", col: 3, want: true},
		{name: "after non-space", row: "hello", col: 5, want: false},
		{name: "after tab", row: "hello\t", col: 6, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canTriggerAtMention(tt.row, tt.col); got != tt.want {
				t.Fatalf("canTriggerAtMention(%q, %d) = %v, want %v", tt.row, tt.col, got, tt.want)
			}
		})
	}
}

func TestInputTokenAtIsRuneSafe(t *testing.T) {
	got, ok := inputTokenAt("你好 @abc", 0, 4, 7)
	if !ok {
		t.Fatal("inputTokenAt() ok = false, want true")
	}
	if got != "abc" {
		t.Fatalf("inputTokenAt() = %q, want %q", got, "abc")
	}
}

func TestHandleInsertKeyOpensAtMentionAfterMultibyteSpace(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.atMentionLoaded = true
	m.atMentionFiles = []string{"internal/tui/app.go"}
	m.input.SetValue("你好 ")

	_ = m.handleInsertKey(atMentionKey())

	if !m.atMentionOpen {
		t.Fatal("atMentionOpen = false, want true")
	}
	if got := m.input.Value(); got != "你好 @" {
		t.Fatalf("input value = %q, want %q", got, "你好 @")
	}
	if m.atMentionList == nil || m.atMentionList.Len() == 0 {
		t.Fatal("atMentionList should be populated")
	}
}

func TestRefreshAtMentionListPrefersExactIndexedMatchOnly(t *testing.T) {
	m := NewModel(nil)
	m.atMentionLoaded = true
	m.atMentionFiles = []string{
		"docs/file.md",
		"docs/architecture/file-mentions.md",
		"docs/other-file.md",
	}
	m.atMentionOpen = true
	m.atMentionQuery = "docs/file.md"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want one exact result")
	}
	if got := m.atMentionList.Len(); got != 1 {
		t.Fatalf("atMentionList.Len() = %d, want 1 exact result", got)
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match, _ := item.Value.(filecompletion.Option)
	if match.Path != "docs/file.md" || match.IsDir {
		t.Fatalf("selected match = %+v, want exact docs/file.md file", match)
	}
}

func TestRefreshAtMentionListAcceptsLineRangeSuffixForIndexedExactMatch(t *testing.T) {
	m := NewModel(nil)
	m.atMentionLoaded = true
	m.atMentionFiles = []string{"docs/file.md", "docs/file_other.md"}
	m.atMentionOpen = true
	m.atMentionQuery = "docs/file.md:12"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want one exact result")
	}
	if got := m.atMentionList.Len(); got != 1 {
		t.Fatalf("atMentionList.Len() = %d, want 1 exact result", got)
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match, _ := item.Value.(filecompletion.Option)
	if match.Path != "docs/file.md" || match.IsDir {
		t.Fatalf("selected match = %+v, want exact docs/file.md file", match)
	}
}

func TestRefreshAtMentionListPrefersExactFilesystemMatchOutsideCache(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "new.txt"), "new")
	mustWriteFile(t, filepath.Join(wd, "new-dir", "child.txt"), "child")

	m := NewModel(nil)
	m.workingDir = wd
	m.atMentionLoaded = true
	m.atMentionFiles = []string{"old.txt"}
	m.atMentionOpen = true
	m.atMentionQuery = "new.txt"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want exact filesystem file result")
	}
	if got := m.atMentionList.Len(); got != 1 {
		t.Fatalf("atMentionList.Len() = %d, want 1 exact result", got)
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match, _ := item.Value.(filecompletion.Option)
	if match.Path != "new.txt" || match.IsDir {
		t.Fatalf("selected match = %+v, want exact new.txt file", match)
	}

	m.atMentionQuery = "new-dir"
	m.refreshAtMentionList()

	item, ok = m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() for directory ok = false, want true")
	}
	match, _ = item.Value.(filecompletion.Option)
	if match.Path != "new-dir/" || !match.IsDir {
		t.Fatalf("selected directory match = %+v, want exact new-dir/ directory", match)
	}
}

func TestRefreshAtMentionListIncludesIgnoredRootPrefixMatch(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "AGENTS.md"), "agents")
	mustWriteFile(t, filepath.Join(wd, ".gitignore"), "AGENTS.md\n")
	mustWriteFile(t, filepath.Join(wd, ".env"), "secret")
	mustWriteFile(t, filepath.Join(wd, "app.png"), "png")

	m := NewModel(nil)
	m.workingDir = wd
	m.atMentionLoaded = true
	m.atMentionFiles = []string{"docs/guide.md"}
	m.atMentionOpen = true
	m.atMentionQuery = "A"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want root prefix result")
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match, _ := item.Value.(filecompletion.Option)
	if match.Path != "AGENTS.md" || match.IsDir {
		t.Fatalf("selected match = %+v, want AGENTS.md file", match)
	}
	if got := m.atMentionList.Len(); got != 1 {
		t.Fatalf("atMentionList.Len() = %d, want only safe root prefix result", got)
	}

	m.atMentionQuery = ""
	m.refreshAtMentionList()
	if m.atMentionList == nil {
		t.Fatal("bare @ list = nil, want indexed results")
	}
	if got := m.atMentionList.Len(); got != 1 {
		t.Fatalf("bare @ list length = %d, want indexed result only", got)
	}
	item, ok = m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() for bare @ ok = false, want true")
	}
	match, _ = item.Value.(filecompletion.Option)
	if match.Path == "AGENTS.md" {
		t.Fatal("bare @ should not include root filesystem fallback result")
	}
}

func TestRefreshAtMentionListKeepsIndexedOrderBeforeRootFallback(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "Makefile"), "all:\n\t@true\n")
	mustWriteFile(t, filepath.Join(wd, ".gitignore"), "Makefile\n")

	m := NewModel(nil)
	m.workingDir = wd
	m.atMentionLoaded = true
	m.atMentionFiles = []string{"main.go", "misc.md"}
	m.atMentionOpen = true
	m.atMentionQuery = "m"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want indexed and fallback results")
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match, _ := item.Value.(filecompletion.Option)
	if match.Path != "main.go" {
		t.Fatalf("selected match = %+v, want indexed fuzzy result main.go first", match)
	}
	if got := m.atMentionList.Len(); got != 3 {
		t.Fatalf("atMentionList.Len() = %d, want 3 merged results", got)
	}
	third, _ := m.atMentionList.Item(2)
	if got := third.Value.(filecompletion.Option).Path; got != "Makefile" {
		t.Fatalf("last merged result = %q, want appended root fallback Makefile", got)
	}
}

func TestRefreshAtMentionListKeepsRootFallbackWhenIndexIsFull(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "AtlasGuide.txt"), "atlas")
	mustWriteFile(t, filepath.Join(wd, ".gitignore"), "AtlasGuide.txt\n")

	files := make([]string, 50)
	for i := range files {
		files[i] = fmt.Sprintf("amber-vault/a-%02d.txt", i)
	}

	m := NewModel(nil)
	m.workingDir = wd
	m.atMentionLoaded = true
	m.atMentionFiles = files
	m.atMentionOpen = true
	m.atMentionQuery = "A"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want indexed and root fallback results")
	}
	if got := m.atMentionList.Len(); got != 50 {
		t.Fatalf("atMentionList.Len() = %d, want capped result list", got)
	}
	firstItem, _ := m.atMentionList.Item(0)
	first := firstItem.Value.(filecompletion.Option)
	if first.Path != "AtlasGuide.txt" || first.IsDir {
		t.Fatalf("first merged result = %+v, want AtlasGuide.txt root fallback", first)
	}
}

func TestRefreshAtMentionListPrefersCaseMatchingRootFallback(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "AtlasGuide.txt"), "atlas")
	mustWriteFile(t, filepath.Join(wd, "amber-map.json"), "{}")
	mustWriteFile(t, filepath.Join(wd, ".gitignore"), "AtlasGuide.txt\namber-map.json\n")

	m := NewModel(nil)
	m.workingDir = wd
	m.atMentionLoaded = true
	m.atMentionFiles = []string{"amber-vault/a-00.txt", "brook/amber-note.txt"}
	m.atMentionOpen = true
	m.atMentionQuery = "A"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want root fallback results")
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match := item.Value.(filecompletion.Option)
	if match.Path != "AtlasGuide.txt" {
		t.Fatalf("selected match = %+v, want uppercase prefix root fallback AtlasGuide.txt", match)
	}
}

func TestFilterAtMentionOptionsByInputSupportHidesUnsupportedMedia(t *testing.T) {
	m := &Model{}
	options := []filecompletion.Option{
		{Path: "docs/", IsDir: true},
		{Path: "notes.md"},
		{Path: "shot.png"},
		{Path: "paper.pdf"},
	}

	got := m.filterAtMentionOptionsByInputSupport(options)
	want := []filecompletion.Option{{Path: "docs/", IsDir: true}, {Path: "notes.md"}}
	if !slices.Equal(got, want) {
		t.Fatalf("filtered options = %#v, want %#v", got, want)
	}
}

func TestBuildFileRefPartsTreatsPDFAtMentionAsAttachment(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "paper.pdf"), "%PDF-1.7\nbody")
	m := &Model{workingDir: wd}

	parts := m.buildFileRefParts("review @./paper.pdf", nil)
	if len(parts) != 1 {
		t.Fatalf("parts len = %d, want pdf", len(parts))
	}
	if parts[0].Type != "pdf" || parts[0].MimeType != "application/pdf" || parts[0].FileName != "paper.pdf" {
		t.Fatalf("attachment part = %+v, want pdf attachment", parts[0])
	}
}

func TestBuildFileRefPartsInjectsLineRange(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "main.go"), "one\ntwo\nthree\nfour\n")
	m := &Model{workingDir: wd}

	parts := m.buildFileRefParts("review @main.go:2-3", []message.ContentPart{{Type: "text", Text: "review @main.go:2-3"}})
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want user text + file ref", len(parts))
	}
	got := parts[1].Text
	if !strings.Contains(got, `<file path="main.go" lines="2-3">`) {
		t.Fatalf("file ref missing lines attribute: %q", got)
	}
	if strings.Contains(got, "one") || strings.Contains(got, "four") || !strings.Contains(got, "two\nthree") {
		t.Fatalf("file ref has wrong body: %q", got)
	}
}

func TestFileRefsFromPartsDisplaysLineRange(t *testing.T) {
	parts := []message.ContentPart{{Type: "text", Text: `<file path="main.go" lines="2-3">` + "\nbody\n</file>"}}
	got := fileRefsFromParts(parts)
	want := []string{"main.go:2-3"}
	if !slices.Equal(got, want) {
		t.Fatalf("fileRefsFromParts() = %#v, want %#v", got, want)
	}
}

func TestRefreshAtMentionListFindsLowScoreFuzzyMatch(t *testing.T) {
	m := NewModel(nil)
	m.atMentionLoaded = true
	m.atMentionFiles = []string{
		"reports/stage1_stage2_design_doc.md",
		"docs/other-file.md",
	}
	m.atMentionOpen = true
	m.atMentionQuery = "s1s2doc"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want fuzzy result")
	}
	if got := m.atMentionList.Len(); got != 1 {
		t.Fatalf("atMentionList.Len() = %d, want 1", got)
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match, _ := item.Value.(filecompletion.Option)
	if got := match.Path; got != "reports/stage1_stage2_design_doc.md" {
		t.Fatalf("selected match = %q, want %q", got, "reports/stage1_stage2_design_doc.md")
	}
}

func TestRefreshAtMentionListFallsBackToFuzzyPathMatch(t *testing.T) {
	wd := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	relPath := filepath.Join("sub", "rel.txt")
	mustWriteFile(t, filepath.Join(wd, relPath), "relative content")
	extraDir := t.TempDir()
	absPath := filepath.Join(extraDir, "abs.txt")
	mustWriteFile(t, absPath, "absolute content")
	mustWriteFile(t, filepath.Join(home, "docs", "home.txt"), "home content")

	m := NewModel(nil)
	m.workingDir = wd

	display := "Check @sub/rel.txt and @" + filepath.ToSlash(absPath) + " and @~/docs/home.txt"
	parts := m.buildFileRefParts(display, []message.ContentPart{{Type: "text", Text: display}})
	if len(parts) != 4 {
		t.Fatalf("len(parts) = %d, want 4", len(parts))
	}
	if parts[0].Text != "Check @sub/rel.txt and @"+filepath.ToSlash(absPath)+" and @~/docs/home.txt" {
		t.Fatalf("text part = %q, want original text first", parts[0].Text)
	}
	if !strings.Contains(parts[1].Text, "relative content") {
		t.Fatalf("relative part = %q, want relative file content", parts[1].Text)
	}
	if !strings.Contains(parts[1].Text, `path="sub/rel.txt"`) {
		t.Fatalf("relative part path = %q, want original relative path", parts[1].Text)
	}
	if !strings.Contains(parts[2].Text, "absolute content") {
		t.Fatalf("absolute part = %q, want absolute file content", parts[2].Text)
	}
	if !strings.Contains(parts[2].Text, filepath.ToSlash(absPath)) {
		t.Fatalf("absolute part path = %q, want absolute path", parts[2].Text)
	}
	if !strings.Contains(parts[3].Text, "home content") {
		t.Fatalf("home part = %q, want home file content", parts[3].Text)
	}
	if !strings.Contains(parts[3].Text, `path="~/docs/home.txt"`) {
		t.Fatalf("home part path = %q, want original home path", parts[3].Text)
	}
}

func TestBuildFileRefPartsKeepsMultiplePasteSegmentsInOrder(t *testing.T) {
	wd := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(wd, "ref.txt"), "inside ref")
	m := NewModel(nil)
	m.workingDir = wd

	display := "intro\n[Pasted text #1 +2 lines]\nmid\n[Pasted text #2 +2 lines]\nsee @ref.txt"
	composerParts := []message.ContentPart{
		{Type: "text", Text: "intro\n"},
		{Type: "text", Text: "alpha\nbeta", DisplayText: "[Pasted text #1 +2 lines]"},
		{Type: "text", Text: "\nmid\n"},
		{Type: "text", Text: "gamma\ndelta", DisplayText: "[Pasted text #2 +2 lines]"},
		{Type: "text", Text: "\nsee @ref.txt"},
	}
	parts := m.buildFileRefParts(display, composerParts)
	if len(parts) != 6 {
		t.Fatalf("len(parts) = %d, want 6 (5 composer segments + 1 file)", len(parts))
	}
	if parts[0].Text != "intro\n" || parts[1].Text != "alpha\nbeta" || parts[1].DisplayText != "[Pasted text #1 +2 lines]" {
		t.Fatalf("composer segments 0-1 = %#v, %#v", parts[0], parts[1])
	}
	if parts[2].Text != "\nmid\n" || parts[3].Text != "gamma\ndelta" {
		t.Fatalf("composer segments 2-3 = %#v, %#v", parts[2], parts[3])
	}
	if parts[4].Text != "\nsee @ref.txt" {
		t.Fatalf("last composer segment = %q", parts[4].Text)
	}
	if !strings.Contains(parts[5].Text, `path="ref.txt"`) {
		t.Fatalf("file part = %q, want embedded ref.txt last", parts[5].Text)
	}
	gotFull := userBlockTextFromParts(parts, "")
	wantFull := "intro\nalpha\nbeta\nmid\ngamma\ndelta\nsee @ref.txt"
	if gotFull != wantFull {
		t.Fatalf("userBlockTextFromParts() = %q, want %q", gotFull, wantFull)
	}
}

func TestInsertAtMentionSelectionDoesNotClearInlinePastes(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	if !m.input.InsertLargePaste(strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, "\n")) {
		t.Fatal("InsertLargePaste() = false, want true")
	}
	// Preserve the inline paste placeholders while setting the value.
	pastes := m.input.InlinePastes()
	nextSeq := m.input.NextPasteSeq()
	m.input.SetDisplayValueAndPastes("[Pasted text #1 +11 lines] @do", pastes, nextSeq)
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = len([]rune("[Pasted text #1 +11 lines] @"))
	m.atMentionQuery = "do"
	m.input.SetCursorPosition(0, len([]rune(m.input.Value())))

	m.atMentionOpen = true
	m.atMentionLine = 0
	// Trigger column points just after '@'.
	s := m.input.Value()
	atIdx := strings.LastIndex(s, "@")
	if atIdx < 0 {
		t.Fatal("expected '@' in input")
	}
	// Convert byte index to rune col.
	m.atMentionTriggerCol = len([]rune(s[:atIdx+1]))
	m.atMentionQuery = "do"
	m.atMentionList = overlaylist.New([]overlaylist.Item{{
		Label: "docs/ARCHITECTURE.md",
		Value: filecompletion.Option{Path: "docs/ARCHITECTURE.md", IsDir: false},
	}}, 10)

	m.insertAtMentionSelection()

	if !m.input.HasInlinePastes() {
		t.Fatal("inline pastes should be preserved after at-mention selection")
	}
	parts := m.input.ContentParts()
	foundLargePaste := false
	for _, p := range parts {
		if p.Type == "text" && strings.Contains(p.Text, "\n11") && p.DisplayText != "" {
			foundLargePaste = true
			break
		}
	}
	if !foundLargePaste {
		t.Fatalf("expected a large paste text part with DisplayText, got parts=%#v", parts)
	}
}

func TestInsertComposerTextReturnsAtMentionLoadCommand(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1
	m.input.SetValue("@")
	m.input.SetCursorPosition(0, 1)

	cmd := m.insertComposerText("abc")
	if cmd == nil {
		t.Fatal("insertComposerText should return at-mention load command when completion is open")
	}
	if got := m.atMentionQuery; got != "abc" {
		t.Fatalf("atMentionQuery = %q, want %q", got, "abc")
	}
}

func TestInsertAtMentionSelectionReturnsDirectoryRefreshCommand(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetValue("@docs")
	m.input.SetCursorPosition(0, len([]rune("@docs")))
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1
	m.atMentionQuery = "docs"
	m.atMentionList = overlaylist.New([]overlaylist.Item{{
		Label: "docs/",
		Value: filecompletion.Option{Path: "docs/", IsDir: true},
	}}, 10)

	cmd := m.insertAtMentionSelection()
	if cmd == nil {
		t.Fatal("directory selection should return at-mention refresh command")
	}
	if got := m.atMentionQuery; got != "docs/" {
		t.Fatalf("atMentionQuery = %q, want %q", got, "docs/")
	}
}

func TestInsertAtMentionSelectionPreservesLineRangeSuffix(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "main.go"), "package main")
	m := NewModel(nil)
	m.mode = ModeInsert
	m.workingDir = wd
	m.input.SetValue("@ma:12")
	m.input.SetCursorPosition(0, len([]rune("@ma:12")))
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1
	m.atMentionQuery = "ma:12"
	m.atMentionList = overlaylist.New([]overlaylist.Item{{
		Label: "main.go",
		Value: filecompletion.Option{Path: "main.go", IsDir: false},
	}}, 10)

	m.insertAtMentionSelection()

	if got := m.input.Value(); got != "@main.go:12" {
		t.Fatalf("input value = %q, want @main.go:12", got)
	}
	if m.atMentionOpen {
		t.Fatal("atMentionOpen = true, want false after file selection")
	}
}

func TestInsertAtMentionSelectionKeepsColonFilenameReplacement(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "note"), "plain")
	mustWriteFile(t, filepath.Join(wd, "note:12"), "colon")
	m := NewModel(nil)
	m.mode = ModeInsert
	m.workingDir = wd
	m.input.SetValue("@note:12")
	m.input.SetCursorPosition(0, len([]rune("@note:12")))
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1
	m.atMentionQuery = "note:12"
	m.atMentionList = overlaylist.New([]overlaylist.Item{{
		Label: "note:12",
		Value: filecompletion.Option{Path: "note:12", IsDir: false},
	}}, 10)

	m.insertAtMentionSelection()

	if got := m.input.Value(); got != "@note:12 " {
		t.Fatalf("input value = %q, want @note:12 with trailing space", got)
	}
}

func TestInsertAtMentionSelectionKeepsDirectoryBrowsingOpen(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(home, "src", "main.go"), "package main")
	m.workingDir = t.TempDir()
	m.input.SetValue("@~/s")
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1
	m.atMentionQuery = "~/s"
	m.atMentionList = overlaylist.New([]overlaylist.Item{{
		Label: "~/src/",
		Value: filecompletion.Option{Path: "~/src/", IsDir: true},
	}}, 10)

	m.insertAtMentionSelection()

	if got := m.input.Value(); got != "@~/src/" {
		t.Fatalf("input value = %q, want %q", got, "@~/src/")
	}
	if !m.atMentionOpen {
		t.Fatal("atMentionOpen = false, want true for directory continuation")
	}
	if got := m.atMentionQuery; got != "~/src/" {
		t.Fatalf("atMentionQuery = %q, want %q", got, "~/src/")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
