package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/imagegen"
)

type imageBackendFixture struct {
	target        imagegen.Target
	image         imagegen.Image
	calls         atomic.Int32
	failure       error
	checkError    error
	run           func(context.Context, imagegen.Request, func() error) (*imagegen.Result, error)
	downloads     atomic.Int32
	downloadError error
	download      func(context.Context, string) (imagegen.Image, error)
	url           string
}

func (b *imageBackendFixture) Target() imagegen.Target                       { return b.target }
func (*imageBackendFixture) Timeout() time.Duration                          { return time.Minute }
func (b *imageBackendFixture) Check(context.Context, imagegen.Request) error { return b.checkError }
func (b *imageBackendFixture) Run(ctx context.Context, r imagegen.Request, before func() error) (*imagegen.Result, error) {
	b.calls.Add(1)
	if b.run != nil {
		return b.run(ctx, r, before)
	}
	if err := before(); err != nil {
		return nil, err
	}
	if b.failure != nil {
		return nil, b.failure
	}
	candidate := imagegen.Candidate{Image: b.image, RevisedPrompt: "A tree"}
	if b.url != "" {
		candidate.Image = imagegen.Image{}
		candidate.URL = b.url
	}
	return &imagegen.Result{Images: []imagegen.Candidate{candidate}, RequestID: "request-1", Usage: map[string]any{"input_tokens": 3}}, nil
}
func (b *imageBackendFixture) Download(ctx context.Context, url string) (imagegen.Image, error) {
	b.downloads.Add(1)
	if b.download != nil {
		return b.download(ctx, url)
	}
	return b.image, b.downloadError
}

func imageFixture(t *testing.T) *imageBackendFixture {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 3))
	img.Set(0, 0, color.NRGBA{G: 255, A: 80})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	original, err := imagegen.ValidateImage(t.Context(), buf.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1", "")
	if err != nil {
		t.Fatal(err)
	}
	return &imageBackendFixture{image: original, target: target}
}

func imageToolContext(t *testing.T, id string) (context.Context, string, *ImageCollector) {
	t.Helper()
	dir := t.TempDir()
	sink := &ImageCollector{}
	ctx := WithSessionDir(t.Context(), dir)
	ctx = WithToolCallID(ctx, id)
	ctx = WithAgentID(ctx, "agent-1")
	ctx = WithTurnID(ctx, 3)
	ctx = WithImageSink(ctx, sink)
	return ctx, dir, sink
}

func TestGenerateImageOriginalRecoveryAndOutput(t *testing.T) {
	backend := imageFixture(t)
	ctx, dir, sink := imageToolContext(t, "call-1")
	workspace := t.TempDir()
	tool := &GenerateImageTool{Backend: backend, BaseDir: workspace}
	raw := json.RawMessage(`{"prompt":"A tree","operation":"generate","output_path":"tree.png","background":"transparent"}`)
	output, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	var summary ImageGenerationSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.State != imagegen.StateSaved || len(summary.Images) != 1 || summary.BillingState != "unknown" || summary.OutputPath != filepath.Join(workspace, "tree.png") {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.Images[0].Path != "" {
		t.Fatal("canonical summary contains an absolute local path")
	}
	original, err := ReadGeneratedOriginal(t.Context(), dir, summary.Images[0].Reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.Data, backend.image.Data) {
		t.Fatal("original bytes changed")
	}
	copyData, err := os.ReadFile(summary.OutputPath)
	if err != nil || !bytes.Equal(copyData, original.Data) {
		t.Fatal("workspace copy mismatch")
	}
	parts := sink.Drain()
	if len(parts) != 1 || len(parts[0].Data) != 0 || parts[0].ArtifactID == "" || parts[0].ImagePath == "" {
		t.Fatalf("parts=%+v", parts)
	}
	var delivered []imagegen.Image
	err = VisitGeneratedOriginals(t.Context(), output, parts, func(_ GeneratedImage, img imagegen.Image) error { delivered = append(delivered, img); return nil })
	if err != nil || len(delivered) != 1 || !bytes.Equal(delivered[0].Data, original.Data) {
		t.Fatal(err)
	}
	manifest, err := ResolveImageArtifactPath(dir, summary.Manifest, workspace)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("b64_json")) || bytes.Contains(data, []byte("data:image")) {
		t.Fatal("original base64 in manifest")
	}
	// The saved result survives a duplicate call even though its copy now exists.
	if repeated, err := tool.Execute(ctx, raw); err != nil || repeated != output || backend.calls.Load() != 1 {
		t.Fatal("saved output_path call was not reused", err)
	}
	// A restored invocation without a workspace copy reuses the same original.
	ctx2 := WithToolCallID(WithSessionDir(t.Context(), dir), "call-2")
	out2, err := tool.Execute(ctx2, json.RawMessage(`{"prompt":"A tree","operation":"generate"}`))
	if err != nil {
		t.Fatal(err)
	}
	calls := backend.calls.Load()
	repeated, err := tool.Execute(ctx2, json.RawMessage(`{"prompt":"A tree","operation":"generate"}`))
	if err != nil || repeated != out2 || backend.calls.Load() != calls {
		t.Fatal("restored call regenerated")
	}
	if _, err := tool.Execute(ctx2, json.RawMessage(`{"prompt":"A lake","operation":"generate"}`)); err == nil {
		t.Fatal("operation rebound to different request")
	}
	if err := os.Remove(filepath.Join(dir, summary.Images[0].RelPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx2, json.RawMessage(`{"prompt":"A tree","operation":"generate"}`)); err == nil || backend.calls.Load() != calls {
		t.Fatal("missing original regenerated")
	}
}

func TestGenerateImagePreflightAndReferenceSnapshot(t *testing.T) {
	backend := imageFixture(t)
	workspace := t.TempDir()
	ctx, _, _ := imageToolContext(t, "call-1")
	tool := &GenerateImageTool{Backend: backend, BaseDir: workspace}
	if err := os.WriteFile(filepath.Join(workspace, "reference.png"), backend.image.Data, 0600); err != nil {
		t.Fatal(err)
	}
	backend.run = func(ctx context.Context, r imagegen.Request, before func() error) (*imagegen.Result, error) {
		if err := os.WriteFile(filepath.Join(workspace, "reference.png"), []byte("replaced"), 0600); err != nil {
			return nil, err
		}
		if len(r.References) != 1 || !bytes.Equal(r.References[0].Data, backend.image.Data) {
			t.Error("reference changed after preflight")
		}
		if err := before(); err != nil {
			return nil, err
		}
		return &imagegen.Result{Images: []imagegen.Candidate{{Image: backend.image}}}, nil
	}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"Change color","operation":"edit","reference_images":["reference.png"]}`)); err != nil {
		t.Fatal(err)
	}
	calls := backend.calls.Load()
	for _, raw := range []string{`{"prompt":"tree","operation":"edit"}`, `{"prompt":"tree","operation":"generate","size":"bad"}`, `{"prompt":"tree","operation":"generate","quality":"bad"}`, `{"prompt":"tree","operation":"edit","reference_images":["missing.png"]}`, `{"prompt":"tree","operation":"generate","output_path":"../outside.png"}`, `{"prompt":"tree","operation":"generate","output_path":"missing/tree.png"}`, `{"prompt":"tree","operation":"generate","unknown":true}`, `{bad`} {
		if _, err := tool.Execute(ctx, json.RawMessage(raw)); err == nil {
			t.Fatal("invalid preflight accepted", raw)
		}
	}
	if backend.calls.Load() != calls {
		t.Fatal("preflight sent paid request")
	}
	backend.checkError = fmt.Errorf("permission revoked")
	if _, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"tree","operation":"generate"}`)); err == nil {
		t.Fatal("revoked permission accepted")
	}
	hidden := &GenerateImageTool{}
	if hidden.IsAvailable() {
		t.Fatal("unconfigured tool visible")
	}
	if _, err := hidden.Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("unconfigured tool executed")
	}
	if tool.WithBaseDir("other").(*GenerateImageTool).BaseDir != "other" || tool.BaseDir != workspace {
		t.Fatal("base dir clone mutated parent")
	}
	if tool.Name() != NameGenerateImage || tool.IsReadOnly() || tool.Description() == "" {
		t.Fatal("wrong tool metadata")
	}
	if _, err := tool.Execute(t.Context(), json.RawMessage(`{"prompt":"tree","operation":"generate"}`)); err == nil {
		t.Fatal("missing persistent session accepted")
	}
}

func TestGenerateImageUnknownAndDownloadFailureNoReplay(t *testing.T) {
	for _, state := range []string{imagegen.StateUnknown, imagegen.StateRejected, imagegen.StateCompleted, imagegen.StateNotSent} {
		t.Run(state, func(t *testing.T) {
			backend := imageFixture(t)
			backend.failure = &imagegen.Failure{State: state, Cause: fmt.Errorf("sample failure")}
			ctx, dir, _ := imageToolContext(t, "call-1")
			tool := &GenerateImageTool{Backend: backend, BaseDir: t.TempDir()}
			raw := json.RawMessage(`{"prompt":"tree","operation":"generate"}`)
			if _, err := tool.Execute(ctx, raw); err == nil {
				t.Fatal("failed request reported success")
			} else if failure, ok := errors.AsType[*imagegen.Failure](err); !ok || failure.State != state {
				t.Fatal("request outcome was lost", err)
			}
			if _, err := tool.Execute(ctx, raw); err == nil || backend.calls.Load() != 1 {
				t.Fatal("failed generation replayed")
			}
			entries, err := os.ReadDir(filepath.Join(dir, "images", "operations"))
			if err != nil || len(entries) != 3 {
				t.Fatal(err)
			}
		})
	}
	backend := imageFixture(t)
	backend.url = "https://example.invalid/image"
	backend.downloadError = fmt.Errorf("download unavailable")
	ctx, dir, _ := imageToolContext(t, "url-call")
	tool := &GenerateImageTool{Backend: backend, BaseDir: t.TempDir()}
	raw := json.RawMessage(`{"prompt":"tree","operation":"generate"}`)
	if _, err := tool.Execute(ctx, raw); err == nil || !strings.Contains(err.Error(), "image generated") {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "images", "operations", "*.json"))
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(backend.url)) || !bytes.Contains(data, []byte(imagegen.StateCompleted)) {
		t.Fatal("receipt lost")
	}
	if _, err := tool.Execute(ctx, raw); err == nil || backend.calls.Load() != 1 || backend.downloads.Load() != 2 {
		t.Fatal("download failure regenerated")
	}
}

func TestGenerateImagePublicationRaceAndClaim(t *testing.T) {
	backend := imageFixture(t)
	workspace := t.TempDir()
	ctx, _, _ := imageToolContext(t, "call-1")
	tool := &GenerateImageTool{Backend: backend, BaseDir: workspace}
	backend.run = func(ctx context.Context, r imagegen.Request, before func() error) (*imagegen.Result, error) {
		if err := before(); err != nil {
			return nil, err
		}
		if err := os.Symlink(filepath.Join(workspace, "protected.png"), filepath.Join(workspace, "tree.png")); err != nil {
			return nil, err
		}
		return &imagegen.Result{Images: []imagegen.Candidate{{Image: backend.image}, {Image: backend.image}}}, nil
	}
	if err := os.WriteFile(filepath.Join(workspace, "protected.png"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"tree","operation":"generate","output_path":"tree.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	var summary ImageGenerationSummary
	_ = json.Unmarshal([]byte(output), &summary)
	if len(summary.Images) != 2 || len(summary.Warnings) != 2 || summary.OutputPath != "" {
		t.Fatalf("summary=%+v", summary)
	}
	data, _ := os.ReadFile(filepath.Join(workspace, "protected.png"))
	if string(data) != "protected" {
		t.Fatal("symlink overwrote protected target")
	}
	backend.run = nil
	ctx2, _, _ := imageToolContext(t, "concurrent-call")
	var wg sync.WaitGroup
	var success atomic.Int32
	calls := backend.calls.Load()
	for range 2 {
		wg.Go(func() {
			if _, err := tool.Execute(ctx2, json.RawMessage(`{"prompt":"tree","operation":"generate"}`)); err == nil {
				success.Add(1)
			}
		})
	}
	wg.Wait()
	if backend.calls.Load() != calls+1 || success.Load() < 1 {
		t.Fatal("same operation dispatched twice")
	}
}

func TestImageArtifactConfinementAndSchema(t *testing.T) {
	backend := imageFixture(t)
	dir := t.TempDir()
	ref, err := SaveImageArtifact(t.Context(), dir, backend.image)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", "/etc/passwd", "artifacts/report.json", "images/sha256-short.png", "images/../main.jsonl"} {
		if _, err := ReadGeneratedOriginal(t.Context(), dir, path); err == nil {
			t.Fatal("invalid image artifact accepted")
		}
	}
	path := filepath.Join(dir, ref.RelPath)
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGeneratedOriginal(t.Context(), dir, ImageArtifactPrefix+ref.RelPath); err == nil {
		t.Fatal("corrupt original accepted")
	}
	tool := &GenerateImageTool{Backend: backend}
	props := tool.Parameters()["properties"].(map[string]any)
	if props["reference_images"] == nil || props["background"] == nil {
		t.Fatal("supported fields hidden")
	}
	backend.target, _ = imagegen.ResolveTarget(imagegen.PresetCompatible, "sample", "https://example.invalid/v1")
	props = tool.Parameters()["properties"].(map[string]any)
	if props["reference_images"] != nil || props["background"] != nil || props["quality"] != nil {
		t.Fatal("unsupported capability exposed")
	}
}

func TestGenerateImageRejectsSanitizedParametersBeforeCharging(t *testing.T) {
	backend := imageFixture(t)
	backend.target, _ = imagegen.ResolveTarget(imagegen.PresetCompatible, "sample-image", "https://example.invalid/v1")
	tool := &GenerateImageTool{Backend: backend}
	for _, raw := range []string{`{"prompt":"tree","operation":"generate","background":"transparent"}`, `{"prompt":"tree","operation":"generate","reference_images":["image.png"]}`, `{"prompt":"tree","operation":"generate","size":null}`, `{"prompt":"tree","prompt":"lake","operation":"generate"}`} {
		if _, _, _, err := SanitizeUnknownArgsWithDiagnostics(tool, json.RawMessage(raw)); err == nil {
			t.Fatal("paid operation silently sanitized arguments", raw)
		}
	}
}

func TestGenerateImageReportsFormatMismatchAndPreservesOriginal(t *testing.T) {
	b := imageFixture(t)
	ctx, dir, _ := imageToolContext(t, "format-call")
	tool := &GenerateImageTool{Backend: b}
	output, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"A tree","operation":"generate","output_format":"jpeg"}`))
	if err != nil {
		t.Fatal(err)
	}
	var summary ImageGenerationSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0], "image/png") || summary.Images[0].MimeType != "image/png" {
		t.Fatal("provider format mismatch was hidden")
	}
	img, err := ReadGeneratedOriginal(t.Context(), dir, summary.Images[0].Reference)
	if err != nil || !bytes.Equal(img.Data, b.image.Data) || b.calls.Load() != 1 {
		t.Fatal("format mismatch converted or regenerated original", err)
	}
}

func TestGenerateImagePublishesAllOriginalAttachments(t *testing.T) {
	backend := imageFixture(t)
	ctx, dir, sink := imageToolContext(t, "all-images")
	candidates := make([]imagegen.Candidate, imagegen.MaxImages)
	for i := range candidates {
		bitmap := image.NewNRGBA(image.Rect(0, 0, 4, 3))
		bitmap.Set(0, 0, color.NRGBA{R: uint8(i * 40), A: 255})
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, bitmap); err != nil {
			t.Fatal(err)
		}
		img, err := imagegen.ValidateImage(t.Context(), encoded.Bytes(), "")
		if err != nil {
			t.Fatal(err)
		}
		candidates[i] = imagegen.Candidate{Image: img}
	}
	backend.run = func(_ context.Context, _ imagegen.Request, before func() error) (*imagegen.Result, error) {
		if err := before(); err != nil {
			return nil, err
		}
		return &imagegen.Result{Images: candidates}, nil
	}
	tool := &GenerateImageTool{Backend: backend}
	raw := json.RawMessage(`{"prompt":"A landscape","operation":"generate"}`)
	output, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	var summary ImageGenerationSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatal(err)
	}
	checkParts := func() {
		t.Helper()
		parts := sink.Drain()
		if len(parts) != len(candidates) {
			t.Fatalf("attachments=%d, want %d", len(parts), len(candidates))
		}
		for i, part := range parts {
			original := summary.Images[i]
			if part.ArtifactID != original.ID || part.ImagePath != filepath.Join(dir, filepath.FromSlash(original.RelPath)) || part.MimeType != original.MimeType || len(part.Data) != 0 {
				t.Fatalf("attachment %d does not identify its original: %+v", i, part)
			}
			img, err := ReadGeneratedOriginal(t.Context(), dir, original.Reference)
			if err != nil || !bytes.Equal(img.Data, candidates[i].Data) {
				t.Fatalf("original %d changed: %v", i, err)
			}
		}
	}
	checkParts()
	// Reusing the same durable call publishes the same saved gallery.
	if repeated, err := tool.Execute(ctx, raw); err != nil || repeated != output {
		t.Fatal("saved gallery not reused", err)
	}
	checkParts()
	if backend.calls.Load() != 1 {
		t.Fatal("gallery reuse regenerated images")
	}
}

func TestImageOptionalDefaultsAndNotSentDiagnostic(t *testing.T) {
	backend := imageFixture(t)
	backend.target.Provider = "sample"
	backend.run = func(_ context.Context, r imagegen.Request, _ func() error) (*imagegen.Result, error) {
		if r.Size != "" || r.Quality != "" || r.AspectRatio != "" || r.Background != "" || r.OutputFormat != "" {
			t.Fatalf("unexpected defaults: %+v", r)
		}
		return nil, &imagegen.Failure{State: imagegen.StateNotSent, Details: imagegen.FailureDetails{Category: imagegen.FailureRateLimit, RetryAfterSeconds: new(7.0)}, Cause: fmt.Errorf("all keys cooling")}
	}
	tool := &GenerateImageTool{Backend: backend}
	schema, err := json.Marshal(tool.Parameters())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(schema), `"1K"`) {
		t.Fatal("single target declared Gemini size")
	}
	ctx, dir, _ := imageToolContext(t, "default-input")
	if _, err = tool.Execute(ctx, json.RawMessage(`{"prompt":"A tree","operation":"generate"}`)); err == nil {
		t.Fatal("expected cooldown failure")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "images", "operations", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var op imageOperation
	if err = json.Unmarshal(data, &op); err != nil {
		t.Fatal(err)
	}
	if op.Target != "sample/gpt-image-1" || op.State != imagegen.StateNotSent || op.Failure == nil || op.Failure.RetryAfter() != 7*time.Second {
		t.Fatalf("operation=%+v", op)
	}
}

func TestImageHTTPFailureDiagnosticsPersistInManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "request-1")
		w.Header().Set("cf-ray", "abcd-TEST")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_size","type":"invalid_request_error","param":"size","message":"Invalid size value; expected 'auto'."}}`))
	}))
	defer server.Close()
	backend := imageFixture(t)
	backend.target.BaseURL = server.URL + "/v1/images"
	backend.run = func(ctx context.Context, r imagegen.Request, before func() error) (*imagegen.Result, error) {
		return imagegen.Execute(ctx, server.Client(), backend.target, r, "sample-credential", before)
	}
	ctx, dir, _ := imageToolContext(t, "request-diagnostics")
	tool := &GenerateImageTool{Backend: backend}
	if _, err := tool.Execute(ctx, json.RawMessage(`{"operation":"generate","prompt":"A tree"}`)); err == nil {
		t.Fatal("expected rejection")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "images", "operations", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var op imageOperation
	if err := json.Unmarshal(data, &op); err != nil {
		t.Fatal(err)
	}
	if op.RequestID != "request-1" || op.Failure == nil || op.Failure.Code != "invalid_size" || op.Failure.Param != "size" || !strings.Contains(op.Failure.Message, "Invalid size value") || op.Failure.Request == nil || op.Failure.Request.Parameters["size"] != "auto" || op.Failure.Response == nil || op.Failure.Response.CFRay != "abcd-TEST" {
		t.Fatalf("manifest failure=%+v", op.Failure)
	}
	if strings.Contains(string(data), "sample-credential") || strings.Contains(string(data), "A tree") {
		t.Fatal("request content leaked into manifest")
	}
}

func TestGenerateImageRecoversPublishedOutput(t *testing.T) {
	for _, scenario := range []string{"same", "different", "symlink", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			backend := imageFixture(t)
			ctx, dir, _ := imageToolContext(t, "recover-output")
			workspace := t.TempDir()
			tool := &GenerateImageTool{Backend: backend, BaseDir: workspace}
			raw := json.RawMessage(`{"prompt":"A tree","operation":"generate","output_path":"tree.png"}`)
			output, err := tool.Execute(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			var summary ImageGenerationSummary
			if err := json.Unmarshal([]byte(output), &summary); err != nil {
				t.Fatal(err)
			}
			manifest, err := ResolveImageArtifactPath(dir, summary.Manifest, workspace)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var op imageOperation
			if err := json.Unmarshal(data, &op); err != nil {
				t.Fatal(err)
			}
			// The workspace copy exists, but the final receipt was not persisted.
			op.State, op.OutputPath = imagegen.StateCompleted, ""
			if err := writeImageOperation(dir, filepath.Join(dir, "images", "operations", summary.OperationID+".json"), op); err != nil {
				t.Fatal(err)
			}
			expected := bytes.Clone(backend.image.Data)
			switch scenario {
			case "different":
				expected[0] ^= 1
				if err := os.WriteFile(summary.OutputPath, expected, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "directory":
				if err := os.Remove(summary.OutputPath); err != nil {
					t.Fatal(err)
				}
				if scenario == "directory" {
					if err := os.Mkdir(summary.OutputPath, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					target := filepath.Join(workspace, "other.png")
					if err := os.WriteFile(target, expected, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, summary.OutputPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			output, err = tool.Execute(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			var restored ImageGenerationSummary
			if err := json.Unmarshal([]byte(output), &restored); err != nil {
				t.Fatal(err)
			}
			if backend.calls.Load() != 1 || restored.State != imagegen.StateSaved {
				t.Fatalf("regenerated or incomplete: %+v", restored)
			}
			if scenario == "same" {
				if restored.OutputPath != summary.OutputPath || len(restored.Warnings) != 0 {
					t.Fatalf("publication was not restored: %+v", restored)
				}
			} else {
				if restored.OutputPath != "" || len(restored.Warnings) != 1 {
					t.Fatalf("collision was accepted: %+v", restored)
				}
				info, err := os.Lstat(summary.OutputPath)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "symlink" && info.Mode()&os.ModeSymlink == 0 || scenario == "directory" && !info.IsDir() {
					t.Fatal("existing output replaced")
				}
			}
			if scenario != "directory" {
				got, err := os.ReadFile(summary.OutputPath)
				if err != nil || !bytes.Equal(got, expected) {
					t.Fatal("existing bytes replaced", err)
				}
			}
		})
	}
}

func TestGenerateImageDoesNotAdoptFreshOutputCollision(t *testing.T) {
	backend := imageFixture(t)
	ctx, _, _ := imageToolContext(t, "fresh-output")
	workspace := t.TempDir()
	backend.run = func(_ context.Context, _ imagegen.Request, before func() error) (*imagegen.Result, error) {
		if err := before(); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(workspace, "tree.png"), backend.image.Data, 0600); err != nil {
			return nil, err
		}
		return &imagegen.Result{Images: []imagegen.Candidate{{Image: backend.image}}}, nil
	}
	output, err := (&GenerateImageTool{Backend: backend, BaseDir: workspace}).Execute(ctx, json.RawMessage(`{"prompt":"A tree","operation":"generate","output_path":"tree.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	var summary ImageGenerationSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.OutputPath != "" || len(summary.Warnings) != 1 {
		t.Fatalf("fresh collision was adopted: %+v", summary)
	}
}
