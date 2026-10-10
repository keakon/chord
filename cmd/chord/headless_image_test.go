package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/imagegen"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHeadlessImageChunksBoundedAndLossless(t *testing.T) {
	data := bytes.Repeat([]byte("sample"), 100000)
	var chunks []*headlessEnvelope
	emitHeadlessImageChunks(imagegen.Image{Data: data, MIME: "image/png", Width: 10, Height: 10}, "artifact:images/sample.png", "call-1", "request-1", "agent-1", func(env *headlessEnvelope) bool { chunks = append(chunks, env); return true })
	if len(chunks) < 2 {
		t.Fatal("large original was not chunked")
	}
	var restored []byte
	for _, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 600000 {
			t.Fatal("image frame exceeds bounded budget")
		}
		p := chunk.Payload.(map[string]any)
		if p["offset"].(int) != len(restored) || p["request_id"] != "request-1" {
			t.Fatal("wrong chunk identity")
		}
		decoded, err := base64.StdEncoding.DecodeString(p["data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		restored = append(restored, decoded...)
	}
	if !bytes.Equal(restored, data) || chunks[len(chunks)-1].Payload.(map[string]any)["last"] != true {
		t.Fatal("original transfer changed bytes")
	}
}

type headlessImageFixture struct {
	image  imagegen.Image
	target imagegen.Target
}

func (b headlessImageFixture) Target() imagegen.Target                     { return b.target }
func (headlessImageFixture) Timeout() time.Duration                        { return time.Minute }
func (headlessImageFixture) Check(context.Context, imagegen.Request) error { return nil }
func (b headlessImageFixture) Download(context.Context, string) (imagegen.Image, error) {
	return b.image, nil
}
func (b headlessImageFixture) Run(ctx context.Context, r imagegen.Request, before func() error) (*imagegen.Result, error) {
	if err := before(); err != nil {
		return nil, err
	}
	return &imagegen.Result{Images: []imagegen.Candidate{{Image: b.image}}}, nil
}

func TestHeadlessImageResultCarriesOriginal(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	img, err := imagegen.ValidateImage(t.Context(), b.Bytes(), "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := imagegen.ResolveTarget(imagegen.PresetCompatible, "test-image", "https://example.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	sink := &tools.ImageCollector{}
	ctx := tools.WithImageSink(tools.WithSessionDir(t.Context(), t.TempDir()), sink)
	tool := tools.GenerateImageTool{Backend: headlessImageFixture{image: img, target: target}}
	output, err := tool.Execute(ctx, json.RawMessage(`{"prompt":"A tree","operation":"generate"}`))
	if err != nil {
		t.Fatal(err)
	}
	event := agent.ToolResultEvent{Name: tools.NameGenerateImage, CallID: "call-1", Status: agent.ToolResultStatusSuccess, Payload: output, Parts: sink.Drain()}
	envelopes := collectHeadlessImageResult(t.Context(), event)
	if len(envelopes) != 2 || envelopes[0].Type != headlessImageResult || envelopes[1].Type != "image_artifact" {
		t.Fatal("missing original image transfer")
	}
	event.Parts = []message.ContentPart{}
	envelopes = collectHeadlessImageResult(t.Context(), event)
	if len(envelopes) != 2 || envelopes[1].Payload.(map[string]any)["delivery_error"] == nil {
		t.Fatal("missing artifact failure hidden")
	}
	event.Status = agent.ToolResultStatusError
	if len(collectHeadlessImageResult(t.Context(), event)) != 1 {
		t.Fatal("failed result delivered image as success")
	}
}

func collectHeadlessImageResult(ctx context.Context, event agent.ToolResultEvent) []*headlessEnvelope {
	out := []*headlessEnvelope{headlessImageResultEnvelope(event)}
	emitHeadlessImageOriginals(ctx, event, func(env *headlessEnvelope) bool { out = append(out, env); return true })
	return out
}

func TestHeadlessImageChunksStopOnBackpressureCancellation(t *testing.T) {
	calls := 0
	if emitHeadlessImageChunks(imagegen.Image{Data: make([]byte, 3*headlessImageChunkBytes)}, "", "", "", "", func(*headlessEnvelope) bool { calls++; return false }) || calls != 1 {
		t.Fatal("delivery continued after output closed")
	}
}
