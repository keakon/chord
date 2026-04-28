package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
		t.Fatal("startAtMentionFileLoad() after load completion should be nil")
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

func TestLoadAtMentionFilesExcludesBinaryAndGitignored(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "main.go"), "package main")
	mustWriteFile(t, filepath.Join(wd, "notes.md"), "notes")
	mustWriteFile(t, filepath.Join(wd, "image.png"), "\x89PNG")
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

	wantIn := []string{"main.go", "notes.md", "icon.svg"}
	for _, p := range wantIn {
		if !slices.Contains(loaded.files, p) {
			t.Errorf("%q should be included; got %v", p, loaded.files)
		}
	}
	wantOut := []string{
		"image.png", "bundle.zip", "libfoo.so", "pkg/mod.pyc", // binary extensions
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
	for i := 0; i < atMentionMaxFiles+50; i++ {
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

	msg := loadAtMentionFiles()()
	loaded, ok := msg.(atMentionFilesLoadedMsg)
	if !ok {
		t.Fatalf("loadAtMentionFiles() msg = %T, want atMentionFilesLoadedMsg", msg)
	}
	if got := len(loaded.files); got != atMentionMaxFiles {
		t.Fatalf("len(files) = %d, want %d", got, atMentionMaxFiles)
	}
	if slices.Contains(loaded.files, fmt.Sprintf("files/file-%05d.txt", atMentionMaxFiles+49)) {
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

func TestAtMentionFuzzyMatchesKeepsNegativeScoreMatches(t *testing.T) {
	matches := atMentionFuzzyMatches([]string{
		"交付报告/stage1_stage2_技术文档.md",
		"docs/other-file.md",
	}, "技术")
	if len(matches) != 1 {
		t.Fatalf("len(matches) = %d, want 1", len(matches))
	}
	if got := matches[0].Path; got != "交付报告/stage1_stage2_技术文档.md" {
		t.Fatalf("top fuzzy match = %q, want %q", got, "交付报告/stage1_stage2_技术文档.md")
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
	mustWriteFile(t, filepath.Join(wd, ".cursor", "rules.md"), "rules")
	mustWriteFile(t, filepath.Join(wd, "docs", "guide.md"), "guide")

	got := atMentionPathMatches("./", wd)
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
}

func TestRefreshAtMentionListFindsChineseFuzzyMatchWithNegativeScore(t *testing.T) {
	m := NewModel(nil)
	m.atMentionLoaded = true
	m.atMentionFiles = []string{
		"交付报告/stage1_stage2_技术文档.md",
		"docs/other-file.md",
	}
	m.atMentionOpen = true
	m.atMentionQuery = "技术"

	m.refreshAtMentionList()

	if m.atMentionList == nil {
		t.Fatal("atMentionList = nil, want Chinese fuzzy result")
	}
	if got := m.atMentionList.Len(); got != 1 {
		t.Fatalf("atMentionList.Len() = %d, want 1", got)
	}
	item, ok := m.atMentionList.SelectedItem()
	if !ok {
		t.Fatal("SelectedItem() ok = false, want true")
	}
	match, _ := item.Value.(atMentionOption)
	if got := match.Path; got != "交付报告/stage1_stage2_技术文档.md" {
		t.Fatalf("selected match = %q, want %q", got, "交付报告/stage1_stage2_技术文档.md")
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
