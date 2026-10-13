package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPresentAtomicBatchAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	sink := &DeliveryCollector{}
	ctx := WithDeliveryCollector(WithSessionDir(context.Background(), t.TempDir()), sink)
	tool := PresentTool{BaseDir: dir}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"files":[{"path":"report.md"},{"path":"missing"}]}`)); err == nil {
		t.Fatal("missing file accepted")
	}
	if len(sink.Drain()) != 0 {
		t.Fatal("partial delivery leaked")
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"files":[{"path":"report.md","description":"first"},{"path":"./report.md","description":"last"}]}`)); err != nil {
		t.Fatal(err)
	}
	files := sink.Drain()
	expected, _ := filepath.EvalSymlinks(filepath.Join(dir, "report.md"))
	if len(files) != 1 || files[0].Description != "last" || files[0].Locator.Workspace.Path != expected {
		t.Fatalf("bad delivery: %#v", files)
	}
}
func TestPresentStrictInputs(t *testing.T) {
	for _, raw := range []string{`{}`, `{"files":[]}`, `{"files":[{}]}`, `{"files":[{"path":"x","artifact":{}}]}`, `{"files":[{"artifact":{"rel_path":"artifacts/x","sha256":"00"}}]}`, `{"files":[{"path":"x","command":"open"}]}`, `{"files":[{"path":"x"}],"id":"model-controlled"}`} {
		if _, err := ParsePresentArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if (PresentTool{}).IsReadOnly() {
		t.Fatal("present is not read only")
	}
	if _, err := (PresentTool{}).Execute(context.Background(), json.RawMessage(`{"files":[{"path":"x"}]}`)); err == nil {
		t.Fatal("nonpersistent call accepted")
	}
}
