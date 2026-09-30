package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestHostedCatalogRejectsNameCollisions(t *testing.T) {
	registry := NewRegistry()
	registry.Register(ReadTool{})
	for _, catalog := range []map[string]config.HostedToolConfig{
		{NameRead: {}}, {"shell": {}}, {"patch": {}}, {"mcp__sample__tool": {}}, {"sample": {}, " sample ": {}}, {"sample": {ImagePaths: []string{"result..image"}}},
	} {
		if err := ValidateHostedToolCatalog(catalog, registry); err == nil {
			t.Fatalf("catalog accepted: %+v", catalog)
		}
	}
	if tool, ok := registry.Get(NameRead); !ok {
		t.Fatal("read was removed")
	} else if _, ok := tool.(ReadTool); !ok {
		t.Fatalf("read replaced: %T", tool)
	}
	if err := ValidateHostedToolCatalog(map[string]config.HostedToolConfig{NameWebSearch: {}, "sample": {Declarations: map[string]config.HostedToolDeclarationConfig{config.ProviderTypeResponses: {Tool: map[string]any{"type": "sample_tool"}}}}}, registry); err != nil {
		t.Fatal(err)
	}
}

func TestHostedImageOutputAndArtifact(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]any{"output": map[string]string{"image": base64.StdEncoding.EncodeToString(encoded.Bytes())}, "text": strings.Repeat("a", 3000)})
	obs := &message.HostedObservation{Calls: []message.HostedCall{{Result: result}}, Items: []json.RawMessage{json.RawMessage(`{"type":"message","file_id":"file-1","filename":"sample.csv"}`)}}
	dir := t.TempDir()
	sink := &ImageCollector{}
	ctx := WithImageSink(WithSessionDir(context.Background(), dir), sink)
	output, err := (HostedTool{spec: HostedToolSpec{ImagePaths: []string{"output.image"}}}).renderResult(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	if parts := sink.Drain(); len(parts) != 1 || parts[0].MimeType != "image/png" {
		t.Fatalf("parts = %+v", parts)
	}
	marker := hostedOutputArtifactPrefix
	start := strings.Index(output, marker)
	if start < 0 {
		t.Fatalf("missing artifact: %s", output)
	}
	relPath := strings.TrimSpace(output[start+len(marker):])
	if strings.Contains(relPath, "{") || strings.Contains(output, "size_bytes") {
		t.Fatal("artifact metadata leaked into the result")
	}
	raw, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]string{"path": relPath})
	if err != nil {
		t.Fatal(err)
	}
	readback, err := (ReadArtifactTool{}).Execute(ctx, args)
	if err != nil || !strings.Contains(readback, "sample.csv") {
		t.Fatalf("artifact reference cannot be read: %v, %s", err, readback)
	}
	var restored message.HostedObservation
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.Calls[0].Result, result) || !strings.Contains(string(raw), "sample.csv") {
		t.Fatal("artifact lost full output")
	}
}

func TestHostedCitationsMapToSources(t *testing.T) {
	obs := &message.HostedObservation{Summary: "Sample fact", Calls: []message.HostedCall{{Result: json.RawMessage(`[{"url":"https://example.invalid/a","title":"A"}]`)}}, Items: []json.RawMessage{json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"Sample fact","annotations":[{"type":"url_citation","url":"https://example.invalid/a","title":"A","start_index":0,"end_index":6},{"type":"url_citation","url":"https://example.invalid/b","title":"B","start_index":7,"end_index":11}]}]}`)}}
	output := formatWebSearchObservation(obs)
	if !strings.Contains(output, "[1] Sample") || !strings.Contains(output, "[2] fact") || !strings.Contains(output, "[2] B") {
		t.Fatalf("output = %s", output)
	}
}
