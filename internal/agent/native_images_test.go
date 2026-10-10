package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestNativeImagesSaveOriginalAndReplayIdentity(t *testing.T) {
	original := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	original.SetNRGBA(0, 0, color.NRGBA{R: 230, A: 80})
	var b bytes.Buffer
	png.Encode(&b, original)
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]string{"id": "image-1", "type": "image_generation_call", "result": base64.StdEncoding.EncodeToString(b.Bytes()), "revised_prompt": "A tree"})
	resp := &message.Response{Hosted: &message.HostedObservation{Calls: []message.HostedCall{{ID: "image-1", Kind: "image_generation_call", Result: raw}}, Items: []json.RawMessage{raw}}}
	if err := saveNativeImages(t.Context(), dir, resp); err != nil {
		t.Fatal(err)
	}
	call := resp.Hosted.Calls[0]
	if len(call.Parts) != 1 || strings.Contains(string(call.Result), "base64") || strings.Contains(string(resp.Hosted.Items[0]), "result") {
		t.Fatal("raw bytes leaked into receipt")
	}
	data, err := os.ReadFile(call.Parts[0].ImagePath)
	if err != nil || !bytes.Equal(data, b.Bytes()) {
		t.Fatal("original bytes or transparency changed")
	}
	var summary tools.ImageGenerationSummary
	if err := json.Unmarshal(call.Result, &summary); err != nil || summary.State != "saved" || len(summary.Images) != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if err := saveNativeImages(t.Context(), dir, resp); err != nil {
		t.Fatal("projection is not idempotent", err)
	}
}

func TestInvalidNativeImageDoesNotPersistRawBytes(t *testing.T) {
	for _, id := range []string{"image-1", ""} {
		raw, _ := json.Marshal(map[string]string{"type": "image_generation_call", "id": id, "result": "invalid-base64"})
		resp := &message.Response{Hosted: &message.HostedObservation{Calls: []message.HostedCall{{ID: id, Kind: "image_generation_call", Result: raw}}, Items: []json.RawMessage{raw}}}
		if saveNativeImages(t.Context(), t.TempDir(), resp) == nil {
			t.Fatal("invalid image accepted")
		}
		if len(resp.Hosted.Calls[0].Result) != 0 || strings.Contains(string(resp.Hosted.Items[0]), "invalid-base64") {
			t.Fatal("failed original entered durable response")
		}
	}

}

func TestNativeImageFallbackDecisionAndFreshTurn(t *testing.T) {
	dir := t.TempDir()
	a := &MainAgent{}
	target, _ := imagegen.ResolveTarget(imagegen.PresetOpenAI, "gpt-image-1.5", "")
	registry := tools.NewRegistry()
	registry.Register(&tools.GenerateImageTool{Backend: &poolTestBackend{target: target}})
	journal := recovery.NativeRequestJournal{SessionDir: dir, AgentID: "sample-agent", TurnID: 1}
	turn := &Turn{ID: 1, Ctx: t.Context()}
	newPolicy := func() *llm.NativeToolPolicy {
		return &llm.NativeToolPolicy{Begin: func(context.Context, llm.NativeRequestRecord) (string, error) { return "request-1", nil }}
	}
	policy := newPolicy()
	a.configureNativeImagePolicy(policy, turn, registry, journal, "main")
	if policy.DisableImage {
		t.Fatal("new turn skipped native")
	}
	if _, err := policy.Begin(t.Context(), llm.NativeRequestRecord{Authorization: message.NativeToolAuthorization{Tool: tools.NameGenerateImage}}); err != nil {
		t.Fatal(err)
	}
	if !policy.ImageFallback() {
		t.Fatal("fallback decision not persisted")
	}
	policy = newPolicy()
	a.configureNativeImagePolicy(policy, turn, registry, journal, "main")
	if !policy.DisableImage {
		t.Fatal("same turn lost local mode")
	}
	files, err := os.ReadDir(filepath.Join(dir, "native-image-fallback"))
	if err != nil || len(files) != 1 {
		t.Fatal("switch decision was not durably recorded")
	}
	data, err := os.ReadFile(filepath.Join(dir, "native-image-fallback", files[0].Name()))
	if err != nil || !strings.Contains(string(data), "request-1") {
		t.Fatal("decision is not bound to the unique request")
	}
	// Runtime turn numbers restart after process replacement. A fresh turn must
	// not inherit an unrelated saved decision with the same numeric ID.
	policy = newPolicy()
	a.configureNativeImagePolicy(policy, &Turn{ID: 1, Ctx: t.Context()}, registry, journal, "main")
	if policy.DisableImage {
		t.Fatal("reused numeric turn inherited an old decision")
	}
	journal.TurnID = 2
	policy = newPolicy()
	a.configureNativeImagePolicy(policy, &Turn{ID: 2, Ctx: t.Context()}, registry, journal, "main")
	if policy.DisableImage {
		t.Fatal("new turn inherited fallback")
	}
}
