package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchDisplayTargetsPreserveModelFacingPaths(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: src/old.go\n" +
		"*** Move to: src/new.go\n" +
		"@@\n-old\n+new\n" +
		"*** Add File: docs/new.md\n+# New\n" +
		"*** Delete File: tmp/old.txt\n" +
		"*** End Patch"
	targets, err := ApplyPatchDisplayTargets(applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("ApplyPatchDisplayTargets error: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("targets = %#v, want three operations", targets)
	}
	if targets[0].Kind != MutationUpdate || targets[0].SourcePath != "src/old.go" || targets[0].TargetPath != "src/new.go" {
		t.Fatalf("move target = %#v", targets[0])
	}
	if targets[0].Added != 1 || targets[0].Removed != 1 {
		t.Fatalf("move stats = +%d -%d, want +1 -1", targets[0].Added, targets[0].Removed)
	}
	if targets[1].Kind != MutationAdd || targets[1].SourcePath != "docs/new.md" {
		t.Fatalf("add target = %#v", targets[1])
	}
	if targets[1].Added != 1 || targets[1].Removed != 0 {
		t.Fatalf("add stats = +%d -%d, want +1 -0", targets[1].Added, targets[1].Removed)
	}
	if targets[2].Kind != MutationDelete || targets[2].SourcePath != "tmp/old.txt" {
		t.Fatalf("delete target = %#v", targets[2])
	}
}

func TestApplyPatchDisplayTargetsAggregateRepeatedUpdates(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: src/demo.go\n@@\n-old\n+middle\n" +
		"*** Update File: src/./demo.go\n@@\n-middle\n+new\n" +
		"*** End Patch"
	targets, err := ApplyPatchDisplayTargets(applyPatchArgs(t, patch))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %#v, want one file-level target", targets)
	}
	target := targets[0]
	if target.SourcePath != "src/demo.go" || target.Added != 2 || target.Removed != 2 {
		t.Fatalf("target = %#v, want first model path with aggregate operation stats", target)
	}
}

func TestApplyPatchDisplayTargetsRejectsNonCanonicalArgs(t *testing.T) {
	raw := json.RawMessage(`{"path":"src/demo.go","patch":"@@\n-old\n+new\n"}`)
	if _, err := ApplyPatchDisplayTargets(raw); err == nil {
		t.Fatal("ApplyPatchDisplayTargets unexpectedly accepted non-canonical args")
	}
}

func TestApplyPatchCodexParserAndHunkCompatibility(t *testing.T) {
	t.Run("first hunk omits marker", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: file.txt\n-old\n+new\n*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "new\n")
	})

	t.Run("pure addition precedes earlier replacement", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: file.txt\n@@\n+tail-one\n+tail-two\n@@\n line1\n-line2\n-line3\n+replacement\n" +
			"*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "line1\nreplacement\ntail-one\ntail-two\n")
	})

	t.Run("update adds final newline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "new\n")
	})

	t.Run("implicit hunk preserves bare empty context", func(t *testing.T) {
		doc, err := ParseApplyPatch("*** Begin Patch\n*** Update File: file.txt\n context before\n\n-context after\n+context updated\n*** End Patch")
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Operations) != 1 || len(doc.Operations[0].Hunks) != 1 {
			t.Fatalf("doc = %#v", doc)
		}
		lines := doc.Operations[0].Hunks[0].Lines
		if len(lines) != 4 || lines[1].Kind != ' ' || lines[1].Text != "" {
			t.Fatalf("lines = %#v, want bare empty context line", lines)
		}
	})
}

func TestApplyPatchProsePunctuationTolerance(t *testing.T) {
	t.Run("preserves unchanged punctuation", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "注册表保持配置数组顺序；旧字段，结尾。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-注册表保持配置数组顺序;旧字段,结尾.\n" +
			"+注册表保持配置数组顺序;新字段,结尾.\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "used punctuation/whitespace-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation/whitespace-tolerant note", out)
		}
		assertApplyPatchFile(t, path, "注册表保持配置数组顺序；新字段，结尾。\n")
	})

	t.Run("rejects ambiguous normalized matches", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "相同；文本。\n中间\n相同；文本。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-相同;文本.\n" +
			"+不同;文本.\n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "punctuation/whitespace-tolerant matching is ambiguous at lines 1, 3") {
			t.Fatalf("error = %v, want ambiguous punctuation candidates", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	t.Run("applies to any decoded text file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env.example")
		content := "MESSAGE=\"old;value。\"\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: .env.example\n" +
			"@@\n" +
			"-MESSAGE=\"old;value.\"\n" +
			"+MESSAGE=\"new;value.\"\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "used punctuation/whitespace-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation/whitespace-tolerant note", out)
		}
		assertApplyPatchFile(t, path, "MESSAGE=\"new;value。\"\n")
	})

	t.Run("applies to extensionless text files", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "Makefile")
		content := "说明:old value。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: Makefile\n" +
			"@@\n" +
			"-说明:old value.\n" +
			"+说明:new value.\n" +
			"*** End Patch"

		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "说明:new value。\n")
	})

	// ApplyPatch shares the same separator-space folding as Edit: "：" and
	// ": " (and ":the" when the space is dropped) are treated as equivalent,
	// and unchanged context keeps the file's own bytes.
	t.Run("applies separator-space folding", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "说明：旧值。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-说明: 旧值.\n" +
			"+说明: 新值.\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "used punctuation/whitespace-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation/whitespace-tolerant note", out)
		}
		// The colon is unchanged context, so the file's full-width "：" and
		// "。" survive; only the delta "新" replaces "旧".
		assertApplyPatchFile(t, path, "说明：新值。\n")
	})

	t.Run("rejects ambiguous separator-space matches", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "a：b\na: b\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		// "a:b" matches neither line exactly but normalizes to the same
		// "a:b" as both variants, so the match must be rejected as ambiguous.
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-a:b\n" +
			"+a:x\n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("error = %v, want ambiguous candidates", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	t.Run("applies model-intended punctuation normalization", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "说明：旧值。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		// old/new differ in original bytes at the colon (full-width vs
		// half-width+space); that difference is the model's intended delta
		// and must come from newText verbatim.
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-说明：旧值。\n" +
			"+说明: 新值.\n" +
			"*** End Patch"

		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "说明: 新值.\n")
	})

	t.Run("reports text that is only part of a line", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "前缀；`pipeline_timeout_seconds` 是旧字段，后缀。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-`pipeline_timeout_seconds` 是旧字段\n" +
			"+`EXTERNAL_TIMEOUT_SECONDS` 是统一字段\n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "only part of current line 1") || !strings.Contains(err.Error(), "include that complete line") {
			t.Fatalf("error = %v, want complete-line guidance", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	t.Run("applies inter-word space tolerance", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "diff and count\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		// The hunk drops the inter-word space ("diffand"); the tolerance
		// treats it as optional and preserves the file's own space in the
		// unchanged context.
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-diffand count\n" +
			"+diffand total\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want tolerant note", out)
		}
		assertApplyPatchFile(t, path, "diff and total\n")
	})

	t.Run("replaces a fully-variant line after tolerant match", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "旧值；结尾。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-旧值;结尾.\n" +
			"+完全不同\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatalf("error = %v, want a fully-variant tolerant replacement to apply", err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want tolerant note", out)
		}
		assertApplyPatchFile(t, path, "完全不同\n")
	})

	t.Run("does not hide shifted surrounding whitespace", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := " 相同；文本。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-相同;文本. \n" +
			"+不同;文本. \n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "hunk not found (1/1)") {
			t.Fatalf("error = %v, want surrounding-whitespace mismatch", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	// An orphaned combining mark (U+0304) leaked into the hunk's removal
	// line — the "### ̄.2.1" shape — is folded by the shared tolerance
	// normalizer just like Edit's old_string, so a model copy that carries the
	// tokenizer artifact still replaces the file's clean heading.

	t.Run("folds orphaned combining mark", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "### 3.2.1 Heading\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-### \u03043.2.1 Heading\n" +
			"+### 3.2.1 Renamed\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want tolerant note", out)
		}
		assertApplyPatchFile(t, path, "### 3.2.1 Renamed\n")
	})
}

func TestApplyPatchNormalizesUnifiedDiffHeader(t *testing.T) {
	// Models copy the unified-diff header spelling ("@@ -19,10 +19,8 @@"),
	// whose "-l,s +l,s" range is line-number noise Chord can never anchor on.
	// It must be stripped: a range-only header collapses to empty (the hunk
	// body anchors), and a range with a trailing section keeps that section as
	// the real anchor. Both must still apply end to end.
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	original := "package x\n\nfunc TestDegraded(t *testing.T) {\n\tif true {\n\t}\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "package x\n\nfunc TestDegraded(t *testing.T) {\n\tif true {\n\t}\n}\n\nfunc TestAdded(t *testing.T) {}\n"

	// Range-only header: collapses to empty, body still anchors and applies.
	rangeOnly := "*** Begin Patch\n" +
		"*** Update File: " + path + "\n" +
		"@@ -19,10 +19,8 @@\n" +
		" func TestDegraded(t *testing.T) {\n" +
		" \tif true {\n" +
		" \t}\n" +
		" }\n" +
		"+\n" +
		"+func TestAdded(t *testing.T) {}\n" +
		"*** End Patch"
	doc, err := ParseApplyPatch(rangeOnly)
	if err != nil {
		t.Fatalf("parse range-only header: %v", err)
	}
	if got := doc.Operations[0].Hunks[0].Header; got != "" {
		t.Fatalf("range-only header = %q, want empty", got)
	}
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, rangeOnly)); err != nil {
		t.Fatalf("range-only header did not apply: %v", err)
	}
	assertApplyPatchFile(t, path, want)

	// Reset the file; the range-with-section case applies from the original.
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// Range with a trailing section: the section becomes the anchor.
	withSection := "*** Begin Patch\n" +
		"*** Update File: " + path + "\n" +
		"@@ -19,10 +19,8 @@ func TestDegraded(t *testing.T) {\n" +
		" \tif true {\n" +
		" \t}\n" +
		" }\n" +
		"+\n" +
		"+func TestAdded(t *testing.T) {}\n" +
		"*** End Patch"
	doc, err = ParseApplyPatch(withSection)
	if err != nil {
		t.Fatalf("parse header with section: %v", err)
	}
	if got := doc.Operations[0].Hunks[0].Header; got != "func TestDegraded(t *testing.T) {" {
		t.Fatalf("header with section = %q, want %q", got, "func TestDegraded(t *testing.T) {")
	}
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, withSection)); err != nil {
		t.Fatalf("header with section did not apply: %v", err)
	}
	assertApplyPatchFile(t, path, want)
}

// TestApplyPatchHunkMarkerDrivesHeaderNormalization pins that the scanner's
// hunk marker is the same token the header normalizer's pattern is built from.
// If the two ever named it separately, a patch could be split on one spelling
// while its line-range noise was stripped by another, leaving the range in
// place as an anchor no file can match.
func TestApplyPatchHunkMarkerDrivesHeaderNormalization(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{raw: "-19,10 +19,8 " + applyPatchHunkMarker, want: ""},
		{raw: "-19,10 +19,8 " + applyPatchHunkMarker + " func greet():", want: "func greet():"},
		{raw: "-1 +1 " + applyPatchHunkMarker + " func greet():", want: "func greet():"},
		// Chord's own spelling carries no range and passes through untouched.
		{raw: "func greet():", want: "func greet():"},
	} {
		if got := applyPatchNormalizeHeader(tc.raw); got != tc.want {
			t.Fatalf("applyPatchNormalizeHeader(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseApplyPatchRejectsNonCodexEnvelope(t *testing.T) {
	_, err := ParseApplyPatch("@@\n-old\n+new\n")
	if err == nil || !strings.Contains(err.Error(), "Begin Patch") {
		t.Fatalf("err=%v", err)
	}
}

func TestNormalizeApplyPatchArgsIgnoresLegacyPathField(t *testing.T) {
	// The legacy single-file {path, patch} wrapper is tolerated by ignoring
	// the `path` field: only `patch` participates, matching the generic
	// sanitization applied to tool arguments before Execute. The patch must
	// still satisfy the constraints (non-empty; the envelope is enforced by
	// ParseApplyPatch at execution time).
	raw := json.RawMessage(`{"path":"file.txt","patch":"*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"}`)
	normalized, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		t.Fatalf("NormalizeApplyPatchArgs error = %v", err)
	}
	if string(normalized) != `{"patch":"*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"}` {
		t.Fatalf("normalized = %s, want patch-only canonical args with path ignored", normalized)
	}
}

func TestNormalizeApplyPatchArgsLegacyPathWithoutPatchRejected(t *testing.T) {
	// A {path, patch} wrapper with an empty or missing patch is rejected even
	// though `path` is ignored: patch is the required field.
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"path":"file.txt"}`),
		json.RawMessage(`{"path":"file.txt","patch":""}`),
		json.RawMessage(`{"path":"file.txt","patch":"   "}`),
	} {
		if _, err := NormalizeApplyPatchArgs(raw); err == nil {
			t.Fatalf("NormalizeApplyPatchArgs(%s) succeeded, want patch-required error", raw)
		}
	}
}

func TestNormalizeApplyPatchArgsBareFreeformText(t *testing.T) {
	raw := json.RawMessage(`*** Begin Patch
*** Update File: file.txt
@@
-old
+new
*** End Patch`)
	normalized, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		t.Fatalf("NormalizeApplyPatchArgs error = %v", err)
	}
	var args ApplyPatchArgs
	if err := json.Unmarshal(normalized, &args); err != nil {
		t.Fatalf("normalized = %s, want canonical object: %v", normalized, err)
	}
	if !strings.Contains(args.Patch, "*** Update File: file.txt") {
		t.Fatalf("patch = %q, want the bare freeform text preserved", args.Patch)
	}
}

func TestNormalizeApplyPatchArgsJSONStringWrapped(t *testing.T) {
	raw := json.RawMessage(`"*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch"`)
	normalized, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		t.Fatalf("NormalizeApplyPatchArgs error = %v", err)
	}
	var args ApplyPatchArgs
	if err := json.Unmarshal(normalized, &args); err != nil {
		t.Fatalf("normalized = %s, want canonical object: %v", normalized, err)
	}
	if !strings.Contains(args.Patch, "*** Delete File: gone.txt") {
		t.Fatalf("patch = %q, want the unwrapped text", args.Patch)
	}
}

func TestNormalizeApplyPatchArgsGatewayLoweringDiagnostic(t *testing.T) {
	// A gateway that received a custom tool but lowered it to a function call
	// emits {"input": "..."} instead of {"patch": "..."}. The error must name
	// the actionable compat knob.
	raw := json.RawMessage(`{"input":"*** Begin Patch\n*** End Patch"}`)
	_, err := NormalizeApplyPatchArgs(raw)
	if err == nil {
		t.Fatal("NormalizeApplyPatchArgs error = nil, want lowering diagnostic")
	}
	if !strings.Contains(err.Error(), "freeform: false") {
		t.Fatalf("error = %q, want hint to set compat.apply_patch.freeform: false", err)
	}
}

func TestNormalizeApplyPatchArgsEmptyObjectStillRequiresPatch(t *testing.T) {
	if _, err := NormalizeApplyPatchArgs(json.RawMessage(`{}`)); err == nil {
		t.Fatal("NormalizeApplyPatchArgs({}) error = nil, want patch is required")
	}
}

func TestNormalizeApplyPatchArgsUnwrapsFreeformEnvelope(t *testing.T) {
	// A model that wrapped its freeform custom-tool input in a JSON envelope
	// despite the custom-tool contract reaches
	// execution as {"patch":"{\"input\":\"*** Begin Patch...\"}"}: the
	// freeform canonicalization double-wraps the envelope. Normalization must
	// strip it so the real patch reaches ParseApplyPatch.
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"
	envelope, err := json.Marshal(map[string]string{"input": patch})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"patch": string(envelope)})
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		t.Fatalf("NormalizeApplyPatchArgs error = %v", err)
	}
	var args ApplyPatchArgs
	if err := json.Unmarshal(normalized, &args); err != nil {
		t.Fatalf("normalized = %s, want canonical object: %v", normalized, err)
	}
	if args.Patch != patch {
		t.Fatalf("patch = %q, want the unwrapped freeform text", args.Patch)
	}
	if _, err := ParseApplyPatch(args.Patch); err != nil {
		t.Fatalf("ParseApplyPatch on normalized patch: %v", err)
	}
}

func TestParseApplyPatchRecoversMissingBeginMarker(t *testing.T) {
	doc, err := ParseApplyPatch("*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Kind != MutationUpdate || doc.Operations[0].Path != "file.txt" {
		t.Fatalf("doc = %#v, want single update op", doc)
	}
}

func TestParseApplyPatchRecoversMissingEndMarker(t *testing.T) {
	doc, err := ParseApplyPatch("*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || len(doc.Operations[0].Hunks) != 1 {
		t.Fatalf("doc = %#v, want single update hunk", doc)
	}
}

func TestParseApplyPatchRecoversMissingEndMarkerForDelete(t *testing.T) {
	doc, err := ParseApplyPatch("*** Begin Patch\n*** Delete File: file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Kind != MutationDelete || doc.Operations[0].Path != "file.txt" {
		t.Fatalf("doc = %#v, want single delete op", doc)
	}
}

func TestParseApplyPatchRecoversMissingEndMarkerForMoveOnlyUpdate(t *testing.T) {
	doc, err := ParseApplyPatch("*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Kind != MutationUpdate || doc.Operations[0].Path != "old.txt" || doc.Operations[0].MovePath != "new.txt" {
		t.Fatalf("doc = %#v, want single move-only update op", doc)
	}
}

func TestParseApplyPatchRejectsLeadingProseBeforeOperation(t *testing.T) {
	_, err := ParseApplyPatch("Applying patch now\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "Begin Patch") {
		t.Fatalf("err = %v, want begin-marker validation", err)
	}
}

func TestApplyPatchStarLinesNextToRealMarkers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("*** old banner\nbody\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: first.txt\n" +
		"@@\n-*** old banner\n+*** new banner\n *** more stars\n" +
		"*** Add File: second.txt\n" +
		"+*** generated\n+content\n" +
		"*** End Patch"
	// The hunk references a context line missing from the file; only the
	// parse/boundary behavior matters here, so apply must fail on matching,
	// not on protocol parsing.
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v, want hunk-match failure instead of a parse error", err)
	}
	doc, err := ParseApplyPatch(strings.ReplaceAll(patch, " *** more stars\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 2 || doc.Operations[1].Content != "*** generated\ncontent\n" {
		t.Fatalf("doc = %+v, want update plus add with literal *** content", doc)
	}
}

func TestApplyPatchHeaderRepeatedAsFirstContextLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	content := "func alpha() {\n\ta := 1\n}\n\nfunc target() {\n\told := 1\n\t_ = old\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	// Models often repeat the @@ header text as the hunk's first context line;
	// matching must then anchor at the header itself, not strictly after it.
	patch := "*** Begin Patch\n" +
		"*** Update File: code.go\n" +
		"@@ func target() {\n" +
		" func target() {\n" +
		"-\told := 1\n" +
		"+\tnew := 2\n" +
		"-\t_ = old\n" +
		"+\t_ = new\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "func alpha() {\n\ta := 1\n}\n\nfunc target() {\n\tnew := 2\n\t_ = new\n}\n"
	if string(got) != want {
		t.Fatalf("code.go = %q, want the hunk applied at the header line", got)
	}
}

// A valid envelope with leading whitespace must be preserved as the current
// patch argument.
func TestApplyPatchEnvelopeWithLeadingWhitespacePreservesCurrentArgs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lead.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "\n  \n*** Begin Patch\n*** Update File: lead.txt\n@@\n-before\n+after\n*** End Patch\n"
	raw, err := json.Marshal(map[string]any{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), raw); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after\n" {
		t.Fatalf("result = %q, %v; want after", got, err)
	}
}

func TestApplyPatchBlankLinesBetweenOperationsAreSeparators(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"a.txt": "one\n", "b.txt": "two\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: a.txt\n@@\n-one\n+ONE\n\n" +
		"*** Update File: b.txt\n@@\n-two\n+TWO\n\n\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "ONE\n", "b.txt": "TWO\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestApplyPatchBlankLineBeforeNextHunkIsSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(path, []byte("one\nmid\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: c.txt\n@@\n-one\n+ONE\n\n@@\n-two\n+TWO\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "ONE\nmid\nTWO\n" {
		t.Fatalf("c.txt = %q, %v; want ONE/mid/TWO", got, err)
	}
}

func TestApplyPatchAddFileTrailingBlankLinesAreSeparators(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n" +
		"*** Add File: fresh.txt\n+hello\n\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "fresh.txt"))
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("fresh.txt = %q, %v; want hello", got, err)
	}
}

func TestApplyPatchAddFileInteriorBlankLineStillRejected(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n" +
		"*** Add File: bad.txt\n+hello\n\n+world\n" +
		"*** End Patch"
	if _, err := ParseApplyPatch(patch); err == nil || !strings.Contains(err.Error(), "must start with +") {
		t.Fatalf("err = %v, want add-file line error", err)
	}
	_ = dir
}

func TestApplyPatchTargetsPreserveRepeatedUpdateOperations(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n" +
		"*** Update File: dup.txt\n@@\n-a\n+b\n" +
		"*** Update File: ./dup.txt\n@@\n-b\n+c\n" +
		"*** End Patch"
	targets, err := ApplyPatchTargets(applyPatchArgs(t, patch), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Kind != MutationUpdate || targets[1].Kind != MutationUpdate || targets[0].SourcePath != filepath.Join(dir, "dup.txt") || targets[1].SourcePath != targets[0].SourcePath {
		t.Fatalf("targets = %#v, want two operations on one normalized path", targets)
	}
	paths := MutationTargetPaths(targets)
	if len(paths) != 1 || paths[0] != filepath.Join(dir, "dup.txt") {
		t.Fatalf("paths = %#v, want one lock and permission path", paths)
	}
}

func TestApplyPatchBlankLineAfterEndOfFileIsSeparator(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("a\n"), 0644)
	os.WriteFile(filepath.Join(dir, "y.txt"), []byte("c\n"), 0644)
	patch := "*** Begin Patch\n" +
		"*** Update File: x.txt\n@@\n-a\n+b\n*** End of File\n\n" +
		"*** Update File: y.txt\n@@\n-c\n+d\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatalf("blank after End of File should be tolerated: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "x.txt"))
	if string(got) != "b\n" {
		t.Fatalf("x.txt = %q, want b", got)
	}
}

func TestApplyPatchBlankLineBetweenHeaderAndFirstHunk(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old\n"), 0644)
	patch := "*** Begin Patch\n" +
		"*** Update File: a.txt\n\n@@\n-old\n+new\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatalf("blank between update header and first @@ should be tolerated: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "new\n" {
		t.Fatalf("a.txt = %q, want new", got)
	}
}

// TestParseApplyPatchKeepsBareHunksAndTrailingBlankLines verifies that
// adjacent bare @@ hunks stay separate hunks within one operation and that
// trailing blank lines of added files survive parsing.
func TestParseApplyPatchKeepsBareHunksAndTrailingBlankLines(t *testing.T) {
	patch := "*** Begin Patch\n" +
		// Multiple bare @@ hunks in one file: boundaries must survive.
		"*** Update File: multi.txt\n" +
		"@@\n-alpha\n+ALPHA\n" +
		"@@\n-gamma\n+GAMMA\n" +
		"@@\n-missing\n+NEVER\n" +
		// Added file with intentional blank lines at EOF.
		"*** Add File: fresh.txt\n+line\n+\n+\n" +
		"*** End Patch"
	doc, err := ParseApplyPatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(doc.Operations[0].Hunks); got != 3 {
		t.Fatalf("hunk count = %d, want 3 separate bare hunks", got)
	}
	if got := doc.Operations[1].Content; got != "line\n\n\n" {
		t.Fatalf("added content = %q, want trailing blank lines preserved by the parser", got)
	}
}

// The parser's hunk-level errors carry the patch line they refer to; a patch
// with several hunks is unactionable without it. The number must track the
// offending hunk, not just be present.
func TestApplyPatchParserErrorsNameTheHunkLine(t *testing.T) {
	firstHunkBad := "*** Begin Patch\n" +
		"*** Update File: unchanged.md\n" +
		"@@\n" +
		" ## 5. Related docs\n" +
		"*** End Patch"
	_, err := ParseApplyPatch(firstHunkBad)
	if err == nil || !strings.Contains(err.Error(), "at line 3") {
		t.Fatalf("err = %v, want the context-only hunk reported at line 3", err)
	}
	secondHunkBad := "*** Begin Patch\n" +
		"*** Update File: unchanged.md\n" +
		"@@\n" +
		" keep\n" +
		"-drop\n" +
		"@@\n" +
		" ## 5. Related docs\n" +
		"*** End Patch"
	_, err = ParseApplyPatch(secondHunkBad)
	if err == nil || !strings.Contains(err.Error(), "at line 6") {
		t.Fatalf("err = %v, want the context-only second hunk reported at line 6", err)
	}
	notAHunk := "*** Begin Patch\n" +
		"*** Update File: unchanged.md\n" +
		"@@\n" +
		" keep\n" +
		"-drop\n" +
		"garbage\n" +
		"*** End Patch"
	if _, err := ParseApplyPatch(notAHunk); err == nil || !strings.Contains(err.Error(), "line 6") {
		t.Fatalf("err = %v, want the stray line reported at line 6", err)
	}
}
