package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkipDirNames(t *testing.T) {
	for _, name := range []string{".git", ".svn", ".hg", ".bzr", ".jj", ".sl", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".pyre", ".pytype", ".venv", "venv", "env", "node_modules", "dist", "build", "target", "out", ".cache", ".idea"} {
		if !skipDirNames[name] {
			t.Errorf("skipDirNames[%q] = false, want true", name)
		}
	}
	// Non-skipped dot directories should not be in the map.
	for _, name := range []string{".chord", ".github", ".vscode", ".config"} {
		if skipDirNames[name] {
			t.Errorf("skipDirNames[%q] = true, want false", name)
		}
	}
}

func TestGitIgnoreMatcherSimple(t *testing.T) {
	dir := t.TempDir()
	gitignore := `.chord/
node_modules/
*.log
!important.log
build/
`
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newGitIgnoreMatcher(dir)
	if m == nil {
		t.Fatal("expected non-nil matcher")
	}

	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{".chord/sessions/123/main.jsonl", false, true},
		{".chord", true, true},
		{"node_modules/react/index.js", false, true},
		{"node_modules", true, true},
		{"app.log", false, true},
		{"important.log", false, false}, // negated
		{"build/output.js", false, true},
		{"build", true, true},
		{"main.go", false, false},
		{"internal/tools/grep.go", false, false},
	}

	for _, tt := range tests {
		got := m.Match(tt.path, tt.isDir)
		if got != tt.want {
			t.Errorf("Match(%q, %v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestGitIgnoreMatcherDirOnly(t *testing.T) {
	dir := t.TempDir()
	gitignore := "docs/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newGitIgnoreMatcher(dir)

	// "docs/" matches directories but not files named "docs".
	if !m.Match("docs", true) {
		t.Error("Match(\"docs\", true) = false, want true")
	}
	if m.Match("docs", false) {
		t.Error("Match(\"docs\", false) = true, want false")
	}
}

func TestGitIgnoreMatcherWithSlash(t *testing.T) {
	dir := t.TempDir()
	gitignore := "src/generated/\n/docs/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newGitIgnoreMatcher(dir)

	// "src/generated/" should match "src/generated" as a dir.
	if !m.Match("src/generated", true) {
		t.Error("Match(\"src/generated\", true) = false, want true")
	}
	// "/docs/" should match "docs" as a dir (anchored to root).
	if !m.Match("docs", true) {
		t.Error("Match(\"docs\", true) = false, want true")
	}
	// "/docs/" should not match "src/docs" (anchored).
	if m.Match("src/docs", true) {
		t.Error("Match(\"src/docs\", true) = true, want false")
	}
}

func TestGitIgnoreMatcherDoublestar(t *testing.T) {
	dir := t.TempDir()
	gitignore := "src/**/generated/\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newGitIgnoreMatcher(dir)

	if !m.Match("src/generated", true) {
		t.Error("Match(\"src/generated\", true) = false, want true")
	}
	if !m.Match("src/foo/generated", true) {
		t.Error("Match(\"src/foo/generated\", true) = false, want true")
	}
	if !m.Match("src/foo/bar/generated", true) {
		t.Error("Match(\"src/foo/bar/generated\", true) = false, want true")
	}
	if m.Match("generated", true) {
		t.Error("Match(\"generated\", true) = true, want false")
	}
}

func TestGitIgnoreMatcherNoFile(t *testing.T) {
	dir := t.TempDir()
	m := newGitIgnoreMatcher(dir)
	if m != nil {
		t.Error("expected nil matcher when no .gitignore exists")
	}
}

func TestIsExcludedPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".git/HEAD", true},
		{".git", true},
		{".chord/sessions/123/main.jsonl", false},
		{".svn/wc.db", true},
		{".hg/store/00manifest.i", true},
		{"src/main.go", false},
		{".github/workflows/ci.yml", false},
		{".vscode/settings.json", false},
		{".venv/lib/python3.12/site-packages/requests/__init__.py", true},
		{"node_modules/react/index.js", true},
		{"build/output.js", true},
		{"dist/bundle.min.js", true},
		{"target/debug/main", true},
		{"out/bin/app", true},
		{".cache/go-build/abc", true},
		{".idea/workspace.xml", true},
	}
	for _, tt := range tests {
		got := isExcludedPath(tt.path)
		if got != tt.want {
			t.Errorf("isExcludedPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
