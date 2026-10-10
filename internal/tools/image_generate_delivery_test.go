package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
)

func TestGenerateImageCompletesDownloadRetriesInOneCall(t *testing.T) {
	b := imageFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write(b.image.Data)
	}))
	defer server.Close()
	b.url = server.URL + "/image"
	b.download = func(ctx context.Context, url string) (imagegen.Image, error) {
		return imagegen.Download(ctx, server.Client(), url, func(string) error { return nil })
	}
	ctx, _, sink := imageToolContext(t, "image-call")
	tool := &GenerateImageTool{Backend: b, BaseDir: t.TempDir()}
	raw := json.RawMessage(`{"operation":"generate","prompt":"A landscape"}`)
	output, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	var summary ImageGenerationSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatal(err)
	}
	if b.calls.Load() != 1 || requests.Load() != 3 || summary.State != imagegen.StateSaved || len(sink.Drain()) != 1 || summary.Images[0].Path != "" {
		t.Fatalf("generation=%d downloads=%d summary=%+v", b.calls.Load(), requests.Load(), summary)
	}
	if repeated, err := tool.Execute(ctx, raw); err != nil || repeated != output || b.calls.Load() != 1 || requests.Load() != 3 {
		t.Fatal("same durable call generated or downloaded again", err)
	}
}

func TestGenerateImageRejectsRemovedRecoveryParameter(t *testing.T) {
	b := imageFixture(t)
	ctx, _, _ := imageToolContext(t, "image-call")
	tool := &GenerateImageTool{Backend: b}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"recover_from":"artifact:images/operations/example.json"}`)); err == nil || b.calls.Load() != 0 {
		t.Fatal("invalid arguments invoked generation")
	}
	encoded, err := json.Marshal(tool.Parameters())
	if err != nil || bytes.Contains(encoded, []byte("recover_from")) || bytes.Contains([]byte(tool.Description()), []byte("recover_from")) {
		t.Fatal("tool exposes a recovery operation")
	}
}

func TestGenerateImageSaveFailurePreservesCompletedOutcome(t *testing.T) {
	b := imageFixture(t)
	ctx, dir, sink := imageToolContext(t, "image-call")
	b.run = func(ctx context.Context, _ imagegen.Request, before func() error) (*imagegen.Result, error) {
		if err := before(); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "images", filepath.Base(imageArtifactRef(b.image).RelPath)), []byte("different data"), 0600); err != nil {
			return nil, err
		}
		return &imagegen.Result{Images: []imagegen.Candidate{{Image: b.image}}}, nil
	}
	tool := &GenerateImageTool{Backend: b}
	_, err := tool.Execute(ctx, json.RawMessage(`{"operation":"generate","prompt":"A landscape"}`))
	if _, ok := errors.AsType[*imageDeliveryFailure](err); !ok || b.calls.Load() != 1 || len(sink.Drain()) != 0 {
		t.Fatal("save failure lost the generated outcome or reported success", err)
	}
}

func TestGenerateImageDeduplicatesRestoredDurableTask(t *testing.T) {
	b := imageFixture(t)
	ctx, _, _ := imageToolContext(t, "durable-call")
	ctx = WithTaskID(ctx, "task-1")
	tool := &GenerateImageTool{Backend: b}
	raw := json.RawMessage(`{"prompt":"A tree","operation":"generate"}`)
	output, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	// Restoring the same task allocates a fresh runtime instance. Its durable
	// call still binds to the same saved operation and must not charge again.
	restored := WithAgentID(ctx, "restored-worker")
	if repeated, err := tool.Execute(restored, raw); err != nil || repeated != output || b.calls.Load() != 1 {
		t.Fatal("restored task regenerated a paid call", err)
	}
}

func TestGenerateImageRejectsSymlinkInputsAndPinsOutputDirectory(t *testing.T) {
	b := imageFixture(t)
	workspace := t.TempDir()
	ctx, _, _ := imageToolContext(t, "path-call")
	tool := &GenerateImageTool{Backend: b, BaseDir: workspace}
	protected := filepath.Join(workspace, "protected")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protected, "input.png"), b.image.Data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, filepath.Join(workspace, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, raw := range []string{`{"prompt":"tree","operation":"edit","reference_images":["alias/input.png"]}`, `{"prompt":"tree","operation":"generate","output_path":"alias/output.png"}`} {
		if _, err := tool.Execute(ctx, json.RawMessage(raw)); err == nil || b.calls.Load() != 0 {
			t.Fatal("symlink bypassed path checks")
		}
	}
	output := filepath.Join(workspace, "output")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := openImageDirectory(workspace, output)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(output, output+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, output); err != nil {
		t.Fatal(err)
	}
	if err := publishImageFile(t.Context(), root, "result.png", b.image.Data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(protected, "result.png")); !os.IsNotExist(err) {
		t.Fatal("publication followed a replaced output parent")
	}
}

func TestGenerateImageMovedOutputDirectoryDoesNotReportFalsePath(t *testing.T) {
	b := imageFixture(t)
	ctx, _, _ := imageToolContext(t, "output-move-call")
	workspace := t.TempDir()
	outputDir := filepath.Join(workspace, "output")
	if err := os.Mkdir(outputDir, 0700); err != nil {
		t.Fatal(err)
	}
	b.run = func(ctx context.Context, r imagegen.Request, before func() error) (*imagegen.Result, error) {
		if err := before(); err != nil {
			return nil, err
		}
		if err := os.Rename(outputDir, outputDir+"-moved"); err != nil {
			return nil, err
		}
		if err := os.Mkdir(outputDir, 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(outputDir, "tree.png"), []byte("unrelated"), 0600); err != nil {
			return nil, err
		}
		return &imagegen.Result{Images: []imagegen.Candidate{{Image: b.image}}}, nil
	}
	tool := &GenerateImageTool{Backend: b, BaseDir: workspace}
	output, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"A tree","operation":"generate","output_path":"output/tree.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	var summary ImageGenerationSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.OutputPath != "" || len(summary.Warnings) == 0 {
		t.Fatal("changed output path reported as delivered", summary)
	}
	data, err := os.ReadFile(filepath.Join(outputDir, "tree.png"))
	if err != nil || string(data) != "unrelated" {
		t.Fatal("replacement directory modified", err)
	}
	data, err = os.ReadFile(filepath.Join(outputDir+"-moved", "tree.png"))
	if err != nil || !bytes.Equal(data, b.image.Data) {
		t.Fatal("pinned directory copy missing", err)
	}
}
