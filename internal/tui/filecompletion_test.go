package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
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

	updated, _ := m.Update(atMentionFilesLoadedMsg{files: []string{"docs/ARCHITECTURE.md"}})
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

func TestAtMentionFilesLoadedKeepsBareAtPopupOpen(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeInsert
	m.input.SetValue("@")
	m.input.SetCursorPosition(0, 1)
	m.atMentionOpen = true
	m.atMentionLine = 0
	m.atMentionTriggerCol = 1
	m.atMentionLoading = true

	updated, _ := m.Update(atMentionFilesLoadedMsg{files: []string{"docs/ARCHITECTURE.md"}})
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

func TestLoadAtMentionFilesUsesGitTrackedAndUntracked(t *testing.T) {
	if _, err := gitCommand(context.Background(), ".", "--version"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	wd := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir(%q) error = %v", wd, err)
	}
	if _, err := gitCommand(context.Background(), wd, "init"); err != nil {
		t.Fatalf("git init error = %v", err)
	}
	mustWriteFile(t, filepath.Join(wd, "tracked.go"), "package main")
	mustWriteFile(t, filepath.Join(wd, "new.md"), "new")
	mustWriteFile(t, filepath.Join(wd, "ignored.log"), "ignored")
	mustWriteFile(t, filepath.Join(wd, "deleted.txt"), "deleted")
	mustWriteFile(t, filepath.Join(wd, ".gitignore"), "*.log\n")
	if _, err := gitCommand(context.Background(), wd, "add", "tracked.go", "deleted.txt", ".gitignore"); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if err := os.Remove(filepath.Join(wd, "deleted.txt")); err != nil {
		t.Fatalf("Remove(deleted.txt) error = %v", err)
	}

	loaded := loadAtMentionFileList(atMentionMaxFiles)
	if !slices.Contains(loaded, "tracked.go") {
		t.Fatalf("tracked file missing from git preload: %v", loaded)
	}
	if !slices.Contains(loaded, "new.md") {
		t.Fatalf("untracked non-ignored file missing from git preload: %v", loaded)
	}
	if slices.Contains(loaded, "ignored.log") {
		t.Fatalf("ignored file should be excluded from git preload: %v", loaded)
	}
	if slices.Contains(loaded, "deleted.txt") {
		t.Fatalf("deleted tracked file should be excluded from git preload: %v", loaded)
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

func TestLoadAtMentionFilesIncludesAttachableMediaAndExcludesOtherBinary(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "main.go"), "package main")
	mustWriteFile(t, filepath.Join(wd, "notes.md"), "notes")
	mustWriteFile(t, filepath.Join(wd, "image.png"), "\x89PNG")
	mustWriteFile(t, filepath.Join(wd, "paper.pdf"), "%PDF-1.7\n")
	mustWriteFile(t, filepath.Join(wd, "icon.svg"), "<svg/>")
	mustWriteFile(t, filepath.Join(wd, "bundle.zip"), "PK")
	mustWriteFile(t, filepath.Join(wd, "libfoo.so"), "\x7fELF")
	mustWriteFile(t, filepath.Join(wd, "pkg", "mod.pyc"), "\x00")
	mustWriteFile(t, filepath.Join(wd, "dist", "app.js"), "console.log(1)")
	mustWriteFile(t, filepath.Join(wd, "out", "result.txt"), "ok")
	mustWriteFile(t, filepath.Join(wd, ".gitignore"), "out/\n*.secret\n")
	mustWriteFile(t, filepath.Join(wd, "keys.secret"), "hunter2")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir(%q) error = %v", wd, err)
	}

	msg := loadAtMentionFiles()()
	loaded, ok := msg.(atMentionFilesLoadedMsg)
	if !ok {
		t.Fatalf("loadAtMentionFiles() msg = %T, want atMentionFilesLoadedMsg", msg)
	}

	wantIn := []string{"main.go", "notes.md", "image.png", "paper.pdf", "icon.svg"}
	for _, p := range wantIn {
		if !slices.Contains(loaded.files, p) {
			t.Errorf("%q should be included; got %v", p, loaded.files)
		}
	}
	wantOut := []string{
		"bundle.zip", "libfoo.so", "pkg/mod.pyc", // non-attachable binary extensions
		"dist/app.js",    // hard-coded skipped dir
		"out/result.txt", // .gitignore dir
		"keys.secret",    // .gitignore file pattern
	}
	for _, p := range wantOut {
		if slices.Contains(loaded.files, p) {
			t.Errorf("%q should be excluded; got %v", p, loaded.files)
		}
	}
}

func TestLoadAtMentionFilesIncludesDeeperRepoPaths(t *testing.T) {
	wd := t.TempDir()
	deepFile := filepath.Join(wd, "a", "b", "c", "d", "e", "f", "g", "deep.txt")
	hiddenFile := filepath.Join(wd, ".cursor", "rules.md")
	mustWriteFile(t, deepFile, "deep")
	mustWriteFile(t, hiddenFile, "rules")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(oldWd)
	}()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir(%q) error = %v", wd, err)
	}

	msg := loadAtMentionFiles()()
	loaded, ok := msg.(atMentionFilesLoadedMsg)
	if !ok {
		t.Fatalf("loadAtMentionFiles() msg = %T, want atMentionFilesLoadedMsg", msg)
	}

	if !slices.Contains(loaded.files, "a/b/c/d/e/f/g/deep.txt") {
		t.Fatalf("deep file missing from preload: %v", loaded.files)
	}
	if slices.Contains(loaded.files, ".cursor/rules.md") {
		t.Fatalf("hidden dot-directory file should be excluded from preload: %v", loaded.files)
	}
}

func TestLoadAtMentionFilesCapsAtNewLimit(t *testing.T) {
	wd := t.TempDir()
	for i := range 8 {
		mustWriteFile(t, filepath.Join(wd, "files", fmt.Sprintf("file-%05d.txt", i)), "x")
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(oldWd)
	}()
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir(%q) error = %v", wd, err)
	}

	msg := loadAtMentionFilesWithLimit(3)()
	loaded, ok := msg.(atMentionFilesLoadedMsg)
	if !ok {
		t.Fatalf("loadAtMentionFiles() msg = %T, want atMentionFilesLoadedMsg", msg)
	}
	if got := len(loaded.files); got != 3 {
		t.Fatalf("len(files) = %d, want %d", got, 3)
	}
	if slices.Contains(loaded.files, "files/file-00007.txt") {
		t.Fatalf("expected files beyond limit to be omitted, but found tail entry")
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

func TestAtMentionPathMatchesRelativeAbsoluteAndHome(t *testing.T) {
	wd := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(wd, "subdir", "nested.txt"), "nested")
	mustWriteFile(t, filepath.Join(wd, "src", "main.go"), "package main")
	mustWriteFile(t, filepath.Join(wd, "docs", "architecture", "overview.md"), "overview")
	mustWriteFile(t, filepath.Join(home, "Workspace", "coding-agents", "chord", "main.go"), "package main")

	rel := atMentionPathMatches("./s", wd)
	if len(rel) < 2 {
		t.Fatalf("relative matches = %v, want at least 2", rel)
	}
	if rel[0].Path != "./src/" || !rel[0].IsDir {
		t.Fatalf("relative first match = %+v, want ./src/ directory", rel[0])
	}
	if rel[1].Path != "./subdir/" || !rel[1].IsDir {
		t.Fatalf("relative second match = %+v, want ./subdir/ directory", rel[1])
	}

	absPrefix := filepath.ToSlash(wd) + "/s"
	abs := atMentionPathMatches(absPrefix, wd)
	if len(abs) < 2 {
		t.Fatalf("absolute matches = %v, want at least 2", abs)
	}
	wantFirst := filepath.ToSlash(filepath.Join(wd, "src")) + "/"
	wantSecond := filepath.ToSlash(filepath.Join(wd, "subdir")) + "/"
	if abs[0].Path != wantFirst || !abs[0].IsDir {
		t.Fatalf("absolute first match = %+v, want %s directory", abs[0], wantFirst)
	}
	if abs[1].Path != wantSecond || !abs[1].IsDir {
		t.Fatalf("absolute second match = %+v, want %s directory", abs[1], wantSecond)
	}

	homeMatches := atMentionPathMatches("~/Wor", wd)
	if len(homeMatches) != 1 {
		t.Fatalf("home matches = %v, want 1", homeMatches)
	}
	if homeMatches[0].Path != "~/Workspace/" || !homeMatches[0].IsDir {
		t.Fatalf("home match = %+v, want ~/Workspace/ directory", homeMatches[0])
	}

	repoRel := atMentionPathMatches("docs/a", wd)
	if len(repoRel) != 1 {
		t.Fatalf("repo-relative matches = %v, want 1", repoRel)
	}
	if repoRel[0].Path != "docs/architecture/" || !repoRel[0].IsDir {
		t.Fatalf("repo-relative match = %+v, want docs/architecture/ directory", repoRel[0])
	}
}

func TestAtMentionPathMatchesPrefersExactFile(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "docs", "architecture", "file-mentions.md"), "doc")
	mustWriteFile(t, filepath.Join(wd, "docs", "architecture", "file-other.md"), "other")

	query := "./docs/architecture/file-mentions.md"
	got := atMentionPathMatches(query, wd)
	want := []atMentionOption{{Path: query, IsDir: false}}
	if !slices.Equal(got, want) {
		t.Fatalf("atMentionPathMatches() = %#v, want %#v", got, want)
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
	match, _ := item.Value.(atMentionOption)
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
	match, _ := item.Value.(atMentionOption)
	if match.Path != "new.txt" || match.IsDir {
		t.Fatalf("selected match = %+v, want exact new.txt file", match)
	}

	m.atMentionQuery = "new-dir"
	m.refreshAtMentionList()

	item, ok = m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() for directory ok = false, want true")
	}
	match, _ = item.Value.(atMentionOption)
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
	match, _ := item.Value.(atMentionOption)
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
	match, _ = item.Value.(atMentionOption)
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
	match, _ := item.Value.(atMentionOption)
	if match.Path != "main.go" {
		t.Fatalf("selected match = %+v, want indexed fuzzy result main.go first", match)
	}
	if got := m.atMentionList.Len(); got != 3 {
		t.Fatalf("atMentionList.Len() = %d, want 3 merged results", got)
	}
	if got := m.atMentionList.items[2].Value.(atMentionOption).Path; got != "Makefile" {
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
	first := m.atMentionList.items[0].Value.(atMentionOption)
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
	match := item.Value.(atMentionOption)
	if match.Path != "AtlasGuide.txt" {
		t.Fatalf("selected match = %+v, want uppercase prefix root fallback AtlasGuide.txt", match)
	}
}

func TestAtMentionRootPrefixMatches(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "AGENTS.md"), "agents")
	mustWriteFile(t, filepath.Join(wd, "Alpha", "note.txt"), "note")
	mustWriteFile(t, filepath.Join(wd, ".env"), "secret")
	mustWriteFile(t, filepath.Join(wd, "app.png"), "png")

	for _, query := range []string{"", "notes/x", "./A", "~/A", ".env"} {
		if got := atMentionRootPrefixMatches(query, wd); got != nil {
			t.Fatalf("atMentionRootPrefixMatches(%q) = %#v, want nil", query, got)
		}
	}

	got := atMentionRootPrefixMatches("A", wd)
	want := []atMentionOption{{Path: "Alpha/", IsDir: true}, {Path: "AGENTS.md", IsDir: false}, {Path: "app.png", IsDir: false}}
	if !slices.Equal(got, want) {
		t.Fatalf("atMentionRootPrefixMatches(%q) = %#v, want %#v", "A", got, want)
	}
}

func TestFilterAtMentionOptionsByInputSupportHidesUnsupportedMedia(t *testing.T) {
	m := &Model{}
	options := []atMentionOption{
		{Path: "docs/", IsDir: true},
		{Path: "notes.md"},
		{Path: "shot.png"},
		{Path: "paper.pdf"},
	}

	got := m.filterAtMentionOptionsByInputSupport(options)
	want := []atMentionOption{{Path: "docs/", IsDir: true}, {Path: "notes.md"}}
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

func TestMergeAtMentionOptions(t *testing.T) {
	primary := []atMentionOption{{Path: "main.go"}, {Path: "misc.md"}}
	secondary := []atMentionOption{{Path: "misc.md"}, {Path: "Makefile"}}
	got := mergeAtMentionOptions(primary, secondary, "m")
	want := []atMentionOption{{Path: "main.go"}, {Path: "misc.md"}, {Path: "Makefile"}}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeAtMentionOptions() = %#v, want %#v", got, want)
	}

	primary = make([]atMentionOption, 0, 49)
	for i := range 49 {
		primary = append(primary, atMentionOption{Path: fmt.Sprintf("p-%02d", i)})
	}
	secondary = []atMentionOption{{Path: "tail-0"}, {Path: "tail-1"}}
	got = mergeAtMentionOptions(primary, secondary, "p")
	if len(got) != 50 {
		t.Fatalf("len(mergeAtMentionOptions()) = %d, want 50", len(got))
	}
	if got[49].Path != "tail-0" {
		t.Fatalf("mergeAtMentionOptions()[49] = %q, want %q", got[49].Path, "tail-0")
	}

	primary = []atMentionOption{{Path: "amber-vault/a-00.txt"}, {Path: "brook/amber-note.txt"}}
	secondary = []atMentionOption{{Path: "amber-map.json"}, {Path: "AtlasGuide.txt"}}
	got = mergeAtMentionOptions(primary, secondary, "A")
	want = []atMentionOption{{Path: "AtlasGuide.txt"}, {Path: "amber-vault/a-00.txt"}, {Path: "brook/amber-note.txt"}, {Path: "amber-map.json"}}
	if !slices.Equal(got, want) {
		t.Fatalf("case-sensitive mergeAtMentionOptions() = %#v, want %#v", got, want)
	}
}

func TestAtMentionOptionsMissingFromIndex(t *testing.T) {
	options := []atMentionOption{{Path: "main.go"}, {Path: "Makefile"}, {Path: "docs/"}}
	files := []string{"main.go", "misc.md"}
	got := atMentionOptionsMissingFromIndex(options, files)
	want := []atMentionOption{{Path: "Makefile"}, {Path: "docs/"}}
	if !slices.Equal(got, want) {
		t.Fatalf("atMentionOptionsMissingFromIndex() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFuzzyMatchesMultiStepPath(t *testing.T) {
	query := "docs/file.md"
	matches := atMentionFuzzyMatches([]string{
		"docs/architecture/file-mentions.md",
		"docs/other-file.md",
		"internal/tui/filecompletion.go",
	}, query)
	if len(matches) == 0 {
		t.Fatal("atMentionFuzzyMatches() returned no matches")
	}
	if got := matches[0].Path; got != "docs/architecture/file-mentions.md" {
		t.Fatalf("top fuzzy match = %q, want %q", got, "docs/architecture/file-mentions.md")
	}
}

func TestAtMentionFuzzyMatchesKeepsLowScoreMatches(t *testing.T) {
	matches := atMentionFuzzyMatches([]string{
		"reports/stage1_stage2_design_doc.md",
		"docs/other-file.md",
	}, "s1s2doc")
	if len(matches) != 1 {
		t.Fatalf("len(matches) = %d, want 1", len(matches))
	}
	if got := matches[0].Path; got != "reports/stage1_stage2_design_doc.md" {
		t.Fatalf("top fuzzy match = %q, want %q", got, "reports/stage1_stage2_design_doc.md")
	}
}

func TestAtMentionFuzzyMatchesPreferShallowerPaths(t *testing.T) {
	files := make([]string, 0, 62)
	for i := 0; i < 60; i++ {
		files = append(files, fmt.Sprintf("nebula/ring/%02d/AtlasGuide.txt", i))
	}
	files = append(files, "orbit/AtlasGuide.txt", "AtlasGuide.txt")

	matches := atMentionFuzzyMatches(files, "A")

	if len(matches) != 50 {
		t.Fatalf("len(matches) = %d, want capped result list", len(matches))
	}
	if got := matches[0].Path; got != "AtlasGuide.txt" {
		t.Fatalf("top fuzzy match = %q, want current-folder AtlasGuide.txt", got)
	}
	if got := matches[1].Path; got != "orbit/AtlasGuide.txt" {
		t.Fatalf("second fuzzy match = %q, want one-level orbit/AtlasGuide.txt", got)
	}
	for _, match := range matches {
		if match.Path == "nebula/ring/59/AtlasGuide.txt" {
			t.Fatalf("deep unrelated path survived top-50 cap: %#v", match)
		}
	}
}

func TestAtMentionFuzzyMatchesHidesHiddenPathsUntilQueryIncludesDotSegment(t *testing.T) {
	files := []string{
		".cursor/rules.md",
		".opencode/config.json",
		"docs/guide.md",
		"src/main.go",
	}

	got := atMentionFuzzyMatches(files, "")
	if len(got) != 2 {
		t.Fatalf("len(matches) = %d, want 2 visible non-hidden matches", len(got))
	}
	if got[0].Path != "docs/guide.md" || got[1].Path != "src/main.go" {
		t.Fatalf("bare @ matches = %#v, want only non-hidden paths", got)
	}

	got = atMentionFuzzyMatches(files, ".c")
	if len(got) == 0 {
		t.Fatal("dot-prefixed query returned no hidden matches")
	}
	if got[0].Path != ".cursor/rules.md" {
		t.Fatalf("dot-prefixed top match = %q, want %q", got[0].Path, ".cursor/rules.md")
	}
}

func TestAtMentionPathMatchesHideHiddenEntriesUntilPrefixIncludesDot(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, ".env"), "secret")
	mustWriteFile(t, filepath.Join(wd, ".cursor", "rules.md"), "rules")
	mustWriteFile(t, filepath.Join(wd, "docs", "guide.md"), "guide")

	got := atMentionPathMatches(".", wd)
	if len(got) == 0 {
		t.Fatal(". returned no hidden matches")
	}
	if got[0].Path != ".cursor/" || !got[0].IsDir {
		t.Fatalf(". first match = %#v, want .cursor/", got)
	}

	got = atMentionPathMatches("./", wd)
	if len(got) != 1 {
		t.Fatalf("len(matches) = %d, want 1 visible entry", len(got))
	}
	if got[0].Path != "./docs/" || !got[0].IsDir {
		t.Fatalf("./ matches = %#v, want only ./docs/", got)
	}

	got = atMentionPathMatches("./.", wd)
	if len(got) == 0 {
		t.Fatal("./. returned no hidden matches")
	}
	if got[0].Path != "./.cursor/" || !got[0].IsDir {
		t.Fatalf("./. first match = %#v, want ./.cursor/", got)
	}

	got = atMentionPathMatches("..", wd)
	if len(got) != 1 || got[0].Path != "../" || !got[0].IsDir {
		t.Fatalf(".. matches = %#v, want only ../", got)
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
	match, _ := item.Value.(atMentionOption)
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
	m.atMentionList = NewOverlayList([]OverlayListItem{{
		Label: "docs/ARCHITECTURE.md",
		Value: atMentionOption{Path: "docs/ARCHITECTURE.md", IsDir: false},
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

func TestAtMentionFileRefsHandlesEscapedSpacesAndDedupes(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "dir with space", "a file.txt"), "content")
	mustWriteFile(t, filepath.Join(wd, "plain.txt"), "plain")

	text := strings.Join([]string{
		`See @dir\ with\ space/a\ file.txt,`,
		`again @dir\ with\ space/a\ file.txt!`,
		`and @plain.txt)`}, " ")

	got := atMentionFileRefs([]string{text}, wd)
	want := []string{`dir with space/a file.txt`, `plain.txt`}
	if !slices.Equal(got, want) {
		t.Fatalf("atMentionFileRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsFallsBackToLongestProseDelimitedPath(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "AGENTS.md"), "agents")
	mustWriteFile(t, filepath.Join(wd, "docs", "requirements,first-draft.md"), "doc")
	mustWriteFile(t, filepath.Join(wd, "docs", "a"), "short")
	mustWriteFile(t, filepath.Join(wd, "docs", "a,b.md"), "long")

	text := strings.Join([]string{
		`Review @AGENTS.md, then summarize.`,
		`Also read @docs/requirements,first-draft.md, then summarize`,
		`Finally inspect @docs/a,b.md, then analyze`,
	}, "\n")

	got := atMentionFileRefs([]string{text}, wd)
	want := []string{`AGENTS.md`, `docs/requirements,first-draft.md`, `docs/a,b.md`}
	if !slices.Equal(got, want) {
		t.Fatalf("atMentionFileRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsPrefersFullCandidateBeforeProseDelimiterFallback(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "docs", "note,analysis"), "full")
	mustWriteFile(t, filepath.Join(wd, "docs", "note"), "short")

	got := atMentionFileRefs([]string{`Review @docs/note,analysis`}, wd)
	want := []string{`docs/note,analysis`}
	if !slices.Equal(got, want) {
		t.Fatalf("atMentionFileRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsPunctuationBoundaries(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "notes.md"), "content")

	// Various punctuation marks should all be recognized as valid boundaries
	cases := []string{
		"path: @notes.md",       // ASCII colon
		"文件：@notes.md",          // Full-width colon
		"另见、@notes.md",          // Full-width comma
		"详情。@notes.md",          // Full-width period
		"see, @notes.md",        // ASCII comma
		"note. @notes.md",       // ASCII period
		"ref; @notes.md",        // ASCII semicolon
		"(@notes.md)",           // Parentheses
		"[@notes.md]",           // Brackets
		"{@notes.md}",           // Braces
		"\"@notes.md\"",         // Quotes
		"'@notes.md'",           // Single quotes
		"\u201c@notes.md\u201d", // Full-width quotes (as escaped runes)
		"see @notes.md",         // Space (existing)
		"\n@notes.md",           // Newline (existing)
		"\t@notes.md",           // Tab (existing)
	}
	for _, text := range cases {
		got := atMentionFileRefs([]string{text}, wd)
		want := []string{"notes.md"}
		if !slices.Equal(got, want) {
			t.Errorf("atMentionFileRefs(%q) = %#v, want %#v", text, got, want)
		}
	}
}

func TestAtMentionFileRefsRejectsIdentifierPrefix(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "something.txt"), "content")
	mustWriteFile(t, filepath.Join(wd, "file.md"), "content")

	// @mentions that are part of identifiers should be rejected
	// even when files with those names exist
	cases := []string{
		"user@something.txt",   // email-like
		"package@file.md",      // version-like
		"my_var@something.txt", // identifier with underscore
		"func123@file.md",      // identifier with digits
	}
	for _, text := range cases {
		got := atMentionFileRefs([]string{text}, wd)
		if len(got) > 0 {
			t.Errorf("atMentionFileRefs(%q) = %#v, want empty (should reject identifier prefix)", text, got)
		}
	}
}

func TestAtMentionFileRefsSkipsRemovedComposerReference(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "keep.txt"), "keep")
	mustWriteFile(t, filepath.Join(wd, "drop.txt"), "drop")

	got := atMentionFileRefs([]string{"keep @keep.txt only"}, wd)
	want := []string{"keep.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("atMentionFileRefs() = %#v, want %#v", got, want)
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
	m.atMentionList = NewOverlayList([]OverlayListItem{{
		Label: "docs/",
		Value: atMentionOption{Path: "docs/", IsDir: true},
	}}, 10)

	cmd := m.insertAtMentionSelection()
	if cmd == nil {
		t.Fatal("directory selection should return at-mention refresh command")
	}
	if got := m.atMentionQuery; got != "docs/" {
		t.Fatalf("atMentionQuery = %q, want %q", got, "docs/")
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
	m.atMentionList = NewOverlayList([]OverlayListItem{{
		Label: "~/src/",
		Value: atMentionOption{Path: "~/src/", IsDir: true},
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
