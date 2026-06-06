package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubImageCapability is a test double for ViewImageCapability.
type stubImageCapability struct {
	image bool
}

func (s stubImageCapability) SupportsViewImageTool() bool {
	return s.image
}

func writeTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

func writeTestJPEG(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write jpeg: %v", err)
	}
	return path
}

func TestViewImageToolMetadata(t *testing.T) {
	tool := NewViewImageTool(stubImageCapability{image: true})
	if tool.Name() != NameViewImage {
		t.Fatalf("Name() = %q, want %q", tool.Name(), NameViewImage)
	}
	if !tool.IsReadOnly() {
		t.Fatalf("IsReadOnly() = false, want true")
	}
	desc := tool.Description()
	for _, want := range []string{"PNG or JPEG", "may be sent to a remote provider"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
}

func TestViewImageToolIsAvailable(t *testing.T) {
	cases := []struct {
		name string
		tool *ViewImageTool
		want bool
	}{
		{name: "nil capability", tool: NewViewImageTool(nil), want: false},
		{name: "view image unsupported", tool: NewViewImageTool(stubImageCapability{image: false}), want: false},
		{name: "view image supported", tool: NewViewImageTool(stubImageCapability{image: true}), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tool.IsAvailable(); got != tc.want {
				t.Fatalf("IsAvailable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestViewImageToolExecuteSuccessPNG(t *testing.T) {
	dir := t.TempDir()
	path := writeTestPNG(t, dir, "shot.png")

	tool := NewViewImageTool(stubImageCapability{image: true})
	sink := &ImageCollector{}
	ctx := WithImageSink(context.Background(), sink)

	args, _ := json.Marshal(map[string]any{"path": path, "label": "screenshot"})
	out, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(out, "screenshot") {
		t.Fatalf("Execute() output %q does not contain label", out)
	}

	parts := sink.Drain()
	if len(parts) != 1 {
		t.Fatalf("sink got %d parts, want 1", len(parts))
	}
	p := parts[0]
	if p.Type != "image" {
		t.Fatalf("part Type = %q, want image", p.Type)
	}
	if p.MimeType != "image/png" && p.MimeType != "image/jpeg" {
		t.Fatalf("part MimeType = %q, want png or jpeg", p.MimeType)
	}
	if len(p.Data) == 0 {
		t.Fatalf("part Data is empty")
	}
	if p.ImagePath != "" {
		t.Fatalf("part ImagePath = %q, want empty (persisted by recovery layer)", p.ImagePath)
	}
	if p.FileName != "shot.png" {
		t.Fatalf("part FileName = %q, want shot.png", p.FileName)
	}
}

func TestViewImageToolExecuteSuccessJPEGLabelDefaultsToBasename(t *testing.T) {
	dir := t.TempDir()
	path := writeTestJPEG(t, dir, "diagram.jpg")

	tool := NewViewImageTool(stubImageCapability{image: true})
	sink := &ImageCollector{}
	ctx := WithImageSink(context.Background(), sink)

	args, _ := json.Marshal(map[string]any{"path": path})
	out, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(out, "diagram.jpg") {
		t.Fatalf("Execute() output %q should default label to basename", out)
	}
	parts := sink.Drain()
	if len(parts) != 1 || parts[0].MimeType != "image/jpeg" {
		t.Fatalf("expected 1 jpeg part, got %+v", parts)
	}
}

func TestViewImageToolExecuteErrors(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeTestPNG(t, dir, "ok.png")
	txtPath := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txtPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	tool := NewViewImageTool(stubImageCapability{image: true})

	t.Run("missing path", func(t *testing.T) {
		ctx := WithImageSink(context.Background(), &ImageCollector{})
		if _, err := tool.Execute(ctx, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("expected error for missing path")
		}
	})

	t.Run("no sink in context", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"path": pngPath})
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Fatalf("expected error when image sink absent")
		}
	})

	t.Run("file not found", func(t *testing.T) {
		ctx := WithImageSink(context.Background(), &ImageCollector{})
		args, _ := json.Marshal(map[string]any{"path": filepath.Join(dir, "nope.png")})
		_, err := tool.Execute(ctx, args)
		if err == nil || !strings.Contains(err.Error(), "file not found") {
			t.Fatalf("expected file-not-found error, got %v", err)
		}
	})

	t.Run("unsupported format", func(t *testing.T) {
		ctx := WithImageSink(context.Background(), &ImageCollector{})
		args, _ := json.Marshal(map[string]any{"path": txtPath})
		if _, err := tool.Execute(ctx, args); err == nil {
			t.Fatalf("expected error for non-image file")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		ctx := WithImageSink(context.Background(), &ImageCollector{})
		if _, err := tool.Execute(ctx, json.RawMessage(`{bad`)); err == nil {
			t.Fatalf("expected error for invalid arguments")
		}
	})
}
