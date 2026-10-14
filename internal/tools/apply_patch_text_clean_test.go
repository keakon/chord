package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyPatchStripsOrphanCombiningMarkFromAddedLine guards the write path:
// a model that leaks an orphan U+0304 into an added (+) line must not have it
// written to the file. The tolerant matcher folds comb-marks out of the context
// lines, but the added line's mark is cleaned off the bytes that get written
// and reported in the cleaned-invisible note.
func TestApplyPatchStripsOrphanCombiningMarkFromAddedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "### 3.2.1 Heading\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-### 3.2.1 Heading\n" +
		"+### 3.2.1 Heading\n" +
		"+## \u03043.2.1 Renamed\n" +
		"*** End Patch"

	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	if !strings.Contains(out, "cleaned 1 invisible character") {
		t.Fatalf("output = %q, want cleaned-invisible note", out)
	}
	assertApplyPatchFile(t, path, "### 3.2.1 Heading\n## 3.2.1 Renamed\n")
}

// TestApplyPatchCleansInvisibleOnlyInDecodableText guards the encoding
// boundary: the invisible-character clean runs on decoded text and re-encodes
// with the file's own encoding. A UTF-16 file whose body bytes (or the added
// line) carry U+0304 must end up with the mark cleaned in UTF-16, not with
// every byte the UTF-8 strip could not decode rewritten as U+FFFD. GB18030
// likewise: GBK lead bytes 0xCC/0xCD decode as combining marks under UTF-8
// fallback, so a GBK body must never be run through a UTF-8 strip.
func TestApplyPatchCleansInvisibleOnlyInDecodableText(t *testing.T) {
	t.Run("utf16 cleans mark in decoded text", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "notes.md")
		content := "### 3.2.1 Heading\n"
		encoded := mustEncodeForTest(content, "utf-16le")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: notes.md\n" +
			"@@\n" +
			"-### 3.2.1 Heading\n" +
			"+### 3.2.1 Heading\n" +
			"+## \u03043.2.1 Renamed\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatalf("Execute err = %v, want success", err)
		}
		if !strings.Contains(out, "cleaned 1 invisible character") {
			t.Fatalf("output = %q, want cleaned-invisible note", out)
		}
		want := mustEncodeForTest("### 3.2.1 Heading\n## 3.2.1 Renamed\n", "utf-16le")
		assertApplyPatchFileBytes(t, path, want)
	})

	t.Run("gbk file survives uncleaned body", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gbk.txt")
		// GB18030 text whose lead/trail bytes (0xCC 0xAB "太", 0xCD 0xAC
		// "同", ...) decode as UTF-8 combining marks: a UTF-8 strip over the
		// raw bytes would fold every such pair into a phantom mark and
		// rewrite the file as garbage. The clean must decode first and, for
		// text with no leaked marks, leave the bytes untouched. The body is
		// long enough for the regional detector to resolve gb18030 over
		// big5 (it needs a clear simplified-Chinese majority).
		content := "这是一段简体中文的说明文字，用来测试编码检测。\n不同的图书馆已经停用同样的图书管理系统。\n国家规定的标准规范文件必须使用统一的格式。\n"
		encoded := mustEncodeForTest(content, "gb18030")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: gbk.txt\n" +
			"@@\n" +
			"-不同的图书馆已经停用同样的图书管理系统。\n" +
			"+不同的图书馆已经停用同样的图书管理制度。\n" +
			"*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatalf("Execute err = %v, want success", err)
		}
		want := mustEncodeForTest("这是一段简体中文的说明文字，用来测试编码检测。\n不同的图书馆已经停用同样的图书管理制度。\n国家规定的标准规范文件必须使用统一的格式。\n", "gb18030")
		assertApplyPatchFileBytes(t, path, want)
	})
}

// TestApplyPatchKeepsCombiningMarkOnVisibleBaseInAddedLine guards the
// corrected orphan-mark boundary on the write path: a combining mark over a
// visible base (here a digit) in an added (+) line is legitimate content —
// Unicode lets marks attach to digits and symbols, e.g. a U+0305 overline —
// and must land in the file verbatim, not be dropped like a floating heading
// artifact (U+0304 after a space).
func TestApplyPatchKeepsCombiningMarkOnVisibleBaseInAddedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "### 3.2.1 Heading\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-### 3.2.1 Heading\n" +
		"+### 3.2.1 Heading\n" +
		"+value 3\u0305 stays\n" +
		"*** End Patch"

	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	if strings.Contains(out, "cleaned") {
		t.Fatalf("output = %q, want no cleaned-invisible note (digit mark is kept)", out)
	}
	want := "### 3.2.1 Heading\nvalue 3\u0305 stays\n"
	assertApplyPatchFile(t, path, want)
}

// TestApplyPatchLeavesUntouchedRegionRunesIntact guards the minimal-change
// boundary of the invisible-character clean: the clean runs only on the
// model-added (+) lines and add-file content, never on a mutation's whole
// after-bytes. File content the patch does not touch must survive byte for
// byte, including zero-width runes, orphan variation selectors, and
// combining marks the write-path strip would remove if they were model text.
func TestApplyPatchLeavesUntouchedRegionRunesIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "keep \u200bzero-width here\nkeep \ufe0f selector here\nkeep 1\u0304 digit mark here\n### 3.2.1 Heading\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-### 3.2.1 Heading\n" +
		"+### 3.2.1 Renamed\n" +
		"*** End Patch"

	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	want := "keep \u200bzero-width here\nkeep \ufe0f selector here\nkeep 1\u0304 digit mark here\n### 3.2.1 Renamed\n"
	assertApplyPatchFile(t, path, want)
}
