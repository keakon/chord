package filecompletion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadAtMentionFilesUsesGitTrackedAndUntracked(t *testing.T) {
	if _, err := exec.CommandContext(context.Background(), "git", "-C", ".", "--version").Output(); err != nil {
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
	if _, err := exec.CommandContext(context.Background(), "git", "-C", wd, "init").Output(); err != nil {
		t.Fatalf("git init error = %v", err)
	}
	mustWriteFile(t, filepath.Join(wd, "tracked.go"), "package main")
	mustWriteFile(t, filepath.Join(wd, "new.md"), "new")
	mustWriteFile(t, filepath.Join(wd, "ignored.log"), "ignored")
	mustWriteFile(t, filepath.Join(wd, "deleted.txt"), "deleted")
	mustWriteFile(t, filepath.Join(wd, ".gitignore"), "*.log\n")
	if _, err := exec.CommandContext(context.Background(), "git", "-C", wd, "add", "tracked.go", "deleted.txt", ".gitignore").Output(); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if err := os.Remove(filepath.Join(wd, "deleted.txt")); err != nil {
		t.Fatalf("Remove(deleted.txt) error = %v", err)
	}

	loaded := Scan("", 10000)
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

	loaded := Scan("", 10000)

	wantIn := []string{"main.go", "notes.md", "image.png", "paper.pdf", "icon.svg"}
	for _, p := range wantIn {
		if !slices.Contains(loaded, p) {
			t.Errorf("%q should be included; got %v", p, loaded)
		}
	}
	wantOut := []string{
		"bundle.zip", "libfoo.so", "pkg/mod.pyc", // non-attachable binary extensions
		"dist/app.js",    // hard-coded skipped dir
		"out/result.txt", // .gitignore dir
		"keys.secret",    // .gitignore file pattern
	}
	for _, p := range wantOut {
		if slices.Contains(loaded, p) {
			t.Errorf("%q should be excluded; got %v", p, loaded)
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

	loaded := Scan("", 10000)

	if !slices.Contains(loaded, "a/b/c/d/e/f/g/deep.txt") {
		t.Fatalf("deep file missing from preload: %v", loaded)
	}
	if slices.Contains(loaded, ".cursor/rules.md") {
		t.Fatalf("hidden dot-directory file should be excluded from preload: %v", loaded)
	}
}

func TestLoadAtMentionFilesKeepsGeneratedRootNames(t *testing.T) {
	for _, name := range []string{"build", "vendor", "dist"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name)
			mustWriteFile(t, filepath.Join(root, "main.go"), "package main")
			mustWriteFile(t, filepath.Join(root, "nested", "build", "generated.go"), "package nested")

			files := Scan(root, 10000)
			if !slices.Contains(files, "main.go") {
				t.Fatalf("explicit root %q was skipped: %v", name, files)
			}
			if slices.Contains(files, "nested/build/generated.go") {
				t.Fatalf("nested generated directory should stay excluded: %v", files)
			}
		})
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

	loaded := Scan("", 3)
	if got := len(loaded); got != 3 {
		t.Fatalf("len(files) = %d, want %d", got, 3)
	}
	if slices.Contains(loaded, "files/file-00007.txt") {
		t.Fatalf("expected files beyond limit to be omitted, but found tail entry")
	}
}

func TestAtMentionFallbackWalkHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if files := scanWalk(ctx, dir, 10000); len(files) != 0 {
		t.Fatalf("cancelled scan returned files: %v", files)
	}
}
