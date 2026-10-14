package filecompletion

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAtMentionPathMatchesRelativeAbsoluteAndHome(t *testing.T) {
	wd := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(wd, "subdir", "nested.txt"), "nested")
	mustWriteFile(t, filepath.Join(wd, "src", "main.go"), "package main")
	mustWriteFile(t, filepath.Join(wd, "docs", "architecture", "overview.md"), "overview")
	mustWriteFile(t, filepath.Join(home, "Workspace", "coding-agents", "chord", "main.go"), "package main")

	rel := PathMatches("./s", wd)
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
	abs := PathMatches(absPrefix, wd)
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

	homeMatches := PathMatches("~/Wor", wd)
	if len(homeMatches) != 1 {
		t.Fatalf("home matches = %v, want 1", homeMatches)
	}
	if homeMatches[0].Path != "~/Workspace/" || !homeMatches[0].IsDir {
		t.Fatalf("home match = %+v, want ~/Workspace/ directory", homeMatches[0])
	}

	repoRel := PathMatches("docs/a", wd)
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
	got := PathMatches(query, wd)
	want := []Option{{Path: query, IsDir: false}}
	if !slices.Equal(got, want) {
		t.Fatalf("PathMatches() = %#v, want %#v", got, want)
	}
}

func TestAtMentionPathMatchesLineRangeSuffix(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "docs", "main.go"), "content")

	got := PathMatches("docs/main.go:12", wd)
	want := []Option{{Path: "docs/main.go"}}
	if !slices.Equal(got, want) {
		t.Fatalf("PathMatches() = %#v, want %#v", got, want)
	}
}

func TestAtMentionPathMatchesPrefersColonFilenameOverLineRangeSuffix(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "docs", "note:12"), "content")
	mustWriteFile(t, filepath.Join(wd, "docs", "note"), "plain")

	got := PathMatches("docs/note:12", wd)
	want := []Option{{Path: "docs/note:12"}}
	if !slices.Equal(got, want) {
		t.Fatalf("PathMatches() = %#v, want %#v", got, want)
	}
}

func TestAtMentionRootPrefixMatches(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "AGENTS.md"), "agents")
	mustWriteFile(t, filepath.Join(wd, "Alpha", "note.txt"), "note")
	mustWriteFile(t, filepath.Join(wd, ".env"), "secret")
	mustWriteFile(t, filepath.Join(wd, "app.png"), "png")

	for _, query := range []string{"", "notes/x", "./A", "~/A", ".env"} {
		if got := RootPrefixMatches(query, wd); got != nil {
			t.Fatalf("RootPrefixMatches(%q) = %#v, want nil", query, got)
		}
	}

	got := RootPrefixMatches("A", wd)
	want := []Option{{Path: "Alpha/", IsDir: true}, {Path: "AGENTS.md", IsDir: false}, {Path: "app.png", IsDir: false}}
	if !slices.Equal(got, want) {
		t.Fatalf("RootPrefixMatches(%q) = %#v, want %#v", "A", got, want)
	}
}

func TestMergeAtMentionOptions(t *testing.T) {
	primary := []Option{{Path: "main.go"}, {Path: "misc.md"}}
	secondary := []Option{{Path: "misc.md"}, {Path: "Makefile"}}
	got := MergeOptions(primary, secondary, "m")
	want := []Option{{Path: "main.go"}, {Path: "misc.md"}, {Path: "Makefile"}}
	if !slices.Equal(got, want) {
		t.Fatalf("MergeOptions() = %#v, want %#v", got, want)
	}

	primary = make([]Option, 0, 49)
	for i := range 49 {
		primary = append(primary, Option{Path: fmt.Sprintf("p-%02d", i)})
	}
	secondary = []Option{{Path: "tail-0"}, {Path: "tail-1"}}
	got = MergeOptions(primary, secondary, "p")
	if len(got) != 50 {
		t.Fatalf("len(MergeOptions()) = %d, want 50", len(got))
	}
	if got[49].Path != "tail-0" {
		t.Fatalf("MergeOptions()[49] = %q, want %q", got[49].Path, "tail-0")
	}

	primary = []Option{{Path: "amber-vault/a-00.txt"}, {Path: "brook/amber-note.txt"}}
	secondary = []Option{{Path: "amber-map.json"}, {Path: "AtlasGuide.txt"}}
	got = MergeOptions(primary, secondary, "A")
	want = []Option{{Path: "AtlasGuide.txt"}, {Path: "amber-vault/a-00.txt"}, {Path: "brook/amber-note.txt"}, {Path: "amber-map.json"}}
	if !slices.Equal(got, want) {
		t.Fatalf("case-sensitive MergeOptions() = %#v, want %#v", got, want)
	}
}

func TestAtMentionOptionsMissingFromIndex(t *testing.T) {
	options := []Option{{Path: "main.go"}, {Path: "Makefile"}, {Path: "docs/"}}
	files := []string{"main.go", "misc.md"}
	got := OptionsMissingFromIndex(options, files)
	want := []Option{{Path: "Makefile"}, {Path: "docs/"}}
	if !slices.Equal(got, want) {
		t.Fatalf("OptionsMissingFromIndex() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFuzzyMatchesMultiStepPath(t *testing.T) {
	query := "docs/file.md"
	matches := FuzzyMatches([]string{
		"docs/architecture/file-mentions.md",
		"docs/other-file.md",
		"internal/tui/filecompletion.go",
	}, nil, query, nil)
	if len(matches) == 0 {
		t.Fatal("FuzzyMatches() returned no matches")
	}
	if got := matches[0].Path; got != "docs/architecture/file-mentions.md" {
		t.Fatalf("top fuzzy match = %q, want %q", got, "docs/architecture/file-mentions.md")
	}
}

func TestAtMentionFuzzyMatchesKeepsLowScoreMatches(t *testing.T) {
	matches := FuzzyMatches([]string{
		"reports/stage1_stage2_design_doc.md",
		"docs/other-file.md",
	}, nil, "s1s2doc", nil)
	if len(matches) != 1 {
		t.Fatalf("len(matches) = %d, want 1", len(matches))
	}
	if got := matches[0].Path; got != "reports/stage1_stage2_design_doc.md" {
		t.Fatalf("top fuzzy match = %q, want %q", got, "reports/stage1_stage2_design_doc.md")
	}
}

func TestAtMentionFuzzyMatchesPreferShallowerPaths(t *testing.T) {
	files := make([]string, 0, 62)
	for i := range 60 {
		files = append(files, fmt.Sprintf("nebula/ring/%02d/AtlasGuide.txt", i))
	}
	files = append(files, "orbit/AtlasGuide.txt", "AtlasGuide.txt")

	matches := FuzzyMatches(files, nil, "A", nil)

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

	got := FuzzyMatches(files, nil, "", nil)
	if len(got) != 2 {
		t.Fatalf("len(matches) = %d, want 2 visible non-hidden matches", len(got))
	}
	if got[0].Path != "docs/guide.md" || got[1].Path != "src/main.go" {
		t.Fatalf("bare @ matches = %#v, want only non-hidden paths", got)
	}

	got = FuzzyMatches(files, nil, ".c", nil)
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

	got := PathMatches(".", wd)
	if len(got) == 0 {
		t.Fatal(". returned no hidden matches")
	}
	if got[0].Path != ".cursor/" || !got[0].IsDir {
		t.Fatalf(". first match = %#v, want .cursor/", got)
	}

	got = PathMatches("./", wd)
	if len(got) != 1 {
		t.Fatalf("len(matches) = %d, want 1 visible entry", len(got))
	}
	if got[0].Path != "./docs/" || !got[0].IsDir {
		t.Fatalf("./ matches = %#v, want only ./docs/", got)
	}

	got = PathMatches("./.", wd)
	if len(got) == 0 {
		t.Fatal("./. returned no hidden matches")
	}
	if got[0].Path != "./.cursor/" || !got[0].IsDir {
		t.Fatalf("./. first match = %#v, want ./.cursor/", got)
	}

	got = PathMatches("..", wd)
	if len(got) != 1 || got[0].Path != "../" || !got[0].IsDir {
		t.Fatalf(".. matches = %#v, want only ../", got)
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

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{text}, wd), wd))
	want := []string{`dir with space/a file.txt`, `plain.txt`}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsParsesLineRanges(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "main.go"), "content")

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{"review @main.go:12 and @main.go:20-25 and @main.go:bad"}, wd), wd))
	want := []string{"main.go:12", "main.go:20-25", "main.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsOnlyTreatsNumericColonSuffixAsLineRange(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "main.go"), "content")

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{"review @main.go:abc"}, wd), wd))
	want := []string{"main.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsPrefersColonFilenameOverLineRange(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "note:12"), "colon")
	mustWriteFile(t, filepath.Join(wd, "note"), "plain")

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{"review @note:12"}, wd), wd))
	want := []string{"note:12"}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsDedupesByPathAndLineRange(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "main.go"), "content")

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{"@main.go:1 @./main.go:1 @main.go:2 @main.go"}, wd), wd))
	want := []string{"main.go:1", "main.go:2", "main.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
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

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{text}, wd), wd))
	want := []string{`AGENTS.md`, `docs/requirements,first-draft.md`, `docs/a,b.md`}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
	}
}

func TestAtMentionFileRefsPrefersFullCandidateBeforeProseDelimiterFallback(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "docs", "note,analysis"), "full")
	mustWriteFile(t, filepath.Join(wd, "docs", "note"), "short")

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{`Review @docs/note,analysis`}, wd), wd))
	want := []string{`docs/note,analysis`}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
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
		got := DisplayRefs(DedupeRefs(ParseRefs([]string{text}, wd), wd))
		want := []string{"notes.md"}
		if !slices.Equal(got, want) {
			t.Errorf("DisplayRefs(%q) = %#v, want %#v", text, got, want)
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
		got := DisplayRefs(DedupeRefs(ParseRefs([]string{text}, wd), wd))
		if len(got) > 0 {
			t.Errorf("DisplayRefs(%q) = %#v, want empty (should reject identifier prefix)", text, got)
		}
	}
}

func TestAtMentionFileRefsSkipsRemovedComposerReference(t *testing.T) {
	wd := t.TempDir()
	mustWriteFile(t, filepath.Join(wd, "keep.txt"), "keep")
	mustWriteFile(t, filepath.Join(wd, "drop.txt"), "drop")

	got := DisplayRefs(DedupeRefs(ParseRefs([]string{"keep @keep.txt only"}, wd), wd))
	want := []string{"keep.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayRefs() = %#v, want %#v", got, want)
	}
}

// Typing a query character by character must produce exactly the same
// completion list as scoring every query against the full index: the narrowing
// cache only skips files the previous query's full match set already excludes,
// which is sound because subsequence-based scoring shrinks the match set
// monotonically under query extension.
func TestAtMentionFuzzyNarrowingMatchesFullScan(t *testing.T) {
	files := []string{
		"cmd/chord/headless.go",
		"internal/agent/main_loop.go",
		"internal/agent/main_handlers_tools.go",
		"internal/ctxmgr/manager.go",
		"internal/llm/responses_convert.go",
		"internal/tui/filecompletion_paths.go",
		"internal/tui/filecompletion_ui.go",
		"internal/tools/truncate.go",
		"docs/usage.md",
		"README.md",
	}
	lower := BuildLowerIndex(files)
	queries := []string{"m", "ma", "mai", "main", "main_", "main_lo", "main_loop", "f", "fi", "file", "filec", "filecompletion", "resp", "respo", "responses", "u", "us", "usa", "usage"}

	var narrow NarrowCache
	for _, query := range queries {
		full := FuzzyMatches(files, lower, query, nil)
		narrowed := FuzzyMatches(files, lower, query, &narrow)
		if len(full) != len(narrowed) {
			t.Fatalf("query %q: full scan returned %d matches, narrowed returned %d", query, len(full), len(narrowed))
		}
		for i := range full {
			if full[i].Path != narrowed[i].Path {
				t.Fatalf("query %q: match %d = %q, want %q", query, i, narrowed[i].Path, full[i].Path)
			}
		}
	}

	// A query that deletes a character (prefix moved backwards) must not reuse
	// the cached candidate set: widen the query and compare against full scans.
	var restart NarrowCache
	for _, query := range []string{"main_loop", "main_lo", "main", "ma"} {
		full := FuzzyMatches(files, lower, query, nil)
		narrowed := FuzzyMatches(files, lower, query, &restart)
		if len(full) != len(narrowed) {
			t.Fatalf("shrinking query %q: full scan returned %d matches, narrowed returned %d", query, len(full), len(narrowed))
		}
		for i := range full {
			if full[i].Path != narrowed[i].Path {
				t.Fatalf("shrinking query %q: match %d = %q, want %q", query, i, narrowed[i].Path, full[i].Path)
			}
		}
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
