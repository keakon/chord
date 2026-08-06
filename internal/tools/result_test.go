package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveResultCreatesImmutableContentAddressedRef(t *testing.T) {
	dir := t.TempDir()
	ctx := WithSessionDir(context.Background(), dir)
	args := mustMarshal(t, map[string]any{"result_type": "application/vnd.test+json", "result": map[string]any{"b": 2, "a": 1}})
	firstOut, err := (SaveResultTool{}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute first: %v", err)
	}
	secondOut, err := (SaveResultTool{}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute second: %v", err)
	}
	if firstOut != secondOut {
		t.Fatalf("refs differ: %s != %s", firstOut, secondOut)
	}
	var ref ResultRef
	if err := json.Unmarshal([]byte(firstOut), &ref); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.ID, "sha256-") || ref.SHA256 == "" || !strings.HasPrefix(ref.RelPath, "artifacts/results/") || ref.SizeBytes == 0 {
		t.Fatalf("ref = %#v", ref)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref.RelPath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"a\":1,\"b\":2}\n" {
		t.Fatalf("canonical result = %q", data)
	}
}

func TestSaveResultRejectsNonObject(t *testing.T) {
	ctx := WithSessionDir(context.Background(), t.TempDir())
	for _, raw := range []string{
		`{"result_type":"test","result":[1,2]}`,
		`{"result_type":"test","result":"text"}`,
		`{"result_type":"test","result":null}`,
	} {
		if _, err := (SaveResultTool{}).Execute(ctx, json.RawMessage(raw)); err == nil {
			t.Fatalf("input %s succeeded", raw)
		}
	}
}

func TestSaveResultIdentityIncludesResultType(t *testing.T) {
	dir := t.TempDir()
	ctx := WithSessionDir(context.Background(), dir)
	first, err := (SaveResultTool{}).Execute(ctx, json.RawMessage(`{"result_type":"type/a","result":{"value":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := (SaveResultTool{}).Execute(ctx, json.RawMessage(`{"result_type":"type/b","result":{"value":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("different result types produced same ref: %s", first)
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
