package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadArtifactPagesAndReportsDigest(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.ToSlash(filepath.Join("artifacts", "reports", "report.txt"))
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "one\ntwo\nthree\nfour\n"
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])
	out, err := (ReadArtifactTool{}).Execute(WithSessionDir(context.Background(), dir), mustMarshal(t, map[string]any{
		"path": rel, "offset": 2, "limit": 2, "expected_sha256": digest,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "ARTIFACT_RESULT lines=2-3 total=4 sha256="+digest) || !strings.HasSuffix(out, "two\nthree\n") {
		t.Fatalf("output = %q", out)
	}
}

// TestSaveImmutableResultEnforcesStoreCeiling pins the size ceiling that
// canonicalResultObject applies on behalf of the immutable result store, and
// that a rejected result leaves nothing behind.
func TestSaveImmutableResultEnforcesStoreCeiling(t *testing.T) {
	dir := t.TempDir()
	oversized := json.RawMessage(`{"value":"` + strings.Repeat("x", maxImmutableResultBytes) + `"}`)
	if _, _, err := SaveImmutableResult(dir, "sample/report", oversized); err == nil ||
		!strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("err = %v, want the oversized result rejected", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "artifacts", "results")); err == nil && len(entries) > 0 {
		t.Fatalf("rejected result wrote %d file(s)", len(entries))
	}
	if _, _, err := SaveImmutableResult(dir, "sample/report", json.RawMessage(`{"value":"small"}`)); err != nil {
		t.Fatalf("SaveImmutableResult: %v", err)
	}
}

func TestReadArtifactRejectsChangedSnapshotDigest(t *testing.T) {
	dir := t.TempDir()
	ctx := WithSessionDir(context.Background(), dir)
	out, err := (SaveArtifactTool{}).Execute(ctx, mustMarshal(t, map[string]any{"filename": "report.md", "content": "old", "mode": "create"}))
	if err != nil {
		t.Fatal(err)
	}
	var old ArtifactRef
	if err := json.Unmarshal([]byte(out), &old); err != nil {
		t.Fatal(err)
	}
	if old.SHA256 == "" {
		t.Fatal("artifact ref missing SHA-256")
	}
	if _, err := (SaveArtifactTool{}).Execute(ctx, mustMarshal(t, map[string]any{"filename": "report.md", "content": "new", "mode": "overwrite"})); err != nil {
		t.Fatal(err)
	}
	if _, err := (ReadArtifactTool{}).Execute(ctx, mustMarshal(t, map[string]any{"path": old.RelPath, "expected_sha256": old.SHA256})); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadArtifactBoundsHugeSingleLine(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.ToSlash(filepath.Join("artifacts", "huge.txt"))
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(strings.Repeat("x", MaxOutputBytes*4)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := (ReadArtifactTool{}).Execute(WithSessionDir(context.Background(), dir), mustMarshal(t, map[string]any{"path": rel}))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxOutputBytes+256 || !strings.Contains(out, "truncated=budget") {
		t.Fatalf("bounded output len=%d header=%q", len(out), strings.SplitN(out, "\n", 2)[0])
	}
}

func TestReadArtifactKeepsOversizedRequestedLineBeforeLaterLines(t *testing.T) {
	dir := t.TempDir()
	rel := filepath.ToSlash(filepath.Join("artifacts", "oversized-first-line.txt"))
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	firstLine := strings.Repeat("x", MaxOutputBytes*2)
	if err := os.WriteFile(abs, []byte(firstLine+"\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := (ReadArtifactTool{}).Execute(WithSessionDir(context.Background(), dir), mustMarshal(t, map[string]any{"path": rel}))
	if err != nil {
		t.Fatal(err)
	}
	header, body, _ := strings.Cut(out, "\n")
	if !strings.Contains(header, "lines=1-1 total=2") || !strings.Contains(header, "truncated=budget") {
		t.Fatalf("header = %q", header)
	}
	if body == "" || strings.Contains(body, "second") || !strings.HasPrefix(firstLine, strings.TrimSuffix(body, "\n")) {
		t.Fatalf("returned body does not contain only the first-line prefix: %q", body)
	}

	// Offset is 1-based: page 2 starts at line 2, the second line.
	offset, limit := 2, 1
	page, err := readArtifactPage(abs, &offset, &limit)
	if err != nil {
		t.Fatal(err)
	}
	if page.StartLine != 2 || page.EndLine != 2 || page.TotalLines != 2 || page.Truncated || len(page.Lines) != 1 || page.Lines[0] != "second" {
		t.Fatalf("second page = %#v", page)
	}
}

func TestReadArtifactRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is platform-specific")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(dir, "artifacts")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(artifactDir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	_, err := (ReadArtifactTool{}).Execute(WithSessionDir(context.Background(), dir), mustMarshal(t, map[string]any{"path": "artifacts/link.txt"}))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadArtifactRejectsSymlinkArtifactsRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is platform-specific")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "artifacts")); err != nil {
		t.Fatal(err)
	}
	_, err := (ReadArtifactTool{}).Execute(WithSessionDir(context.Background(), dir), mustMarshal(t, map[string]any{"path": "artifacts/secret.txt"}))
	if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateArtifactRefsChecksSnapshotMetadata(t *testing.T) {
	dir := t.TempDir()
	ctx := WithSessionDir(context.Background(), dir)
	out, err := (SaveArtifactTool{}).Execute(ctx, mustMarshal(t, map[string]any{"filename": "report.md", "content": "body"}))
	if err != nil {
		t.Fatal(err)
	}
	var ref ArtifactRef
	if err := json.Unmarshal([]byte(out), &ref); err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateArtifactRefs(dir, []ArtifactRef{ref})
	if err != nil || len(validated) != 1 || validated[0].SHA256 != ref.SHA256 {
		t.Fatalf("validated = %#v error=%v", validated, err)
	}
	ref.SizeBytes++
	if _, err := ValidateArtifactRefs(dir, []ArtifactRef{ref}); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestSaveArtifactResultModeStoresImmutableResultRef(t *testing.T) {
	dir := t.TempDir()
	ctx := WithSessionDir(context.Background(), dir)
	args := mustMarshal(t, map[string]any{"result_type": "application/vnd.test+json", "result": map[string]any{"b": 2, "a": 1}})
	firstOut, err := (SaveArtifactTool{}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute first: %v", err)
	}
	var ref ResultRef
	if err := json.Unmarshal([]byte(firstOut), &ref); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.ID, "sha256-") || ref.SHA256 == "" || ref.ResultType != "application/vnd.test+json" || !strings.HasPrefix(ref.RelPath, "artifacts/results/") || ref.SizeBytes == 0 {
		t.Fatalf("ref = %#v", ref)
	}
	if ref.ID != resultRefID(ref.ResultType, ref.SHA256) {
		t.Fatalf("ref id %q does not match result_type and sha256", ref.ID)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref.RelPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"a\":1,\"b\":2}\n" {
		t.Fatalf("canonical result = %q", data)
	}
	// Same result_type and content reuse the same immutable object.
	secondOut, err := (SaveArtifactTool{}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute second: %v", err)
	}
	if secondOut != firstOut {
		t.Fatalf("idempotent refs differ: %s != %s", firstOut, secondOut)
	}
}

func TestSaveArtifactResultModeRejectsNonObjectResult(t *testing.T) {
	ctx := WithSessionDir(context.Background(), t.TempDir())
	for _, raw := range []string{
		`{"result_type":"test","result":[1,2]}`,
		`{"result_type":"test","result":"text"}`,
		`{"result_type":"test","result":null}`,
	} {
		if _, err := (SaveArtifactTool{}).Execute(ctx, json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
			t.Fatalf("input %s error = %v", raw, err)
		}
	}
}

func TestSaveArtifactResultModeIdentityIncludesResultType(t *testing.T) {
	dir := t.TempDir()
	ctx := WithSessionDir(context.Background(), dir)
	first, err := (SaveArtifactTool{}).Execute(ctx, json.RawMessage(`{"result_type":"type/a","result":{"value":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := (SaveArtifactTool{}).Execute(ctx, json.RawMessage(`{"result_type":"type/b","result":{"value":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("different result types produced same ref: %s", first)
	}
}

func TestSaveArtifactResultModeRequiresResultTypeAndResultPair(t *testing.T) {
	ctx := WithSessionDir(context.Background(), t.TempDir())
	if _, err := (SaveArtifactTool{}).Execute(ctx, json.RawMessage(`{"result_type":"type/test"}`)); err == nil || !strings.Contains(err.Error(), "result is required when result_type is provided") {
		t.Fatalf("missing result error = %v", err)
	}
	if _, err := (SaveArtifactTool{}).Execute(ctx, json.RawMessage(`{"result":{"value":1}}`)); err == nil || !strings.Contains(err.Error(), "result_type is required when result is provided") {
		t.Fatalf("missing result_type error = %v", err)
	}
}

func TestSaveArtifactResultModeRejectsFileGroupMix(t *testing.T) {
	ctx := WithSessionDir(context.Background(), t.TempDir())
	for _, raw := range []string{
		`{"result_type":"type/test","result":{"value":1},"filename":"report.md","content":"body"}`,
		`{"result_type":"type/test","result":{"value":1},"filename":"report.md","content":"body","mode":"create"}`,
		`{"result_type":"type/test","result":{"value":1},"mode":"overwrite"}`,
	} {
		if _, err := (SaveArtifactTool{}).Execute(ctx, json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), "cannot be combined with filename, content, or mode") {
			t.Fatalf("input %s error = %v", raw, err)
		}
	}
}

func TestValidateResultRefRejectsTamperedIdentity(t *testing.T) {
	dir := t.TempDir()
	ref, _, err := SaveImmutableResult(dir, "type/test", json.RawMessage(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	ref.ID = "sha256-" + strings.Repeat("0", 64)
	if _, err := ValidateResultRef(dir, ref, "type/test"); err == nil || !strings.Contains(err.Error(), "id does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateResultRefRejectsNonCanonicalArtifactPath(t *testing.T) {
	dir := t.TempDir()
	ref, canonical, err := SaveImmutableResult(dir, "type/test", json.RawMessage(`{"value":1}`))
	if err != nil {
		t.Fatal(err)
	}
	mutablePath := filepath.Join(dir, "artifacts", "mutable.json")
	if err := os.WriteFile(mutablePath, append(canonical, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ref.RelPath = "artifacts/results/../mutable.json"
	if _, err := ValidateResultRef(dir, ref, "type/test"); err == nil || !strings.Contains(err.Error(), "rel_path does not match id") {
		t.Fatalf("error = %v", err)
	}
}
